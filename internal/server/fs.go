package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"autoto/internal/process"
)

type fsEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

func (s *Server) fsBrowse(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveFSPathForRequest(r, r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		writeError(w, statusFromFSError(err), err.Error())
		return
	}
	items := make([]fsEntry, 0, len(entries))
	for _, entry := range entries {
		childPath := filepath.Join(path, entry.Name())
		resolvedChild, err := s.resolveFSPathForRequest(r, childPath)
		if err != nil { // Do not expose metadata for symlinks outside the project boundary.
			continue
		}
		info, err := os.Stat(resolvedChild)
		if err != nil {
			continue
		}
		items = append(items, fsEntry{Name: entry.Name(), Path: childPath, IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime().UTC().Format(http.TimeFormat)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "entries": items})
}

type fsDirectoryShortcut struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Server) fsDirectories(w http.ResponseWriter, r *http.Request) {
	defaultProjectDir := s.configSnapshot().Paths.DefaultProjectDir
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		path = defaultDirectoryRoot(defaultProjectDir)
	}
	var abs string
	var err error
	capabilities := s.capabilitiesForRequest(r)
	if capabilities.FilesystemScope == "project" {
		abs, err = s.resolveFSPathForRequest(r, path)
	} else {
		abs, err = s.resolveFSPathForRequest(r, path)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		writeError(w, statusFromFSError(err), err.Error())
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "path must be a directory")
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		writeError(w, statusFromFSError(err), err.Error())
		return
	}
	items := make([]fsEntry, 0, len(entries))
	remote := capabilities.FilesystemScope == "project"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		childPath := filepath.Join(abs, entry.Name())
		if remote {
			resolved, err := s.resolveFSPathForRequest(r, childPath)
			if err != nil {
				continue
			}
			childPath = resolved
		}
		info, err := os.Stat(childPath)
		if err != nil || !info.IsDir() {
			continue
		}
		items = append(items, fsEntry{Name: entry.Name(), Path: childPath, IsDir: true, Size: info.Size(), ModTime: info.ModTime().UTC().Format(http.TimeFormat)})
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	parent := ""
	if parentDir := filepath.Dir(abs); parentDir != abs {
		if !remote {
			parent = parentDir
		} else if resolvedParent, err := s.resolveFSPathForRequest(r, parentDir); err == nil {
			parent = resolvedParent
		}
	}
	shortcuts := directoryShortcuts(defaultProjectDir, s.fsHostScopeForRequest(r))
	if remote {
		base, _ := s.resolveFSPathForRequest(r, "")
		shortcuts = []fsDirectoryShortcut{{Name: "Projects", Path: base}}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      abs,
		"name":      filepath.Base(abs),
		"parent":    parent,
		"entries":   items,
		"shortcuts": shortcuts,
	})
}

func (s *Server) fsNativeDirectory(w http.ResponseWriter, r *http.Request) {
	capabilities := s.capabilitiesForRequest(r)
	if s.remoteAccessGateRequired(r) && !capabilities.NativePickerAllowed {
		writeError(w, http.StatusForbidden, "native directory selection requires a full remote session and policy approval")
		return
	}
	defaultProjectDir := s.configSnapshot().Paths.DefaultProjectDir
	defaultPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if defaultPath == "" {
		defaultPath = defaultDirectoryRoot(defaultProjectDir)
	}
	if abs, err := filepath.Abs(defaultPath); err == nil {
		if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
			defaultPath = abs
		} else {
			defaultPath = defaultDirectoryRoot(defaultProjectDir)
		}
	}

	// Prefer the desktop shell host when registered (cross-platform Wails dialogs).
	if host := s.shellDialog(); host != nil {
		path, canceled, err := host.PickDirectory(r.Context(), "选择 Autoto 工作资料夹", defaultPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "原生资料夹选择器打开失败："+err.Error())
			return
		}
		if canceled || strings.TrimSpace(path) == "" {
			writeJSON(w, http.StatusOK, map[string]any{"canceled": true})
			return
		}
		path = filepath.Clean(strings.TrimSpace(path))
		info, err := os.Stat(path)
		if err != nil {
			writeError(w, statusFromFSError(err), err.Error())
			return
		}
		if !info.IsDir() {
			writeError(w, http.StatusBadRequest, "path must be a directory")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": path, "name": filepath.Base(path), "canceled": false})
		return
	}

	// Browser/CLI fallback without a desktop shell host: drive each OS's own
	// native folder dialog directly. macOS uses AppleScript; Windows uses a
	// PowerShell FolderBrowserDialog. Other platforms have no native fallback
	// here and must use the built-in directory browser.
	var path string
	switch runtime.GOOS {
	case "windows":
		picked, canceled, err := pickDirectoryPowerShell(r.Context(), defaultPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "原生资料夹选择器打开失败："+err.Error())
			return
		}
		if canceled {
			writeJSON(w, http.StatusOK, map[string]any{"canceled": true})
			return
		}
		path = picked
	case "darwin":
		script := `set chosenFolder to choose folder with prompt "选择 Autoto 工作资料夹"`
		if defaultPath != "" {
			script = `set defaultFolder to POSIX file ` + appleScriptString(defaultPath) + ` as alias
set chosenFolder to choose folder with prompt "选择 Autoto 工作资料夹" default location defaultFolder`
		}
		script += "\nPOSIX path of chosenFolder"

		output, err := exec.CommandContext(r.Context(), "osascript", "-e", script).CombinedOutput()
		if err != nil {
			message := strings.TrimSpace(string(output))
			if strings.Contains(message, "User canceled") || strings.Contains(message, "-128") {
				writeJSON(w, http.StatusOK, map[string]any{"canceled": true})
				return
			}
			if message == "" {
				message = err.Error()
			}
			writeError(w, http.StatusInternalServerError, "原生资料夹选择器打开失败："+message)
			return
		}
		path = filepath.Clean(strings.TrimSpace(string(output)))
	default:
		writeError(w, http.StatusNotImplemented, "当前系统暂不支持原生资料夹选择器，请使用内置目录浏览器")
		return
	}

	if path == "." || path == "" {
		writeError(w, http.StatusInternalServerError, "原生资料夹选择器没有返回路径")
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeError(w, statusFromFSError(err), err.Error())
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "path must be a directory")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "name": filepath.Base(path), "canceled": false})
}

func appleScriptString(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// pickDirectoryPowerShell shows the native Windows folder picker via a
// PowerShell FolderBrowserDialog. It returns the selected path, whether the
// user canceled, and any launch error. FolderBrowserDialog requires an STA
// thread, so the child process is started with -STA.
func pickDirectoryPowerShell(ctx context.Context, defaultPath string) (string, bool, error) {
	script := `Add-Type -AssemblyName System.Windows.Forms | Out-Null
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = '选择 Autoto 工作资料夹'
$dialog.ShowNewFolderButton = $true`
	if strings.TrimSpace(defaultPath) != "" {
		script += "\n$dialog.SelectedPath = " + powerShellSingleQuote(defaultPath)
	}
	script += `
$owner = New-Object System.Windows.Forms.Form
$owner.TopMost = $true
if ($dialog.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($dialog.SelectedPath) }
$owner.Dispose()`

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-STA", "-Command", script)
	// The dialog is the only window this should show. Without this, PowerShell
	// also allocates a console, so picking a folder flashes a black window next
	// to the picker.
	process.HideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", false, errors.New(message)
	}
	path := strings.TrimSpace(stdout.String())
	if path == "" {
		return "", true, nil
	}
	return filepath.Clean(path), false, nil
}

// powerShellSingleQuote wraps a value in a single-quoted PowerShell literal,
// doubling embedded single quotes so the path cannot break out of the string.
func powerShellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func defaultDirectoryRoot(defaultProjectDir string) string {
	if defaultProjectDir != "" {
		if info, err := os.Stat(defaultProjectDir); err == nil && info.IsDir() {
			return defaultProjectDir
		}
		// The project sandbox and the folder picker root must agree. If the
		// configured projects dir is missing, create it instead of falling
		// back to $HOME (which then fails as "path escapes default project
		// directory" under resolveFSPath).
		if err := os.MkdirAll(defaultProjectDir, 0o755); err == nil {
			if info, err := os.Stat(defaultProjectDir); err == nil && info.IsDir() {
				return defaultProjectDir
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return string(filepath.Separator)
}

// filesystemRoots lists the roots worth offering as picker shortcuts: every
// mounted drive letter on Windows, otherwise the single filesystem root.
func filesystemRoots() []string {
	if runtime.GOOS != "windows" {
		return []string{string(filepath.Separator)}
	}
	roots := make([]string, 0, 24)
	for letter := 'C'; letter <= 'Z'; letter++ {
		root := string(letter) + `:\`
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			roots = append(roots, root)
		}
	}
	return roots
}

func directoryShortcuts(defaultProjectDir string, hostScope bool) []fsDirectoryShortcut {
	shortcuts := make([]fsDirectoryShortcut, 0, 12)
	base := strings.TrimSpace(defaultProjectDir)
	if base != "" {
		if info, err := os.Stat(base); err != nil || !info.IsDir() {
			_ = os.MkdirAll(base, 0o755)
		}
	}
	add := func(name, path string) {
		if path == "" {
			return
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			// Project-scoped browsing cannot leave DefaultProjectDir, so hide
			// host-wide shortcuts that would only surface as load failures. A
			// host-scoped session reaches them, so keep them.
			if !hostScope && base != "" {
				baseAbs, err := filepath.Abs(base)
				if err != nil {
					return
				}
				pathAbs, err := filepath.Abs(path)
				if err != nil || !fsPathWithin(baseAbs, pathAbs) {
					return
				}
			}
			shortcuts = append(shortcuts, fsDirectoryShortcut{Name: name, Path: path})
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add("Home", home)
		add("Desktop", filepath.Join(home, "Desktop"))
		add("Downloads", filepath.Join(home, "Downloads"))
		add("Documents", filepath.Join(home, "Documents"))
	}
	add("Projects", defaultProjectDir)
	for _, root := range filesystemRoots() {
		add(root, root)
	}
	return shortcuts
}

func (s *Server) fsPreview(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolveFSPathForRequest(r, r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeError(w, statusFromFSError(err), err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	const maxPreviewBytes = 256 * 1024
	file, err := os.Open(path)
	if err != nil {
		writeError(w, statusFromFSError(err), err.Error())
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxPreviewBytes+1))
	if err != nil {
		writeError(w, statusFromFSError(err), err.Error())
		return
	}
	truncated := len(data) > maxPreviewBytes
	if truncated {
		data = data[:maxPreviewBytes]
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "size": info.Size(), "truncated": truncated, "text": string(data)})
}

type mkdirRequest struct {
	Path string `json:"path"`
}

func (s *Server) fsMkdir(w http.ResponseWriter, r *http.Request) {
	var req mkdirRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	path, err := s.resolveFSPathForRequest(r, req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		writeError(w, statusFromFSError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}

func (s *Server) fsBasePath() string {
	cfg := s.configSnapshot()
	base := cfg.Paths.DefaultProjectDir
	if base == "" {
		base = cfg.Paths.HomeDir
	}
	// Keep sandbox root aligned with the directory picker default.
	if strings.TrimSpace(base) != "" {
		if info, err := os.Stat(base); err != nil || !info.IsDir() {
			_ = os.MkdirAll(base, 0o755)
		}
	}
	return base
}

// resolveFSPath checks physical containment after resolving symlinks. For paths
// that do not exist yet, it resolves the nearest existing ancestor so mkdir and
// write-adjacent operations cannot escape through an in-project symlink.
func (s *Server) resolveFSPath(input string) (string, error) {
	baseAbs, err := filepath.Abs(s.fsBasePath())
	if err != nil {
		return "", err
	}
	baseReal, err := resolvePhysicalFSPath(baseAbs)
	if err != nil {
		return "", err
	}
	path := input
	if path == "" {
		path = baseReal
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseReal, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := resolvePhysicalFSPath(abs)
	if err != nil {
		return "", err
	}
	if !fsPathWithin(baseReal, resolved) {
		return "", errors.New("path escapes default project directory")
	}
	return resolved, nil
}

func resolvePhysicalFSPath(path string) (string, error) {
	path = filepath.Clean(path)
	missing := make([]string, 0)
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}

func fsPathWithin(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func statusFromFSError(err error) int {
	if errors.Is(err, os.ErrNotExist) {
		return http.StatusNotFound
	}
	if errors.Is(err, os.ErrPermission) {
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}
