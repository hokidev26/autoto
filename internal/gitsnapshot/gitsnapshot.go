package gitsnapshot

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type StatusEntry struct {
	Path           string
	IndexStatus    string
	WorktreeStatus string
	Untracked      bool
}

// FingerprintBudget bounds checkpoint hashing work across a snapshot. It is
// intentionally per-snapshot and not safe for concurrent use.
type FingerprintBudget struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
	totalBytes    int64
}

func (b *FingerprintBudget) consume(bytes int64) error {
	if bytes < 0 {
		return errors.New("invalid fingerprint byte count")
	}
	if b != nil && b.MaxTotalBytes > 0 && b.totalBytes+bytes > b.MaxTotalBytes {
		return fmt.Errorf("checkpoint fingerprint total byte budget exceeds %d", b.MaxTotalBytes)
	}
	if b != nil {
		b.totalBytes += bytes
	}
	return nil
}

func (b *FingerprintBudget) TotalBytes() int64 {
	if b == nil {
		return 0
	}
	return b.totalBytes
}

func ParsePorcelainV1NoRenames(out string) ([]StatusEntry, error) {
	parts := strings.Split(out, "\x00")
	entries := make([]StatusEntry, 0, len(parts))
	for _, record := range parts {
		if record == "" {
			continue
		}
		if len(record) < 4 {
			return nil, errors.New("invalid git status record")
		}
		entry := StatusEntry{
			Path:           filepath.ToSlash(record[3:]),
			IndexStatus:    record[:1],
			WorktreeStatus: record[1:2],
		}
		if entry.Path == "" {
			return nil, errors.New("git status reported an empty path")
		}
		entry.Untracked = entry.IndexStatus == "?" && entry.WorktreeStatus == "?"
		entries = append(entries, entry)
	}
	return entries, nil
}

func IndexFingerprint(raw string) string {
	if raw == "" {
		return "missing"
	}
	return FingerprintData("index", 0, []byte(raw))
}

func WorktreeFingerprint(repoRoot, relativePath string) (string, error) {
	return WorktreeFingerprintWithBudget(context.Background(), repoRoot, relativePath, nil)
}

// WorktreeFingerprintWithBudget hashes a worktree entry while honoring caller
// cancellation plus per-file and aggregate byte budgets.
func WorktreeFingerprintWithBudget(ctx context.Context, repoRoot, relativePath string, budget *FingerprintBudget) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err := Path(repoRoot, relativePath)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		return FingerprintData("symlink", info.Mode(), []byte(target)), nil
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("checkpoint path is not a regular file or symlink: %s", relativePath)
	}
	if budget != nil && budget.MaxFileBytes > 0 && info.Size() > budget.MaxFileBytes {
		return "", fmt.Errorf("checkpoint fingerprint file %s exceeds %d byte budget", relativePath, budget.MaxFileBytes)
	}
	if budget != nil && budget.MaxTotalBytes > 0 && budget.totalBytes+info.Size() > budget.MaxTotalBytes {
		return "", fmt.Errorf("checkpoint fingerprint total byte budget exceeds %d", budget.MaxTotalBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return fingerprintReaderWithBudget(ctx, "file", info.Mode(), file, budget)
}

func FingerprintReader(kind string, mode os.FileMode, reader io.Reader) (string, error) {
	return fingerprintReaderWithBudget(context.Background(), kind, mode, reader, nil)
}

func fingerprintReaderWithBudget(ctx context.Context, kind string, mode os.FileMode, reader io.Reader, budget *FingerprintBudget) (string, error) {
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	var fileBytes int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		read, err := reader.Read(buffer)
		if read > 0 {
			fileBytes += int64(read)
			if budget != nil && budget.MaxFileBytes > 0 && fileBytes > budget.MaxFileBytes {
				return "", fmt.Errorf("checkpoint fingerprint file exceeds %d byte budget", budget.MaxFileBytes)
			}
			if err := budget.consume(int64(read)); err != nil {
				return "", err
			}
			if _, writeErr := hash.Write(buffer[:read]); writeErr != nil {
				return "", writeErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return fingerprintPrefix(kind, mode) + fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func FingerprintData(kind string, mode os.FileMode, data []byte) string {
	hash := sha256.New()
	_, _ = hash.Write(data)
	return fingerprintPrefix(kind, mode) + fmt.Sprintf("%x", hash.Sum(nil))
}

func Path(repoRoot, relativePath string) (string, error) {
	relativePath = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relativePath)))
	if relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("git checkpoint path escapes repository: %q", relativePath)
	}
	path := filepath.Join(repoRoot, relativePath)
	resolved, err := filepath.Rel(repoRoot, path)
	if err != nil || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) || filepath.IsAbs(resolved) {
		return "", fmt.Errorf("git checkpoint path escapes repository: %q", relativePath)
	}
	return path, nil
}

func fingerprintPrefix(kind string, mode os.FileMode) string {
	return fmt.Sprintf("%s:type=%#o:perm=%#o:", kind, uint32(mode.Type()), uint32(mode.Perm()))
}
