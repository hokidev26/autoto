package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

var (
	ErrConflict      = errors.New("conflict")
	ErrInvalidCursor = errors.New("invalid cursor")
)

func sqliteDSN(path string) string {
	// modernc.org/sqlite expects a file URI. On Windows, Path must be
	// "/C:/..." so String() becomes "file:///C:/..." rather than "file:C:/..."
	// (which is mis-parsed and breaks pragma/bootstrap on empty DBs).
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if filepath.IsAbs(path) && !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	fileURL := &url.URL{Scheme: "file", Opaque: "", Path: cleaned}
	query := fileURL.Query()
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	// A run performs dozens of small committing writes (messages, tool call
	// audit rows, run bookkeeping). The default rollback journal with
	// synchronous=FULL costs an fsync plus journal delete per commit, which is
	// tens of milliseconds each and dominates run latency. WAL with
	// synchronous=NORMAL keeps commits durable against process crashes and only
	// risks the most recent commits on host power loss, which this local
	// workspace database can rebuild from its next run.
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(NORMAL)")
	fileURL.RawQuery = query.Encode()
	return fileURL.String()
}

func Open(ctx context.Context, path string) (*Store, error) {
	path = filepath.Clean(path)
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite path: %w", err)
	}
	path = absolutePath
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := secureSQLiteFile(path, true, false); err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	store := &Store{db: database}
	if err := store.migrate(ctx); err != nil {
		database.Close()
		return nil, err
	}
	if err := store.ensureRuntimeSettings(ctx); err != nil {
		database.Close()
		return nil, err
	}
	if err := store.revalidateSkills(ctx); err != nil {
		database.Close()
		return nil, err
	}
	if err := secureSQLiteFiles(path); err != nil {
		database.Close()
		return nil, err
	}
	return store, nil
}

func secureSQLiteFiles(path string) error {
	if err := secureSQLiteFile(path, false, false); err != nil {
		return err
	}
	for _, candidate := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if err := secureSQLiteFile(candidate, false, true); err != nil {
			return err
		}
	}
	return nil
}

func secureSQLiteFile(path string, create, missingOK bool) error {
	initial, err := os.Lstat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect SQLite file %s: %w", path, err)
		}
		if !create {
			if missingOK {
				return nil
			}
			return fmt.Errorf("inspect SQLite file %s: %w", path, err)
		}
	} else if err := validateSQLiteFileInfo(path, initial); err != nil {
		return err
	}

	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if missingOK && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open SQLite file %s: %w", path, err)
	}
	defer file.Close()

	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened SQLite file %s: %w", path, err)
	}
	if err := validateSQLiteFileInfo(path, opened); err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		if missingOK && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("reinspect SQLite file %s: %w", path, err)
	}
	if err := validateSQLiteFileInfo(path, current); err != nil {
		return err
	}
	if !os.SameFile(opened, current) {
		return fmt.Errorf("SQLite file %s changed while being opened", path)
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure SQLite file %s: %w", path, err)
	}
	return nil
}

func validateSQLiteFileInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("SQLite file %s must not be a symbolic link", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("SQLite file %s must be a regular file", path)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate(ctx context.Context) error {
	return runMigrations(ctx, s.db)
}

var (
	nowMu   sync.Mutex
	lastNow time.Time
)

// timestampLayout keeps a fixed-width fractional second so stored timestamps
// sort lexically in the same order as they do chronologically. time.RFC3339Nano
// strips trailing zeros, which makes "…:05Z" sort after "…:05.000000001Z" and
// silently reorders every ORDER BY created_at query and keyset cursor.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

func Now() string {
	nowMu.Lock()
	defer nowMu.Unlock()
	now := time.Now().UTC()
	if !now.After(lastNow) {
		now = lastNow.Add(time.Nanosecond)
	}
	lastNow = now
	return now.Format(timestampLayout)
}

func NewID() string { return uuid.NewString() }

func nullEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed: unique")
}

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func IsConflict(err error) bool {
	return errors.Is(err, ErrConflict)
}

func WrapNotFound(name, id string, err error) error {
	if IsNotFound(err) {
		return fmt.Errorf("%s not found: %s", name, id)
	}
	return err
}
