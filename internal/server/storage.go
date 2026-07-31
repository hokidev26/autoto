package server

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
)

const storageScanLimit = 5000

type storageSummaryResponse struct {
	GeneratedAt     string         `json:"generatedAt"`
	ScanLimit       int            `json:"scanLimit"`
	TotalKnownBytes int64          `json:"totalKnownBytes"`
	Entries         []storageEntry `json:"entries"`
}

type storageEntry struct {
	Key            string `json:"key"`
	Label          string `json:"label"`
	Path           string `json:"path"`
	Exists         bool   `json:"exists"`
	IsDir          bool   `json:"isDir"`
	SizeBytes      int64  `json:"sizeBytes"`
	FileCount      int64  `json:"fileCount"`
	DirectoryCount int64  `json:"directoryCount"`
	EntriesScanned int64  `json:"entriesScanned"`
	Truncated      bool   `json:"truncated"`
	Error          string `json:"error,omitempty"`
}

// storageSummaryCache holds a single cached result. The cache is keyed by a
// snapshot of the four configured paths; any change to the config naturally
// produces a different key and therefore a fresh scan.
type storageSummaryCache struct {
	mu       sync.Mutex
	key      string
	result   storageSummaryResponse
	cachedAt time.Time

	// singleflight: at most one scan runs at a time per path key.
	inflight map[string]*storageSummaryFlight
}

type storageSummaryFlight struct {
	done   chan struct{}
	result storageSummaryResponse
}

// storageSummaryCacheTTL is how long a cached result is considered fresh.
// Requests within this window reuse the previous scan without touching the disk.
const storageSummaryCacheTTL = 30 * time.Second

// storageSummaryKey builds an opaque string that uniquely identifies the
// combination of paths used for a scan.
func storageSummaryKey(cfg config.Config, configPath string) string {
	return strings.Join([]string{
		cfg.Paths.HomeDir,
		cfg.Paths.DatabasePath,
		configPath,
		cfg.Paths.DefaultProjectDir,
	}, "\x00")
}

func (s *Server) storageSummary(w http.ResponseWriter, r *http.Request) {
	cfg := s.configSnapshot()
	configPath := s.configPathSnapshot()
	summary := s.buildStorageSummaryWithCache(r.Context(), cfg, configPath, storageScanLimit)
	writeJSON(w, http.StatusOK, summary)
}

// buildStorageSummaryWithCache returns a cached result if the paths are
// unchanged and the entry is still fresh; otherwise it coalesces concurrent
// requests into a single scan.
func (s *Server) buildStorageSummaryWithCache(ctx context.Context, cfg config.Config, configPath string, scanLimit int) storageSummaryResponse {
	key := storageSummaryKey(cfg, effectiveConfigPath(cfg, configPath))
	now := time.Now()

	s.storageCache.mu.Lock()
	if s.storageCache.key == key && now.Sub(s.storageCache.cachedAt) < storageSummaryCacheTTL {
		result := s.storageCache.result
		s.storageCache.mu.Unlock()
		return result
	}
	// Check or create a singleflight entry.
	if s.storageCache.inflight == nil {
		s.storageCache.inflight = make(map[string]*storageSummaryFlight)
	}
	if flight, ok := s.storageCache.inflight[key]; ok {
		// Another goroutine is already scanning; wait for it.
		s.storageCache.mu.Unlock()
		select {
		case <-flight.done:
			return flight.result
		case <-ctx.Done():
			return buildStorageSummary(ctx, cfg, configPath, scanLimit)
		}
	}
	flight := &storageSummaryFlight{done: make(chan struct{})}
	s.storageCache.inflight[key] = flight
	s.storageCache.mu.Unlock()

	result := buildStorageSummary(ctx, cfg, configPath, scanLimit)

	s.storageCache.mu.Lock()
	flight.result = result
	if s.storageCache.inflight[key] == flight {
		delete(s.storageCache.inflight, key)
	}
	// Only commit to the cache when the context is still live (cancelled
	// contexts may have received a degraded partial scan).
	if ctx.Err() == nil {
		s.storageCache.key = key
		s.storageCache.result = result
		s.storageCache.cachedAt = now
	}
	s.storageCache.mu.Unlock()
	close(flight.done)
	return result
}

func (s *Server) configPathSnapshot() string {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.configPath
}

// buildStorageSummary scans the four storage roots in parallel (max 2
// concurrent workers) and deduplicates bytes when paths are nested.
func buildStorageSummary(ctx context.Context, cfg config.Config, configPath string, scanLimit int) storageSummaryResponse {
	if scanLimit <= 0 {
		scanLimit = storageScanLimit
	}
	resolvedConfigPath := effectiveConfigPath(cfg, configPath)

	type scanWork struct {
		key   string
		label string
		path  string
		isDB  bool
	}
	work := []scanWork{
		{key: "home", label: "Autoto home", path: cfg.Paths.HomeDir},
		{key: "database", label: "SQLite database", path: cfg.Paths.DatabasePath, isDB: true},
		{key: "config", label: "Config file", path: resolvedConfigPath},
		{key: "projects", label: "Default project directory", path: cfg.Paths.DefaultProjectDir},
	}

	entries := make([]storageEntry, len(work))

	// Bounded concurrency: at most 2 directory scans run simultaneously so a
	// large projects tree cannot saturate the disk alongside the home scan.
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for i, w := range work {
		wg.Add(1)
		go func(idx int, sw scanWork) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if sw.isDB {
				entries[idx] = scanDatabaseFiles(sw.key, sw.label, sw.path)
			} else {
				entries[idx] = scanStoragePath(ctx, sw.key, sw.label, sw.path, scanLimit)
			}
		}(i, w)
	}
	wg.Wait()

	// Deduplicate TotalKnownBytes: when a path is nested inside another path
	// that was already scanned (e.g. database or config inside HomeDir), its
	// bytes are already counted by the parent scan and must not be added again.
	// Paths that had errors or were not found are excluded from both checks.
	homePath := filepath.Clean(cfg.Paths.HomeDir)
	total := int64(0)
	for i, entry := range entries {
		if entry.SizeBytes == 0 || !entry.Exists || entry.Error != "" {
			total += entry.SizeBytes
			continue
		}
		// Home entry always contributes its own bytes.
		if i == 0 {
			total += entry.SizeBytes
			continue
		}
		// For all other entries, skip if the path is nested inside HomeDir
		// (meaning it was already counted there) and the home entry itself was
		// successfully scanned.
		entryClean := ""
		if entry.Path != "" {
			entryClean = filepath.Clean(entry.Path)
		}
		homeEntry := entries[0]
		if entryClean != "" && homePath != "" && homeEntry.Exists && homeEntry.Error == "" &&
			isNestedPath(entryClean, homePath) {
			// already counted in homeDir scan
			continue
		}
		total += entry.SizeBytes
	}

	return storageSummaryResponse{GeneratedAt: db.Now(), ScanLimit: scanLimit, TotalKnownBytes: total, Entries: entries}
}

// isNestedPath reports whether child is equal to or nested inside parent.
// Both paths must already be cleaned with filepath.Clean.
func isNestedPath(child, parent string) bool {
	if child == parent {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(child, parent+sep)
}

func effectiveConfigPath(cfg config.Config, configPath string) string {
	if configPath != "" {
		return configPath
	}
	if cfg.Paths.HomeDir != "" {
		return filepath.Join(cfg.Paths.HomeDir, "config.json")
	}
	return ""
}

func scanDatabaseFiles(key, label, path string) storageEntry {
	entry := storageEntry{Key: key, Label: label, Path: path}
	if path == "" {
		entry.Error = "path is not configured"
		return entry
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Lstat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			entry.Error = err.Error()
			continue
		}
		entry.Exists = true
		entry.FileCount++
		entry.EntriesScanned++
		if info.IsDir() {
			entry.DirectoryCount++
			continue
		}
		entry.SizeBytes += info.Size()
	}
	return entry
}

func scanStoragePath(ctx context.Context, key, label, path string, scanLimit int) storageEntry {
	entry := storageEntry{Key: key, Label: label, Path: path}
	if path == "" {
		entry.Error = "path is not configured"
		return entry
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return entry
		}
		entry.Error = err.Error()
		return entry
	}
	entry.Exists = true
	entry.IsDir = info.IsDir()
	if !info.IsDir() {
		entry.FileCount = 1
		entry.EntriesScanned = 1
		entry.SizeBytes = info.Size()
		return entry
	}

	walkErr := filepath.WalkDir(path, func(current string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry.Error == "" {
				entry.Error = walkErr.Error()
			}
			return nil
		}
		select {
		case <-ctx.Done():
			entry.Error = ctx.Err().Error()
			return ctx.Err()
		default:
		}
		if entry.EntriesScanned >= int64(scanLimit) {
			entry.Truncated = true
			if dirEntry.IsDir() && current != path {
				return filepath.SkipDir
			}
			return fs.SkipAll
		}
		entry.EntriesScanned++
		if current == path {
			entry.DirectoryCount++
			return nil
		}
		info, err := dirEntry.Info()
		if err != nil {
			if entry.Error == "" {
				entry.Error = err.Error()
			}
			return nil
		}
		if info.IsDir() {
			entry.DirectoryCount++
			return nil
		}
		entry.FileCount++
		entry.SizeBytes += info.Size()
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) && entry.Error == "" {
		entry.Error = walkErr.Error()
	}
	return entry
}
