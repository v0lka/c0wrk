package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/papers"
)

func TestLiteratureSeed(t *testing.T) {
	cases := []struct {
		name string
		rec  *papers.PaperRecord
		want string
	}{
		{
			name: "doi preferred over arxiv",
			rec: &papers.PaperRecord{
				Title: "T",
				Identifiers: []papers.Identifier{
					{Scheme: "arxiv", Value: "1706.03762"},
					{Scheme: "doi", Value: "10.1/x"},
				},
			},
			want: "10.1/x",
		},
		{
			name: "arxiv when no doi (scheme case-insensitive)",
			rec: &papers.PaperRecord{
				Title:       "T",
				Identifiers: []papers.Identifier{{Scheme: "arXiv", Value: "1706.03762"}},
			},
			want: "arXiv:1706.03762",
		},
		{
			name: "any other identifier before the title",
			rec: &papers.PaperRecord{
				Title:       "T",
				Identifiers: []papers.Identifier{{Scheme: "url", Value: "https://example.org/p"}},
			},
			want: "https://example.org/p",
		},
		{
			name: "empty identifier falls back to the trimmed title",
			rec: &papers.PaperRecord{
				Title:       "  Attention Is All You Need  ",
				Identifiers: []papers.Identifier{{Scheme: "doi", Value: "   "}},
			},
			want: "Attention Is All You Need",
		},
		{name: "no seedable field", rec: &papers.PaperRecord{}, want: ""},
		{name: "nil record", rec: nil, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := literatureSeed(tc.rec); got != tc.want {
				t.Fatalf("literatureSeed = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTailMessage(t *testing.T) {
	if got := tailMessage("  hello  "); got != "hello" {
		t.Fatalf("tailMessage trimmed = %q", got)
	}
	long := strings.Repeat("a", literatureMsgMaxRunes+50)
	got := tailMessage(long)
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("expected an ellipsis prefix, got %.10q", got)
	}
	if len([]rune(got)) != literatureMsgMaxRunes+1 {
		t.Fatalf("expected %d runes, got %d", literatureMsgMaxRunes+1, len([]rune(got)))
	}
}

// writeHelper writes an executable fake "python" script.
func writeHelper(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-helper.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	return path
}

// okHelper writes a minimal literature.json to the path following --out.
const okHelper = `out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --out) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s' '{"seed":{"title":"S"}}' > "$out"
exit 0
`

func TestRunLiteratureHelperSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	paperDir := t.TempDir()
	dto := runLiteratureHelper("/bin/sh", writeHelper(t, okHelper), "10.1/x", paperDir)

	if dto.Status != litStatusOK {
		t.Fatalf("status = %q (message %q), want %q", dto.Status, dto.Message, litStatusOK)
	}
	if dto.Content != `{"seed":{"title":"S"}}` {
		t.Fatalf("content = %q", dto.Content)
	}
	wantPath := filepath.Join(paperDir, papers.LiteratureFileName)
	if dto.Path != wantPath {
		t.Fatalf("path = %q, want %q", dto.Path, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected literature.json on disk: %v", err)
	}
}

func TestRunLiteratureHelperExitCodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	cases := []struct {
		name   string
		body   string
		status string
	}{
		{"offline", "echo 'cannot reach api.openalex.org' >&2\nexit 2\n", litStatusOffline},
		{"unresolved", "exit 1\n", litStatusUnresolved},
		{"rate limited", "exit 3\n", litStatusRateLimited},
		{"other failure", "exit 7\n", litStatusError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dto := runLiteratureHelper("/bin/sh", writeHelper(t, tc.body), "10.1/x", t.TempDir())
			if dto.Status != tc.status {
				t.Fatalf("status = %q, want %q", dto.Status, tc.status)
			}
			if tc.name == "offline" && !strings.Contains(dto.Message, "cannot reach") {
				t.Fatalf("expected the stderr tail in the message, got %q", dto.Message)
			}
		})
	}
}

func TestRunLiteratureHelperMissingInterpreter(t *testing.T) {
	dto := runLiteratureHelper(filepath.Join(t.TempDir(), "no-such-python"), "/x", "seed", t.TempDir())
	if dto.Status != litStatusError {
		t.Fatalf("status = %q, want %q", dto.Status, litStatusError)
	}
	if dto.Message == "" {
		t.Fatal("expected a message")
	}
}

// ---------------------------------------------------------------------------
// RunPaperLiterature — per-research-root mutation mutex
// ---------------------------------------------------------------------------

// fakeBlockingManagedPython writes an executable stand-in for the managed
// interpreter (at the path VenvPythonPath probes) that signals it started,
// blocks until a release sentinel exists, then writes the --out payload and
// exits 0 — letting a test observe the helper in flight.
func fakeBlockingManagedPython(t *testing.T, agentDir, marker, release string) {
	t.Helper()
	dir := filepath.Join(config.ToolsDir(agentDir), "python", "venv", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir venv bin: %v", err)
	}
	body := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --out) out=\"$2\"; shift 2 ;; *) shift ;; esac; done\n" +
		"printf started > " + marker + "\n" +
		"while [ ! -f " + release + " ]; do sleep 0.05; done\n" +
		"printf '%s' '{\"generated_at\":\"2026-09-16T00:00:00Z\",\"seed\":{\"title\":\"S\"}}' > \"$out\"\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "python3"), []byte(body), 0o755); err != nil {
		t.Fatalf("write fake python: %v", err)
	}
}

func waitForFile(t *testing.T, path string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestRunPaperLiterature_HoldsRootMutationMutex verifies the lookup holds the
// per-research-root mutation mutex — the one shared with SetPaperPinned and
// RecordFlashcardReview — for the WHOLE helper run, so a lookup cannot
// interleave with a concurrent pin/flashcard write.
func TestRunPaperLiterature_HoldsRootMutationMutex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	api, projectID, ws, effectiveRoot := papersTestFrontend(t, "", project.ResearchPins{})
	agentDir := t.TempDir()
	api.agentDir = agentDir

	marker := filepath.Join(agentDir, "started")
	release := filepath.Join(agentDir, "release")
	fakeBlockingManagedPython(t, agentDir, marker, release)

	// The seeded helper script's contents are irrelevant (the fake python
	// ignores them); only its presence matters to pass the scriptPath check.
	script := filepath.Join(config.SkillsDir(agentDir), studyPaperSkillName, literatureScriptRelPath)
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(script, []byte("# stub\n"), 0o644); err != nil {
		t.Fatalf("write stub script: %v", err)
	}

	libraryRoot := config.PaperLibraryPath(ws)
	dir := seedTestPaper(t, libraryRoot, papers.PaperRecord{
		Title:       "Mutex Probe",
		Identifiers: []papers.Identifier{{Scheme: "doi", Value: "10.1/mutex"}},
	})
	slug := filepath.Base(dir)

	mu := api.researchMutationMu(effectiveRoot)

	type outcome struct {
		dto *PaperLiteratureDTO
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		dto, err := api.RunPaperLiterature(projectID, slug)
		done <- outcome{dto, err}
	}()

	if !waitForFile(t, marker) {
		_ = os.WriteFile(release, nil, 0o644) // unblock the leaked helper
		t.Fatal("the helper never started")
	}

	// The run is in flight: the per-root mutex must be held.
	if mu.TryLock() {
		mu.Unlock()
		_ = os.WriteFile(release, nil, 0o644)
		t.Fatal("RunPaperLiterature did not hold the per-research-root mutation mutex during the run")
	}

	// Release the helper; the run must complete and drop the lock.
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatalf("write release: %v", err)
	}
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("RunPaperLiterature error: %v", out.err)
		}
		if out.dto == nil || out.dto.Status != litStatusOK {
			t.Fatalf("status = %+v, want %q", out.dto, litStatusOK)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunPaperLiterature did not return after the helper was released")
	}

	if !mu.TryLock() {
		t.Fatal("RunPaperLiterature left the per-research-root mutation mutex held")
	}
	mu.Unlock()
}
