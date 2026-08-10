//go:build desktop && windows

package desktop

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	stableWebviewProfileDirName = "Autoto"
	stableWebviewDataDirName    = "WebView"
	seenStorageKey              = "autoto.conversationSeen"
)

// stableWebviewUserDataPath returns one profile for every desktop build. Wails'
// default on Windows is %APPDATA%\\<executable-name>, which made a file named
// autoto-desktop-integration-20260810e.exe get a fresh browser profile when the
// previous d.exe was replaced. That reset localStorage, including the user's
// read/unread conversation marks.
//
// The path is deliberately under APPDATA rather than the configurable Autoto
// home: it is WebView browser state, not server/database state, and Wails uses
// this location for its per-user desktop data. An empty path preserves Wails'
// defaults on platforms where this Windows-specific location is unavailable.
func stableWebviewUserDataPath(logger *slog.Logger) string {
	appData := strings.TrimSpace(os.Getenv("APPDATA"))
	if appData == "" {
		return ""
	}
	path := filepath.Join(appData, stableWebviewProfileDirName, stableWebviewDataDirName)
	if err := os.MkdirAll(path, 0o700); err != nil {
		logger.Warn("stable desktop WebView profile unavailable; using Wails default", "error", err)
		return ""
	}
	migrateLegacySeenState(appData, path, logger)
	return path
}

// migrateLegacySeenState copies only the localStorage LevelDB files that contain
// the seen-map key into the stable profile's LevelDB. It is intentionally narrow:
// copying an entire browser profile can import cookies, credentials, locks, cache,
// or data belonging to another version. The old profiles remain untouched.
//
// A LevelDB database is a group of .ldb/.log files. Copying the matching files is
// safe only before the stable profile has ever been opened; once it has data, the
// migration is skipped. If the old profile is locked by a running version, the
// files are still read-only copied and the target is created as a separate profile.
func migrateLegacySeenState(appData, stablePath string, logger *slog.Logger) {
	targetDir := filepath.Join(stablePath, "Default", "Local Storage", "leveldb")
	if entries, err := os.ReadDir(targetDir); err == nil && len(entries) > 0 {
		return
	}
	legacyDir, ok := newestLegacySeenLevelDB(appData)
	if !ok {
		return
	}
	if err := copyLevelDBFiles(legacyDir, targetDir); err != nil {
		logger.Warn("migrate legacy desktop seen state failed", "error", err)
		return
	}
	logger.Info("migrated desktop conversation seen state", "source", filepath.Dir(filepath.Dir(filepath.Dir(legacyDir))), "target", stablePath)
}

func newestLegacySeenLevelDB(appData string) (string, bool) {
	entries, err := os.ReadDir(appData)
	if err != nil {
		return "", false
	}
	type candidate struct {
		dir string
		mod int64
	}
	candidates := make([]candidate, 0)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "autoto-desktop-") || entry.Name() == stableWebviewProfileDirName {
			continue
		}
		levelDB := filepath.Join(appData, entry.Name(), "EBWebView", "Default", "Local Storage", "leveldb")
		if !levelDBContains(levelDB, seenStorageKey) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{dir: levelDB, mod: info.ModTime().UnixNano()})
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod > candidates[j].mod })
	return candidates[0].dir, true
}

func levelDBContains(dir, needle string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ldb") && !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		file, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 16<<20))
		_ = file.Close()
		if readErr == nil && strings.Contains(string(data), needle) {
			return true
		}
	}
	return false
}

func copyLevelDBFiles(sourceDir, targetDir string) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return err
	}
	copied := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ldb") && !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		source, err := os.Open(filepath.Join(sourceDir, entry.Name()))
		if err != nil {
			continue
		}
		target, createErr := os.OpenFile(filepath.Join(targetDir, entry.Name()), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			_ = source.Close()
			return createErr
		}
		_, copyErr := io.Copy(target, source)
		closeTargetErr := target.Close()
		closeSourceErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeTargetErr != nil {
			return closeTargetErr
		}
		if closeSourceErr != nil {
			return closeSourceErr
		}
		copied++
	}
	if copied == 0 {
		return fmt.Errorf("legacy seen database contained no LevelDB files")
	}
	return nil
}
