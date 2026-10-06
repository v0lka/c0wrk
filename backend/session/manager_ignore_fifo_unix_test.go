//go:build unix

package session

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/ignore"
)

// TestStartIgnoreBuild_FifoIgnoreFileDoesNotHangShutdown reproduces the field
// shutdown freeze end-to-end at the manager boundary: a work-directory root
// containing a FIFO named ".gitignore". This is the exact shape of the crash
// under ~/.c0wrk (registered as a project work directory), where leftover test
// fixtures left FIFO files named .gitignore beneath projects/*/temp/.
//
// A read-open of the FIFO blocks until a writer appears, so before the ignore
// package's regular-file guard the background walk never returned and
// Shutdown's stopBackground join burned the whole stopTimeout on the main
// thread — the reported beach-ball freeze. The manager's production resolver
// builder (ignore.NewResolverContext) is used, so this asserts the full chain:
// manager walk → ignore package → no hang.
func TestStartIgnoreBuild_FifoIgnoreFileDoesNotHangShutdown(t *testing.T) {
	m, _, _ := testManager(t)
	m.ignoreResolverBuild = ignore.NewResolverContext // production builder

	root := testWorkspacePath(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(sub, ".gitignore"), 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	m.startIgnoreBuild(root)

	if !waitBackgroundSettled(m, 5*time.Second) {
		t.Fatal("ignore walk hung on a FIFO named .gitignore (background tracker never drained)")
	}
	v, ok := m.ignoreCache.Load(root)
	if !ok {
		t.Fatal("expected a cached resolver after the build")
	}
	resolver, ok := v.(*ignore.Resolver)
	if !ok || resolver == nil {
		t.Fatalf("cache must hold a non-nil *ignore.Resolver, got %T", v)
	}
	if !resolver.Ignored(filepath.Join(root, "x.log"), false) {
		t.Fatal("resolver did not honour the regular .gitignore after skipping the FIFO")
	}
}
