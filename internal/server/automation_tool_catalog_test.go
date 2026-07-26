package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"autoto/internal/db"
	"autoto/internal/tools"
)

func TestAutomationToolCatalogInstallUsesClosedWorldFakeRunner(t *testing.T) {
	ctx := context.Background()
	homeDir := t.TempDir()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	t.Setenv("NODE_OPTIONS", "--require=untrusted.js")
	t.Setenv("NODE_AUTH_TOKEN", "node-secret")
	t.Setenv("NPM_TOKEN", "npm-secret")
	t.Setenv("NPM_CONFIG_REGISTRY", "https://evil.example/")

	definition := automationTestDefinition(t, "playwright-mcp")
	nodePath := filepath.Join(homeDir, "runtime", "node.exe")
	npmPath := filepath.Join(homeDir, "runtime", "npm.cmd")
	commands := make([]AutomationToolCommand, 0, 2)
	catalog := NewAutomationToolCatalog(homeDir, store, AutomationToolCatalogOptions{
		GOOS: "windows",
		LookPath: func(name string) (string, error) {
			switch name {
			case "node":
				return nodePath, nil
			case "npm":
				return npmPath, nil
			default:
				return "", errors.New("not found")
			}
		},
		RunCommand: func(_ context.Context, command AutomationToolCommand) (AutomationToolCommandResult, error) {
			commands = append(commands, cloneAutomationTestCommand(command))
			if reflect.DeepEqual(command.Args, []string{"--version"}) {
				if command.Executable != nodePath || command.Dir != "" {
					t.Fatalf("unexpected Node invocation: %+v", command)
				}
				return AutomationToolCommandResult{Output: "v22.4.1\n"}, nil
			}
			prefix := automationTestArgValue(command.Args, "--prefix")
			wantArgs := []string{
				"install", "--prefix", prefix,
				"--registry=" + automationNPMRegistry,
				"--ignore-scripts", "--no-audit", "--no-fund", "--save-exact",
				"--package-lock=true", "--global=false",
				definition.PackageName + "@" + definition.Version,
			}
			if command.Executable != npmPath || prefix == "" || !reflect.DeepEqual(command.Args, wantArgs) || command.Dir != "" {
				t.Fatalf("unexpected npm invocation: %+v wantArgs=%v", command, wantArgs)
			}
			environment := automationTestEnvironment(command.Env)
			if environment["NODE_OPTIONS"] != "" || environment["NPM_CONFIG_REGISTRY"] != automationNPMRegistry {
				t.Fatalf("unsafe or uncontrolled environment: %+v", environment)
			}
			for _, forbidden := range []string{"NODE_AUTH_TOKEN", "NPM_TOKEN", "NPM_AUTH_TOKEN"} {
				if _, ok := environment[forbidden]; ok {
					t.Fatalf("credential %s leaked to npm", forbidden)
				}
			}
			userConfig := environment["NPM_CONFIG_USERCONFIG"]
			globalConfig := environment["NPM_CONFIG_GLOBALCONFIG"]
			if filepath.Dir(userConfig) != prefix || filepath.Dir(globalConfig) != prefix {
				t.Fatalf("npm configuration escaped staging: user=%q global=%q prefix=%q", userConfig, globalConfig, prefix)
			}
			for _, configPath := range []string{userConfig, globalConfig} {
				data, readErr := os.ReadFile(configPath)
				if readErr != nil || string(data) != automationNPMRC() {
					t.Fatalf("unexpected controlled npm config %q: err=%v data=%q", configPath, readErr, data)
				}
			}
			writeValidAutomationTestInstallation(t, prefix, definition)
			return AutomationToolCommandResult{Output: "installed"}, nil
		},
	})

	item, err := catalog.Install(ctx, definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(homeDir, "optional-tools", "automation", definition.ID)
	if item.ManagedPath != wantPath || !item.Installed || item.InstalledVersion != definition.Version {
		t.Fatalf("unexpected installed snapshot: %+v", item)
	}
	if item.Configured || item.Enabled || !item.CanConfigure || item.CanEnable {
		t.Fatalf("install must not configure or enable MCP: %+v", item)
	}
	if item.MCPServerID != tools.ManagedAutomationMCPServerID(definition.ID) {
		t.Fatalf("unexpected stable MCP id: %q", item.MCPServerID)
	}
	if item.Purpose != "browserAutomation" || item.RiskBoundary != "browserSideEffects" || len(item.DataAccess) == 0 || len(item.SafetyDefaults) == 0 {
		t.Fatalf("managed catalog metadata is incomplete: %+v", item)
	}
	second, err := catalog.Install(ctx, definition.ID)
	if err != nil || !second.Installed || second.Configured || second.Enabled {
		t.Fatalf("repeated install was not idempotent: item=%+v err=%v", second, err)
	}
	if len(commands) != 2 {
		t.Fatalf("expected only one Node version and npm install sequence, got %+v", commands)
	}
	for _, path := range []string{filepath.Join(wantPath, ".npmrc"), filepath.Join(wantPath, ".npm-globalrc"), filepath.Join(wantPath, ".npm-cache")} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("temporary npm artifact remained at %q: %v", path, statErr)
		}
	}
	servers, err := store.ListMCPServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 0 {
		t.Fatalf("installation unexpectedly created MCP configuration: %+v", servers)
	}
	entries, err := os.ReadDir(filepath.Dir(wantPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != definition.ID {
		t.Fatalf("staging directories were not cleaned: %+v", entries)
	}
}

func TestAutomationToolCatalogRejectsUnavailableOrUnsupportedRequestsBeforeInstall(t *testing.T) {
	ctx := context.Background()
	calls := 0
	fakeRunner := func(context.Context, AutomationToolCommand) (AutomationToolCommandResult, error) {
		calls++
		return AutomationToolCommandResult{}, errors.New("must not run")
	}
	available := func(name string) (string, error) { return filepath.Join(t.TempDir(), name), nil }

	catalog := NewAutomationToolCatalog("", nil, AutomationToolCatalogOptions{GOOS: "windows", LookPath: available, RunCommand: fakeRunner})
	if _, err := catalog.Install(ctx, "playwright-mcp"); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("expected missing fixed home directory rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("invalid home directory started a command: %d", calls)
	}
	item, err := catalog.Get(ctx, "playwright-mcp")
	if err != nil || item.ManagedPath != "" || item.Installed || item.CanInstall {
		t.Fatalf("empty home directory leaked the current working directory into catalog state: item=%+v err=%v", item, err)
	}

	catalog = NewAutomationToolCatalog(t.TempDir(), nil, AutomationToolCatalogOptions{GOOS: "plan9", LookPath: available, RunCommand: fakeRunner})
	if _, err := catalog.Install(ctx, "playwright-mcp"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported platform rejection, got %v", err)
	}
	if _, err := catalog.Install(ctx, "power-automate-desktop"); err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("expected external installer rejection, got %v", err)
	}
	if _, err := catalog.Install(ctx, "unknown-tool"); !db.IsNotFound(err) {
		t.Fatalf("expected unknown id not found, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("unsupported or unknown requests started commands: %d", calls)
	}

	catalog = NewAutomationToolCatalog(t.TempDir(), nil, AutomationToolCatalogOptions{
		GOOS: "windows",
		LookPath: func(name string) (string, error) {
			if name == "node" {
				return "", errors.New("missing node")
			}
			return filepath.Join(t.TempDir(), name), nil
		},
		RunCommand: fakeRunner,
	})
	if _, err := catalog.Install(ctx, "playwright-mcp"); err == nil || !strings.Contains(err.Error(), "Node.js is required") {
		t.Fatalf("expected missing Node rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("missing prerequisite started commands: %d", calls)
	}

	catalog = NewAutomationToolCatalog(t.TempDir(), nil, AutomationToolCatalogOptions{
		GOOS: "windows",
		LookPath: func(name string) (string, error) {
			if name == "node" {
				return filepath.Join(t.TempDir(), "node.exe"), nil
			}
			return "", errors.New("missing npm")
		},
		RunCommand: fakeRunner,
	})
	if _, err := catalog.Install(ctx, "playwright-mcp"); err == nil || !strings.Contains(err.Error(), "npm is required") {
		t.Fatalf("expected missing npm rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("missing npm started commands: %d", calls)
	}
}

func TestAutomationToolCatalogRejectsSymlinkedManagedParentBeforeRunner(t *testing.T) {
	homeDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(homeDir, "optional-tools")); err != nil {
		t.Skipf("symbolic links are unavailable in this environment: %v", err)
	}
	calls := 0
	catalog := NewAutomationToolCatalog(homeDir, nil, AutomationToolCatalogOptions{
		GOOS:     "windows",
		LookPath: func(name string) (string, error) { return filepath.Join(homeDir, "runtime", name), nil },
		RunCommand: func(context.Context, AutomationToolCommand) (AutomationToolCommandResult, error) {
			calls++
			return AutomationToolCommandResult{}, errors.New("must not run")
		},
	})
	item, err := catalog.Get(context.Background(), "playwright-mcp")
	if err != nil || item.CanInstall || item.Installed {
		t.Fatalf("symlinked managed parent appeared installable: item=%+v err=%v", item, err)
	}
	if _, err := catalog.Install(context.Background(), "playwright-mcp"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic") {
		t.Fatalf("expected symlinked parent rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("symlinked parent started Node or npm: %d", calls)
	}
	if _, err := os.Lstat(filepath.Join(outside, "automation")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installer wrote through managed parent symlink: %v", err)
	}
}

func TestAutomationToolCatalogRejectsSymlinkedExistingTargetBeforeRunner(t *testing.T) {
	homeDir := t.TempDir()
	outside := t.TempDir()
	baseDir := filepath.Join(homeDir, "optional-tools", "automation")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(baseDir, "playwright-mcp")
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symbolic links are unavailable in this environment: %v", err)
	}
	calls := 0
	catalog := NewAutomationToolCatalog(homeDir, nil, AutomationToolCatalogOptions{
		GOOS:     "windows",
		LookPath: func(name string) (string, error) { return filepath.Join(homeDir, "runtime", name), nil },
		RunCommand: func(context.Context, AutomationToolCommand) (AutomationToolCommandResult, error) {
			calls++
			return AutomationToolCommandResult{}, errors.New("must not run")
		},
	})
	if _, err := catalog.Install(context.Background(), "playwright-mcp"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "real directory") {
		t.Fatalf("expected symlinked target rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("symlinked target started Node or npm: %d", calls)
	}
	if resolved, err := filepath.EvalSymlinks(target); err != nil || resolved != outside {
		t.Fatalf("existing target symlink was modified: resolved=%q err=%v", resolved, err)
	}
}

func TestAutomationToolCatalogInstallTimeoutUsesFakeRunner(t *testing.T) {
	catalog := NewAutomationToolCatalog(t.TempDir(), nil, AutomationToolCatalogOptions{
		GOOS:           "windows",
		LookPath:       func(name string) (string, error) { return filepath.Join(t.TempDir(), name), nil },
		InstallTimeout: 10 * time.Millisecond,
		RunCommand: func(ctx context.Context, command AutomationToolCommand) (AutomationToolCommandResult, error) {
			<-ctx.Done()
			return AutomationToolCommandResult{}, ctx.Err()
		},
	})
	if _, err := catalog.Install(context.Background(), "playwright-mcp"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "deadline") {
		t.Fatalf("expected bounded install timeout, got %v", err)
	}
}

func TestValidateAutomationToolInstallationRejectsUntrustedMetadata(t *testing.T) {
	definition := automationTestDefinition(t, "playwright-mcp")
	tests := []struct {
		name   string
		mutate func(*testing.T, string, automationToolDefinition)
	}{
		{name: "package name", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			manifest := automationTestReadManifest(t, root, definition)
			manifest["name"] = "evil-package"
			automationTestWriteManifest(t, root, definition, manifest)
		}},
		{name: "package version", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			manifest := automationTestReadManifest(t, root, definition)
			manifest["version"] = "999.0.0"
			automationTestWriteManifest(t, root, definition, manifest)
		}},
		{name: "bin path", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			manifest := automationTestReadManifest(t, root, definition)
			manifest["bin"] = map[string]string{definition.binName: "../../outside.js"}
			automationTestWriteManifest(t, root, definition, manifest)
		}},
		{name: "top dependency range", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			lock := automationTestReadLock(t, root)
			top := lock.Packages[""]
			top.Dependencies[definition.PackageName] = "^" + definition.Version
			lock.Packages[""] = top
			automationTestWriteLock(t, root, lock)
		}},
		{name: "extra top dependency", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			lock := automationTestReadLock(t, root)
			top := lock.Packages[""]
			top.Dependencies["unexpected-package"] = "1.0.0"
			lock.Packages[""] = top
			automationTestWriteLock(t, root, lock)
		}},
		{name: "linked dependency", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			lock := automationTestReadLock(t, root)
			key := "node_modules/" + definition.PackageName
			entry := lock.Packages[key]
			entry.Link = true
			lock.Packages[key] = entry
			automationTestWriteLock(t, root, lock)
		}},
		{name: "wrong locked version", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			lock := automationTestReadLock(t, root)
			key := "node_modules/" + definition.PackageName
			entry := lock.Packages[key]
			entry.Version = "999.0.0"
			lock.Packages[key] = entry
			automationTestWriteLock(t, root, lock)
		}},
		{name: "untrusted registry", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			lock := automationTestReadLock(t, root)
			key := "node_modules/" + definition.PackageName
			entry := lock.Packages[key]
			entry.Resolved = "https://registry.npmjs.org.evil.example/package.tgz"
			lock.Packages[key] = entry
			automationTestWriteLock(t, root, lock)
		}},
		{name: "registry port", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			lock := automationTestReadLock(t, root)
			key := "node_modules/" + definition.PackageName
			entry := lock.Packages[key]
			entry.Resolved = "https://registry.npmjs.org:443/package.tgz"
			lock.Packages[key] = entry
			automationTestWriteLock(t, root, lock)
		}},
		{name: "missing resolved", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			lock := automationTestReadLock(t, root)
			key := "node_modules/" + definition.PackageName
			entry := lock.Packages[key]
			entry.Resolved = ""
			lock.Packages[key] = entry
			automationTestWriteLock(t, root, lock)
		}},
		{name: "sha256 integrity", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			lock := automationTestReadLock(t, root)
			key := "node_modules/" + definition.PackageName
			entry := lock.Packages[key]
			entry.Integrity = "sha256-" + base64.StdEncoding.EncodeToString(make([]byte, 32))
			lock.Packages[key] = entry
			automationTestWriteLock(t, root, lock)
		}},
		{name: "malformed sha512 integrity", mutate: func(t *testing.T, root string, definition automationToolDefinition) {
			lock := automationTestReadLock(t, root)
			key := "node_modules/" + definition.PackageName
			entry := lock.Packages[key]
			entry.Integrity = "sha512-not-base64"
			lock.Packages[key] = entry
			automationTestWriteLock(t, root, lock)
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeValidAutomationTestInstallation(t, root, definition)
			testCase.mutate(t, root, definition)
			if _, err := validateAutomationToolInstallation(root, definition); err == nil {
				t.Fatal("expected unsafe package metadata to be rejected")
			}
		})
	}

	t.Run("symlinked bin", func(t *testing.T) {
		root := t.TempDir()
		writeValidAutomationTestInstallation(t, root, definition)
		binPath := automationTestBinPath(root, definition)
		if err := os.Remove(binPath); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.js")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, binPath); err != nil {
			t.Skipf("symbolic links are unavailable in this environment: %v", err)
		}
		if _, err := validateAutomationToolInstallation(root, definition); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic") {
			t.Fatalf("expected symlink rejection, got %v", err)
		}
	})

	t.Run("symlinked dependency", func(t *testing.T) {
		root := t.TempDir()
		writeValidAutomationTestInstallation(t, root, definition)
		outside := t.TempDir()
		dependencyPath := filepath.Join(root, "node_modules", "linked-dependency")
		if err := os.Symlink(outside, dependencyPath); err != nil {
			t.Skipf("symbolic links are unavailable in this environment: %v", err)
		}
		if _, err := validateAutomationToolInstallation(root, definition); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic") {
			t.Fatalf("expected dependency symlink rejection, got %v", err)
		}
	})

	t.Run("symlinked root", func(t *testing.T) {
		realRoot := t.TempDir()
		writeValidAutomationTestInstallation(t, realRoot, definition)
		alias := filepath.Join(t.TempDir(), "managed-link")
		if err := os.Symlink(realRoot, alias); err != nil {
			t.Skipf("symbolic links are unavailable in this environment: %v", err)
		}
		if _, err := validateAutomationToolInstallation(alias, definition); err == nil || !strings.Contains(strings.ToLower(err.Error()), "real directory") {
			t.Fatalf("expected root symlink rejection, got %v", err)
		}
	})
}

func TestAutomationToolCatalogFailedInstallPreservesExistingVersion(t *testing.T) {
	definition := automationTestDefinition(t, "playwright-mcp")
	for _, testCase := range []struct {
		name      string
		installFn func(*testing.T, string, automationToolDefinition) error
	}{
		{name: "npm failure", installFn: func(_ *testing.T, _ string, _ automationToolDefinition) error {
			return errors.New("offline fake failure")
		}},
		{name: "validation failure", installFn: func(t *testing.T, root string, definition automationToolDefinition) error {
			writeValidAutomationTestInstallation(t, root, definition)
			manifest := automationTestReadManifest(t, root, definition)
			manifest["version"] = "wrong"
			automationTestWriteManifest(t, root, definition, manifest)
			return nil
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			homeDir := t.TempDir()
			target := filepath.Join(homeDir, "optional-tools", "automation", definition.ID)
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(target, "existing-version.txt")
			if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			catalog := NewAutomationToolCatalog(homeDir, nil, AutomationToolCatalogOptions{
				GOOS:     "windows",
				LookPath: func(name string) (string, error) { return filepath.Join(homeDir, "runtime", name), nil },
				RunCommand: func(_ context.Context, command AutomationToolCommand) (AutomationToolCommandResult, error) {
					if reflect.DeepEqual(command.Args, []string{"--version"}) {
						return AutomationToolCommandResult{Output: "v22.0.0"}, nil
					}
					prefix := automationTestArgValue(command.Args, "--prefix")
					err := testCase.installFn(t, prefix, definition)
					return AutomationToolCommandResult{Output: "fake npm"}, err
				},
			})
			if _, err := catalog.Install(context.Background(), definition.ID); err == nil {
				t.Fatal("expected fake installation failure")
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "keep" {
				t.Fatalf("existing installation was damaged: err=%v data=%q", err, data)
			}
			entries, err := os.ReadDir(filepath.Dir(target))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != definition.ID {
				t.Fatalf("failed installation left staging or backup paths: %+v", entries)
			}
		})
	}
}

func TestAutomationToolCatalogConfigureCreatesDisabledStableMCPAndPreservesEdits(t *testing.T) {
	ctx := context.Background()
	homeDir := t.TempDir()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	definition := automationTestDefinition(t, "chrome-devtools-mcp")
	managedPath := filepath.Join(homeDir, "optional-tools", "automation", definition.ID)
	writeValidAutomationTestInstallation(t, managedPath, definition)
	nodePath := filepath.Join(homeDir, "runtime", "node.exe")
	catalog := NewAutomationToolCatalog(homeDir, store, AutomationToolCatalogOptions{
		GOOS: "windows",
		LookPath: func(name string) (string, error) {
			if name == "node" || name == "npm" {
				return filepath.Join(homeDir, "runtime", name+".exe"), nil
			}
			return "", errors.New("not found")
		},
		RunCommand: func(context.Context, AutomationToolCommand) (AutomationToolCommandResult, error) {
			t.Fatal("configuration must not execute Node, npm, or MCP")
			return AutomationToolCommandResult{}, nil
		},
	})

	unrelated, err := store.CreateMCPServer(ctx, db.MCPServer{Name: "User MCP", Command: "user-command", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	item, err := catalog.Configure(ctx, definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !item.Configured || item.Enabled || item.CanConfigure || !item.CanEnable {
		t.Fatalf("configuration must remain disabled and separately enableable: %+v", item)
	}
	server, err := store.GetMCPServer(ctx, tools.ManagedAutomationMCPServerID(definition.ID))
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := append([]string{automationTestBinPath(managedPath, definition)}, definition.mcpArgs...)
	if server.ID != item.MCPServerID || server.Enabled || server.Transport != "stdio" || server.Command != nodePath || !reflect.DeepEqual(server.Args, wantArgs) || !reflect.DeepEqual(server.Env, definition.mcpEnv) {
		t.Fatalf("unexpected managed MCP configuration: %+v wantArgs=%v", server, wantArgs)
	}

	server.Name = "User-edited managed MCP"
	server.Command = "custom-command"
	server.Args = []string{"custom-arg"}
	server.Env = map[string]string{"CUSTOM": "1"}
	server.Enabled = true
	if _, err := store.UpdateMCPServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	item, err = catalog.Configure(ctx, definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := store.GetMCPServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.Name != server.Name || preserved.Command != server.Command || !reflect.DeepEqual(preserved.Args, server.Args) || !reflect.DeepEqual(preserved.Env, server.Env) || !preserved.Enabled || !item.Enabled {
		t.Fatalf("idempotent configure overwrote explicit user edits: server=%+v item=%+v", preserved, item)
	}
	if _, err := store.GetMCPServer(ctx, unrelated.ID); err != nil {
		t.Fatalf("unrelated MCP configuration was changed or deleted: %v", err)
	}
}

func TestAutomationToolCatalogExternalCardsRemainInformational(t *testing.T) {
	catalog := NewAutomationToolCatalog(t.TempDir(), nil, AutomationToolCatalogOptions{
		GOOS:     "windows",
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
	})
	items, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(automationToolDefinitions) {
		t.Fatalf("unexpected catalog length: %d", len(items))
	}
	byID := make(map[string]AutomationToolCatalogItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	pad := byID["power-automate-desktop"]
	if pad.Name != "Power Automate Desktop" || pad.CanInstall || pad.CanConfigure || pad.CanEnable || pad.InstallMode != "external" || len(pad.Prerequisites) != 2 {
		t.Fatalf("Power Automate Desktop must remain an informational external card: %+v", pad)
	}
	computerUse := byID["computer-use"]
	if computerUse.Name != "Computer use" || computerUse.CanInstall || computerUse.CanConfigure || computerUse.CanEnable || computerUse.InstallMode != "capability" || len(computerUse.Prerequisites) != 1 || computerUse.Prerequisites[0].Available {
		t.Fatalf("Computer use must remain an unavailable capability card: %+v", computerUse)
	}
}

func automationTestDefinition(t *testing.T, id string) automationToolDefinition {
	t.Helper()
	definition, ok := automationToolDefinitionByID(id)
	if !ok {
		t.Fatalf("missing test definition %q", id)
	}
	return definition
}

func writeValidAutomationTestInstallation(t *testing.T, root string, definition automationToolDefinition) {
	t.Helper()
	manifest := map[string]any{
		"name":    definition.PackageName,
		"version": definition.Version,
		"bin":     map[string]string{definition.binName: definition.binRelative},
	}
	automationTestWriteManifest(t, root, definition, manifest)
	binPath := automationTestBinPath(root, definition)
	if err := os.MkdirAll(filepath.Dir(binPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("#!/usr/bin/env node\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))
	lock := automationPackageLock{
		LockfileVersion: 3,
		Packages: map[string]automationLockedEntry{
			"": {Dependencies: map[string]string{definition.PackageName: definition.Version}},
			"node_modules/" + definition.PackageName: {
				Name: definition.PackageName, Version: definition.Version,
				Resolved:  "https://registry.npmjs.org/" + definition.PackageName + "/-/package-" + definition.Version + ".tgz",
				Integrity: integrity,
			},
		},
	}
	automationTestWriteLock(t, root, lock)
}

func automationTestBinPath(root string, definition automationToolDefinition) string {
	return filepath.Join(root, "node_modules", filepath.FromSlash(definition.PackageName), filepath.FromSlash(definition.binRelative))
}

func automationTestManifestPath(root string, definition automationToolDefinition) string {
	return filepath.Join(root, "node_modules", filepath.FromSlash(definition.PackageName), "package.json")
}

func automationTestWriteManifest(t *testing.T, root string, definition automationToolDefinition, manifest map[string]any) {
	t.Helper()
	path := automationTestManifestPath(root, definition)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func automationTestReadManifest(t *testing.T, root string, definition automationToolDefinition) map[string]any {
	t.Helper()
	data, err := os.ReadFile(automationTestManifestPath(root, definition))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func automationTestWriteLock(t *testing.T, root string, lock automationPackageLock) {
	t.Helper()
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package-lock.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func automationTestReadLock(t *testing.T, root string) automationPackageLock {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock automationPackageLock
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	return lock
}

func automationTestArgValue(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func automationTestEnvironment(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[strings.ToUpper(strings.TrimSpace(key))] = value
		}
	}
	return result
}

func cloneAutomationTestCommand(command AutomationToolCommand) AutomationToolCommand {
	command.Args = append([]string(nil), command.Args...)
	command.Env = append([]string(nil), command.Env...)
	return command
}
