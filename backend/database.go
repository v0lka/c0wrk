package backend

import (
	"context"
	"database/sql"
	"log/slog"
	"net/url"
	"strings"

	_ "modernc.org/sqlite" // register SQLite driver
)

// Connection-pool sizing for the shared SQLite database. WAL allows
// concurrent readers beside one writer, so a small pool lets read-only RPCs
// proceed while an agent's writes are in flight; hot-path writes are
// additionally serialized through a dedicated single-writer goroutine
// (session.EventPersister). The numbers stay modest: one desktop app, and
// each pooled connection carries its own SQLite page cache.
const (
	maxOpenConns = 8
	maxIdleConns = 4
)

// Connection-scoped PRAGMAs applied to EVERY pooled connection via the DSN.
// Unlike journal_mode (a database-level property), busy_timeout and
// foreign_keys are per-connection: the driver's `_pragma` DSN parameter runs
// them on each new connection — a single one-shot ExecContext was only
// correct under a one-connection pool.
const (
	pragmaBusyTimeout = "busy_timeout(5000)"
	pragmaForeignKeys = "foreign_keys(1)"
)

// OpenDatabase opens a SQLite database at dbPath, sizes the pool, and applies
// WAL + the per-connection PRAGMAs. Callers own the returned *sql.DB and must
// close it. WAL is best-effort and then CONFIRMED at runtime (the journal_mode
// is read back and logged); a WAL failure logs a warning and keeps the default
// journal mode rather than failing the open.
func OpenDatabase(dbPath string, logger *slog.Logger) (*sql.DB, error) {
	if logger == nil {
		logger = slog.Default()
	}

	db, err := sql.Open("sqlite", dsnWithPragmas(dbPath))
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)

	// WAL is a persistent database property — setting it once is enough. The
	// pragma returns the resulting mode; read it back to confirm it took.
	var journalMode string
	if err := db.QueryRowContext(context.Background(), "PRAGMA journal_mode=WAL").Scan(&journalMode); err != nil {
		logger.Warn("failed to enable WAL mode", "error", err)
		return db, nil
	}
	if !strings.EqualFold(journalMode, "wal") {
		logger.Warn("sqlite journal_mode is not WAL", "journal_mode", journalMode)
		return db, nil
	}
	logger.Info("sqlite journal_mode confirmed", "journal_mode", "wal", "max_open_conns", maxOpenConns)

	return db, nil
}

// dsnWithPragmas appends the per-connection `_pragma` parameters to dbPath so
// every pooled connection receives busy_timeout and foreign_keys. Paths are
// passed through unchanged (the driver splits the query off the first '?').
func dsnWithPragmas(dbPath string) string {
	q := url.Values{}
	// Order matters for readability only; the driver special-cases busy_timeout
	// to run first regardless of submission order.
	q.Add("_pragma", pragmaBusyTimeout)
	q.Add("_pragma", pragmaForeignKeys)
	return dbPath + "?" + q.Encode()
}
