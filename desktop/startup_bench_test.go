package desktop

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
)

// BenchmarkStartupCriticalPath measures the synchronous startup work that
// precedes application construction: applying defaults, opening the database,
// initializing stores, and preloading projects and sessions. It deliberately
// excludes Wails context, ONNX models, the MCP gateway, and network I/O.
//
// Run it explicitly with `make bench-startup`. Benchmarks report stable,
// comparable ns/op and allocation metrics rather than failing the unit suite
// based on wall-clock time, which varies with CI runner load.
func BenchmarkStartupCriticalPath(b *testing.B) {
	b.Run("empty_database", benchmarkStartupCriticalPathEmpty)
	b.Run("preload_seeded_data", benchmarkStartupCriticalPathWithData)
}

func benchmarkStartupCriticalPathEmpty(b *testing.B) {
	baseDir := b.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Each iteration needs a new database to model a cold startup. Creating
		// and removing its parent fixture is excluded from the measured path.
		b.StopTimer()
		dir := filepath.Join(baseDir, fmt.Sprintf("run-%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatalf("MkdirAll(%q): %v", dir, err)
		}
		b.StartTimer()

		cfg := &config.Config{}
		config.ApplyDefaults(cfg)

		db, err := backend.OpenDatabase(filepath.Join(dir, "startup.db"), log)
		if err != nil {
			b.Fatalf("OpenDatabase: %v", err)
		}
		projStore, err := project.NewSQLiteProjectStore(db)
		if err != nil {
			_ = db.Close()
			b.Fatalf("NewSQLiteProjectStore: %v", err)
		}
		sessStore, err := session.NewSQLiteSessionStore(db)
		if err != nil {
			_ = db.Close()
			b.Fatalf("NewSQLiteSessionStore: %v", err)
		}
		projectMgr := project.NewManager(projStore, dir, nil)
		projects, err := projectMgr.ListProjects()
		if err != nil {
			_ = db.Close()
			b.Fatalf("ListProjects: %v", err)
		}
		if len(projects) > 0 {
			if _, err := sessStore.ListSessionsByProject(context.Background(), projects[0].ID); err != nil {
				_ = db.Close()
				b.Fatalf("ListSessionsByProject: %v", err)
			}
		}

		b.StopTimer()
		if err := db.Close(); err != nil {
			b.Fatalf("db.Close: %v", err)
		}
		if err := os.RemoveAll(dir); err != nil {
			b.Fatalf("RemoveAll(%q): %v", dir, err)
		}
		b.StartTimer()
	}
}

func benchmarkStartupCriticalPathWithData(b *testing.B) {
	dir := b.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	// Fixture creation is not part of preload timing.
	db, err := backend.OpenDatabase(filepath.Join(dir, "startup.db"), log)
	if err != nil {
		b.Fatalf("OpenDatabase: %v", err)
	}
	b.Cleanup(func() {
		if err := db.Close(); err != nil {
			b.Errorf("db.Close: %v", err)
		}
	})

	projStore, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		b.Fatalf("NewSQLiteProjectStore: %v", err)
	}
	sessStore, err := session.NewSQLiteSessionStore(db)
	if err != nil {
		b.Fatalf("NewSQLiteSessionStore: %v", err)
	}
	projectMgr := project.NewManager(projStore, dir, nil)

	proj, err := projectMgr.CreateProject("bench-project", "")
	if err != nil {
		b.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < 10; i++ {
		if err := sessStore.SaveSession(context.Background(), session.SessionInfo{
			ID:        fmt.Sprintf("sess-%d", i),
			ProjectID: proj.ID,
			Name:      fmt.Sprintf("Session %d", i),
			CreatedAt: now,
		}); err != nil {
			b.Fatalf("SaveSession(%d): %v", i, err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		projects, err := projectMgr.ListProjects()
		if err != nil {
			b.Fatalf("ListProjects: %v", err)
		}
		if len(projects) == 0 {
			b.Fatal("ListProjects returned no seeded project")
		}
		if _, err := sessStore.ListSessionsByProject(context.Background(), projects[0].ID); err != nil {
			b.Fatalf("ListSessionsByProject: %v", err)
		}
	}
}
