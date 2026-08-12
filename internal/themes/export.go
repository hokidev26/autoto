package themes

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// ExportArchive packages an installed theme back into a .autoto-theme ZIP.
// The archive contains the canonical manifest plus every declared resource,
// so re-importing it reproduces the same content revision. Bundled themes can
// be exported too: that is the natural starting point for someone editing a
// copy of a built-in theme.
func (store *Store) ExportArchive(id string) ([]byte, string, error) {
	if store == nil {
		return nil, "", errors.New("theme store is unavailable")
	}
	if !validID(id) {
		return nil, "", ErrNotFound
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if bundled, ok := store.bundled[id]; ok {
		theme, err := materializeBundled(bundled)
		if err != nil {
			return nil, "", err
		}
		archive, err := buildThemeArchive(theme.Manifest, func(resourcePath string) ([]byte, error) {
			data, ok := bundled.resources[resourcePath]
			if !ok {
				return nil, fmt.Errorf("bundled theme resource %s is missing", resourcePath)
			}
			return data, nil
		}, nil)
		if err != nil {
			return nil, "", err
		}
		return archive, exportFilename(theme.Manifest), nil
	}
	theme, err := store.loadActiveLocal(id)
	if err != nil {
		return nil, "", err
	}
	versionDir, err := store.secureVersionDirectory(id, theme.Revision)
	if err != nil {
		return nil, "", err
	}
	readResource := func(resourcePath string) ([]byte, error) {
		file, _, err := openRegularWithin(versionDir, resourcePath)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return readBoundedImage(file)
	}
	// The license participates in the content revision, so an export that
	// dropped it would re-import as a different theme than the one on disk.
	var license []byte
	if licenseFile, _, err := openRegularWithin(versionDir, LicenseFilename); err == nil {
		license, err = readBoundedImage(licenseFile)
		licenseFile.Close()
		if err != nil {
			return nil, "", fmt.Errorf("read theme license: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	archive, err := buildThemeArchive(theme.Manifest, readResource, license)
	if err != nil {
		return nil, "", err
	}
	return archive, exportFilename(theme.Manifest), nil
}

func exportFilename(manifest Manifest) string {
	return manifest.ID + "-" + manifest.Version + ".autoto-theme"
}

func buildThemeArchive(manifest Manifest, readResource func(string) ([]byte, error), license []byte) ([]byte, error) {
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode theme manifest: %w", err)
	}
	canonical = append(canonical, '\n')
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	writeEntry := func(name string, data []byte) error {
		entry, err := writer.Create(name)
		if err != nil {
			return fmt.Errorf("create archive entry %s: %w", name, err)
		}
		if _, err := entry.Write(data); err != nil {
			return fmt.Errorf("write archive entry %s: %w", name, err)
		}
		return nil
	}
	if err := writeEntry(ManifestFilename, canonical); err != nil {
		return nil, err
	}
	if license != nil {
		if err := writeEntry(LicenseFilename, license); err != nil {
			return nil, err
		}
	}
	paths := declaredResourcePaths(manifest)
	sort.Strings(paths)
	for _, resourcePath := range paths {
		data, err := readResource(resourcePath)
		if err != nil {
			return nil, fmt.Errorf("read theme resource %s: %w", resourcePath, err)
		}
		if err := writeEntry(resourcePath, data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finalize theme archive: %w", err)
	}
	return buffer.Bytes(), nil
}
