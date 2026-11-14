package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// newVectorStatusAPI builds a FrontendAPI with a pristine per-root registry:
// no factory wired, no focus, no managers. The startup-publication flag is
// seeded so the guardless field readers run against this fixture.
func newVectorStatusAPI(t *testing.T) *FrontendAPI {
	t.Helper()
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: "vector-status-fixture",
		emitEvent:       func(string, ...any) {},
	}
	f.seedPublished.Store(true)
	return f
}

// TestGetVectorIndexStatus_BranchesAlwaysExplainThemselves is the FocusManager
// routing table: each row drives GetVectorIndexStatus down one branch of the
// registry and pins the resulting wire state. Two rules hold for every row —
// an "unavailable" snapshot ALWAYS carries a non-empty reason, and the two
// non-failure windows (the embedder still loading, an eviction drain) never
// report "unavailable" at all: they resolve on their own, so rendering them as
// a broken index is the bug this table prevents.
func TestGetVectorIndexStatus_BranchesAlwaysExplainThemselves(t *testing.T) {
	root := canonicalRoot(t.TempDir())

	cases := []struct {
		name      string
		setup     func(*FrontendAPI, *VectorRoots)
		wantState string
		wantKind  string
	}{
		{
			name:      "no focus root",
			setup:     func(_ *FrontendAPI, vr *VectorRoots) { vr.LeaveFocus() },
			wantState: vectorStateUnavailable,
			wantKind:  vectorErrorKindNoTarget,
		},
		{
			name: "embedder init still loading",
			setup: func(_ *FrontendAPI, vr *VectorRoots) {
				// No factory and no settled readiness: the background ONNX
				// init has not answered yet.
				vr.focus = root
			},
			wantState: vectorStateLoading,
			wantKind:  vectorErrorKindLoading,
		},
		{
			name: "embedder init settled without a factory",
			setup: func(_ *FrontendAPI, vr *VectorRoots) {
				ready := make(chan struct{})
				close(ready)
				vr.SetFactory(nil, ready, nil)
				vr.focus = root
			},
			wantState: vectorStateUnavailable,
			wantKind:  string(vectorindex.IndexErrorKindFatal),
		},
		{
			name: "eviction drain window",
			setup: func(_ *FrontendAPI, vr *VectorRoots) {
				ready := make(chan struct{})
				close(ready)
				vr.SetFactory(nil, ready, nil)
				vr.focus = root
				// An eviction of this root is still draining: the channel
				// never closes, and the bounded wait is shortened so the probe
				// does not sit out the production 25 s.
				vr.evictWait = time.Millisecond
				vr.closing[root] = make(chan struct{})
			},
			wantState: vectorStateLoading,
			wantKind:  vectorErrorKindRestarting,
		},
		{
			name: "root cannot be routed",
			setup: func(_ *FrontendAPI, vr *VectorRoots) {
				ready := make(chan struct{})
				close(ready)
				vr.SetFactory(func() (*vectorindex.Manager, error) {
					return nil, errors.New("embedder is down")
				}, ready, nil)
				vr.focus = root
			},
			wantState: vectorStateUnavailable,
			wantKind:  string(vectorindex.IndexErrorKindFatal),
		},
		{
			name: "registry shut down",
			setup: func(_ *FrontendAPI, vr *VectorRoots) {
				vr.ShutdownAll()
				// ShutdownAll clears the focus; a caller racing the shutdown
				// still holds the root — that is the branch this pin covers.
				vr.focus = root
			},
			wantState: vectorStateUnavailable,
			wantKind:  vectorErrorKindShuttingDown,
		},
		{
			name: "master switch off",
			setup: func(_ *FrontendAPI, vr *VectorRoots) {
				vr.focus = root
				vr.Disable()
			},
			wantState: vectorStateDisabled,
			wantKind:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newVectorStatusAPI(t)
			vr := api.vectorRootsRegistry()
			t.Cleanup(vr.ShutdownAll)
			tc.setup(api, vr)

			st := api.GetVectorIndexStatus()
			if st.State != tc.wantState {
				t.Fatalf("state = %q (last_error %q), want %q", st.State, st.LastError, tc.wantState)
			}
			if st.ErrorKind != tc.wantKind {
				t.Errorf("error kind = %q, want %q", st.ErrorKind, tc.wantKind)
			}

			switch st.State {
			case vectorStateUnavailable:
				if st.LastError == "" {
					t.Fatal("unavailable status without a reason — the surface cannot explain itself")
				}
				if st.ErrorKind == "" {
					t.Error("unavailable status without an error kind")
				}
			case vectorStateLoading:
				if st.LastError == "" {
					t.Error("loading status without a reason — the window is unexplained")
				}
			case vectorStateDisabled:
				// A switched-off subsystem is a USER state, not a failure:
				// the switch is the explanation, so no cause is attached.
				if st.LastError != "" {
					t.Errorf("disabled status carried a failure reason %q", st.LastError)
				}
			}
		})
	}
}

// TestGetVectorIndexStatus_NoProjectExplainsItself pins the No-Project branch:
// the dormant subsystem still reports "unavailable" (the historical wire state
// the UI keys off), but it now says why instead of leaving the surface bare.
func TestGetVectorIndexStatus_NoProjectExplainsItself(t *testing.T) {
	api := newVectorStatusAPI(t)
	api.activeProjectID = project.NoProjectID
	t.Cleanup(api.vectorRootsRegistry().ShutdownAll)

	st := api.GetVectorIndexStatus()
	if st.State != vectorStateUnavailable {
		t.Fatalf("state = %q, want %q", st.State, vectorStateUnavailable)
	}
	if st.LastError == "" {
		t.Fatal("No-Project status reported unavailable with no reason")
	}
	if st.ErrorKind != vectorErrorKindNoTarget {
		t.Errorf("error kind = %q, want %q", st.ErrorKind, vectorErrorKindNoTarget)
	}
	if !strings.Contains(st.LastError, "No Project") {
		t.Errorf("last error = %q, want it to name the No-Project reason", st.LastError)
	}
}

// TestRootCallbacks_OnFailureEmitsTheCause pins the event contract: the
// vector_index:status payload for a failed init carries the cause and its
// kind, so a subscriber that only sees the stream (never the RPC) still learns
// why the index went unavailable. The Warn is the deliberate diagnostic and is
// asserted at its source.
func TestRootCallbacks_OnFailureEmitsTheCause(t *testing.T) {
	api := newVectorStatusAPI(t)
	root := canonicalRoot(t.TempDir())

	var emitted []VectorIndexStatus
	api.emitEvent = func(name string, data ...any) {
		if name != EventVectorIndexStatus || len(data) != 1 {
			return
		}
		if st, ok := data[0].(VectorIndexStatus); ok {
			emitted = append(emitted, st)
		}
	}

	vr := api.vectorRootsRegistry()
	t.Cleanup(vr.ShutdownAll)
	vr.focus = root

	initErr := fmt.Errorf("attach snapshot view: %w", vectorindex.ErrCorpusLegMissing)
	captureMiscDiagnostics(t, api, miscExpectedDiagnostic{
		message: "vector index init failed; search unavailable for this root",
		attrs: map[string]string{
			"root":  root,
			"error": initErr.Error(),
		},
	})

	// A manager with no recorded reason yet: the emitter must fall back to the
	// error it was handed, never emit a bare state.
	vr.rootCallbacks(root, &vectorindex.Manager{}).OnFailure(initErr)

	if len(emitted) != 1 {
		t.Fatalf("emitted %d statuses, want exactly 1: %+v", len(emitted), emitted)
	}
	st := emitted[0]
	if st.State != string(vectorindex.IndexStateUnavailable) {
		t.Errorf("state = %q, want %q", st.State, vectorindex.IndexStateUnavailable)
	}
	if st.LastError == "" {
		t.Fatal("failed-init event carried no reason")
	}
	if !strings.Contains(st.LastError, vectorindex.ErrCorpusLegMissing.Error()) {
		t.Errorf("last error = %q, want it to carry the wrapped cause", st.LastError)
	}
	if st.ErrorKind != string(vectorindex.IndexErrorKindRepairable) {
		t.Errorf("error kind = %q, want %q", st.ErrorKind, vectorindex.IndexErrorKindRepairable)
	}
}

// TestRootCallbacks_OnProgressCarriesTheSuspensionPair pins the stream
// counterpart of ADR-122's pair-persistence rule: every progress push emitted
// while the manager's suspended verdict stands carries the pair — including a
// terminal READY report (a failing branch-switch pass over a live collection),
// which without the rule would wipe the verdict from the UI's event stream and
// repaint the silent suspension as a bare, healthy ready. Non-unavailable
// pushes of any other kind stay clean, exactly as before.
func TestRootCallbacks_OnProgressCarriesTheSuspensionPair(t *testing.T) {
	api := newVectorStatusAPI(t)
	root := canonicalRoot(t.TempDir())
	vr := api.vectorRootsRegistry()
	t.Cleanup(vr.ShutdownAll)
	vr.focus = root

	var emitted []VectorIndexStatus
	api.emitEvent = func(name string, data ...any) {
		if name != EventVectorIndexStatus || len(data) != 1 {
			return
		}
		if st, ok := data[0].(VectorIndexStatus); ok {
			emitted = append(emitted, st)
		}
	}

	// The carry rule itself, as data: unavailable always carries (any kind,
	// even a freshly-created manager's empty one), and the suspended kind
	// carries in EVERY state — a non-suspended kind on a non-unavailable
	// state does not.
	for _, tc := range []struct {
		state vectorindex.IndexState
		kind  string
		want  bool
	}{
		{vectorindex.IndexStateUnavailable, "", true},
		{vectorindex.IndexStateUnavailable, string(vectorindex.IndexErrorKindFatal), true},
		{vectorindex.IndexStateReady, "", false},
		{vectorindex.IndexStateReady, string(vectorindex.IndexErrorKindRepairable), false},
		{vectorindex.IndexStateReady, string(vectorindex.IndexErrorKindSuspended), true},
		{vectorindex.IndexStateIndexing, string(vectorindex.IndexErrorKindSuspended), true},
		{vectorindex.IndexStateReindexing, string(vectorindex.IndexErrorKindSuspended), true},
	} {
		if got := carryStatusReason(tc.state, tc.kind); got != tc.want {
			t.Errorf("carryStatusReason(state=%q, kind=%q) = %v, want %v", tc.state, tc.kind, got, tc.want)
		}
	}

	cbs := vr.rootCallbacks(root, &vectorindex.Manager{})

	// A ready report with no suspension recorded: clean payload, no pair.
	cbs.OnProgress(vectorindex.PhaseBoth, vectorindex.IndexStateReady, 3, 9, "a.go")
	if len(emitted) != 1 {
		t.Fatalf("emitted %d statuses after the ready push, want 1", len(emitted))
	}
	if emitted[0].LastError != "" || emitted[0].ErrorKind != "" {
		t.Errorf("a clean ready push carried a pair: reason=%q kind=%q", emitted[0].LastError, emitted[0].ErrorKind)
	}
	if emitted[0].State != string(vectorindex.IndexStateReady) || emitted[0].FilesIndexed != 3 || emitted[0].TotalFiles != 9 {
		t.Errorf("ready push payload = %+v, want the pass's own progress values", emitted[0])
	}

	// An unavailable report always carries the pair slot (a fresh manager has
	// an empty one — the values come from the manager either way).
	cbs.OnProgress(vectorindex.PhaseBoth, vectorindex.IndexStateUnavailable, 0, 0, "")
	if len(emitted) != 2 {
		t.Fatalf("emitted %d statuses after the unavailable push, want 2", len(emitted))
	}
	if emitted[1].State != string(vectorindex.IndexStateUnavailable) {
		t.Errorf("unavailable push state = %q", emitted[1].State)
	}
}

// TestVectorIndexStatus_OmitEmptyKeepsExistingConsumersHappy pins the wire
// shape: a healthy status serializes without the new fields at all, so a
// consumer (or guard) that only knows the pre-existing keys sees exactly the
// payload it saw before.
func TestVectorIndexStatus_OmitEmptyKeepsExistingConsumersHappy(t *testing.T) {
	raw, err := json.Marshal(VectorIndexStatus{State: "ready", Indices: []string{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"last_error", "error_kind"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("healthy status payload carries %q: %s", key, raw)
		}
	}
	if !strings.Contains(string(raw), `"state":"ready"`) {
		t.Errorf("payload lost the state key: %s", raw)
	}

	// The failure payload carries both, and the manager's own deadline-free
	// state ("unavailable" + a cause) round-trips.
	raw, err = json.Marshal(VectorIndexStatus{
		State:     "unavailable",
		Indices:   []string{},
		LastError: "corpus is unopenable",
		ErrorKind: "repairable",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"last_error":"corpus is unopenable"`, `"error_kind":"repairable"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("failure payload = %s, want %s", raw, want)
		}
	}
}

// TestVectorIndexStatusFromRegistryError_DefaultBranchRidesTheTaxonomy pins
// the two-layer symmetry of the failure kind: a manager-build error that
// reaches the surface through the registry (SwitchProject/ManagerForRoot —
// creating storage directories, opening the store) is classified by the SAME
// taxonomy the manager's own failures use, so one cause keeps one verdict
// whichever layer surfaced it. An OS-level fault stays `transient` (a retry
// after it clears can succeed); only a genuinely settled failure reads
// `fatal` — the hardcoded "fatal" lied to the user about curable states.
func TestVectorIndexStatusFromRegistryError_DefaultBranchRidesTheTaxonomy(t *testing.T) {
	f := newVectorStatusAPI(t)

	cases := []struct {
		name    string
		err     error
		wantKin string
	}{
		{
			name:    "storage permission fault",
			err:     fmt.Errorf("switching project: %w", &fs.PathError{Op: "mkdir", Path: "/tmp/vi", Err: fs.ErrPermission}),
			wantKin: string(vectorindex.IndexErrorKindTransient),
		},
		{
			name:    "no space left on device",
			err:     fmt.Errorf("switching project: %w", &fs.PathError{Op: "mkdir", Path: "/tmp/vi", Err: syscall.ENOSPC}),
			wantKin: string(vectorindex.IndexErrorKindTransient),
		},
		{
			name:    "settled wiring failure",
			err:     errors.New("embedder factory failed after settled init"),
			wantKin: string(vectorindex.IndexErrorKindFatal),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := f.vectorIndexStatusFromRegistryError(tc.err)
			if st.State != vectorStateUnavailable {
				t.Errorf("state = %q; want unavailable", st.State)
			}
			if st.ErrorKind != tc.wantKin {
				t.Errorf("kind = %q; want %q", st.ErrorKind, tc.wantKin)
			}
			if st.LastError == "" {
				t.Error("the unavailable snapshot carries no cause")
			}
		})
	}
}
