// Package store owns the SQLite database: connection, migrations and every
// SQL statement. It holds no retrieval or query-policy logic.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	// Pure-Go SQLite driver: no cgo, so cross-compiled builds stay simple.
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB wraps the SQLite handle.
type DB struct {
	sql  *sql.DB
	path string
}

// Open connects to the database at path, creating it and its directory if
// needed, and applies pending migrations. ":memory:" opens a private in-memory
// database.
//
// The handle uses a single connection. Callers must fully drain or close a
// *sql.Rows before issuing another statement, and must use the transaction —
// never the DB — inside a Tx callback; otherwise the second statement waits for
// a connection that is never released.
func Open(ctx context.Context, path string) (*DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("creating database directory: %w", err)
		}
	}
	handle, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database %s: %w", path, err)
	}
	handle.SetMaxOpenConns(1)

	pragmas := []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 10000"}
	if path != ":memory:" {
		// WAL lets a long-running MCP server read while a CLI refresh writes.
		pragmas = append(pragmas, "PRAGMA journal_mode = WAL")
	}
	for _, p := range pragmas {
		if _, err := handle.ExecContext(ctx, p); err != nil {
			handle.Close()
			return nil, fmt.Errorf("opening database %s: applying %q: %w", path, p, err)
		}
	}

	db := &DB{sql: handle, path: path}
	if err := db.migrate(ctx); err != nil {
		handle.Close()
		return nil, err
	}
	return db, nil
}

// SQL exposes the handle.
func (d *DB) SQL() *sql.DB { return d.sql }

// Path reports where the database lives.
func (d *DB) Path() string { return d.path }

// Close releases the connection.
func (d *DB) Close() error { return d.sql.Close() }

// Tx runs fn in a transaction, committing only if fn succeeds. The transaction
// is rolled back if fn fails or panics: with a single connection, a
// transaction left open would block every later statement.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	committed = true
	return tx.Commit()
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version < 1 {
			return nil, fmt.Errorf("migration %s must be named NNNN_description.sql", name)
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migrations must be numbered contiguously from 1; found %s at position %d", m.name, i+1)
		}
	}
	return out, nil
}

func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("creating migration ledger: %w", err)
	}
	current, err := d.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	all, err := loadMigrations()
	if err != nil {
		return err
	}
	if current > len(all) {
		return fmt.Errorf("database schema version %d is newer than this devmodels (knows %d); upgrade devmodels or use another database", current, len(all))
	}
	for _, m := range all[current:] {
		err := d.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("applying migration %s: %w", m.name, err)
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
				m.version, m.name, time.Now().UTC().Format(time.RFC3339))
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion reports the highest applied migration.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	if err := d.sql.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("reading schema version: %w", err)
	}
	return int(v.Int64), nil
}

// MigrationCount reports how many migrations this build embeds.
func MigrationCount() int {
	all, _ := loadMigrations()
	return len(all)
}

func timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
