package preview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	KindStatic = "static"
	KindVite   = "vite"
	KindNext   = "next"

	maxPackageJSONSize       = 512 << 10
	maxScannedDirectories    = 200
	maxFingerprintFileSize   = 8 << 20
	maxFingerprintTotalBytes = 16 << 20
	portPlaceholder          = "{port}"
)

var (
	scanContainers = []string{"apps", "packages", "examples"}
	skippedDirs    = map[string]struct{}{
		"node_modules": {},
		"dist":         {},
		"build":        {},
		"vendor":       {},
	}
	viteConfigs = []string{
		"vite.config.js", "vite.config.mjs", "vite.config.cjs",
		"vite.config.ts", "vite.config.mts", "vite.config.cts",
	}
	nextConfigs = []string{
		"next.config.js", "next.config.mjs", "next.config.cjs", "next.config.ts",
	}
)

// Profile is the complete client-visible preview description. Execution details
// intentionally remain private to the backend.
type Profile struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

type detectedProfile struct {
	Profile
	workspace string
	workdir   string
	relDir    string
	argv      []string
}

type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// Detect returns only safe, server-defined preview profiles found in root and
// one directory below apps, packages, and examples.
func Detect(root string) ([]Profile, error) {
	detected, err := detectProfiles(root)
	if err != nil {
		return nil, err
	}
	profiles := make([]Profile, 0, len(detected))
	for _, item := range detected {
		profiles = append(profiles, item.Profile)
	}
	return profiles, nil
}

func detectProfiles(root string) ([]detectedProfile, error) {
	workspace, err := canonicalWorkspace(root)
	if err != nil {
		return nil, err
	}

	dirs := []string{workspace}
scanContainersLoop:
	for _, containerName := range scanContainers {
		container := filepath.Join(workspace, containerName)
		info, err := os.Lstat(container)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(container)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if shouldSkipDir(name) || entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				continue
			}
			if len(dirs) >= maxScannedDirectories {
				break scanContainersLoop
			}
			dirs = append(dirs, filepath.Join(container, name))
		}
	}

	var profiles []detectedProfile
	for _, dir := range dirs {
		found, err := detectDirectory(workspace, dir)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, found...)
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].relDir == profiles[j].relDir {
			return profiles[i].Kind < profiles[j].Kind
		}
		return profiles[i].relDir < profiles[j].relDir
	})
	return profiles, nil
}

func canonicalWorkspace(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("preview workspace is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve preview workspace: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve preview workspace: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("stat preview workspace: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("preview workspace must be a directory")
	}
	return filepath.Clean(real), nil
}

func detectDirectory(workspace, dir string) ([]detectedProfile, error) {
	relDir, err := filepath.Rel(workspace, dir)
	if err != nil {
		return nil, err
	}
	if relDir == "" {
		relDir = "."
	}

	packagePath := filepath.Join(dir, "package.json")
	pkg, packageOK, err := readPackageJSON(packagePath)
	if err != nil {
		return nil, err
	}
	lockPath, manager, err := findLockfile(workspace, dir)
	if err != nil {
		return nil, err
	}

	var profiles []detectedProfile
	indexPath := filepath.Join(dir, "index.html")
	if isRegularFile(indexPath) {
		files := []string{indexPath}
		if packageOK {
			files = append(files, packagePath)
		}
		if lockPath != "" {
			files = append(files, lockPath)
		}
		configs := append(existingFiles(dir, viteConfigs), existingFiles(dir, nextConfigs)...)
		files = append(files, configs...)
		profile, err := makeDetectedProfile(workspace, dir, relDir, KindStatic, nil, files)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}

	if !packageOK || !dynamicSupported() {
		return profiles, nil
	}

	viteConfigPaths := existingFiles(dir, viteConfigs)
	if hasDependency(pkg, "vite") || len(viteConfigPaths) > 0 {
		argv := fixedArgv(manager, KindVite)
		files := append([]string{packagePath}, viteConfigPaths...)
		if lockPath != "" {
			files = append(files, lockPath)
		}
		profile, err := makeDetectedProfile(workspace, dir, relDir, KindVite, argv, files)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}

	nextConfigPaths := existingFiles(dir, nextConfigs)
	if hasDependency(pkg, "next") || len(nextConfigPaths) > 0 {
		argv := fixedArgv(manager, KindNext)
		files := append([]string{packagePath}, nextConfigPaths...)
		if lockPath != "" {
			files = append(files, lockPath)
		}
		profile, err := makeDetectedProfile(workspace, dir, relDir, KindNext, argv, files)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func readPackageJSON(path string) (packageJSON, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return packageJSON{}, false, nil
		}
		return packageJSON{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxPackageJSONSize {
		return packageJSON{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return packageJSON{}, false, err
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return packageJSON{}, false, nil
	}
	return pkg, true, nil
}

func findLockfile(workspace, dir string) (string, string, error) {
	for current := dir; ; current = filepath.Dir(current) {
		for _, option := range []struct {
			name    string
			manager string
		}{
			{name: "pnpm-lock.yaml", manager: "pnpm"},
			{name: "yarn.lock", manager: "yarn"},
			{name: "package-lock.json", manager: "npm"},
		} {
			candidate := filepath.Join(current, option.name)
			if isRegularFile(candidate) {
				return candidate, option.manager, nil
			}
		}
		if samePath(current, workspace) {
			break
		}
		parent := filepath.Dir(current)
		if !withinPath(workspace, parent) {
			break
		}
	}
	return "", "npm", nil
}

func fixedArgv(manager, kind string) []string {
	var argv []string
	switch manager {
	case "pnpm":
		argv = []string{"pnpm", "exec"}
	case "yarn":
		argv = []string{"yarn", "exec"}
	default:
		argv = []string{"npm", "exec", "--yes=false", "--"}
	}
	if kind == KindVite {
		return append(argv, "vite", "--host", "127.0.0.1", "--port", portPlaceholder, "--strictPort")
	}
	return append(argv, "next", "dev", "-H", "127.0.0.1", "-p", portPlaceholder)
}

func makeDetectedProfile(workspace, dir, relDir, kind string, argv, files []string) (detectedProfile, error) {
	id, err := fingerprintProfile(workspace, relDir, kind, argv, files)
	if err != nil {
		return detectedProfile{}, err
	}
	location := filepath.ToSlash(relDir)
	if location == "." {
		location = "workspace"
	}
	return detectedProfile{
		Profile: Profile{
			ID:    id,
			Label: profileLabel(kind) + " (" + location + ")",
			Kind:  kind,
		},
		workspace: workspace,
		workdir:   dir,
		relDir:    relDir,
		argv:      append([]string(nil), argv...),
	}, nil
}

func fingerprintProfile(workspace, relDir, kind string, argv, files []string) (string, error) {
	hash := sha256.New()
	writeFingerprintPart(hash, "preview-v1")
	writeFingerprintPart(hash, kind)
	writeFingerprintPart(hash, filepath.ToSlash(relDir))
	for _, arg := range argv {
		writeFingerprintPart(hash, arg)
	}

	unique := make(map[string]struct{}, len(files))
	ordered := make([]string, 0, len(files))
	for _, file := range files {
		clean := filepath.Clean(file)
		if _, ok := unique[clean]; ok {
			continue
		}
		unique[clean] = struct{}{}
		ordered = append(ordered, clean)
	}
	sort.Strings(ordered)
	var totalBytes int64
	for _, file := range ordered {
		rel, err := filepath.Rel(workspace, file)
		if err != nil || !withinPath(workspace, file) {
			return "", errors.New("preview fingerprint file is outside workspace")
		}
		info, err := os.Lstat(file)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxFingerprintFileSize || totalBytes+info.Size() > maxFingerprintTotalBytes {
			return "", errors.New("preview fingerprint input is too large or invalid")
		}
		writeFingerprintPart(hash, filepath.ToSlash(rel))
		input, err := os.Open(file)
		if err != nil {
			return "", err
		}
		copied, copyErr := io.Copy(hash, io.LimitReader(input, maxFingerprintFileSize+1))
		closeErr := input.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if copied > maxFingerprintFileSize || totalBytes+copied > maxFingerprintTotalBytes {
			return "", errors.New("preview fingerprint input is too large")
		}
		totalBytes += copied
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeFingerprintPart(dst io.Writer, value string) {
	_, _ = io.WriteString(dst, value)
	_, _ = io.WriteString(dst, "\x00")
}

func profileLabel(kind string) string {
	switch kind {
	case KindVite:
		return "Vite"
	case KindNext:
		return "Next.js"
	default:
		return "Static"
	}
}

func hasDependency(pkg packageJSON, name string) bool {
	_, dependency := pkg.Dependencies[name]
	_, devDependency := pkg.DevDependencies[name]
	return dependency || devDependency
}

func existingFiles(dir string, names []string) []string {
	files := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		if isRegularFile(path) {
			files = append(files, path)
		}
	}
	return files
}

func isRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular()
}

func shouldSkipDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	_, skip := skippedDirs[strings.ToLower(name)]
	return skip
}

func withinPath(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func samePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}
