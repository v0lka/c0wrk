package backend

import (
	"context"
	"database/sql"
	"log/slog"

	_ "modernc.org/sqlite" // register SQLite driver
)

// OpenDatabase opens a SQLite database at dbPath and applies recommended
// PRAGMAs (WAL journal mode, foreign keys). Callers own the returned *sql.DB
// and must close it when done. Pragma failures are logged as warnings but do
// not cause the function to return an error.
func OpenDatabase(dbPath string, logger *slog.Logger) (*sql.DB, error) {
	if logger == nil {
		logger = slog.Default()
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Write-serialized SQLite: single connection prevents "database is locked"
	// errors from concurrent writes while still allowing concurrent reads.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		logger.Warn("failed to enable WAL mode", "error", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA busy_timeout=5000"); err != nil {
		logger.Warn("failed to set busy_timeout", "error", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
		logger.Warn("failed to enable foreign keys", "error", err)
	}

	return db, nil
}
