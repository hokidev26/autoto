// Package spill persists oversized tool output outside the model's context.
// The model receives a bounded preview plus the path written here and reads the
// rest back with the ordinary Read and Grep tools, so no retrieval tool of its
// own is needed.
package spill

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Retention is deliberately generous. A spilled file is referenced by a tool
// result that stays in the conversation transcript, so deleting it early turns
// a readable locator into a dead path; a week outlives the point where context
// compaction has dropped the referencing result from every live request.
const Retention = 7 * 24 * time.Hour

// conversationIDPattern matches the identifiers Autoto uses for agents. The
// directory name is derived from caller input, so anything else is rejected
// rather than sanitized: a silently rewritten owner would let two conversations
// share a directory.
var conversationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

var ErrInvalidConversation = errors.New("invalid spill conversation id")

type Store struct {
	root string
	now  func() time.Time
}

func RootForHome(homeDir string) string {
	return filepath.Join(homeDir, "data", "tool_output_spill")
}

func New(homeDir string) (*Store, error) {
	if strings.TrimSpace(homeDir) == "" {
		return nil, errors.New("tool output spill home directory is required")
	}
	root, err := filepath.Abs(RootForHome(homeDir))
	if err != nil {
		return nil, fmt.Errorf("resolve tool output spill root: %w", err)
	}
	if err := ensurePrivateDir(root); err != nil {
		return nil, err
	}
	return &Store{root: root, now: time.Now}, nil
}

func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// Save writes content verbatim and answers with the absolute path. The file is
// created exclusively under a random name so two concurrent tool results can
// never land on the same path, and 0600/0700 because tool output routinely
// carries command output, environment values, and other secrets.
func (s *Store) Save(conversationID, toolName, content string) (string, error) {
	if s == nil {
		return "", errors.New("tool output spill store is nil")
	}
	conversationID = strings.TrimSpace(conversationID)
	if !conversationIDPattern.MatchString(conversationID) {
		return "", ErrInvalidConversation
	}
	dir := filepath.Join(s.root, conversationID)
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	prefix := fileNamePrefix(toolName)
	for attempt := 0; attempt < 8; attempt++ {
		suffix, err := randomSuffix()
		if err != nil {
			return "", fmt.Errorf("name tool output spill file: %w", err)
		}
		path := filepath.Join(dir, prefix+"-"+suffix+".txt")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create tool output spill file: %w", err)
		}
		if _, err := file.WriteString(content); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return "", fmt.Errorf("write tool output spill file: %w", err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("close tool output spill file: %w", err)
		}
		return path, nil
	}
	return "", errors.New("could not allocate a tool output spill file name")
}

// Prune removes spill files older than maxAge and any conversation directory
// left empty behind them, and reports how many files it deleted. Cleanup is
// age-based because Autoto never deletes a conversation, so there is no owner
// lifecycle to hang deletion off; without this the tree grows for as long as
// the home directory survives.
func (s *Store) Prune(maxAge time.Duration) (int, error) {
	if s == nil {
		return 0, nil
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := s.now().Add(-maxAge)
	removed := 0
	var failures error
	for _, entry := range entries {
		if !entry.IsDir() || !conversationIDPattern.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(s.root, entry.Name())
		count, err := pruneConversation(dir, cutoff)
		removed += count
		failures = errors.Join(failures, err)
	}
	return removed, failures
}

func pruneConversation(dir string, cutoff time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	remaining := 0
	var failures error
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			remaining++
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			remaining++
			failures = errors.Join(failures, err)
			continue
		}
		removed++
	}
	if remaining == 0 {
		// Remove is used rather than RemoveAll so a directory that gained a file
		// between the scan and here survives.
		_ = os.Remove(dir)
	}
	return removed, failures
}

var fileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func fileNamePrefix(toolName string) string {
	prefix := fileNameUnsafe.ReplaceAllString(strings.TrimSpace(toolName), "_")
	prefix = strings.Trim(prefix, ".")
	if prefix == "" {
		return "tool"
	}
	if len(prefix) > 48 {
		prefix = prefix[:48]
	}
	return prefix
}

func randomSuffix() (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func ensurePrivateDir(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("tool output spill directory %s is not a real directory", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create tool output spill directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure tool output spill directory: %w", err)
	}
	return nil
}
