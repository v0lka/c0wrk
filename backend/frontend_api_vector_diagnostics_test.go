package backend

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// TestDumpVectorIndexDiagnostics_NoProjectRefuses pins the routing refusal:
// the diagnostics action means nothing without an indexable target, and the
// refusal names the cause instead of writing an empty bundle.
func TestDumpVectorIndexDiagnostics_NoProjectRefuses(t *testing.T) {
	agentDir := t.TempDir()
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
		agentDir:        agentDir,
	}
	f.seedPublished.Store(true)

	_, err := f.DumpVectorIndexDiagnostics()
	if err == nil {
		t.Fatal("expected refusal in No Project mode")
	}
	if !strings.Contains(err.Error(), "No Project") {
		t.Errorf("error %q does not name the No Project cause", err)
	}
	// Nothing was written: the diagnostics directory must not exist.
	if _, statErr := os.Stat(config.DiagnosticsDir(agentDir)); !os.IsNotExist(statErr) {
		t.Errorf("refused dump created %s (err=%v)", config.DiagnosticsDir(agentDir), statErr)
	}
}

// TestDumpVectorIndexDiagnostics_NoFocusRefuses pins the registry-level
// refusal: no focus root — nothing to diagnose — rejects with the carried
// routing cause (the same funnel ReindexVectorIndex uses).
func TestDumpVectorIndexDiagnostics_NoFocusRefuses(t *testing.T) {
	api := newVectorStatusAPI(t)
	vr := api.vectorRootsRegistry()
	t.Cleanup(vr.ShutdownAll)
	vr.LeaveFocus()

	_, err := api.DumpVectorIndexDiagnostics()
	if err == nil {
		t.Fatal("expected refusal without a focus root")
	}
	if !strings.Contains(err.Error(), "vector index not available") {
		t.Errorf("error %q does not carry the routing cause", err)
	}
}

// TestDumpVectorIndexDiagnostics_WritesBundle pins the AC: the RPC WRITES the
// diagnostics bundle — a 0600 file under <agentDir>/diagnostics/ with a UTC
// timestamped name, carrying the human header (the stall verdict), the full
// status snapshot as JSON, and the all-goroutine stack dump — and returns
// exactly where it landed. Nothing is automatic: before the RPC runs, no
// diagnostics directory exists.
func TestDumpVectorIndexDiagnostics_WritesBundle(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	// Focus the checkout (the default target) and wait for its index
	// identity to settle so the bundle's status snapshot is a real one.
	if err := h.api.SetGitPanelFocus(""); err != nil {
		t.Fatalf("SetGitPanelFocus reset: %v", err)
	}
	ctx := context.Background()
	mgr, err := h.roots.FocusManager(ctx)
	if err != nil {
		t.Fatalf("focus manager: %v", err)
	}
	waitForVectorBranch(t, mgr, "main")

	diagDir := config.DiagnosticsDir(h.agentDir)
	if _, statErr := os.Stat(diagDir); !os.IsNotExist(statErr) {
		t.Fatalf("diagnostics directory %s exists before any collection", diagDir)
	}

	res, err := h.api.DumpVectorIndexDiagnostics()
	if err != nil {
		t.Fatalf("DumpVectorIndexDiagnostics: %v", err)
	}
	if res.Path == "" {
		t.Fatal("empty result path")
	}
	if filepath.Dir(res.Path) != diagDir {
		t.Errorf("bundle path %q outside the diagnostics dir %q", res.Path, diagDir)
	}
	if !strings.HasPrefix(filepath.Base(res.Path), "vector-index-") || !strings.HasSuffix(filepath.Base(res.Path), ".txt") {
		t.Errorf("bundle name %q misses the vector-index prefix/.txt suffix", filepath.Base(res.Path))
	}
	if _, err := time.Parse("20060102T150405.000000000Z", strings.TrimSuffix(strings.TrimPrefix(filepath.Base(res.Path), "vector-index-"), ".txt")); err != nil {
		t.Errorf("bundle name timestamp not UTC-stamped: %v", err)
	}

	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatalf("stat bundle: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		// POSIX-only evidence: Windows files carry no POSIX permission bits
		// (os.Stat reports the read/write bits only; owner-only enforcement
		// lives in ACLs the Go mode cannot express).
		t.Errorf("bundle permissions = %v, want 0600 (owner-only evidence)", info.Mode().Perm())
	}

	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if int64(len(raw)) != res.Bytes {
		t.Errorf("reported bytes = %d, file has %d", res.Bytes, len(raw))
	}
	content := string(raw)
	for _, want := range []string{
		"c0wrk vector-index diagnostics",
		"collected: ",
		"stalled: no",
		"## status",
		"## goroutines",
		"goroutine ",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("bundle missing %q", want)
		}
	}
	// The status snapshot is the machine part: it must parse as the wire
	// status (with the branch identity the harness settled on).
	if !strings.Contains(content, `"state"`) {
		t.Error("bundle status section without the wire JSON fields")
	}
	if res.Truncated {
		t.Error("a live test process must not exhaust the dump cap")
	}
}

// TestRootCallbacks_OnStallChangeEmitsFocusedFullSnapshot pins the event
// half of the sub-status: a stall transition reaches the wire as a FULL
// status snapshot read back from the manager (so the push stream and
// GetVectorIndexStatus cannot disagree — the payload's branch/progress ride
// along), and stays silent for a non-focused root (the single UI-channel
// rule). The stalled FIELD propagation is pinned piecewise: the manager
// records its stall state core-side (TestManagerRecordStall_StatusAndCallback,
// which also pins that recordStall invokes OnStallChange) and the wire
// mapping is TestApplyStallToWire — this test pins the emitter's own
// contract. A live 5-minute stall cannot be forced against a real pass here.
func TestRootCallbacks_OnStallChangeEmitsFocusedFullSnapshot(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	focus := h.proj.WorkspacePath
	if err := h.api.SetGitPanelFocus(""); err != nil {
		t.Fatalf("SetGitPanelFocus reset: %v", err)
	}
	mgr, err := h.roots.FocusManager(context.Background())
	if err != nil {
		t.Fatalf("focus manager: %v", err)
	}
	waitForVectorBranch(t, mgr, "main")

	var emitted []VectorIndexStatus
	h.api.emitEvent = func(event string, data ...any) {
		if event != EventVectorIndexStatus || len(data) != 1 {
			return
		}
		if st, ok := data[0].(VectorIndexStatus); ok {
			emitted = append(emitted, st)
		}
	}

	cbs := h.roots.rootCallbacks(focus, mgr)
	cbs.OnStallChange(vectorindex.StallSnapshot{
		Stalled:   true,
		Stage:     string(vectorindex.StageEmbedding),
		SilentFor: 400 * time.Second,
		Watch:     1,
	})
	if len(emitted) != 1 {
		t.Fatalf("focused stall produced %d events, want 1", len(emitted))
	}
	st := emitted[0]
	// FULL snapshot: the progress/branch context the pill already renders
	// rides along, so the event never blanks the UI state.
	if st.Branch != "main" {
		t.Errorf("emitted snapshot Branch = %q, want main (read back from the manager)", st.Branch)
	}
	if st.State == "" || len(st.Indices) == 0 {
		t.Errorf("emitted snapshot is not a full status: %+v", st)
	}
	// The manager is not stalled here (no live stall recorded), so the
	// payload honestly reports not-stalled — the fields themselves are
	// mapping-pinned (TestApplyStallToWire).
	if st.Stalled {
		t.Errorf("snapshot reported stalled without a recorded stall: %+v", st)
	}

	// Its resolve is emitted the same way.
	cbs.OnStallChange(vectorindex.StallSnapshot{Stalled: false, Watch: 1})
	if len(emitted) != 2 {
		t.Fatalf("resolve produced %d events total, want 2", len(emitted))
	}
	if emitted[1].Stalled {
		t.Error("resolve event still carries stalled")
	}

	// A NON-focused root stays silent: the callbacks built for a foreign
	// root never emit while the focus is the checkout (the gate keys on the
	// ROOT the callbacks belong to).
	foreign := filepath.Join(t.TempDir(), "foreign-root")
	cbsOther := h.roots.rootCallbacks(foreign, mgr)
	cbsOther.OnStallChange(vectorindex.StallSnapshot{Stalled: true, Watch: 2})
	if len(emitted) != 2 {
		t.Fatalf("non-focused stall produced %d events total, want 2", len(emitted))
	}
}

// TestApplyStallToWire pins the wire mapping of the pass-liveness sub-status:
// an open window surfaces as stalled + stage + whole-second silence, a
// resolved/absent one leaves the zero fields (omitempty drops them on the
// wire, so "stalled" is visible exactly while it exists).
func TestApplyStallToWire(t *testing.T) {
	result := VectorIndexStatus{State: "indexing"}

	applyStallToWire(&result, vectorindex.StallSnapshot{})
	if result.Stalled || result.StallStage != "" || result.StallSeconds != 0 {
		t.Errorf("absent stall mapped fields: %+v", result)
	}

	applyStallToWire(&result, vectorindex.StallSnapshot{
		Stalled:   true,
		Stage:     string(vectorindex.StageReadHashChunk),
		SilentFor: 305 * time.Second,
		Watch:     4,
	})
	if !result.Stalled {
		t.Error("open window not surfaced")
	}
	if result.StallStage != "read_hash_chunk" {
		t.Errorf("StallStage = %q, want read_hash_chunk", result.StallStage)
	}
	if result.StallSeconds != 305 {
		t.Errorf("StallSeconds = %d, want 305 (whole seconds)", result.StallSeconds)
	}

	// A RESOLVED window (Stalled=false, later Watch) must clear the fields —
	// the wire never reports a stale stall.
	result = VectorIndexStatus{State: "indexing", Stalled: true, StallStage: "stale", StallSeconds: 999}
	applyStallToWire(&result, vectorindex.StallSnapshot{Stalled: false, Watch: 4})
	if result.Stalled || result.StallStage != "" || result.StallSeconds != 0 {
		t.Errorf("resolved window kept stale fields: %+v", result)
	}
}

// TestVectorDiagnosticsBundle_StallEvidence pins the human header: a stalled
// snapshot names the stage and the live silence in words, and a truncated
// dump is flagged in the header before the machine sections.
func TestVectorDiagnosticsBundle_StallEvidence(t *testing.T) {
	st := VectorIndexStatus{
		State:        "indexing",
		Progress:     0.5,
		FilesIndexed: 5,
		TotalFiles:   10,
		Stalled:      true,
		StallStage:   "embedding",
		StallSeconds: 335,
	}
	bundle := string(vectorDiagnosticsBundle(st, "goroutine 1 [running]:\n", false))
	if !strings.Contains(bundle, `stalled: stage "embedding", no progress for 335 s (threshold: 5 m)`) {
		t.Errorf("stall evidence line missing: %s", bundle)
	}
	if strings.Contains(bundle, "TRUNCATED") {
		t.Error("untruncated dump flagged as truncated")
	}

	bundle = string(vectorDiagnosticsBundle(st, "partial", true))
	if !strings.Contains(bundle, "TRUNCATED at the 512 KiB cap") {
		t.Error("truncated dump not flagged in the header")
	}
}
