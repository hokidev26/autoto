// Package appearanceassets stores the user-selected global appearance image.
package appearanceassets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"autoto/internal/themes"
)

const (
	MaxImageBytes   = themes.MaxImageBytes
	CurrentFilename = "current.json"
)

var (
	ErrNotFound = errors.New("appearance background not found")
	ErrInvalid  = errors.New("invalid appearance background")
)

type Metadata struct {
	Revision    string `json:"revision"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	URL         string `json:"url"`
}

type Resource struct {
	io.ReadSeekCloser
	Metadata Metadata
	ModTime  time.Time
}

type pointer struct {
	Revision    string `json:"revision"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

type Store struct {
	mu   sync.RWMutex
	root string
}

func New(homeDir string) (*Store, error) {
	if strings.TrimSpace(homeDir) == "" {
		return nil, errors.New("home directory is required")
	}
	absolute, err := filepath.Abs(homeDir)
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create home directory: %w", err)
	}
	realHome, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve home directory symlinks: %w", err)
	}
	root := filepath.Join(realHome, "appearance", "backgrounds")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create appearance background root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect appearance background root: %w", err)
	}
	if _, err := secureDirectory(root); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (s *Store) Current() (Metadata, error) {
	if s == nil {
		return Metadata{}, errors.New("appearance background store is unavailable")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, err := s.readPointerLocked()
	if errors.Is(err, os.ErrNotExist) {
		return Metadata{}, ErrNotFound
	}
	if err != nil {
		return Metadata{}, err
	}
	return s.metadataFromPointer(p), nil
}

func (s *Store) Import(reader io.Reader, filename string) (Metadata, error) {
	if s == nil {
		return Metadata{}, errors.New("appearance background store is unavailable")
	}
	if reader == nil {
		return Metadata{}, fmt.Errorf("%w: reader is required", ErrInvalid)
	}
	filename, err := normalizeFilename(filename)
	if err != nil {
		return Metadata{}, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxImageBytes+1))
	if err != nil {
		return Metadata{}, fmt.Errorf("read appearance background: %w", err)
	}
	if len(data) > MaxImageBytes {
		return Metadata{}, fmt.Errorf("%w: image exceeds %d bytes", ErrInvalid, MaxImageBytes)
	}
	details, err := themes.ValidateImageDetails(filename, data)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	digest := sha256.Sum256(data)
	revision := hex.EncodeToString(digest[:])
	p := pointer{
		Revision: revision, Filename: filename, ContentType: details.ContentType,
		Size: int64(len(data)), Width: details.Width, Height: details.Height,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := secureDirectory(s.root); err != nil {
		return Metadata{}, err
	}
	objectName, err := storageObjectName(revision, details.ContentType)
	if err != nil {
		return Metadata{}, err
	}
	object := filepath.Join(s.root, objectName)
	if existing, err := os.Lstat(object); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return Metadata{}, errors.New("appearance background object is unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Metadata{}, err
	} else {
		temp, err := os.CreateTemp(s.root, ".upload-*")
		if err != nil {
			return Metadata{}, fmt.Errorf("create appearance background upload: %w", err)
		}
		tempName := temp.Name()
		defer os.Remove(tempName)
		if err := temp.Chmod(0o600); err != nil {
			temp.Close()
			return Metadata{}, err
		}
		if _, err := temp.Write(data); err != nil {
			temp.Close()
			return Metadata{}, fmt.Errorf("write appearance background: %w", err)
		}
		if err := temp.Sync(); err != nil {
			temp.Close()
			return Metadata{}, fmt.Errorf("flush appearance background: %w", err)
		}
		if err := temp.Close(); err != nil {
			return Metadata{}, err
		}
		if err := os.Rename(tempName, object); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return Metadata{}, fmt.Errorf("publish appearance background: %w", err)
			}
		}
		_ = os.Chmod(object, 0o600)
	}
	if err := s.writePointerLocked(p); err != nil {
		return Metadata{}, err
	}
	return s.metadataFromPointer(p), nil
}

func (s *Store) Delete() error {
	if s == nil {
		return errors.New("appearance background store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readPointerLocked(); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return s.removePointerLocked()
}

func (s *Store) OpenResource(revision, filename string) (*Resource, error) {
	if s == nil {
		return nil, errors.New("appearance background store is unavailable")
	}
	if !validRevision(revision) {
		return nil, ErrNotFound
	}
	normalized, err := normalizeFilename(filename)
	if err != nil || normalized != filename {
		return nil, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, err := s.readPointerLocked()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	objectName, err := storageObjectName(revision, p.ContentType)
	if err != nil {
		return nil, ErrNotFound
	}
	if p.Revision != revision || (filename != objectName && filename != p.Filename) {
		return nil, ErrNotFound
	}
	file, info, err := openRegularWithin(s.root, objectName)
	if errors.Is(err, os.ErrNotExist) {
		legacyName := revision + filepath.Ext(p.Filename)
		if legacyName != objectName {
			file, info, err = openRegularWithin(s.root, legacyName)
		}
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxImageBytes+1))
	file.Close()
	if err != nil {
		return nil, err
	}
	if len(data) > MaxImageBytes {
		return nil, ErrNotFound
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != revision {
		return nil, ErrNotFound
	}
	details, err := themes.ValidateImageDetails(p.Filename, data)
	if err != nil || details.ContentType != p.ContentType || details.Width != p.Width || details.Height != p.Height || int64(len(data)) != p.Size {
		return nil, ErrNotFound
	}
	metadata := s.metadataFromPointer(p)
	return &Resource{ReadSeekCloser: &memoryReadSeekCloser{Reader: bytes.NewReader(data)}, Metadata: metadata, ModTime: info.ModTime()}, nil
}

func (s *Store) metadataFromPointer(p pointer) Metadata {
	objectName, _ := storageObjectName(p.Revision, p.ContentType)
	return Metadata{
		Revision: p.Revision, Filename: p.Filename, ContentType: p.ContentType,
		Size: p.Size, Width: p.Width, Height: p.Height,
		URL: "/appearance/backgrounds/" + p.Revision + "/" + objectName,
	}
}

func (s *Store) readPointerLocked() (pointer, error) {
	var p pointer
	file, _, err := openRegularWithin(s.root, CurrentFilename)
	if err != nil {
		return p, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return p, err
	}
	if len(data) > 4096 {
		return p, errors.New("appearance background pointer is too large")
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("decode appearance background pointer: %w", err)
	}
	if !validRevision(p.Revision) {
		return p, errors.New("appearance background pointer revision is invalid")
	}
	if _, err := normalizeFilename(p.Filename); err != nil {
		return p, err
	}
	if _, err := storageObjectName(p.Revision, p.ContentType); err != nil || p.Size <= 0 || p.Width <= 0 || p.Height <= 0 {
		return p, errors.New("appearance background pointer metadata is invalid")
	}
	return p, nil
}

func (s *Store) writePointerLocked(p pointer) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.root, ".current-*")
	if err != nil {
		return fmt.Errorf("create appearance background pointer: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return fmt.Errorf("write appearance background pointer: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, filepath.Join(s.root, CurrentFilename)); err != nil {
		return fmt.Errorf("activate appearance background pointer: %w", err)
	}
	return os.Chmod(filepath.Join(s.root, CurrentFilename), 0o600)
}

func (s *Store) removePointerLocked() error {
	if err := os.Remove(filepath.Join(s.root, CurrentFilename)); err != nil {
		return err
	}
	return nil
}

func normalizeFilename(filename string) (string, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" || !utf8.ValidString(filename) || utf8.RuneCountInString(filename) > 120 || filepath.IsAbs(filename) || filepath.Base(filename) != filename || strings.ContainsAny(filename, `/\\`) || strings.HasPrefix(filename, ".") || hasWindowsDrivePrefix(filename) {
		return "", fmt.Errorf("%w: filename is unsafe", ErrInvalid)
	}
	for _, r := range filename {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: filename contains unsafe characters", ErrInvalid)
		}
	}
	if _, err := allowedImageExtension(filename); err != nil {
		return "", err
	}
	return filename, nil
}

func allowedImageExtension(filename string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
		return ext, nil
	default:
		return "", fmt.Errorf("%w: filename extension must be png, jpg, jpeg, or webp", ErrInvalid)
	}
}

func storageObjectName(revision, contentType string) (string, error) {
	if !validRevision(revision) {
		return "", errors.New("appearance background revision is invalid")
	}
	var extension string
	switch contentType {
	case "image/png":
		extension = ".png"
	case "image/jpeg":
		extension = ".jpg"
	case "image/webp":
		extension = ".webp"
	default:
		return "", errors.New("appearance background content type is invalid")
	}
	return revision + extension, nil
}

func hasWindowsDrivePrefix(filename string) bool {
	if len(filename) < 2 || filename[1] != ':' {
		return false
	}
	first := filename[0]
	return (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')
}

func validRevision(revision string) bool {
	if len(revision) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(revision)
	return err == nil && strings.ToLower(revision) == revision
}

func secureDirectory(directory string) (string, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("appearance background path is not a safe directory")
	}
	return filepath.EvalSymlinks(directory)
}

func openRegularWithin(base, relative string) (*os.File, os.FileInfo, error) {
	resolvedBase, err := secureDirectory(base)
	if err != nil {
		return nil, nil, err
	}
	if relative == "" || filepath.IsAbs(relative) || filepath.Base(relative) != relative {
		return nil, nil, errors.New("appearance background file path is invalid")
	}
	cursor := filepath.Join(resolvedBase, relative)
	info, err := os.Lstat(cursor)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, errors.New("appearance background file is not a stable regular file")
	}
	file, err := os.Open(cursor)
	if err != nil {
		return nil, nil, err
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !stat.Mode().IsRegular() || !os.SameFile(info, stat) {
		file.Close()
		return nil, nil, errors.New("appearance background file changed")
	}
	return file, stat, nil
}

type memoryReadSeekCloser struct{ *bytes.Reader }

func (r *memoryReadSeekCloser) Close() error { return nil }
