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
		name     string
		body     string
		status   string
		contains string
	}{
		// The documented helper codes are only trusted together with their
		// stderr marker (Issue 34): a bare 1/2/3 is ambiguous (CPython exits 1
		// on an uncaught exception, argparse exits 2 on a usage error, and the
		// helper returns 3 for an output-write failure too).
		{"offline", "echo 'network unavailable: down' >&2\nexit 2\n", litStatusOffline, "network unavailable"},
		{"unresolved", "echo 'could not resolve the seed: nope' >&2\nexit 1\n", litStatusUnresolved, ""},
		{"rate limited", "echo 'rate limited: 429' >&2\nexit 3\n", litStatusRateLimited, ""},
		{"plain crash exit 1", "echo 'Traceback (most recent call last):' >&2\nexit 1\n", litStatusError, ""},
		{"argparse usage exit 2", "echo 'usage: literature.py ...' >&2\nexit 2\n", litStatusError, ""},
		{"write failure exit 3", "echo 'could not write /x: boom' >&2\nexit 3\n", litStatusError, ""},
		{"other failure", "exit 7\n", litStatusError, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dto := runLiteratureHelper("/bin/sh", writeHelper(t, tc.body), "10.1/x", t.TempDir())
			if dto.Status != tc.status {
				t.Fatalf("status = %q (message %q), want %q", dto.Status, dto.Message, tc.status)
			}
			if tc.contains != "" && !strings.Contains(dto.Message, tc.contains) {
				t.Fatalf("expected %q in the message, got %q", tc.contains, dto.Message)
			}
		})
	}
}

// TestRunLiteratureHelperSeedAfterTerminator pins Issue 34's argparse fix: the
// seed is passed after a `--` terminator, so a seed that begins with `-` is
// never parsed as an option and reaches the helper as the positional. The
// fake helper mimics argparse: any dash-prefixed argument that is not a
// recognized option is rejected with a usage message on stderr and exit 2 —
// so the run only succeeds when production keeps emitting the `--`
// terminator before the seed.
func TestRunLiteratureHelperSeedAfterTerminator(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	// The fake helper records the positional seed (the last argument) into the
	// --out payload, proving it arrived intact despite the leading dash. The
	// `-*)` arm sits BEFORE the catch-all so a leading-dash seed arriving
	// without a preceding `--` is treated as an unrecognized option, exactly
	// like the real argparse parser; `--format`/`--timeout`/`--out` are the
	// recognized options production passes.
	body := `out=""
seed=""
while [ $# -gt 0 ]; do
  case "$1" in
    --format) shift 2 ;;
    --timeout) shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --) shift; seed="$1"; shift ;;
    -*) echo "usage: literature.py [--format FORMAT] [--timeout N] --out OUT [--] seed: unrecognized argument: $1" >&2; exit 2 ;;
    *) seed="$1"; shift ;;
  esac
done
printf '%s' "$seed" > "$out"
exit 0
`
	paperDir := t.TempDir()
	dto := runLiteratureHelper("/bin/sh", writeHelper(t, body), "-dash-titled paper", paperDir)
	if dto.Status != litStatusOK {
		t.Fatalf("status = %q (message %q), want %q", dto.Status, dto.Message, litStatusOK)
	}
	if dto.Content != "-dash-titled paper" {
		t.Fatalf("seed = %q, want the leading-dash seed intact", dto.Content)
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

// TestRunPaperLiterature_DoesNotHoldRowMutexDuringRun pins Issues 17+3: the
// network-bound helper run must NOT hold the projects-row mutation mutex (it
// writes no row), so a concurrent pin/flashcard/research mutation is not
// head-of-line-blocked for up to literatureRunTimeout. Concurrent lookups of
// the SAME paper are serialized on a separate per-paper guard instead.
func TestRunPaperLiterature_DoesNotHoldRowMutexDuringRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	api, projectID, ws, effectiveRoot := papersTestFrontend(t, "", project.ResearchPins{})
	agentDir := t.TempDir()
	api.agentDir = agentDir

	marker := filepath.Join(agentDir, "started")
	release := filepath.Join(agentDir, "release")
	fakeBlockingManagedPython(t, agentDir, marker, release)

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

	rowMu := api.researchMutationMu(effectiveRoot)
	guard := api.researchMutationMu(paperRunMuKey(dir))

	done := make(chan error, 1)
	go func() {
		_, err := api.RunPaperLiterature(projectID, slug)
		done <- err
	}()

	if !waitForFile(t, marker) {
		_ = os.WriteFile(release, nil, 0o644) // unblock the leaked helper
		t.Fatal("the helper never started")
	}

	// The run is in flight: the projects-row mutex must NOT be held.
	if !rowMu.TryLock() {
		_ = os.WriteFile(release, nil, 0o644)
		t.Fatal("RunPaperLiterature held the projects-row mutation mutex across the helper run")
	}
	rowMu.Unlock()

	// The per-paper guard IS held for the run (serializes same-paper lookups).
	if guard.TryLock() {
		guard.Unlock()
		_ = os.WriteFile(release, nil, 0o644)
		t.Fatal("RunPaperLiterature did not hold the per-paper guard during the run")
	}

	// A concurrent projects-row writer (a pin) must not be blocked by the run.
	pinDone := make(chan error, 1)
	go func() { pinDone <- api.SetPaperPinned(projectID, slug, true) }()
	select {
	case err := <-pinDone:
		if err != nil {
			_ = os.WriteFile(release, nil, 0o644)
			t.Fatalf("concurrent SetPaperPinned failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		_ = os.WriteFile(release, nil, 0o644)
		t.Fatal("SetPaperPinned blocked behind the literature run — the row mutex is held too long")
	}

	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatalf("write release: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPaperLiterature error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunPaperLiterature did not return after the helper was released")
	}
}
