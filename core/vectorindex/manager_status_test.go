package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
)

// TestClassifyInitError_Table pins the status taxonomy on the store's own
// repair vocabulary: a proven store defect is repairable (a rebuild heals
// it), an OS-level fault is transient (retry later), and anything else is
// fatal. This is what VectorIndexStatus.ErrorKind reports, so a UI picks the
// remedy without parsing messages.
func TestClassifyInitError_Table(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want IndexErrorKind
	}{
		{"nil carries no kind", nil, ""},
		{"missing file", fs.ErrNotExist, IndexErrorKindRepairable},
		{"corpus not published", ErrCorpusNotPublished, IndexErrorKindRepairable},
		{"corpus era mismatch", ErrCorpusEraMismatch, IndexErrorKindRepairable},
		{"corpus lexical leg missing", ErrCorpusLegMissing, IndexErrorKindRepairable},
		{"corpus unopenable", ErrCorpusUnopenable, IndexErrorKindRepairable},
		{"overlay unopenable", ErrOverlayUnopenable, IndexErrorKindRepairable},
		{"no indexable files", ErrCorpusNoIndexableFiles, IndexErrorKindRepairable},
		{"torn chunk record", errChunkRecordTorn, IndexErrorKindRepairable},
		{"wrapped defect", fmt.Errorf("attach snapshot view: %w", ErrCorpusUnopenable), IndexErrorKindRepairable},
		{"permission denied", fs.ErrPermission, IndexErrorKindTransient},
		{"no space", syscall.ENOSPC, IndexErrorKindTransient},
		{"too many open files", syscall.EMFILE, IndexErrorKindTransient},
		{"io error", syscall.EIO, IndexErrorKindTransient},
		{"cancelled", context.Canceled, IndexErrorKindTransient},
		{"deadline", context.DeadlineExceeded, IndexErrorKindTransient},
		{"wrapped permission", fmt.Errorf("opening lexical index: %w", fs.ErrPermission), IndexErrorKindTransient},
		{"branch detect", errors.New("detecting branch: exit status 128"), IndexErrorKindFatal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyInitError(tc.err); got != tc.want {
				t.Errorf("ClassifyInitError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestNotifyInitFailure_RecordsReasonAndKind pins the invariant: a terminal
// "unavailable" always says WHY. The callback runs AFTER the reason is
// recorded (the backend's status emitter reads it back), and the wrapped
// cause survives into the reported text.
func TestNotifyInitFailure_RecordsReasonAndKind(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantKind IndexErrorKind
		wantText string
	}{
		{
			name:     "proven store defect",
			err:      fmt.Errorf("attach snapshot view: %w", ErrCorpusLegMissing),
			wantKind: IndexErrorKindRepairable,
			wantText: ErrCorpusLegMissing.Error(),
		},
		{
			name:     "transient OS fault",
			err:      fmt.Errorf("opening lexical index: %w", fs.ErrPermission),
			wantKind: IndexErrorKindTransient,
			wantText: "opening lexical index",
		},
		{
			name:     "settled failure",
			err:      errors.New("detecting branch: exit status 128"),
			wantKind: IndexErrorKindFatal,
			wantText: "detecting branch",
		},
		{
			name:     "nil error still explains itself",
			err:      nil,
			wantKind: IndexErrorKindUnknown,
			wantText: "without a reported cause",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{}
			var seenDuringCallback IndexStatus
			called := false
			m.notifyInitFailure(ProjectCallbacks{
				OnFailure: func(error) {
					called = true
					// The emitter's view: the reason must already be there.
					seenDuringCallback = m.GetIndexStatus()
				},
			}, tc.err)
			if !called {
				t.Fatal("OnFailure was not invoked")
			}
			if seenDuringCallback.State != IndexStateUnavailable {
				t.Errorf("state during callback = %q, want %q", seenDuringCallback.State, IndexStateUnavailable)
			}
			if seenDuringCallback.LastError == "" || seenDuringCallback.ErrorKind == "" {
				t.Fatalf("callback saw unavailable with no cause: %+v", seenDuringCallback)
			}

			st := m.GetIndexStatus()
			if st.State != IndexStateUnavailable {
				t.Errorf("state = %q, want %q", st.State, IndexStateUnavailable)
			}
			if st.ErrorKind != tc.wantKind {
				t.Errorf("kind = %q, want %q", st.ErrorKind, tc.wantKind)
			}
			if !strings.Contains(st.LastError, tc.wantText) {
				t.Errorf("last error = %q, want it to contain %q", st.LastError, tc.wantText)
			}
		})
	}
}

// TestManagerStatus_UnavailableAlwaysCarriesReason covers every route into the
// terminal state the manager owns — the init-failure path and the pass-level
// progress report (which carries a state and no error at all).
func TestManagerStatus_UnavailableAlwaysCarriesReason(t *testing.T) {
	t.Run("failed attach", func(t *testing.T) {
		m := &Manager{}
		m.notifyInitFailure(ProjectCallbacks{}, fmt.Errorf("attach snapshot view: %w", ErrCorpusNotPublished))
		assertUnavailableWithReason(t, m.GetIndexStatus())
	})

	t.Run("pass-level report", func(t *testing.T) {
		m := &Manager{}
		m.wrapProgress(nil)(PhaseBoth, IndexStateUnavailable, 0, 0, "")
		st := m.GetIndexStatus()
		assertUnavailableWithReason(t, st)
		if st.ErrorKind != IndexErrorKindUnknown {
			t.Errorf("kind = %q, want %q for a cause-less pass report", st.ErrorKind, IndexErrorKindUnknown)
		}
	})

	t.Run("pass report keeps the recorded cause", func(t *testing.T) {
		m := &Manager{}
		m.notifyInitFailure(ProjectCallbacks{}, fs.ErrPermission)
		m.wrapProgress(nil)(PhaseBoth, IndexStateUnavailable, 0, 0, "")
		st := m.GetIndexStatus()
		assertUnavailableWithReason(t, st)
		if st.ErrorKind != IndexErrorKindTransient {
			t.Errorf("kind = %q, want the recorded %q", st.ErrorKind, IndexErrorKindTransient)
		}
		if !strings.Contains(st.LastError, fs.ErrPermission.Error()) {
			t.Errorf("last error = %q, want the recorded cause", st.LastError)
		}
	})
}

// TestManagerStatus_RecoveryClearsTheReason pins the other half: a fresh init
// attempt, a successful attach and a ready report all drop the stale cause, so
// a recovered index never carries the story of its last failure.
func TestManagerStatus_RecoveryClearsTheReason(t *testing.T) {
	clearers := []struct {
		name      string
		clear     func(*Manager)
		wantState IndexState
	}{
		{
			name: "fresh init attempt",
			clear: func(m *Manager) {
				m.setStatus(map[string]any{"state": IndexStateLoading, "phase": PhaseOpen})
			},
			wantState: IndexStateLoading,
		},
		{
			// The attach path clears the reason and lets the pass's own
			// progress report move the state; here the clear is pinned alone.
			name:      "successful attach",
			clear:     func(m *Manager) { m.clearIndexError() },
			wantState: IndexStateUnavailable,
		},
		{
			name: "ready progress report",
			clear: func(m *Manager) {
				m.wrapProgress(nil)(PhaseBoth, IndexStateReady, 4, 4, "")
			},
			wantState: IndexStateReady,
		},
	}
	for _, tc := range clearers {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{}
			m.notifyInitFailure(ProjectCallbacks{}, ErrCorpusUnopenable)
			if st := m.GetIndexStatus(); st.LastError == "" {
				t.Fatal("precondition: no reason recorded")
			}

			tc.clear(m)

			st := m.GetIndexStatus()
			if st.LastError != "" || st.ErrorKind != "" {
				t.Errorf("reason survived recovery: %+v", st)
			}
			if st.State != tc.wantState {
				t.Errorf("state = %q, want %q", st.State, tc.wantState)
			}
		})
	}
}

// TestTransientAndRepairable_AreDisjoint pins the relationship the taxonomy
// relies on: the two classifiers never claim the same error, and Repairable's
// verdict is exactly "a proven defect that is not a transient fault".
func TestTransientAndRepairable_AreDisjoint(t *testing.T) {
	errs := []error{
		nil,
		fs.ErrNotExist,
		ErrCorpusLegMissing,
		errChunkRecordTorn,
		fs.ErrPermission,
		syscall.ENOSPC,
		context.Canceled,
		errors.New("something else"),
	}
	for _, err := range errs {
		if Transient(err) && Repairable(err) {
			t.Errorf("%v is both transient and repairable", err)
		}
	}
	if !Transient(fs.ErrPermission) || Transient(ErrCorpusLegMissing) {
		t.Error("Transient misclassifies the permission/leg-missing pair")
	}
	if Transient(nil) || Repairable(nil) {
		t.Error("nil proves nothing: it must be neither transient nor repairable")
	}
}

// assertUnavailableWithReason asserts the one invariant every unavailable
// status owes a UI: a non-empty cause and a non-empty kind.
func assertUnavailableWithReason(t *testing.T, st IndexStatus) {
	t.Helper()
	if st.State != IndexStateUnavailable {
		t.Fatalf("state = %q, want %q", st.State, IndexStateUnavailable)
	}
	if st.LastError == "" {
		t.Fatal("unavailable without a reason")
	}
	if st.ErrorKind == "" {
		t.Fatal("unavailable without an error kind")
	}
}

// TestWrapProgress_ExplainsItselfBeforeTheCallback pins the ordering the status
// emitters depend on: by the time a consumer's OnProgress runs, the manager's
// snapshot already carries the reason (a pass-level "unavailable" gets a
// defensive one because the callback itself transports only a state). An event
// built from GetIndexStatus() is therefore never a bare state — and a ready
// report has already cleared the cause it recovered from.
func TestWrapProgress_ExplainsItselfBeforeTheCallback(t *testing.T) {
	m := &Manager{}
	var duringCallback []IndexStatus
	wrapped := m.wrapProgress(func(_ IndexPhase, _ IndexState, _, _ int, _ string) {
		duringCallback = append(duringCallback, m.GetIndexStatus())
	})

	wrapped(PhaseBoth, IndexStateUnavailable, 0, 0, "")
	wrapped(PhaseBoth, IndexStateReady, 2, 2, "")

	if len(duringCallback) != 2 {
		t.Fatalf("callback ran %d times, want 2", len(duringCallback))
	}
	assertUnavailableWithReason(t, duringCallback[0])
	if duringCallback[1].LastError != "" || duringCallback[1].ErrorKind != "" {
		t.Errorf("ready callback saw a stale reason: %+v", duringCallback[1])
	}
}
