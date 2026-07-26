package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"autoto/internal/db"
	"autoto/internal/tools"
)

const (
	automationToolInstallTimeout = 2 * time.Minute
	automationToolOutputLimit    = 64 << 10
	automationNPMRegistry        = "https://registry.npmjs.org/"
)

type AutomationToolPrerequisite struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}

type AutomationToolCatalogItem struct {
	ID               string                       `json:"id"`
	Name             string                       `json:"name,omitempty"`
	Kind             string                       `json:"kind"`
	InstallMode      string                       `json:"installMode"`
	Publisher        string                       `json:"publisher"`
	License          string                       `json:"license"`
	Purpose          string                       `json:"purpose"`
	RiskBoundary     string                       `json:"riskBoundary"`
	DataAccess       []string                     `json:"dataAccess"`
	SafetyDefaults   []string                     `json:"safetyDefaults"`
	PackageName      string                       `json:"packageName"`
	Version          string                       `json:"version"`
	SourceURL        string                       `json:"sourceUrl"`
	DocsURL          string                       `json:"docsUrl"`
	InstallURL       string                       `json:"installUrl"`
	Platforms        []string                     `json:"platforms"`
	Supported        bool                         `json:"supported"`
	ManagedPath      string                       `json:"managedPath"`
	Installed        bool                         `json:"installed"`
	InstalledVersion string                       `json:"installedVersion"`
	Configured       bool                         `json:"configured"`
	MCPServerID      string                       `json:"mcpServerId"`
	Enabled          bool                         `json:"enabled"`
	CanInstall       bool                         `json:"canInstall"`
	CanConfigure     bool                         `json:"canConfigure"`
	CanEnable        bool                         `json:"canEnable"`
	Prerequisites    []AutomationToolPrerequisite `json:"prerequisites"`
}

type automationToolDefinition struct {
	AutomationToolCatalogItem
	binName     string
	binRelative string
	nodeMinimum string
	nodeDetail  string
	mcpArgs     []string
	mcpEnv      map[string]string
}

var automationToolDefinitions = []automationToolDefinition{
	{
		AutomationToolCatalogItem: AutomationToolCatalogItem{
			ID: "playwright-mcp", Name: "Playwright MCP", Kind: "mcp", InstallMode: "managed-npm",
			Publisher: "Microsoft", License: "Apache-2.0", Purpose: "browserAutomation", RiskBoundary: "browserSideEffects",
			DataAccess:     []string{"pageContent", "consoleNetwork", "screenshots", "browserProfile"},
			SafetyDefaults: []string{"systemChrome", "isolatedContext", "coreCapabilities", "noExistingProfile"},
			PackageName:    "@playwright/mcp", Version: "0.0.78",
			SourceURL: "https://github.com/microsoft/playwright-mcp", DocsURL: "https://github.com/microsoft/playwright-mcp",
			InstallURL: "https://www.npmjs.com/package/@playwright/mcp/v/0.0.78", Platforms: []string{"windows", "darwin", "linux"},
		},
		binName: "playwright-mcp", binRelative: "cli.js", nodeMinimum: "18.0.0", nodeDetail: "Node.js >=18",
		mcpArgs: []string{"--browser", "chrome", "--isolated", "--caps", "core"},
	},
	{
		AutomationToolCatalogItem: AutomationToolCatalogItem{
			ID: "chrome-devtools-mcp", Name: "Chrome DevTools MCP", Kind: "mcp", InstallMode: "managed-npm",
			Publisher: "Google LLC", License: "Apache-2.0", Purpose: "browserDiagnostics", RiskBoundary: "browserSideEffects",
			DataAccess:     []string{"pageContent", "consoleNetwork", "screenshots", "browserProfile", "performanceData"},
			SafetyDefaults: []string{"isolatedProfile", "slimMode", "noTelemetry", "noCrux", "noUpdateChecks"},
			PackageName:    "chrome-devtools-mcp", Version: "1.6.0",
			SourceURL: "https://github.com/ChromeDevTools/chrome-devtools-mcp", DocsURL: "https://github.com/ChromeDevTools/chrome-devtools-mcp",
			InstallURL: "https://www.npmjs.com/package/chrome-devtools-mcp/v/1.6.0", Platforms: []string{"windows", "darwin", "linux"},
		},
		binName: "chrome-devtools-mcp", binRelative: "build/src/bin/chrome-devtools-mcp.js", nodeMinimum: "20.19.0", nodeDetail: "Node.js >=20.19",
		mcpArgs: []string{"--isolated", "--slim", "--no-usage-statistics", "--no-performance-crux"},
		mcpEnv:  map[string]string{"CHROME_DEVTOOLS_MCP_NO_UPDATE_CHECKS": "1", "NODE_OPTIONS": ""},
	},
	{
		AutomationToolCatalogItem: AutomationToolCatalogItem{
			ID: "power-automate-desktop", Name: "Power Automate Desktop", Kind: "external", InstallMode: "external",
			Publisher: "Microsoft", License: "Proprietary", Purpose: "desktopAutomation", RiskBoundary: "externalApplication",
			DataAccess:     []string{"desktopSession", "localFiles", "userConfiguredCredentials"},
			SafetyDefaults: []string{"externalInstallOnly", "noAgentBridge"},
			SourceURL:      "https://learn.microsoft.com/en-us/power-automate/desktop-flows/install",
			DocsURL:        "https://learn.microsoft.com/en-us/power-automate/desktop-flows/install",
			InstallURL:     "https://learn.microsoft.com/en-us/power-automate/desktop-flows/install", Platforms: []string{"windows"},
		},
	},
	{
		AutomationToolCatalogItem: AutomationToolCatalogItem{
			ID: "computer-use", Name: "Computer use", Kind: "capability", InstallMode: "capability",
			Publisher: "OpenAI", License: "Service", Purpose: "computerUseCapability", RiskBoundary: "providerBridgeUnavailable",
			DataAccess:     []string{"screen", "mouseKeyboard", "providerContext"},
			SafetyDefaults: []string{"providerBridgeRequired", "noLocalInstall"},
			SourceURL:      "https://developers.openai.com/api/docs/guides/tools-computer-use",
			DocsURL:        "https://developers.openai.com/api/docs/guides/tools-computer-use",
			InstallURL:     "https://developers.openai.com/api/docs/guides/tools-computer-use", Platforms: []string{"windows", "darwin", "linux"},
		},
	},
}

// AutomationToolCommand is the closed-world command description supplied to a
// test fake or to the production exec.CommandContext runner.
type AutomationToolCommand struct {
	Executable string
	Args       []string
	Dir        string
	Env        []string
}

type AutomationToolCommandResult struct {
	Output string
}

type AutomationToolCatalogOptions struct {
	GOOS           string
	LookPath       func(string) (string, error)
	RunCommand     func(context.Context, AutomationToolCommand) (AutomationToolCommandResult, error)
	InstallTimeout time.Duration
}

type AutomationToolCatalog struct {
	homeDir        string
	store          *db.Store
	goos           string
	lookPath       func(string) (string, error)
	runCommand     func(context.Context, AutomationToolCommand) (AutomationToolCommandResult, error)
	installTimeout time.Duration
	mu             sync.Mutex
}

func NewAutomationToolCatalog(homeDir string, store *db.Store, options AutomationToolCatalogOptions) *AutomationToolCatalog {
	goos := strings.TrimSpace(options.GOOS)
	if goos == "" {
		goos = runtime.GOOS
	}
	lookPath := options.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	runCommand := options.RunCommand
	if runCommand == nil {
		runCommand = runAutomationToolCommand
	}
	installTimeout := options.InstallTimeout
	if installTimeout <= 0 {
		installTimeout = automationToolInstallTimeout
	}
	return &AutomationToolCatalog{
		homeDir: normalizeAutomationHomeDir(homeDir), store: store, goos: goos,
		lookPath: lookPath, runCommand: runCommand, installTimeout: installTimeout,
	}
}

func normalizeAutomationHomeDir(homeDir string) string {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return ""
	}
	absolute, err := filepath.Abs(homeDir)
	if err != nil {
		return ""
	}
	absolute = filepath.Clean(absolute)
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		absolute = filepath.Clean(resolved)
	}
	return absolute
}

func runAutomationToolCommand(ctx context.Context, invocation AutomationToolCommand) (AutomationToolCommandResult, error) {
	output := &boundedAutomationToolOutput{limit: automationToolOutputLimit}
	command := exec.CommandContext(ctx, invocation.Executable, invocation.Args...)
	command.Dir = invocation.Dir
	command.Env = append([]string(nil), invocation.Env...)
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	return AutomationToolCommandResult{Output: output.String()}, err
}

type boundedAutomationToolOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (output *boundedAutomationToolOutput) Write(value []byte) (int, error) {
	original := len(value)
	remaining := output.limit - output.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
			output.truncated = true
		}
		_, _ = output.buffer.Write(value)
	} else if len(value) > 0 {
		output.truncated = true
	}
	return original, nil
}

func (output *boundedAutomationToolOutput) String() string {
	value := output.buffer.String()
	if output.truncated {
		value += "\n...[truncated]"
	}
	return value
}

func (catalog *AutomationToolCatalog) List(ctx context.Context) ([]AutomationToolCatalogItem, error) {
	items := make([]AutomationToolCatalogItem, 0, len(automationToolDefinitions))
	for _, definition := range automationToolDefinitions {
		item, err := catalog.snapshot(ctx, definition)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (catalog *AutomationToolCatalog) Get(ctx context.Context, id string) (AutomationToolCatalogItem, error) {
	definition, ok := automationToolDefinitionByID(id)
	if !ok {
		return AutomationToolCatalogItem{}, fmt.Errorf("%w: optional automation tool %q", sql.ErrNoRows, id)
	}
	return catalog.snapshot(ctx, definition)
}

func (catalog *AutomationToolCatalog) Install(ctx context.Context, id string) (AutomationToolCatalogItem, error) {
	definition, ok := automationToolDefinitionByID(id)
	if !ok {
		return AutomationToolCatalogItem{}, fmt.Errorf("%w: optional automation tool %q", sql.ErrNoRows, id)
	}
	if definition.InstallMode != "managed-npm" {
		return AutomationToolCatalogItem{}, fmt.Errorf("optional automation tool %q is not managed by the backend", id)
	}
	if !platformSupported(definition.Platforms, catalog.goos) {
		return AutomationToolCatalogItem{}, fmt.Errorf("optional automation tool %q is unsupported on %s", id, catalog.goos)
	}

	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	installCtx, cancel := context.WithTimeout(ctx, catalog.installTimeout)
	defer cancel()

	baseDir, err := catalog.prepareManagedBaseDir()
	if err != nil {
		return AutomationToolCatalogItem{}, err
	}
	target := catalog.managedPath(definition.ID)
	if targetInfo, targetErr := os.Lstat(target); targetErr == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.IsDir() {
			return AutomationToolCatalogItem{}, errors.New("existing managed package target must be a real directory")
		}
		if _, validationErr := catalog.validateManagedInstallation(definition); validationErr == nil {
			return catalog.snapshot(ctx, definition)
		}
		if treeErr := validateAutomationInstallationTree(target); treeErr != nil {
			return AutomationToolCatalogItem{}, fmt.Errorf("existing managed package tree is unsafe: %w", treeErr)
		}
	} else if !errors.Is(targetErr, os.ErrNotExist) {
		return AutomationToolCatalogItem{}, targetErr
	}
	nodePath, err := catalog.absoluteExecutable("node")
	if err != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("Node.js is required: %w", err)
	}
	npmPath, err := catalog.absoluteExecutable("npm")
	if err != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("npm is required: %w", err)
	}
	if err := catalog.verifyNodeVersion(installCtx, nodePath, definition.nodeMinimum); err != nil {
		return AutomationToolCatalogItem{}, err
	}

	staging, err := os.MkdirTemp(baseDir, "."+definition.ID+"-staging-")
	if err != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("create automation staging directory: %w", err)
	}
	keepStaging := true
	defer func() {
		if keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	npmrcPath := filepath.Join(staging, ".npmrc")
	if err := os.WriteFile(npmrcPath, []byte(automationNPMRC()), 0o600); err != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("write controlled npm configuration: %w", err)
	}
	globalNPMRCPath := filepath.Join(staging, ".npm-globalrc")
	if err := os.WriteFile(globalNPMRCPath, []byte(automationNPMRC()), 0o600); err != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("write controlled global npm configuration: %w", err)
	}
	cacheDir := filepath.Join(staging, ".npm-cache")
	invocation := AutomationToolCommand{
		Executable: npmPath,
		Args: []string{
			"install", "--prefix", staging,
			"--registry=" + automationNPMRegistry,
			"--ignore-scripts", "--no-audit", "--no-fund", "--save-exact",
			"--package-lock=true", "--global=false",
			definition.PackageName + "@" + definition.Version,
		},
		Env: automationNPMEnvironment(npmrcPath, globalNPMRCPath, cacheDir),
	}
	result, runErr := catalog.runCommand(installCtx, invocation)
	if installCtx.Err() != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("npm install timed out or was canceled: %w", installCtx.Err())
	}
	if runErr != nil {
		message := strings.TrimSpace(result.Output)
		if message == "" {
			return AutomationToolCatalogItem{}, fmt.Errorf("npm install failed: %w", runErr)
		}
		return AutomationToolCatalogItem{}, fmt.Errorf("npm install failed: %w: %s", runErr, message)
	}
	if err := cleanupAutomationToolStaging(staging, cacheDir, npmrcPath, globalNPMRCPath); err != nil {
		return AutomationToolCatalogItem{}, err
	}
	if _, err := validateAutomationToolInstallation(staging, definition); err != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("validate installed package: %w", err)
	}
	if err := requireAutomationDirectoryTree(catalog.homeDir, baseDir); err != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("revalidate managed automation directory: %w", err)
	}
	if err := requireAutomationResolvedPathWithin(catalog.homeDir, baseDir); err != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("revalidate managed automation directory: %w", err)
	}

	if err := replaceAutomationToolDirectory(staging, target); err != nil {
		return AutomationToolCatalogItem{}, err
	}
	keepStaging = false
	return catalog.snapshot(ctx, definition)
}

func (catalog *AutomationToolCatalog) Configure(ctx context.Context, id string) (AutomationToolCatalogItem, error) {
	definition, ok := automationToolDefinitionByID(id)
	if !ok {
		return AutomationToolCatalogItem{}, fmt.Errorf("%w: optional automation tool %q", sql.ErrNoRows, id)
	}
	if definition.InstallMode != "managed-npm" {
		return AutomationToolCatalogItem{}, fmt.Errorf("optional automation tool %q cannot be configured by the backend", id)
	}
	if !platformSupported(definition.Platforms, catalog.goos) {
		return AutomationToolCatalogItem{}, fmt.Errorf("optional automation tool %q is unsupported on %s", id, catalog.goos)
	}
	if catalog.store == nil {
		return AutomationToolCatalogItem{}, errors.New("MCP registry is unavailable")
	}

	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	serverID := tools.ManagedAutomationMCPServerID(definition.ID)
	if _, err := catalog.store.GetMCPServer(ctx, serverID); err == nil {
		return catalog.snapshot(ctx, definition)
	} else if !db.IsNotFound(err) {
		return AutomationToolCatalogItem{}, err
	}

	binPath, err := catalog.validateManagedInstallation(definition)
	if err != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("optional automation tool %q is not installed correctly: %w", id, err)
	}
	nodePath, err := catalog.absoluteExecutable("node")
	if err != nil {
		return AutomationToolCatalogItem{}, fmt.Errorf("Node.js is required: %w", err)
	}
	server := db.MCPServer{
		ID: serverID, Name: definition.Name, Transport: "stdio", Command: nodePath,
		Args: append([]string{binPath}, definition.mcpArgs...), CWD: "",
		Env: cloneAutomationStringMap(definition.mcpEnv), Enabled: false,
	}
	if _, err := catalog.store.CreateMCPServer(ctx, server); err != nil {
		// A concurrent or pre-existing owner of the stable ID wins. Never rewrite
		// it, because the user may already have edited that MCP configuration.
		if _, getErr := catalog.store.GetMCPServer(ctx, serverID); getErr != nil {
			return AutomationToolCatalogItem{}, err
		}
	}
	return catalog.snapshot(ctx, definition)
}

func (catalog *AutomationToolCatalog) snapshot(ctx context.Context, definition automationToolDefinition) (AutomationToolCatalogItem, error) {
	item := definition.AutomationToolCatalogItem
	item.Platforms = append([]string(nil), definition.Platforms...)
	item.DataAccess = append([]string(nil), definition.DataAccess...)
	item.SafetyDefaults = append([]string(nil), definition.SafetyDefaults...)
	item.Prerequisites = []AutomationToolPrerequisite{}
	item.Supported = platformSupported(item.Platforms, catalog.goos)
	if definition.InstallMode == "managed-npm" {
		item.ManagedPath = catalog.managedPath(definition.ID)
		item.MCPServerID = tools.ManagedAutomationMCPServerID(definition.ID)
		nodeAvailable := catalog.executableAvailable("node")
		npmAvailable := catalog.executableAvailable("npm")
		item.Prerequisites = append(item.Prerequisites,
			AutomationToolPrerequisite{ID: "node", Available: nodeAvailable, Detail: definition.nodeDetail},
			AutomationToolPrerequisite{ID: "npm", Available: npmAvailable, Detail: "npm from the local Node.js installation"},
		)
		if _, err := catalog.validateManagedInstallation(definition); err == nil {
			item.Installed = true
			item.InstalledVersion = definition.Version
		}
		if catalog.store != nil {
			server, err := catalog.store.GetMCPServer(ctx, item.MCPServerID)
			switch {
			case err == nil:
				item.Configured = true
				item.Enabled = server.Enabled
			case db.IsNotFound(err):
			default:
				return AutomationToolCatalogItem{}, err
			}
		}
		managedPathAvailable := strings.TrimSpace(item.ManagedPath) != "" && automationDirectoryTreeAvailable(catalog.homeDir, catalog.managedBaseDir())
		item.CanInstall = item.Supported && managedPathAvailable && nodeAvailable && npmAvailable
		item.CanConfigure = item.Supported && managedPathAvailable && item.Installed && !item.Configured && nodeAvailable && catalog.store != nil
		item.CanEnable = item.Supported && item.Installed && item.Configured && nodeAvailable
	} else if definition.ID == "power-automate-desktop" {
		item.Prerequisites = append(item.Prerequisites,
			AutomationToolPrerequisite{ID: "windows", Available: catalog.goos == "windows", Detail: "Windows 10/11 or Windows Server"},
			AutomationToolPrerequisite{ID: "dotnet8", Available: false, Detail: ".NET 8 Runtime is required for Power Automate Desktop 2.55 and newer; verify it in Microsoft's installer"},
		)
	} else if definition.ID == "computer-use" {
		item.Prerequisites = append(item.Prerequisites,
			AutomationToolPrerequisite{ID: "provider-bridge", Available: false, Detail: "Requires a supported provider and an explicit local computer-use bridge"},
		)
	}
	return item, nil
}

func (catalog *AutomationToolCatalog) managedBaseDir() string {
	if catalog == nil || strings.TrimSpace(catalog.homeDir) == "" {
		return ""
	}
	return filepath.Join(catalog.homeDir, "optional-tools", "automation")
}

func (catalog *AutomationToolCatalog) managedPath(id string) string {
	baseDir := catalog.managedBaseDir()
	if baseDir == "" {
		return ""
	}
	return filepath.Join(baseDir, id)
}

func (catalog *AutomationToolCatalog) prepareManagedBaseDir() (string, error) {
	baseDir := catalog.managedBaseDir()
	if baseDir == "" {
		return "", errors.New("a fixed application home directory is required for managed automation tools")
	}
	if err := walkAutomationDirectoryTree(catalog.homeDir, baseDir, true); err != nil {
		return "", fmt.Errorf("prepare managed automation directory: %w", err)
	}
	return baseDir, nil
}

func requireAutomationDirectoryTree(root, target string) error {
	return walkAutomationDirectoryTree(root, target, false)
}

func walkAutomationDirectoryTree(root, target string, create bool) error {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbsolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbsolute, targetAbsolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("managed automation directory escapes the application home")
	}
	current := rootAbsolute
	parts := append([]string{"."}, strings.Split(filepath.Clean(relative), string(filepath.Separator))...)
	for _, part := range parts {
		if part != "." && part != "" {
			current = filepath.Join(current, part)
		}
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) && create && part != "." {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return mkdirErr
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed automation directory must not contain symbolic links")
		}
		if !info.IsDir() {
			return errors.New("managed automation path contains a non-directory component")
		}
		if create && runtime.GOOS != "windows" {
			if chmodErr := os.Chmod(current, 0o700); chmodErr != nil {
				return chmodErr
			}
		}
	}
	return nil
}

func automationDirectoryTreeAvailable(root, target string) bool {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbsolute, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbsolute, targetAbsolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return false
	}
	current := rootAbsolute
	parts := append([]string{"."}, strings.Split(filepath.Clean(relative), string(filepath.Separator))...)
	for _, part := range parts {
		if part != "." && part != "" {
			current = filepath.Join(current, part)
		}
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return part != "."
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false
		}
	}
	return true
}

func requireAutomationResolvedPathWithin(root, target string) error {
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	targetResolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(filepath.Clean(rootResolved), filepath.Clean(targetResolved))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("managed automation path resolves outside the application home")
	}
	return nil
}

func (catalog *AutomationToolCatalog) validateManagedInstallation(definition automationToolDefinition) (string, error) {
	managedPath := catalog.managedPath(definition.ID)
	if managedPath == "" {
		return "", errors.New("managed package root is required")
	}
	if err := requireAutomationResolvedPathWithin(catalog.homeDir, managedPath); err != nil {
		return "", err
	}
	return validateAutomationToolInstallation(managedPath, definition)
}

func (catalog *AutomationToolCatalog) executableAvailable(name string) bool {
	_, err := catalog.lookPath(name)
	return err == nil
}

func (catalog *AutomationToolCatalog) absoluteExecutable(name string) (string, error) {
	resolved, err := catalog.lookPath(name)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func (catalog *AutomationToolCatalog) verifyNodeVersion(ctx context.Context, nodePath, minimum string) error {
	result, err := catalog.runCommand(ctx, AutomationToolCommand{
		Executable: nodePath, Args: []string{"--version"}, Env: automationSanitizedEnvironment(),
	})
	if ctx.Err() != nil {
		return fmt.Errorf("check Node.js version: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("check Node.js version: %w", err)
	}
	version := strings.TrimSpace(result.Output)
	version = strings.TrimPrefix(version, "v")
	if compareAutomationSemver(version, minimum) < 0 {
		return fmt.Errorf("Node.js %s or newer is required (found %s)", minimum, strings.TrimSpace(result.Output))
	}
	return nil
}

func automationToolDefinitionByID(id string) (automationToolDefinition, bool) {
	id = strings.TrimSpace(id)
	for _, definition := range automationToolDefinitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return automationToolDefinition{}, false
}

func platformSupported(platforms []string, goos string) bool {
	for _, platform := range platforms {
		if platform == goos {
			return true
		}
	}
	return false
}

func automationNPMRC() string {
	return strings.Join([]string{
		"registry=" + automationNPMRegistry,
		"ignore-scripts=true",
		"audit=false",
		"fund=false",
		"package-lock=true",
		"save-exact=true",
		"global=false",
		"update-notifier=false",
		"", // final newline
	}, "\n")
}

func automationSanitizedEnvironment() []string {
	base := os.Environ()
	clean := make([]string, 0, len(base)+1)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(strings.TrimSpace(key))
		if upper == "NODE_OPTIONS" || upper == "NODE_AUTH_TOKEN" || upper == "NPM_TOKEN" || upper == "NPM_AUTH_TOKEN" || strings.HasPrefix(upper, "NPM_CONFIG_") {
			continue
		}
		clean = append(clean, entry)
	}
	return append(clean, "NODE_OPTIONS=")
}

func automationNPMEnvironment(npmrcPath, globalNPMRCPath, cacheDir string) []string {
	environment := automationSanitizedEnvironment()
	controlled := []string{
		"NPM_CONFIG_USERCONFIG=" + npmrcPath,
		"NPM_CONFIG_GLOBALCONFIG=" + globalNPMRCPath,
		"NPM_CONFIG_CACHE=" + cacheDir,
		"NPM_CONFIG_REGISTRY=" + automationNPMRegistry,
		"NPM_CONFIG_IGNORE_SCRIPTS=true",
		"NPM_CONFIG_AUDIT=false",
		"NPM_CONFIG_FUND=false",
		"NPM_CONFIG_PACKAGE_LOCK=true",
		"NPM_CONFIG_SAVE_EXACT=true",
		"NPM_CONFIG_GLOBAL=false",
		"NPM_CONFIG_UPDATE_NOTIFIER=false",
	}
	return append(environment, controlled...)
}

func compareAutomationSemver(value, minimum string) int {
	parse := func(input string) ([3]int, bool) {
		var parsed [3]int
		input = strings.TrimSpace(strings.TrimPrefix(input, "v"))
		if before, _, found := strings.Cut(input, "-"); found {
			input = before
		}
		parts := strings.Split(input, ".")
		if len(parts) < 2 || len(parts) > 3 {
			return parsed, false
		}
		for index := range parts {
			number, err := strconv.Atoi(parts[index])
			if err != nil || number < 0 {
				return parsed, false
			}
			parsed[index] = number
		}
		return parsed, true
	}
	actual, actualOK := parse(value)
	required, requiredOK := parse(minimum)
	if !actualOK || !requiredOK {
		return -1
	}
	for index := range actual {
		if actual[index] < required[index] {
			return -1
		}
		if actual[index] > required[index] {
			return 1
		}
	}
	return 0
}

type automationPackageManifest struct {
	Name    string          `json:"name"`
	Version string          `json:"version"`
	Bin     json.RawMessage `json:"bin"`
}

type automationPackageLock struct {
	LockfileVersion int                              `json:"lockfileVersion"`
	Packages        map[string]automationLockedEntry `json:"packages"`
}

type automationLockedEntry struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Resolved     string            `json:"resolved"`
	Integrity    string            `json:"integrity"`
	Link         bool              `json:"link"`
	Dependencies map[string]string `json:"dependencies"`
}

func validateAutomationToolInstallation(root string, definition automationToolDefinition) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("managed package root is required")
	}
	root = filepath.Clean(root)
	if err := validateAutomationInstallationTree(root); err != nil {
		return "", err
	}
	packageRelative := filepath.Join("node_modules", filepath.FromSlash(definition.PackageName))
	packageDir := filepath.Join(root, packageRelative)
	manifestPath := filepath.Join(packageDir, "package.json")
	if err := requireAutomationRegularFile(root, manifestPath); err != nil {
		return "", err
	}
	manifestData, err := readBoundedAutomationJSON(manifestPath)
	if err != nil {
		return "", err
	}
	var manifest automationPackageManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return "", fmt.Errorf("decode package.json: %w", err)
	}
	if manifest.Name != definition.PackageName {
		return "", fmt.Errorf("package name is %q, want %q", manifest.Name, definition.PackageName)
	}
	if manifest.Version != definition.Version {
		return "", fmt.Errorf("package version is %q, want %q", manifest.Version, definition.Version)
	}
	binRelative, err := automationManifestBin(manifest.Bin, definition.binName)
	if err != nil {
		return "", err
	}
	if filepath.ToSlash(binRelative) != filepath.ToSlash(definition.binRelative) {
		return "", fmt.Errorf("package bin %q points to %q, want %q", definition.binName, binRelative, definition.binRelative)
	}
	if filepath.IsAbs(binRelative) || filepath.Clean(binRelative) == ".." || strings.HasPrefix(filepath.Clean(binRelative), ".."+string(filepath.Separator)) {
		return "", errors.New("package bin path escapes the package directory")
	}
	binPath := filepath.Join(packageDir, filepath.FromSlash(binRelative))
	if err := requireAutomationRegularFile(packageDir, binPath); err != nil {
		return "", fmt.Errorf("validate package bin: %w", err)
	}

	lockPath := filepath.Join(root, "package-lock.json")
	if err := requireAutomationRegularFile(root, lockPath); err != nil {
		return "", err
	}
	lockData, err := readBoundedAutomationJSON(lockPath)
	if err != nil {
		return "", err
	}
	var lock automationPackageLock
	if err := json.Unmarshal(lockData, &lock); err != nil {
		return "", fmt.Errorf("decode package-lock.json: %w", err)
	}
	if lock.LockfileVersion < 2 || len(lock.Packages) == 0 {
		return "", errors.New("package-lock.json must use a packages-based lockfile")
	}
	top, ok := lock.Packages[""]
	if !ok || len(top.Dependencies) != 1 || top.Dependencies[definition.PackageName] != definition.Version {
		return "", errors.New("package-lock.json top-level dependency is not the single exact whitelisted package")
	}
	packageLockKey := "node_modules/" + definition.PackageName
	lockedPackage, ok := lock.Packages[packageLockKey]
	if !ok || lockedPackage.Version != definition.Version {
		return "", errors.New("package-lock.json does not pin the expected package version")
	}
	if strings.TrimSpace(lockedPackage.Resolved) == "" || strings.TrimSpace(lockedPackage.Integrity) == "" {
		return "", errors.New("package-lock.json does not include registry provenance for the expected package")
	}
	for key, entry := range lock.Packages {
		if entry.Link {
			return "", fmt.Errorf("package-lock entry %q must not be a linked dependency", key)
		}
		resolvedValue := strings.TrimSpace(entry.Resolved)
		if resolvedValue == "" {
			if key == "" {
				continue
			}
			return "", fmt.Errorf("package-lock entry %q is missing its registry URL", key)
		}
		resolved, err := url.Parse(resolvedValue)
		if err != nil || !strings.EqualFold(resolved.Scheme, "https") || !strings.EqualFold(resolved.Hostname(), "registry.npmjs.org") || resolved.Port() != "" || resolved.User != nil || resolved.Fragment != "" || strings.TrimSpace(resolved.Path) == "" {
			return "", fmt.Errorf("package-lock entry %q has a non-whitelisted registry URL", key)
		}
		if err := validateAutomationIntegrity(entry.Integrity); err != nil {
			return "", fmt.Errorf("package-lock entry %q %w", key, err)
		}
	}
	absoluteBin, err := filepath.Abs(binPath)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absoluteBin), nil
}

func automationManifestBin(raw json.RawMessage, name string) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errors.New("package bin is missing")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return "", errors.New("package bin is empty")
		}
		return single, nil
	}
	var bins map[string]string
	if err := json.Unmarshal(raw, &bins); err != nil {
		return "", errors.New("package bin has an invalid shape")
	}
	value := strings.TrimSpace(bins[name])
	if value == "" {
		return "", fmt.Errorf("package bin %q is missing", name)
	}
	return value, nil
}

func validateAutomationIntegrity(value string) error {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "sha512-") || strings.ContainsAny(value, " \t\r\n?") {
		return errors.New("does not use a single sha512 integrity value")
	}
	encoded := strings.TrimPrefix(value, "sha512-")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(decoded) != 64 {
		return errors.New("has an invalid sha512 integrity value")
	}
	return nil
}

func validateAutomationInstallationTree(root string) error {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rootInfo, err := os.Lstat(rootAbsolute)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("managed package root must be a real directory")
	}
	entries := 0
	return filepath.WalkDir(rootAbsolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 200000 {
			return errors.New("managed package contains too many filesystem entries")
		}
		relative, err := filepath.Rel(rootAbsolute, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return errors.New("managed package tree escapes its root")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("managed package tree must not contain symbolic links")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("managed package tree must contain only directories and regular files")
		}
		return nil
	})
}

func requireAutomationRegularFile(root, path string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("managed package root is required")
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rootInfo, err := os.Lstat(rootAbsolute)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("managed package root must be a real directory")
	}
	pathAbsolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbsolute, pathAbsolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("managed package path escapes its root")
	}
	current := rootAbsolute
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for index, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed package path must not contain symbolic links")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return errors.New("managed package path contains a non-directory component")
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return errors.New("managed package file must be a regular file")
		}
	}
	return nil
}

func readBoundedAutomationJSON(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("managed package metadata is too large")
	}
	return data, nil
}

func cleanupAutomationToolStaging(staging string, paths ...string) error {
	info, err := os.Lstat(staging)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("managed package staging path must be a real directory")
	}
	stagingAbsolute, err := filepath.Abs(staging)
	if err != nil {
		return err
	}
	for _, path := range paths {
		pathAbsolute, absoluteErr := filepath.Abs(path)
		if absoluteErr != nil {
			return absoluteErr
		}
		relative, relativeErr := filepath.Rel(stagingAbsolute, pathAbsolute)
		if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return errors.New("temporary npm path escapes staging")
		}
		if removeErr := os.RemoveAll(pathAbsolute); removeErr != nil {
			return fmt.Errorf("remove temporary npm artifact: %w", removeErr)
		}
	}
	return nil
}

func replaceAutomationToolDirectory(staging, target string) error {
	stagingInfo, err := os.Lstat(staging)
	if err != nil {
		return err
	}
	if stagingInfo.Mode()&os.ModeSymlink != 0 || !stagingInfo.IsDir() {
		return errors.New("managed package staging path must be a real directory")
	}
	parent := filepath.Dir(target)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return errors.New("managed package parent must be a real directory")
	}
	if targetInfo, targetErr := os.Lstat(target); targetErr == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.IsDir() {
			return errors.New("existing managed package target must be a real directory")
		}
	} else if !errors.Is(targetErr, os.ErrNotExist) {
		return targetErr
	}
	marker, err := os.CreateTemp(parent, "."+filepath.Base(target)+"-backup-")
	if err != nil {
		return fmt.Errorf("prepare managed package replacement: %w", err)
	}
	backup := marker.Name()
	if err := marker.Close(); err != nil {
		_ = os.Remove(backup)
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	hadExisting := false
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("preserve previous managed package: %w", err)
		}
		hadExisting = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staging, target); err != nil {
		if hadExisting {
			_ = os.Rename(backup, target)
		}
		return fmt.Errorf("activate managed package: %w", err)
	}
	if hadExisting {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func cloneAutomationStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
