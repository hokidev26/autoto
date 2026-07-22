package imageassets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	MaxPNGBytes  int64 = 10 << 20
	MaxDimension       = 8192
	MaxPixels    int64 = 32_000_000
)

var (
	pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	storageKeyRE = regexp.MustCompile(`^objects/([0-9a-f]{2})/([0-9a-f]{64})\.png$`)

	ErrInvalidPNG  = errors.New("invalid generated PNG")
	ErrInvalidKey  = errors.New("invalid generated image storage key")
	ErrUnavailable = errors.New("generated image is unavailable")
)

type Asset struct {
	StorageKey string
	SHA256     string
	ByteSize   int64
	Width      int
	Height     int
}

type Expected struct {
	SHA256   string
	ByteSize int64
	Width    int
	Height   int
}

type CleanupReport struct {
	Removed []string
	Failed  map[string]error
}

type Store struct {
	root       string
	objectsDir string
	stagingDir string
	now        func() time.Time
}

func RootForHome(homeDir string) string {
	return filepath.Join(homeDir, "data", "generated_images")
}

func New(homeDir string) (*Store, error) {
	if strings.TrimSpace(homeDir) == "" {
		return nil, errors.New("generated image home directory is required")
	}
	root, err := filepath.Abs(RootForHome(homeDir))
	if err != nil {
		return nil, fmt.Errorf("resolve generated image root: %w", err)
	}
	s := &Store{
		root:       root,
		objectsDir: filepath.Join(root, "objects"),
		stagingDir: filepath.Join(root, "staging"),
		now:        time.Now,
	}
	for _, dir := range []string{s.root, s.objectsDir, s.stagingDir} {
		if err := ensurePrivateDir(dir); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (s *Store) PutPNG(data []byte) (Asset, error) {
	if s == nil {
		return Asset{}, errors.New("generated image store is nil")
	}
	if err := s.validateBaseDirs(); err != nil {
		return Asset{}, err
	}
	asset, err := inspectPNG(data)
	if err != nil {
		return Asset{}, err
	}
	asset.StorageKey = fmt.Sprintf("objects/%s/%s.png", asset.SHA256[:2], asset.SHA256)
	destination, err := s.resolveKey(asset.StorageKey)
	if err != nil {
		return Asset{}, err
	}
	if err := ensurePrivateDir(filepath.Dir(destination)); err != nil {
		return Asset{}, err
	}
	if _, err := os.Lstat(destination); err == nil {
		file, openErr := s.Open(asset.StorageKey, asset.Expected())
		if openErr != nil {
			return Asset{}, openErr
		}
		_ = file.Close()
		return asset, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Asset{}, fmt.Errorf("inspect generated image destination: %w", err)
	}

	temporary, err := os.CreateTemp(s.stagingDir, ".publish-*.png")
	if err != nil {
		return Asset{}, fmt.Errorf("create generated image staging file: %w", err)
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		_ = temporary.Close()
		if !published {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return Asset{}, fmt.Errorf("secure generated image staging file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return Asset{}, fmt.Errorf("write generated image staging file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return Asset{}, fmt.Errorf("sync generated image staging file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Asset{}, fmt.Errorf("close generated image staging file: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		// Content-addressed deduplication can race. Windows will not replace an
		// existing destination, so accept only an independently verified winner.
		if file, openErr := s.Open(asset.StorageKey, asset.Expected()); openErr == nil {
			_ = file.Close()
			return asset, nil
		}
		return Asset{}, fmt.Errorf("publish generated image: %w", err)
	}
	published = true
	if err := os.Chmod(destination, 0o600); err != nil {
		return Asset{}, fmt.Errorf("secure generated image object: %w", err)
	}
	bestEffortSyncDir(filepath.Dir(destination))
	return asset, nil
}

func (a Asset) Expected() Expected {
	return Expected{SHA256: a.SHA256, ByteSize: a.ByteSize, Width: a.Width, Height: a.Height}
}

// Open verifies the controlled key, path containment, file type, content hash,
// PNG structure and dimensions before returning a handle positioned at byte 0.
func (s *Store) Open(storageKey string, expected Expected) (*os.File, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	path, err := s.resolveKey(storageKey)
	if err != nil {
		return nil, err
	}
	if err := s.validateObjectParents(path); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := validateRegularFile(path, before); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxPNGBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	asset, err := inspectPNG(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	matches := storageKey == fmt.Sprintf("objects/%s/%s.png", asset.SHA256[:2], asset.SHA256) &&
		asset.SHA256 == strings.ToLower(expected.SHA256) && asset.ByteSize == expected.ByteSize &&
		asset.Width == expected.Width && asset.Height == expected.Height
	if !matches {
		return nil, ErrUnavailable
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	ok = true
	return file, nil
}

func (s *Store) CleanupStaging(maxAge time.Duration) (CleanupReport, error) {
	return s.cleanupTree(s.stagingDir, maxAge, nil, false)
}

func (s *Store) MarkAndSweep(referenced map[string]struct{}, gracePeriod time.Duration) (CleanupReport, error) {
	return s.cleanupTree(s.objectsDir, gracePeriod, referenced, true)
}

func (s *Store) cleanupTree(root string, maxAge time.Duration, referenced map[string]struct{}, objects bool) (CleanupReport, error) {
	report := CleanupReport{Failed: make(map[string]error)}
	cutoff := s.now().Add(-maxAge)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			report.Failed[path] = err
			return nil
		}
		if !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			return nil
		}
		key := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if objects {
			key = "objects/" + key
			if !validStorageKey(key) {
				return nil
			}
			if _, ok := referenced[key]; ok {
				return nil
			}
		}
		if err := os.Remove(path); err != nil {
			report.Failed[key] = err
			return nil
		}
		report.Removed = append(report.Removed, key)
		return nil
	})
	if len(report.Failed) == 0 {
		report.Failed = nil
	}
	return report, err
}

func inspectPNG(data []byte) (Asset, error) {
	if int64(len(data)) > MaxPNGBytes || !bytes.HasPrefix(data, pngSignature) {
		return Asset{}, ErrInvalidPNG
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" || config.Width <= 0 || config.Height <= 0 ||
		config.Width > MaxDimension || config.Height > MaxDimension || int64(config.Width)*int64(config.Height) > MaxPixels {
		return Asset{}, ErrInvalidPNG
	}
	digest := sha256.Sum256(data)
	return Asset{
		SHA256:   hex.EncodeToString(digest[:]),
		ByteSize: int64(len(data)),
		Width:    config.Width,
		Height:   config.Height,
	}, nil
}

func validStorageKey(storageKey string) bool {
	matches := storageKeyRE.FindStringSubmatch(storageKey)
	return len(matches) == 3 && matches[1] == matches[2][:2]
}

func (s *Store) resolveKey(storageKey string) (string, error) {
	if !validStorageKey(storageKey) || filepath.IsAbs(storageKey) || filepath.VolumeName(storageKey) != "" || strings.Contains(storageKey, `\`) {
		return "", ErrInvalidKey
	}
	path := filepath.Join(s.root, filepath.FromSlash(storageKey))
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", ErrInvalidKey
	}
	return path, nil
}

func (s *Store) validateBaseDirs() error {
	for _, dir := range []string{s.root, s.objectsDir, s.stagingDir} {
		info, err := os.Lstat(dir)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrUnavailable
		}
	}
	return nil
}

func (s *Store) validateObjectParents(path string) error {
	for _, dir := range []string{s.root, s.objectsDir, filepath.Dir(path)} {
		info, err := os.Lstat(dir)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrUnavailable
		}
	}
	return nil
}

func ensurePrivateDir(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("generated image directory %s is not a real directory", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create generated image directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure generated image directory: %w", err)
	}
	return nil
}

func validateRegularFile(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrUnavailable, path)
	}
	return nil
}

func bestEffortSyncDir(path string) {
	dir, err := os.Open(path)
	if err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
}
