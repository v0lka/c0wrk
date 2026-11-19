package session

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/ignore"
)

// TestIgnoreResolverWithin_TimesOutOnSlowBuild pins the file-tree's bounded
// join: a not-yet-built resolver must NOT hold the caller past the wait
// budget — it returns ok=false while the background walk continues, and the
// same caller (or the delivery) picks the finished resolver up afterwards.
// The build is gated on a channel instead of a sleep: the timeout only ever
// fires because the wait budget is real.
func TestIgnoreResolverWithin_TimesOutOnSlowBuild(t *testing.T) {
	m, _, _ := testManager(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var gateOnce sync.Once
	m.ignoreResolverBuild = func(ctx context.Context, root string) (*ignore.Resolver, error) {
		gateOnce.Do(func() { close(started) })
		<-release
		return ignore.NewResolverContext(ctx, root)
	}

	root := testWorkspacePath(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	type joinResult struct {
		r  *ignore.Resolver
		ok bool
	}
	done := make(chan joinResult, 1)
	go func() {
		r, ok := m.IgnoreResolverWithin(root, 25*time.Millisecond)
		done <- joinResult{r, ok}
	}()

	<-started // the bounded join started the tracked background build
	select {
	case res := <-done:
		if res.ok {
			t.Fatal("join must give up while the build is gated, got ok=true")
		}
		if res.r != nil {
			t.Fatalf("timeout join must return no resolver, got %v", res.r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bounded join never returned")
	}

	// The gated build is still running; release it, wait for the tracked
	// goroutine to drain (which is also what closes the entry's done channel),
	// and take the finished resolver through the same entry point.
	close(release)
	if !waitBackgroundSettled(m, 5*time.Second) {
		t.Fatal("background walk never drained after release")
	}
	key, keyErr := ignoreCacheKey(root)
	if keyErr != nil {
		key = root
	}
	v, loaded := m.ignoreCache.Load(key)
	if !loaded {
		t.Fatal("finished build left no cache entry")
	}
	entry, okEntry := v.(*ignoreResolverEntry)
	if !okEntry {
		t.Fatalf("cache must hold an *ignoreResolverEntry, got %T", v)
	}
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		t.Fatal("entry never signalled completion")
	}
	r, ok := m.IgnoreResolverWithin(root, 25*time.Millisecond)
	if !ok || r == nil {
		t.Fatalf("finished build must serve the bounded join, got ok=%v r=%v", ok, r)
	}
	if !r.Ignored(filepath.Join(root, "x.log"), false) {
		t.Fatal("resolver did not honour the .gitignore")
	}
}

// TestIgnoreResolverWithin_FastBuildServesFirstJoin pins the cheap-tree path:
// a build that lands within the wait budget serves the joiner directly, so a
// small workspace gets .aiignore flags on the FIRST listing.
func TestIgnoreResolverWithin_FastBuildServesFirstJoin(t *testing.T) {
	m, _, _ := testManager(t)
	m.ignoreResolverBuild = ignore.NewResolverContext

	root := testWorkspacePath(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, ok := m.IgnoreResolverWithin(root, 5*time.Second)
	if !ok || r == nil {
		t.Fatalf("a fast build must serve the bounded join, got ok=%v r=%v", ok, r)
	}
	if !r.Ignored(filepath.Join(root, "x.log"), false) {
		t.Fatal("resolver did not honour the .gitignore")
	}
}
