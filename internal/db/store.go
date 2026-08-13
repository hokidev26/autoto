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
	// db is the writer pool, pinned to a single connection. Every write, every
	// transaction that writes, and every read that must observe uncommitted
	// state of an open transaction goes through it. See the comment in Open.
	db *sql.DB
	// readDB is a small read-only pool (each connection runs
	// PRAGMA query_only=1). Confirmed pure-read hot paths route through it via
	// reader()/ReadDB() so UI reads no longer queue behind streaming writes.
	readDB *sql.DB

	// Domain substores. Their methods are promoted, so the exported *Store API
	// stays store.X(...) with the same names and signatures.
	*accountPreferenceStore
	*agentRoleStore
	*agentRuntimeSnapshotStore
	*apiRequestStore
	*backendStore
	*backgroundTaskStore
	*channelStore
	*contextAskStore
	*deviceActionStore
	*executionStore
	*gatewayStore
	*generatedImageStore
	*integrationStore
	*lifecycleHookStore
	*liveSnapshotStore
	*mcpStore
	*memoryStore
	*messageStore
	*modelAggregateStore
	*notificationStore
	*oauthAppStore
	*planStore
	*pluginStore
	*projectStore
	*promptStore
	*providerAccountStore
	*providerSecretStore
	*remoteCollaborationStore
	*runStore
	*runtimeSettingsStore
	*scheduleStore
	*skillStore
	*specStore
	*storedDefStore
	*toolAvailabilityStore
	*toolCallStore
	*toolExecutionGroupStore
	*userStore
}

var (
	ErrConflict      = errors.New("conflict")
	ErrInvalidCursor = errors.New("invalid cursor")
)

// sqliteFileURL builds the file URI both DSNs share. On Windows, Path must be
// "/C:/..." so String() becomes "file:///C:/..." rather than "file:C:/..."
// (which is mis-parsed and breaks pragma/bootstrap on empty DBs).
func sqliteFileURL(path string) *url.URL {
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if filepath.IsAbs(path) && !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return &url.URL{Scheme: "file", Opaque: "", Path: cleaned}
}

func sqliteDSN(path string) string {
	fileURL := sqliteFileURL(path)
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

// sqliteReadDSN is the DSN for the read-only pool. modernc.org/sqlite executes
// every _pragma parameter as a PRAGMA statement on each new connection, so
// query_only(1) is applied per connection: any write attempted through this
// pool fails with SQLITE_READONLY instead of silently succeeding. That is the
// safety net for the read/write split — a mis-routed write is a loud error.
// journal_mode/synchronous are omitted: the writer pool has already put the
// database in WAL mode by the time this pool opens, and readers do not fsync.
func sqliteReadDSN(path string) string {
	fileURL := sqliteFileURL(path)
	query := fileURL.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "query_only(1)")
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
	// The store splits reads from writes across two pools.
	//
	// Writer pool (this one, MaxOpenConns(1)): a single connection serializes
	// every write. The write paths here are compare-and-set sequences (read a
	// revision, decide, write it back) spread across many methods, and one
	// connection keeps them mutually exclusive without every one of them
	// holding an explicit transaction and handling SQLITE_BUSY retries. That
	// correctness assumption is unchanged: all writes, all transactions, and
	// any path that might read-modify-write stay on this pool.
	//
	// Read pool (readDB below): confirmed pure-read paths — the live snapshot,
	// message listing/paging, navigation and project/workline/agent Get/List
	// queries, task workspace, and the server's overview/usage statistics —
	// route through reader()/ReadDB(). Its connections run PRAGMA query_only=1,
	// so a mis-routed write fails with SQLITE_READONLY instead of corrupting
	// state. This restores the multiple-readers half of WAL: UI reads no
	// longer queue behind streaming writes.
	//
	// Routing rules for new code:
	//   - Reads that must see uncommitted state of an open write transaction
	//     must stay inside that transaction on the writer pool. Never split a
	//     write-then-read sequence that lives in one uncommitted transaction.
	//   - Read-after-commit is safe on the read pool: writes commit before
	//     their methods return, and WAL readers always see the latest
	//     committed data.
	//   - When unsure, use the writer pool; routing a read there is only a
	//     performance cost, never a correctness one.
	database.SetMaxOpenConns(1)
	store := &Store{db: database}
	wireSubstores(store)
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
	// The read pool opens after migrations so its first connection meets a
	// database that already exists and is already in WAL mode. Four readers is
	// enough for the UI surfaces that poll concurrently without inviting a
	// thundering herd on this local workload.
	readDatabase, err := sql.Open("sqlite", sqliteReadDSN(path))
	if err != nil {
		database.Close()
		return nil, err
	}
	readDatabase.SetMaxOpenConns(4)
	readDatabase.SetMaxIdleConns(4)
	if err := readDatabase.PingContext(ctx); err != nil {
		readDatabase.Close()
		database.Close()
		return nil, fmt.Errorf("open read-only SQLite pool: %w", err)
	}
	store.readDB = readDatabase
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

func (s *Store) Close() error {
	var firstErr error
	if s.readDB != nil {
		firstErr = s.readDB.Close()
	}
	if err := s.db.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// DB returns the writer pool. Existing callers depend on it accepting writes,
// so its semantics are unchanged by the read/write split.
func (s *Store) DB() *sql.DB { return s.db }

// ReadDB returns the read-only pool for callers that issue their own pure-read
// SQL (the server's overview/usage statistics). Its connections are
// query_only, so any write through it fails with SQLITE_READONLY.
func (s *Store) ReadDB() *sql.DB { return s.reader() }

// reader is the routing point for the store's own read-only methods. The
// fallback keeps Stores constructed without Open (some tests build
// &Store{db: ...} directly) working on their single pool.
func (s *Store) reader() *sql.DB {
	if s.readDB != nil {
		return s.readDB
	}
	return s.db
}

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

// LogicalNow returns a time that is never behind any timestamp Now() has
// already issued, without consuming a slot in the sequence.
//
// Now() guarantees strictly increasing values, so on a platform with coarse
// clock granularity — Windows ticks every few milliseconds — a burst of calls
// within one tick pushes the sequence ahead of the wall clock by a nanosecond
// each. Code that writes a row with Now() and then compares it against
// time.Now() is therefore comparing two different clocks, and a just-written
// row can look like it is scheduled in the future. Readers that need to say
// "everything due as of now" must use this instead.
func LogicalNow() time.Time {
	nowMu.Lock()
	defer nowMu.Unlock()
	now := time.Now().UTC()
	if lastNow.After(now) {
		return lastNow
	}
	return now
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

func isForeignKeyConstraint(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "foreign key constraint")
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
