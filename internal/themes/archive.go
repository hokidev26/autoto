package themes

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxArchiveBytes   = 20 << 20
	MaxArchiveEntries = 64
	MaxExtractedBytes = 40 << 20
	MaxImageBytes     = 8 << 20
)

// ImportResult reports the installed theme plus the update semantics: whether
// an existing install was replaced and, if so, which version it carried. The
// UI uses this to say "updated v1.0.0 → v1.1.0" instead of a bare "imported".
type ImportResult struct {
	Theme           Theme  `json:"theme"`
	Replaced        bool   `json:"replaced"`
	PreviousVersion string `json:"previousVersion,omitempty"`
}

// Import validates and installs a .autoto-theme ZIP stream.
func (store *Store) Import(reader io.Reader, options ImportOptions) (ImportResult, error) {
	if store == nil {
		return ImportResult{}, errors.New("theme store is unavailable")
	}
	if reader == nil {
		return ImportResult{}, fmt.Errorf("%w: archive reader is required", ErrInvalidArchive)
	}
	if _, err := secureDirectory(store.root); err != nil {
		return ImportResult{}, err
	}
	archiveFile, err := os.CreateTemp(store.root, ".upload-*.autoto-theme")
	if err != nil {
		return ImportResult{}, fmt.Errorf("create bounded theme upload: %w", err)
	}
	archivePath := archiveFile.Name()
	defer os.Remove(archivePath)
	if err := archiveFile.Chmod(0o600); err != nil {
		archiveFile.Close()
		return ImportResult{}, fmt.Errorf("protect theme upload: %w", err)
	}
	written, err := io.Copy(archiveFile, io.LimitReader(reader, MaxArchiveBytes+1))
	if err != nil {
		archiveFile.Close()
		return ImportResult{}, fmt.Errorf("read theme archive: %w", err)
	}
	if written > MaxArchiveBytes {
		archiveFile.Close()
		return ImportResult{}, fmt.Errorf("%w: archive exceeds %d bytes", ErrInvalidArchive, MaxArchiveBytes)
	}
	if err := archiveFile.Sync(); err != nil {
		archiveFile.Close()
		return ImportResult{}, fmt.Errorf("flush theme upload: %w", err)
	}
	if _, err := archiveFile.Seek(0, io.SeekStart); err != nil {
		archiveFile.Close()
		return ImportResult{}, fmt.Errorf("rewind theme upload: %w", err)
	}
	zipReader, err := zip.NewReader(archiveFile, written)
	if err != nil {
		archiveFile.Close()
		return ImportResult{}, fmt.Errorf("%w: open ZIP: %v", ErrInvalidArchive, err)
	}
	if len(zipReader.File) == 0 || len(zipReader.File) > MaxArchiveEntries {
		archiveFile.Close()
		return ImportResult{}, fmt.Errorf("%w: archive entries must be between 1 and %d", ErrInvalidArchive, MaxArchiveEntries)
	}
	entries, manifestEntry, err := inspectArchive(zipReader.File)
	if err != nil {
		archiveFile.Close()
		return ImportResult{}, err
	}
	manifestBytes, err := readZipEntry(manifestEntry, MaxManifestBytes)
	if err != nil {
		archiveFile.Close()
		return ImportResult{}, fmt.Errorf("%w: manifest: %v", ErrInvalidArchive, err)
	}
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		archiveFile.Close()
		return ImportResult{}, fmt.Errorf("%w: %v", ErrInvalidArchive, err)
	}
	if err := validateArchiveDeclarations(entries, manifest); err != nil {
		archiveFile.Close()
		return ImportResult{}, err
	}
	staging, err := os.MkdirTemp(store.root, ".staging-*")
	if err != nil {
		archiveFile.Close()
		return ImportResult{}, fmt.Errorf("create theme staging directory: %w", err)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		archiveFile.Close()
		_ = os.RemoveAll(staging)
		return ImportResult{}, fmt.Errorf("protect theme staging directory: %w", err)
	}
	if err := extractArchive(staging, entries, manifest, manifestBytes); err != nil {
		archiveFile.Close()
		_ = os.RemoveAll(staging)
		return ImportResult{}, err
	}
	if err := archiveFile.Close(); err != nil {
		_ = os.RemoveAll(staging)
		return ImportResult{}, fmt.Errorf("close theme upload: %w", err)
	}
	return store.publishStaging(staging, manifest, options)
}

// InstallManifest installs a theme described entirely by its manifest, with no
// image resources. This is the theme editor's save path: the manifest passes
// exactly the same validation and publish steps as an imported archive.
func (store *Store) InstallManifest(manifest Manifest, options ImportOptions) (ImportResult, error) {
	if store == nil {
		return ImportResult{}, errors.New("theme store is unavailable")
	}
	if err := ValidateManifest(manifest); err != nil {
		return ImportResult{}, fmt.Errorf("%w: %v", ErrInvalidArchive, err)
	}
	if len(declaredResourcePaths(manifest)) != 0 {
		return ImportResult{}, fmt.Errorf("%w: a manifest-only install cannot declare image resources", ErrInvalidArchive)
	}
	if _, err := secureDirectory(store.root); err != nil {
		return ImportResult{}, err
	}
	staging, err := os.MkdirTemp(store.root, ".staging-*")
	if err != nil {
		return ImportResult{}, fmt.Errorf("create theme staging directory: %w", err)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.RemoveAll(staging)
		return ImportResult{}, fmt.Errorf("protect theme staging directory: %w", err)
	}
	if err := extractArchive(staging, nil, manifest, nil); err != nil {
		_ = os.RemoveAll(staging)
		return ImportResult{}, err
	}
	return store.publishStaging(staging, manifest, options)
}

// publishStaging hashes a fully staged theme directory and atomically installs
// it as the active revision. It owns the staging directory: on every path the
// directory is either renamed into place or removed.
func (store *Store) publishStaging(staging string, manifest Manifest, options ImportOptions) (ImportResult, error) {
	stagingOwned := true
	defer func() {
		if stagingOwned {
			_ = os.RemoveAll(staging)
		}
	}()
	revision, err := hashThemeDirectory(staging, manifest, "", nil)
	if err != nil {
		return ImportResult{}, fmt.Errorf("hash imported theme: %w", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if _, bundled := store.bundled[manifest.ID]; bundled {
		return ImportResult{}, fmt.Errorf("%w: %s", ErrBundledProtected, manifest.ID)
	}
	if _, err := secureDirectory(store.root); err != nil {
		return ImportResult{}, err
	}
	themeDir := filepath.Join(store.root, manifest.ID)
	existing, statErr := os.Lstat(themeDir)
	exists := statErr == nil
	createdThemeDir := false
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return ImportResult{}, fmt.Errorf("inspect theme destination: %w", statErr)
	}
	result := ImportResult{}
	if exists {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.IsDir() {
			return ImportResult{}, errors.New("theme destination is not a safe directory")
		}
		if !options.Replace {
			// Name the installed version so the conflict prompt can show what
			// a replace would overwrite without a second request.
			if previous, previousErr := store.loadActiveLocal(manifest.ID); previousErr == nil {
				return ImportResult{}, fmt.Errorf("%w: %s (installed version %s)", ErrConflict, manifest.ID, previous.Version)
			}
			return ImportResult{}, fmt.Errorf("%w: %s", ErrConflict, manifest.ID)
		}
		result.Replaced = true
		if previous, previousErr := store.loadActiveLocal(manifest.ID); previousErr == nil {
			result.PreviousVersion = previous.Version
		}
	} else {
		if err := os.Mkdir(themeDir, 0o700); err != nil {
			return ImportResult{}, fmt.Errorf("create theme destination: %w", err)
		}
		createdThemeDir = true
	}
	if err := os.Chmod(themeDir, 0o700); err != nil {
		return ImportResult{}, fmt.Errorf("protect theme destination: %w", err)
	}
	versionDir := filepath.Join(themeDir, revision)
	publishedVersion := false
	if info, err := os.Lstat(versionDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ImportResult{}, errors.New("theme revision destination is unsafe")
		}
		storedManifest, err := loadManifestSecure(versionDir)
		if err != nil {
			return ImportResult{}, err
		}
		storedRevision, err := hashThemeDirectory(versionDir, storedManifest, "", nil)
		if err != nil || storedRevision != revision || storedManifest.ID != manifest.ID {
			return ImportResult{}, errors.New("existing theme revision content is inconsistent")
		}
		if err := os.RemoveAll(staging); err != nil {
			return ImportResult{}, fmt.Errorf("discard duplicate theme revision: %w", err)
		}
		stagingOwned = false
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(staging, versionDir); err != nil {
			if createdThemeDir {
				_ = os.Remove(themeDir)
			}
			return ImportResult{}, fmt.Errorf("publish theme revision: %w", err)
		}
		publishedVersion = true
		stagingOwned = false
	} else {
		return ImportResult{}, fmt.Errorf("inspect theme revision destination: %w", err)
	}
	if err := writeCurrentRevision(themeDir, revision); err != nil {
		if createdThemeDir {
			_ = os.RemoveAll(themeDir)
		} else if publishedVersion {
			_ = os.RemoveAll(versionDir)
		}
		return ImportResult{}, err
	}
	theme, err := store.loadActiveLocal(manifest.ID)
	if err != nil {
		return ImportResult{}, err
	}
	result.Theme = theme
	return result, nil
}

type archiveEntry struct {
	file *zip.File
	name string
	dir  bool
}

func inspectArchive(files []*zip.File) ([]archiveEntry, *zip.File, error) {
	entries := make([]archiveEntry, 0, len(files))
	seen := make(map[string]string, len(files))
	var manifest *zip.File
	var declaredSize uint64
	for _, file := range files {
		name, isDir, err := validateArchivePath(file)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrInvalidArchive, err)
		}
		key := strings.ToLower(strings.TrimSuffix(name, "/"))
		if previous, duplicate := seen[key]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate normalized path %q and %q", ErrInvalidArchive, previous, name)
		}
		seen[key] = name
		if !isDir {
			if file.UncompressedSize64 > uint64(MaxExtractedBytes)-declaredSize {
				return nil, nil, fmt.Errorf("%w: declared extracted content exceeds %d bytes", ErrInvalidArchive, MaxExtractedBytes)
			}
			declaredSize += file.UncompressedSize64
		}
		if name == ManifestFilename {
			if isDir {
				return nil, nil, fmt.Errorf("%w: manifest must be a regular file", ErrInvalidArchive)
			}
			manifest = file
		}
		entries = append(entries, archiveEntry{file: file, name: name, dir: isDir})
	}
	if manifest == nil {
		return nil, nil, fmt.Errorf("%w: archive must contain %s at its root", ErrInvalidArchive, ManifestFilename)
	}
	return entries, manifest, nil
}

func validateArchivePath(file *zip.File) (string, bool, error) {
	name := file.Name
	if name == "" || len(name) > 300 || !utf8.ValidString(name) || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || path.IsAbs(name) {
		return "", false, fmt.Errorf("unsafe archive path %q", name)
	}
	isDir := strings.HasSuffix(name, "/")
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" || path.Clean(trimmed) != trimmed {
		return "", false, fmt.Errorf("archive path %q is not normalized", name)
	}
	for _, component := range strings.Split(trimmed, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") {
			return "", false, fmt.Errorf("archive path %q contains an unsafe or hidden component", name)
		}
	}
	mode := file.Mode()
	if mode&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("archive path %q is a symlink", name)
	}
	if isDir {
		if mode&os.ModeType != 0 && !mode.IsDir() {
			return "", false, fmt.Errorf("archive path %q is not a directory", name)
		}
		return trimmed + "/", true, nil
	}
	if !mode.IsRegular() {
		return "", false, fmt.Errorf("archive path %q is not a regular file", name)
	}
	return trimmed, false, nil
}

func validateArchiveDeclarations(entries []archiveEntry, manifest Manifest) error {
	allowedFiles := map[string]struct{}{
		ManifestFilename: {},
		LicenseFilename:  {},
	}
	allowedDirs := make(map[string]struct{})
	for _, resourcePath := range declaredResourcePaths(manifest) {
		allowedFiles[resourcePath] = struct{}{}
		for parent := path.Dir(resourcePath); parent != "."; parent = path.Dir(parent) {
			allowedDirs[parent+"/"] = struct{}{}
		}
	}
	for _, entry := range entries {
		if entry.dir {
			if _, ok := allowedDirs[entry.name]; !ok {
				return fmt.Errorf("%w: undeclared directory %q", ErrInvalidArchive, entry.name)
			}
			continue
		}
		if _, ok := allowedFiles[entry.name]; !ok {
			return fmt.Errorf("%w: undeclared file %q", ErrInvalidArchive, entry.name)
		}
		if entry.name != ManifestFilename && entry.name != LicenseFilename {
			if _, err := normalizeResourcePath(entry.name); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidArchive, err)
			}
		}
	}
	for _, required := range declaredResourcePaths(manifest) {
		found := false
		for _, entry := range entries {
			if !entry.dir && entry.name == required {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: declared resource %q is missing", ErrInvalidArchive, required)
		}
	}
	return nil
}

func extractArchive(staging string, entries []archiveEntry, manifest Manifest, originalManifest []byte) error {
	canonicalManifest, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode normalized theme manifest: %w", err)
	}
	canonicalManifest = append(canonicalManifest, '\n')
	if err := os.WriteFile(filepath.Join(staging, ManifestFilename), canonicalManifest, 0o600); err != nil {
		return fmt.Errorf("write staged theme manifest: %w", err)
	}
	actualTotal := int64(len(originalManifest))
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, entry := range entries {
		if entry.name == ManifestFilename {
			continue
		}
		destination := filepath.Join(staging, filepath.FromSlash(strings.TrimSuffix(entry.name, "/")))
		if !pathWithin(staging, destination) {
			return fmt.Errorf("%w: archive path escapes staging", ErrInvalidArchive)
		}
		if entry.dir {
			if err := os.MkdirAll(destination, 0o700); err != nil {
				return fmt.Errorf("create staged theme directory: %w", err)
			}
			if err := os.Chmod(destination, 0o700); err != nil {
				return fmt.Errorf("protect staged theme directory: %w", err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return fmt.Errorf("create staged theme resource parent: %w", err)
		}
		remaining := int64(MaxExtractedBytes) - actualTotal
		if remaining <= 0 {
			return fmt.Errorf("%w: extracted content exceeds %d bytes", ErrInvalidArchive, MaxExtractedBytes)
		}
		limit := remaining
		if entry.name != LicenseFilename && limit > MaxImageBytes {
			limit = MaxImageBytes
		}
		data, err := readZipEntry(entry.file, limit)
		if err != nil {
			return fmt.Errorf("%w: resource %q: %v", ErrInvalidArchive, entry.name, err)
		}
		actualTotal += int64(len(data))
		if actualTotal > MaxExtractedBytes {
			return fmt.Errorf("%w: extracted content exceeds %d bytes", ErrInvalidArchive, MaxExtractedBytes)
		}
		if entry.name != LicenseFilename {
			if _, err := validateImageBytes(entry.name, data); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidArchive, err)
			}
		}
		if err := os.WriteFile(destination, data, 0o600); err != nil {
			return fmt.Errorf("write staged theme resource: %w", err)
		}
		if err := os.Chmod(destination, 0o600); err != nil {
			return fmt.Errorf("protect staged theme resource: %w", err)
		}
	}
	return nil
}

func readZipEntry(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("exceeds %d bytes", limit)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("exceeds %d bytes", limit)
	}
	return data, nil
}

func writeCurrentRevision(themeDir, revision string) error {
	current, err := os.CreateTemp(themeDir, ".current-*")
	if err != nil {
		return fmt.Errorf("create active revision pointer: %w", err)
	}
	tempName := current.Name()
	defer os.Remove(tempName)
	if err := current.Chmod(0o600); err != nil {
		current.Close()
		return fmt.Errorf("protect active revision pointer: %w", err)
	}
	if _, err := io.Copy(current, bytes.NewBufferString(revision+"\n")); err != nil {
		current.Close()
		return fmt.Errorf("write active revision pointer: %w", err)
	}
	if err := current.Sync(); err != nil {
		current.Close()
		return fmt.Errorf("flush active revision pointer: %w", err)
	}
	if err := current.Close(); err != nil {
		return fmt.Errorf("close active revision pointer: %w", err)
	}
	if err := os.Rename(tempName, filepath.Join(themeDir, "current")); err != nil {
		return fmt.Errorf("activate theme revision atomically: %w", err)
	}
	return nil
}
