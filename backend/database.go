package backend

import (
	"context"
	"database/sql"
	"log/slog"
	"net/url"
	"strings"

	_ "modernc.org/sqlite" // register SQLite driver
)

// Connection-pool sizing for the shared SQLite database.
//
// SQLite in WAL mode supports any number of concurrent READERS alongside a
// single writer, so the pool no longer needs to be pinned to one connection.
// A small pool lets read-only RPCs (session/message/workspace loads) proceed
// while an active agent's writes are in flight, instead of queueing behind
// them on a shared connection. Hot-path writes are additionally serialized
// through a dedicated single-writer goroutine (see session.EventPersister), so
// the pool never has to arbitrate a stampede of concurrent writers.
//
// These are deliberately modest: the workload is a single desktop app, and each
// pooled connection carries its own SQLite page cache.
const (
	maxOpenConns = 8
	maxIdleConns = 4
)

// Connection-scoped PRAGMAs applied to EVERY pooled connection via the DSN.
//
// Unlike journal_mode (a database-level property persisted in the file header),
// busy_timeout and foreign_keys are PER-CONNECTION settings. Setting them once
// with a single ExecContext was only correct while the pool was pinned to one
// connection; with a real pool every new connection must receive them, which is
// exactly what the driver's `_pragma` DSN parameter does (it runs the pragma on
// each new connection, see modernc.org/sqlite applyQueryParams).
const (
	pragmaBusyTimeout = "busy_timeout(5000)"
	pragmaForeignKeys = "foreign_keys(1)"
)

// OpenDatabase opens a SQLite database at dbPath, sizes the connection pool,
// and applies recommended PRAGMAs (WAL journal mode, foreign keys, busy
// timeout). Callers own the returned *sql.DB and must close it when done.
//
// WAL is enabled best-effort and then CONFIRMED at runtime: the resulting
// journal_mode is read back and logged, so a silently non-WAL database is
// visible instead of assumed. A WAL failure (e.g. an unsupported filesystem) is
// logged as a warning and does not fail the open — the database keeps working
// in its default journal mode.
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

	// WAL is a persistent database property, so setting it once is enough for
	// every pooled connection. The pragma returns the resulting mode; read it
	// back to confirm the mode actually took effect.
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
