package version

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDefaults verifies the compile-time defaults when the package is built
// without ldflags overrides (the `go test ./core/version` path).
func TestDefaults(t *testing.T) {
	if Version != "dev" {
		t.Errorf("Version = %q, want %q", Version, "dev")
	}
	if GitCommit != "none" {
		t.Errorf("GitCommit = %q, want %q", GitCommit, "none")
	}
	if BuildDate != "unknown" {
		t.Errorf("BuildDate = %q, want %q", BuildDate, "unknown")
	}
}

// TestLdflagsInjection verifies that the linker can override Version via
// `-ldflags "-X .../version.Version=<v>"`.
//
// It works in two phases driven by the C0WRK_VERSION_SENTINEL env var:
//  1. Parent run (no sentinel): re-invokes `go test` with -ldflags and the
//     sentinel set, delegating to phase 2.
//  2. Sentinel run: asserts the ldflags value actually landed in Version.
func TestLdflagsInjection(t *testing.T) {
	// Phase 2 — sentinel run: the test binary was compiled with -ldflags, so
	// Version should hold the injected value.
	if want := os.Getenv("C0WRK_VERSION_SENTINEL"); want != "" {
		if Version != want {
			t.Fatalf("injected Version = %q, want %q", Version, want)
		}
		return
	}

	if testing.Short() {
		t.Skip("skipping ldflags injection subprocess test in short mode")
	}

	// Phase 1 — parent run: re-invoke this test with ldflags injection.
	const want = "v9.9.9-test"
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate package directory")
	}
	pkgDir := filepath.Dir(thisFile)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test",
		"-ldflags", "-X github.com/v0lka/c0wrk/core/version.Version="+want,
		"-run", "TestLdflagsInjection$",
		".",
	)
	cmd.Dir = pkgDir
	cmd.Env = append(os.Environ(), "C0WRK_VERSION_SENTINEL="+want)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test with ldflags injection failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("injected sub-test did not report ok:\n%s", out)
	}
}
