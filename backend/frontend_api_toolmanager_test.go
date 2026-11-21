package backend

import (
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/core/toolmanager"
)

// tsSpecs builds a pending list from name/version pairs.
func tsSpecs(pairs ...[2]string) []toolmanager.ToolSpec {
	out := make([]toolmanager.ToolSpec, len(pairs))
	for i, p := range pairs {
		out[i] = toolmanager.ToolSpec{Name: p[0], Version: p[1]}
	}
	return out
}

// The zero-value store IS the steady state: a brand-new FrontendAPI (the
// desktop host seeds one before Startup) must report an inactive, empty
// install — never an error, never a half-shaped snapshot.
func TestGetToolInstallStatus_ZeroValueIsSteadyState(t *testing.T) {
	f := &FrontendAPI{}
	st := f.GetToolInstallStatus()
	if st.Active {
		t.Error("Active = true on a zero-value store, want false")
	}
	if st.Pending == nil || len(st.Pending) != 0 {
		t.Errorf("Pending = %#v, want an empty non-nil slice", st.Pending)
	}
	if st.ToolsTotal != 0 {
		t.Errorf("ToolsTotal = %d, want 0", st.ToolsTotal)
	}
}

// Setting a pending list activates the store and records the full list with
// the splash denominator — the RPC answer a lost tool_manager:start must
// make recoverable.
func TestSetToolInstallPending_ActivatesWithPendingList(t *testing.T) {
	f := &FrontendAPI{}
	f.SetToolInstallPending(tsSpecs([2]string{"rg", "14.1.0"}, [2]string{"uv", "0.6.0"}))

	st := f.GetToolInstallStatus()
	if !st.Active {
		t.Fatal("Active = false right after a set, want true")
	}
	if st.ToolsTotal != 2 {
		t.Errorf("ToolsTotal = %d, want 2", st.ToolsTotal)
	}
	if len(st.Pending) != 2 {
		t.Fatalf("Pending has %d entries, want 2", len(st.Pending))
	}
	if st.Pending[0].Name != "rg" || st.Pending[0].Version != "14.1.0" {
		t.Errorf("Pending[0] = %+v, want rg 14.1.0", st.Pending[0])
	}
	if st.Pending[1].Name != "uv" || st.Pending[1].Version != "0.6.0" {
		t.Errorf("Pending[1] = %+v, want uv 0.6.0", st.Pending[1])
	}
}

// The NeedsInstall-probe-failure fallback: an empty NON-NIL slice is still a
// set — active with an empty list and a zero denominator, exactly the shape
// the accompanying start event carries.
func TestSetToolInstallPending_EmptyListStaysActive(t *testing.T) {
	f := &FrontendAPI{}
	f.SetToolInstallPending([]toolmanager.ToolSpec{})

	st := f.GetToolInstallStatus()
	if !st.Active {
		t.Error("Active = false for the empty-probe fallback, want true (the splash is up)")
	}
	if st.Pending == nil || len(st.Pending) != 0 {
		t.Errorf("Pending = %#v, want an empty non-nil slice", st.Pending)
	}
	if st.ToolsTotal != 0 {
		t.Errorf("ToolsTotal = %d, want 0", st.ToolsTotal)
	}
}

// nil resolves the store — the clear that accompanies every
// tool_manager:done emission. Steady state afterwards, whatever was stored.
func TestSetToolInstallPending_NilClears(t *testing.T) {
	f := &FrontendAPI{}
	f.SetToolInstallPending(tsSpecs([2]string{"rg", "14.1.0"}))
	f.SetToolInstallPending(nil)

	st := f.GetToolInstallStatus()
	if st.Active {
		t.Error("Active = true after the clear, want false")
	}
	if st.Pending == nil || len(st.Pending) != 0 {
		t.Errorf("Pending = %#v, want an empty non-nil slice", st.Pending)
	}
	if st.ToolsTotal != 0 {
		t.Errorf("ToolsTotal = %d, want 0 (fully reset, not just deactivated)", st.ToolsTotal)
	}

	// Clearing an already-clear store stays a harmless no-op.
	f.SetToolInstallPending(nil)
	if st := f.GetToolInstallStatus(); st.Active || len(st.Pending) != 0 || st.ToolsTotal != 0 {
		t.Errorf("double clear changed the steady state: %+v", st)
	}
}

// Reading returns a copy: a caller mutating the returned Pending slice must
// not reach the store (and the store's own slice must not alias the
// caller's).
func TestToolInstallStatus_ReadsReturnCopies(t *testing.T) {
	f := &FrontendAPI{}
	specs := tsSpecs([2]string{"rg", "14.1.0"}, [2]string{"uv", "0.6.0"})
	f.SetToolInstallPending(specs)

	// Mutating the CALLER's slice after the set must not reach the store.
	specs[0].Name = "mutated"
	if st := f.GetToolInstallStatus(); st.Pending[0].Name != "rg" {
		t.Errorf("caller-side mutation leaked into the store: Pending[0].Name = %q", st.Pending[0].Name)
	}

	// Mutating a READ's slice must not reach the store either.
	st := f.GetToolInstallStatus()
	st.Pending[0] = ToolInstallPendingToolDTO{Name: "mutated", Version: "9.9.9"}
	st.Active = false
	if again := f.GetToolInstallStatus(); !again.Active || again.Pending[0].Name != "rg" {
		t.Errorf("read-side mutation leaked into the store: %+v", again)
	}
}

// The set/clear transitions are written by the startup goroutine while RPC
// readers arrive on Wails call goroutines: hammer both sides concurrently —
// -race flags any unsynchronized word.
func TestToolInstallStatus_ConcurrentSetClearRead(t *testing.T) {
	f := &FrontendAPI{}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			f.SetToolInstallPending(tsSpecs([2]string{"rg", "14.1.0"}))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			f.SetToolInstallPending(nil)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			st := f.GetToolInstallStatus()
			if len(st.Pending) > 1 {
				t.Errorf("Pending grew beyond any set list: %d entries", len(st.Pending))
				return
			}
		}
	}()
	wg.Wait()

	// Whatever interleaving ran last, the store ends in a well-formed state.
	f.SetToolInstallPending(tsSpecs([2]string{"markitdown", "0.1.0"}))
	if st := f.GetToolInstallStatus(); !st.Active || st.ToolsTotal != 1 || st.Pending[0].Name != "markitdown" {
		t.Errorf("post-race set lost: %+v", st)
	}
}
