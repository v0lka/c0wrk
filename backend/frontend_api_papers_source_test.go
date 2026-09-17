package backend

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/papers"
)

// ---------------------------------------------------------------------------
// FetchPaperOriginal — paper.html source-fetch RPC
// ---------------------------------------------------------------------------

// fetchCall captures what the seam received, so tests can pin the plumbing:
// the RPC must hand the fetcher the containment-checked library root and the
// RESOLVED record (parsed from disk), not the caller's id-or-slug string.
type fetchCall struct {
	libraryRoot string
	rec         papers.PaperRecord
}

// writePaperHTMLSeam returns a seam that records its arguments and writes
// paper.html into <libraryRoot>/<slug>/ — the observable effect the happy
// path asserts on disk. The real fetcher's write pipeline (staging, atomic
// rename, containment) is covered by core/papers/original_test.go.
func writePaperHTMLSeam(calls *[]fetchCall) func(context.Context, *http.Client, string, papers.PaperRecord) papers.FetchResult {
	return func(_ context.Context, _ *http.Client, libraryRoot string, rec papers.PaperRecord) papers.FetchResult {
		*calls = append(*calls, fetchCall{libraryRoot: libraryRoot, rec: rec})
		dir := filepath.Join(libraryRoot, rec.ResolvedSlug())
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return papers.FetchResult{Status: papers.FetchStatusError, Err: err}
		}
		if err := os.WriteFile(filepath.Join(dir, papers.OriginalHTMLFileName), []byte("<html>body</html>"), 0o644); err != nil {
			return papers.FetchResult{Status: papers.FetchStatusError, Err: err}
		}
		return papers.FetchResult{
			Status:      papers.FetchStatusOK,
			ArxivID:     papers.NormalizeArxivID(rec.Identifiers),
			ResolvedURL: "https://arxiv.org/html/1706.03762",
		}
	}
}

// TestFetchPaperOriginal_OK pins the happy path: the RPC resolves the paper
// inside the containment-checked library, hands the fetcher the library root
// and the resolved record, maps ok, passes the resolved URL through, and
// paper.html exists on disk after the call.
func TestFetchPaperOriginal_OK(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)
	seedTestPaper(t, libraryRoot, papers.PaperRecord{
		Title:       "Attention Is All You Need",
		Slug:        "attention",
		Identifiers: []papers.Identifier{{Scheme: "arxiv", Value: "arXiv:1706.03762"}},
	})

	var calls []fetchCall
	api.fetchPaperOriginalFn = writePaperHTMLSeam(&calls)

	dto, err := api.FetchPaperOriginal(projectID, "attention")
	if err != nil {
		t.Fatalf("FetchPaperOriginal: %v", err)
	}
	if dto.Status != origStatusOK {
		t.Fatalf("status = %q, want %q", dto.Status, origStatusOK)
	}
	if dto.URL != "https://arxiv.org/html/1706.03762" {
		t.Fatalf("url = %q, want the resolved URL passed through", dto.URL)
	}
	if _, err := os.Stat(filepath.Join(libraryRoot, "attention", papers.OriginalHTMLFileName)); err != nil {
		t.Fatalf("expected paper.html on disk: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("fetcher called %d times, want 1", len(calls))
	}
	if calls[0].libraryRoot != libraryRoot {
		t.Fatalf("fetcher library root = %q, want %q", calls[0].libraryRoot, libraryRoot)
	}
	if got := papers.NormalizeArxivID(calls[0].rec.Identifiers); got != "1706.03762" {
		t.Fatalf("resolved record identifiers = %v (normalized %q), want the arXiv id", calls[0].rec.Identifiers, got)
	}
}

// TestFetchPaperOriginal_StatusMapping covers the RPC-level status mapping:
// offline / no_arxiv / not_found / error from the fetcher surface as the
// matching DTO status (never a Go error, never a silent empty failure), with
// no URL for a non-success outcome.
func TestFetchPaperOriginal_StatusMapping(t *testing.T) {
	cases := []struct {
		name string
		res  papers.FetchResult
		want string
	}{
		{"offline", papers.FetchResult{Status: papers.FetchStatusOffline, Err: errors.New("dial tcp: refused")}, origStatusOffline},
		{"no_arxiv", papers.FetchResult{Status: papers.FetchStatusNoArxiv}, origStatusNoArxiv},
		{"not_found", papers.FetchResult{Status: papers.FetchStatusNotFound}, origStatusNotFound},
		{"error", papers.FetchResult{Status: papers.FetchStatusError, Err: errors.New("boom")}, origStatusError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
			libraryRoot := config.PaperLibraryPath(ws)
			dir := seedTestPaper(t, libraryRoot, papers.PaperRecord{Title: "Mapped", Slug: "mapped"})
			res := tc.res
			api.fetchPaperOriginalFn = func(context.Context, *http.Client, string, papers.PaperRecord) papers.FetchResult {
				return res
			}

			dto, err := api.FetchPaperOriginal(projectID, "mapped")
			if err != nil {
				t.Fatalf("FetchPaperOriginal: %v (a non-success fetch is data, not an error)", err)
			}
			if dto.Status != tc.want {
				t.Fatalf("status = %q, want %q", dto.Status, tc.want)
			}
			if dto.URL != "" {
				t.Fatalf("url = %q, want empty for status %q", dto.URL, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(dir, papers.OriginalHTMLFileName)); statErr == nil {
				t.Fatalf("status %q must not write paper.html", tc.want)
			}
		})
	}
}

// TestFetchPaperOriginal_NoArxivRealFetcher runs the PRODUCTION fetcher (no
// seam): a card with no arXiv identifier classifies as no_arxiv without
// touching the network, and nothing is written.
func TestFetchPaperOriginal_NoArxivRealFetcher(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)
	dir := seedTestPaper(t, libraryRoot, papers.PaperRecord{
		Title:       "No Arxiv Card",
		Slug:        "no-arxiv-card",
		Identifiers: []papers.Identifier{{Scheme: "doi", Value: "10.1/no-arxiv"}},
	})

	dto, err := api.FetchPaperOriginal(projectID, "no-arxiv-card")
	if err != nil {
		t.Fatalf("FetchPaperOriginal: %v", err)
	}
	if dto.Status != origStatusNoArxiv {
		t.Fatalf("status = %q, want %q", dto.Status, origStatusNoArxiv)
	}
	if _, statErr := os.Stat(filepath.Join(dir, papers.OriginalHTMLFileName)); statErr == nil {
		t.Fatal("no_arxiv must not write paper.html")
	}
}

// TestFetchPaperOriginal_UnknownPaper pins the error contract: an unknown
// paper id (or slug) is a Go error, never a silent status.
func TestFetchPaperOriginal_UnknownPaper(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	seedTestPaper(t, config.PaperLibraryPath(ws), papers.PaperRecord{Title: "Known", Slug: "known"})
	api.fetchPaperOriginalFn = func(context.Context, *http.Client, string, papers.PaperRecord) papers.FetchResult {
		t.Fatal("the fetcher must not run for an unknown paper")
		return papers.FetchResult{Status: papers.FetchStatusError}
	}

	if _, err := api.FetchPaperOriginal(projectID, "no-such-paper"); err == nil {
		t.Fatal("expected an error for an unknown paper")
	} else if want := "not found in the library"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to mention %q", err, want)
	}
}

// TestFetchPaperOriginal_ProjectGuards pins the project-resolution guards:
// an empty paper id, an unknown project, and the No Project pseudo-project
// are Go errors before any fetch runs.
func TestFetchPaperOriginal_ProjectGuards(t *testing.T) {
	api, projectID, _, _ := papersTestFrontend(t, "", project.ResearchPins{})
	api.fetchPaperOriginalFn = func(context.Context, *http.Client, string, papers.PaperRecord) papers.FetchResult {
		t.Error("the fetcher must not run for an unresolvable project/paper")
		return papers.FetchResult{Status: papers.FetchStatusError}
	}

	if _, err := api.FetchPaperOriginal(projectID, "  "); err == nil {
		t.Fatal("expected an error for an empty paper id")
	}
	if _, err := api.FetchPaperOriginal("no-such-project", "x"); err == nil {
		t.Fatal("expected an error for an unknown project")
	}
	if _, err := api.projectManager.EnsureNoProject(); err != nil {
		t.Fatalf("ensure no-project: %v", err)
	}
	if _, err := api.FetchPaperOriginal(project.NoProjectID, "x"); err == nil {
		t.Fatal("expected an error for the No Project pseudo-project")
	}
}

// TestFetchPaperOriginal_HoldsPaperGuardNotRowMutex pins the concurrency
// contract (mirroring TestRunPaperLiterature_DoesNotHoldRowMutexDuringRun):
// the network-bound fetch must NOT hold the per-research-root row-mutation
// mutex, while the per-paper-directory guard IS held for the run so two
// clicks on the same paper cannot interleave their writes.
func TestFetchPaperOriginal_HoldsPaperGuardNotRowMutex(t *testing.T) {
	api, projectID, ws, effectiveRoot := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)
	dir := seedTestPaper(t, libraryRoot, papers.PaperRecord{Title: "Guard Probe", Slug: "guard-probe"})

	started := make(chan struct{})
	release := make(chan struct{})
	api.fetchPaperOriginalFn = func(_ context.Context, _ *http.Client, root string, rec papers.PaperRecord) papers.FetchResult {
		if err := os.MkdirAll(filepath.Join(root, rec.ResolvedSlug()), 0o755); err != nil {
			return papers.FetchResult{Status: papers.FetchStatusError, Err: err}
		}
		close(started)
		<-release
		if err := os.WriteFile(filepath.Join(root, rec.ResolvedSlug(), papers.OriginalHTMLFileName), []byte("x"), 0o644); err != nil {
			return papers.FetchResult{Status: papers.FetchStatusError, Err: err}
		}
		return papers.FetchResult{Status: papers.FetchStatusOK, ResolvedURL: "https://arxiv.org/html/1"}
	}

	rowMu := api.researchMutationMu(effectiveRoot)
	guard := api.researchMutationMu(paperRunMuKey(dir))

	done := make(chan *PaperOriginalDTO, 1)
	errDone := make(chan error, 1)
	go func() {
		dto, err := api.FetchPaperOriginal(projectID, "guard-probe")
		if err != nil {
			errDone <- err
			return
		}
		done <- dto
	}()

	select {
	case <-started:
	case err := <-errDone:
		t.Fatalf("FetchPaperOriginal failed before the fetch started: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the fetcher never started")
	}

	// The fetch is in flight: the projects-row mutex must NOT be held (a pin
	// or flashcard write stays concurrent), but the per-paper guard IS.
	if !rowMu.TryLock() {
		close(release)
		t.Fatal("FetchPaperOriginal held the projects-row mutation mutex across the fetch")
	}
	rowMu.Unlock()
	if guard.TryLock() {
		guard.Unlock()
		close(release)
		t.Fatal("FetchPaperOriginal did not hold the per-paper guard during the fetch")
	}

	close(release)
	select {
	case dto := <-done:
		if dto.Status != origStatusOK {
			t.Fatalf("status = %q, want %q", dto.Status, origStatusOK)
		}
	case err := <-errDone:
		t.Fatalf("FetchPaperOriginal: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("FetchPaperOriginal did not return after the fetcher was released")
	}
	if _, err := os.Stat(filepath.Join(dir, papers.OriginalHTMLFileName)); err != nil {
		t.Fatalf("expected paper.html on disk: %v", err)
	}
}

// TestPaperOriginalDTO_Mapping pins the pure outcome mapping, including the
// defense-in-depth fold of an unrecognized core status to error.
func TestPaperOriginalDTO_Mapping(t *testing.T) {
	cases := []struct {
		name string
		res  papers.FetchResult
		want string
		url  string
	}{
		{"ok carries the resolved url", papers.FetchResult{Status: papers.FetchStatusOK, ResolvedURL: "https://ar5iv.labs.arxiv.org/html/9"}, origStatusOK, "https://ar5iv.labs.arxiv.org/html/9"},
		{"offline", papers.FetchResult{Status: papers.FetchStatusOffline}, origStatusOffline, ""},
		{"no_arxiv", papers.FetchResult{Status: papers.FetchStatusNoArxiv}, origStatusNoArxiv, ""},
		{"not_found", papers.FetchResult{Status: papers.FetchStatusNotFound}, origStatusNotFound, ""},
		{"error", papers.FetchResult{Status: papers.FetchStatusError}, origStatusError, ""},
		{"zero status folds to error", papers.FetchResult{}, origStatusError, ""},
		// An unrecognized status folds to error while the URL still maps
		// verbatim (the status is the contract; the core leaves ResolvedURL
		// empty unless a fetch got far enough to have one).
		{"unknown status folds to error", papers.FetchResult{Status: papers.FetchStatus("teleported"), ResolvedURL: "https://far"}, origStatusError, "https://far"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dto := paperOriginalDTO(tc.res)
			if dto.Status != tc.want {
				t.Fatalf("status = %q, want %q", dto.Status, tc.want)
			}
			if dto.URL != tc.url {
				t.Fatalf("url = %q, want %q", dto.URL, tc.url)
			}
		})
	}
}
