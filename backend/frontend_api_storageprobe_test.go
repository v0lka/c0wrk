package backend

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/storageprobe"
)

// storageAdvisoryRecorder captures the runtime_error payloads and Warn
// records a storage-liveness advisory produces through a bare FrontendAPI
// fixture.
type storageAdvisoryRecorder struct {
	mu     sync.Mutex
	toasts []map[string]string
	warns  []string // joined kinds of each Warn record
}

func (r *storageAdvisoryRecorder) handle(name string, args ...any) {
	if name != EventRuntimeError {
		return
	}
	if len(args) < 1 {
		return
	}
	payload, ok := args[0].(map[string]string)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toasts = append(r.toasts, payload)
}

func (r *storageAdvisoryRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level != slog.LevelWarn {
		return nil
	}
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == "kinds" {
			r.mu.Lock()
			r.warns = append(r.warns, a.Value.String())
			r.mu.Unlock()
		}
		return true
	})
	return nil
}

func (r *storageAdvisoryRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *storageAdvisoryRecorder) WithGroup(string) slog.Handler      { return r }
func (r *storageAdvisoryRecorder) Enabled(context.Context, slog.Level) bool {
	return true
}

func (r *storageAdvisoryRecorder) toastCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.toasts)
}

func (r *storageAdvisoryRecorder) warnCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.warns)
}

// newStorageAdvisoryFixture builds a bare FrontendAPI wired for the advisory
// probes: seed published, injected emitter, capturing logger.
func newStorageAdvisoryFixture(t *testing.T) (*FrontendAPI, *storageAdvisoryRecorder) {
	t.Helper()
	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	rec := &storageAdvisoryRecorder{}
	f.emitEvent = rec.handle
	f.logger = slog.New(rec)
	return f, rec
}

// stubWorkspaceProbe swaps probeWorkspaceFn for a fake returning the given
// findings (restored via t.Cleanup).
func stubWorkspaceProbe(t *testing.T, findings func(path string) []storageprobe.Finding) {
	t.Helper()
	orig := probeWorkspaceFn
	probeWorkspaceFn = findings
	t.Cleanup(func() { probeWorkspaceFn = orig })
}

func stubConfigDirProbe(t *testing.T, findings func(path string) []storageprobe.Finding) {
	t.Helper()
	orig := probeConfigDirFn
	probeConfigDirFn = findings
	t.Cleanup(func() { probeConfigDirFn = orig })
}

// TestProbeWorkspaceLiveness_NetworkFindingWarnsAndToastsOnce is the
// acceptance criteria's core behavior: a network-mount finding produces
// exactly one Warn + one runtime_error toast note (error_code
// workspace_storage_advisory), and a second probe of the SAME project stays
// silent — the note is once per project per session.
func TestProbeWorkspaceLiveness_NetworkFindingWarnsAndToastsOnce(t *testing.T) {
	f, rec := newStorageAdvisoryFixture(t)
	stubWorkspaceProbe(t, func(path string) []storageprobe.Finding {
		return []storageprobe.Finding{{Kind: storageprobe.KindNetworkVolume, Detail: "fake statfs: network"}}
	})
	p := &project.ProjectInfo{ID: "p1", WorkspacePath: "/ws/nfs"}

	f.probeWorkspaceLiveness(p)
	f.probeWorkspaceLiveness(p) // same project again — must stay silent

	if got := rec.toastCount(); got != 1 {
		t.Fatalf("runtime_error toasts = %d, want exactly 1 (once per project per session)", got)
	}
	toast := rec.toasts[0]
	if toast["error_code"] != storageAdvisoryErrCode {
		t.Errorf("error_code = %q, want %q", toast["error_code"], storageAdvisoryErrCode)
	}
	if toast["id"] == "" {
		t.Error("toast id is empty")
	}
	if !strings.Contains(toast["message"], "/ws/nfs") {
		t.Errorf("toast message %q does not name the workspace path", toast["message"])
	}
	if !strings.Contains(toast["message"], "network volume") {
		t.Errorf("toast message %q does not name the network-volume cause", toast["message"])
	}
	if got := rec.warnCount(); got != 1 {
		t.Fatalf("Warn records = %d, want exactly 1", got)
	}
}

// TestProbeWorkspaceLiveness_SecondProjectFiresAgain pins the guard's
// granularity: the once-per-session rule keys on the PROJECT, so a different
// project's finding still fires.
func TestProbeWorkspaceLiveness_SecondProjectFiresAgain(t *testing.T) {
	f, rec := newStorageAdvisoryFixture(t)
	stubWorkspaceProbe(t, func(path string) []storageprobe.Finding {
		return []storageprobe.Finding{{Kind: storageprobe.KindNetworkVolume, Detail: "fake"}}
	})

	f.probeWorkspaceLiveness(&project.ProjectInfo{ID: "p1", WorkspacePath: "/ws/a"})
	f.probeWorkspaceLiveness(&project.ProjectInfo{ID: "p2", WorkspacePath: "/ws/b"})

	if got := rec.toastCount(); got != 2 {
		t.Fatalf("toasts = %d, want 2 (one per distinct project)", got)
	}
}

// TestProbeWorkspaceLiveness_CleanProbeSilent pins the local-storage contract:
// no findings → no Warn, no toast, and the project stays unmarked (a later
// switch re-probes).
func TestProbeWorkspaceLiveness_CleanProbeSilent(t *testing.T) {
	f, rec := newStorageAdvisoryFixture(t)
	stubWorkspaceProbe(t, func(path string) []storageprobe.Finding { return nil })

	f.probeWorkspaceLiveness(&project.ProjectInfo{ID: "p1", WorkspacePath: "/ws/local"})
	f.probeWorkspaceLiveness(&project.ProjectInfo{ID: "p1", WorkspacePath: "/ws/local"})

	if got := rec.toastCount(); got != 0 {
		t.Fatalf("clean probe raised %d toast(s), want 0", got)
	}
	if got := rec.warnCount(); got != 0 {
		t.Fatalf("clean probe logged %d Warn(s), want 0", got)
	}
}

// TestProbeWorkspaceLiveness_GuardsSkip pins the skip conditions: No Project
// and an empty workspace path never probe.
func TestProbeWorkspaceLiveness_GuardsSkip(t *testing.T) {
	f, rec := newStorageAdvisoryFixture(t)
	calls := 0
	stubWorkspaceProbe(t, func(path string) []storageprobe.Finding {
		calls++
		return []storageprobe.Finding{{Kind: storageprobe.KindNetworkVolume, Detail: "fake"}}
	})

	f.probeWorkspaceLiveness(nil)
	f.probeWorkspaceLiveness(&project.ProjectInfo{ID: project.NoProjectID, IsNoProject: true, WorkspacePath: "/internal"})
	f.probeWorkspaceLiveness(&project.ProjectInfo{ID: "p1", WorkspacePath: "   "})

	if calls != 0 {
		t.Fatalf("probe ran %d time(s) for guarded inputs, want 0", calls)
	}
	if got := rec.toastCount(); got != 0 {
		t.Fatalf("guarded inputs raised %d toast(s), want 0", got)
	}
}

// TestProbeConfigDirLiveness_OncePerSession covers the config-directory leg:
// a finding fires exactly one note for the whole session — the directory is
// global, so a second call (from another project switch) stays silent; and an
// unset agent dir never probes.
func TestProbeConfigDirLiveness_OncePerSession(t *testing.T) {
	f, rec := newStorageAdvisoryFixture(t)
	f.agentDir = "/synced/.c0wrk"
	stubConfigDirProbe(t, func(path string) []storageprobe.Finding {
		return []storageprobe.Finding{{Kind: storageprobe.KindSyncedConfigDir, Detail: "fake: under CloudStorage"}}
	})

	f.probeConfigDirLiveness()
	f.probeConfigDirLiveness() // same session — must stay silent

	if got := rec.toastCount(); got != 1 {
		t.Fatalf("config-dir toasts = %d, want exactly 1 per session", got)
	}
	if !strings.Contains(rec.toasts[0]["message"], "/synced/.c0wrk") {
		t.Errorf("toast message %q does not name the config dir", rec.toasts[0]["message"])
	}
	if !strings.Contains(rec.toasts[0]["message"], "cloud-sync") {
		t.Errorf("toast message %q does not name the sync-provider cause", rec.toasts[0]["message"])
	}

	// An unset agent dir skips the probe entirely (its own fixture guard map
	// is untouched).
	f2, rec2 := newStorageAdvisoryFixture(t)
	f2.probeConfigDirLiveness()
	if got := rec2.toastCount(); got != 0 {
		t.Fatalf("unset agent dir raised %d toast(s), want 0", got)
	}
}

// TestProbeConfigDirLiveness_CleanSilent mirrors the workspace negative half
// for the config directory.
func TestProbeConfigDirLiveness_CleanSilent(t *testing.T) {
	f, rec := newStorageAdvisoryFixture(t)
	f.agentDir = "/local/.c0wrk"
	stubConfigDirProbe(t, func(path string) []storageprobe.Finding { return nil })

	f.probeConfigDirLiveness()

	if got := rec.toastCount(); got != 0 {
		t.Fatalf("clean config dir raised %d toast(s), want 0", got)
	}
	if got := rec.warnCount(); got != 0 {
		t.Fatalf("clean config dir logged %d Warn(s), want 0", got)
	}
}

// TestStorageAdvisoryMessage pins the operator-facing sentence chain: each
// finding kind contributes its own sentence, joined in order.
func TestStorageAdvisoryMessage(t *testing.T) {
	msg := storageAdvisoryMessage("/ws", []storageprobe.Finding{
		{Kind: storageprobe.KindNetworkVolume},
		{Kind: storageprobe.KindPlaceholderFiles},
	})
	if !strings.Contains(msg, "/ws is on a network volume") {
		t.Errorf("message %q misses the network-volume sentence", msg)
	}
	if !strings.Contains(msg, "placeholder files") {
		t.Errorf("message %q misses the placeholder sentence", msg)
	}
	if !strings.Contains(msg, "OneDrive") {
		t.Errorf("message %q misses the OneDrive example", msg)
	}

	cfgMsg := storageAdvisoryMessage("/synced/.c0wrk", []storageprobe.Finding{
		{Kind: storageprobe.KindSyncedConfigDir},
	})
	if !strings.Contains(cfgMsg, "cloud-sync provider") {
		t.Errorf("config message %q misses the sync-provider sentence", cfgMsg)
	}

	unknownMsg := storageAdvisoryMessage("/ws", []storageprobe.Finding{{Kind: "mystery"}})
	if !strings.Contains(unknownMsg, "unusual storage class") {
		t.Errorf("unknown-kind message %q misses the fallback sentence", unknownMsg)
	}
}

// TestProbeStorageAdvisories_NoConfigDirInWorkspaceFixture pins the combined
// entry point: a fixture without agentDir runs the workspace leg only.
func TestProbeStorageAdvisories_NoConfigDirInWorkspaceFixture(t *testing.T) {
	f, rec := newStorageAdvisoryFixture(t)
	stubWorkspaceProbe(t, func(path string) []storageprobe.Finding {
		return []storageprobe.Finding{{Kind: storageprobe.KindNetworkVolume, Detail: "fake"}}
	})
	configCalls := 0
	stubConfigDirProbe(t, func(path string) []storageprobe.Finding {
		configCalls++
		return nil
	})

	f.probeStorageAdvisories(&project.ProjectInfo{ID: "p1", WorkspacePath: "/ws/nfs"})

	if got := rec.toastCount(); got != 1 {
		t.Fatalf("toasts = %d, want exactly 1 (workspace leg)", got)
	}
	if configCalls != 0 {
		t.Fatalf("config-dir probe ran %d time(s) without an agent dir, want 0", configCalls)
	}
}
