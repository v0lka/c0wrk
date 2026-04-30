package backend

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	db, err := OpenDatabase(dbPath, logger)
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	// Verify WAL mode was applied
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("failed to query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", journalMode)
	}

	// Verify foreign keys are enabled
	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("failed to query foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("expected foreign_keys=1, got %d", fk)
	}
}

func TestOpenDatabase_InvalidPath(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// A path under a non-existent directory should fail when the DB is first used.
	// sql.Open with modernc/sqlite may defer the actual open, so we ping to force it.
	db, err := OpenDatabase("/no/such/dir/test.db", logger)
	if err != nil {
		// Driver returned error immediately — that's fine.
		return
	}
	defer func() { _ = db.Close() }()

	// If Open succeeded, Ping should fail because the directory doesn't exist.
	if err := db.PingContext(context.Background()); err == nil {
		t.Fatal("expected Ping to fail for non-existent directory")
	}
}
