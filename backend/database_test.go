package backend

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestOpenDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := OpenDatabase(dbPath, testLogger())
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

// TestOpenDatabase_PoolIsNotPinnedToOneConnection guards the whole point of the
// change: the pool must allow more than one connection so read RPCs are not
// serialized behind writes.
func TestOpenDatabase_PoolIsNotPinnedToOneConnection(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "pool.db"), testLogger())
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	defer func() { _ = db.Close() }()

	if got := db.Stats().MaxOpenConnections; got <= 1 {
		t.Fatalf("expected a multi-connection pool, got MaxOpenConnections=%d", got)
	}
	if got := db.Stats().MaxOpenConnections; got != maxOpenConns {
		t.Errorf("MaxOpenConnections = %d, want %d", got, maxOpenConns)
	}
}

// TestOpenDatabase_PragmasApplyToEveryConnection verifies that the
// connection-scoped pragmas (foreign_keys, busy_timeout) reach EVERY pooled
// connection, not just the first one used at open time.
func TestOpenDatabase_PragmasApplyToEveryConnection(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "pragmas.db"), testLogger())
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	// Hold one connection checked out so the second is guaranteed to be a
	// distinct physical connection.
	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn1: %v", err)
	}
	defer func() { _ = conn1.Close() }()

	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn2: %v", err)
	}
	defer func() { _ = conn2.Close() }()

	for i, c := range []*sql.Conn{conn1, conn2} {
		var fk int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("conn%d: query foreign_keys: %v", i+1, err)
		}
		if fk != 1 {
			t.Errorf("conn%d: foreign_keys=%d, want 1 (per-connection pragma missing)", i+1, fk)
		}
		var busyTimeout int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("conn%d: query busy_timeout: %v", i+1, err)
		}
		if busyTimeout != 5000 {
			t.Errorf("conn%d: busy_timeout=%d, want 5000", i+1, busyTimeout)
		}
	}
}

// TestOpenDatabase_ReadsNotBlockedByOpenWrite is the load-check for acceptance
// criterion 1: with an open write transaction held on one connection, a read on
// another connection must complete promptly (WAL readers are not blocked by the
// writer). Under the old single-connection pool the read would have queued
// behind the writer and hit the context deadline.
func TestOpenDatabase_ReadsNotBlockedByOpenWrite(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "readwrite.db"), testLogger())
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO t (id, v) VALUES (1, 'committed')"); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	// Pin a connection and hold an OPEN write transaction (uncommitted insert).
	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire writer conn: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if _, err := writer.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	if _, err := writer.ExecContext(ctx, "INSERT INTO t (id, v) VALUES (2, 'uncommitted')"); err != nil {
		t.Fatalf("uncommitted insert: %v", err)
	}
	defer func() { _, _ = writer.ExecContext(context.Background(), "ROLLBACK") }()

	// A concurrent read must complete promptly and see the last committed snapshot.
	readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var v string
	if err := db.QueryRowContext(readCtx, "SELECT v FROM t WHERE id = 1").Scan(&v); err != nil {
		t.Fatalf("read was blocked by the open write transaction: %v", err)
	}
	if v != "committed" {
		t.Errorf("read value = %q, want %q", v, "committed")
	}
}

func TestOpenDatabase_InvalidPath(t *testing.T) {
	// A path under a non-existent directory should fail when the DB is first used.
	// sql.Open with modernc/sqlite may defer the actual open, so we ping to force it.
	db, err := OpenDatabase("/no/such/dir/test.db", testLogger())
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

// TestOpenDatabase_SingleConnectionSerializesReads is the negative control for
// TestOpenDatabase_ReadsNotBlockedByOpenWrite: pinning the pool back to a single
// connection reproduces the old behavior — a read queues behind the open write
// and misses its deadline. This proves the widened pool is what unblocks reads.
func TestOpenDatabase_SingleConnectionSerializesReads(t *testing.T) {
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "single.db"), testLogger())
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO t (id, v) VALUES (1, 'committed')"); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire writer conn: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if _, err := writer.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	if _, err := writer.ExecContext(ctx, "INSERT INTO t (id, v) VALUES (2, 'uncommitted')"); err != nil {
		t.Fatalf("uncommitted insert: %v", err)
	}
	defer func() { _, _ = writer.ExecContext(context.Background(), "ROLLBACK") }()

	readCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var v string
	if err := db.QueryRowContext(readCtx, "SELECT v FROM t WHERE id = 1").Scan(&v); err == nil {
		t.Fatal("expected the read to block behind the single held connection, but it succeeded")
	}
}

// BenchmarkReadsDuringOpenWrite measures read throughput on the shared pool
// while a writer holds an open transaction — the shape of "an active subagent
// writes while a read RPC runs". Under the old single-connection pool readers
// queued behind the writer; with the widened pool + WAL they proceed against the
// committed snapshot. Run with:
//
//	go test ./backend -run '^$' -bench BenchmarkReadsDuringOpenWrite -benchtime 2000x
func BenchmarkReadsDuringOpenWrite(b *testing.B) {
	db, err := OpenDatabase(filepath.Join(b.TempDir(), "bench.db"), testLogger())
	if err != nil {
		b.Fatalf("OpenDatabase returned error: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		b.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO t (id, v) VALUES (1, 'committed')"); err != nil {
		b.Fatalf("seed insert: %v", err)
	}

	writer, err := db.Conn(ctx)
	if err != nil {
		b.Fatalf("acquire writer conn: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if _, err := writer.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		b.Fatalf("begin immediate: %v", err)
	}
	if _, err := writer.ExecContext(ctx, "INSERT INTO t (id, v) VALUES (2, 'uncommitted')"); err != nil {
		b.Fatalf("uncommitted insert: %v", err)
	}
	defer func() { _, _ = writer.ExecContext(context.Background(), "ROLLBACK") }()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var v string
			if err := db.QueryRowContext(ctx, "SELECT v FROM t WHERE id = 1").Scan(&v); err != nil {
				b.Fatalf("read failed while a writer held the lock: %v", err)
			}
		}
	})
}
