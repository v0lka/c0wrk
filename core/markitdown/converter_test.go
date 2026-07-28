package markitdown

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testLogger is a discard-backed logger so tests stay silent regardless of
// whether the Converter logs at Debug or Warn.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newBareConverter builds a Converter directly, bypassing NewConverter's
// exec.LookPath check. This lets the unconditional error-path tests run even
// when the markitdown CLI is not installed.
func newBareConverter(t *testing.T, timeout time.Duration) *Converter {
	t.Helper()
	return &Converter{log: testLogger(), timeout: timeout}
}

func TestSupportedExtensions(t *testing.T) {
	want := []string{
		"pdf", "docx", "pptx", "xlsx", "odt",
		"html", "htm",
		"csv", "tsv",
		"txt", "md", "json", "xml", "rst",
	}

	got := SupportedExtensions()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SupportedExtensions() = %v, want %v", got, want)
	}

	// Both HTML spellings must be present.
	extSet := make(map[string]bool, len(got))
	for _, e := range got {
		extSet[e] = true
	}
	if !extSet["htm"] || !extSet["html"] {
		t.Errorf("expected both htm and html in %v", got)
	}

	// Returned slice must be a defensive copy: mutating it must not change the
	// package whitelist observed by a subsequent call.
	got[0] = "MUTATED"
	if SupportedExtensions()[0] == "MUTATED" {
		t.Error("SupportedExtensions() did not return a defensive copy")
	}
}

func TestIsSupported(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"lowercase md", "doc.md", true},
		{"uppercase MD", "doc.MD", true},
		{"mixed-case PdF", "report.PdF", true},
		{"htm", "page.htm", true},
		{"html", "page.html", true},
		{"txt", "notes.txt", true},
		{"json", "data.json", true},
		{"csv", "sheet.csv", true},
		{"rst", "guide.rst", true},
		{"xlsx", "budget.xlsx", true},
		{"odt", "letter.odt", true},

		{"mp3 unsupported", "song.mp3", false},
		{"jpg unsupported", "photo.jpg", false},
		{"uppercase unsupported JPG", "photo.JPG", false},
		{"only last extension considered", "data.json.bak", false},
		{"no extension", "README", false},
		{"dotfile only", ".bashrc", false},
		{"empty path", "", false},
		{"directory-looking with ext", "archive.zip", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSupported(tc.path); got != tc.want {
				t.Errorf("IsSupported(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// NewConverter must fail (wrapping exec.ErrNotFound) when markitdown is not
// resolvable. PATH is scoped to an empty temp dir so the result is
// deterministic regardless of whether the CLI is installed in the environment.
func TestNewConverter_MarkitdownAbsent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := NewConverter(testLogger(), time.Minute)
	if err == nil {
		t.Fatal("expected error when markitdown is not on PATH, got nil")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("expected error to wrap exec.ErrNotFound, got: %v", err)
	}
	if !strings.Contains(err.Error(), "markitdown") {
		t.Errorf("expected error message to mention markitdown, got: %v", err)
	}
}

// NewConverter must succeed when markitdown is on PATH. Skipped when the CLI
// is unavailable so CI without the managed tool stays green.
func TestNewConverter_MarkitdownPresent(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}

	c, err := NewConverter(testLogger(), time.Minute)
	if err != nil {
		t.Fatalf("NewConverter returned error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil Converter")
	}
}

// Unsupported formats must be rejected before the file is even stat'd, so this
// path is deterministic without markitdown installed.
func TestConvert_UnsupportedFormat(t *testing.T) {
	c := newBareConverter(t, time.Minute)
	path := filepath.Join(t.TempDir(), "song.mp3")

	_, err := c.Convert(context.Background(), path)
	if err == nil {
		t.Fatal("expected unsupported-format error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported file format") {
		t.Errorf("expected 'unsupported file format' in error, got: %v", err)
	}
}

// A supported-but-missing file must surface as a not-exist error. Runs
// unconditionally (no markitdown required — the CLI is never invoked).
func TestConvert_MissingFile(t *testing.T) {
	c := newBareConverter(t, time.Minute)
	missing := filepath.Join(t.TempDir(), "nope.md")

	_, err := c.Convert(context.Background(), missing)
	if err == nil {
		t.Fatal("expected missing-file error, got nil")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected error to wrap fs.ErrNotExist, got: %v", err)
	}
}

// A pre-canceled context must abort conversion and the error must wrap
// context.Canceled. Deterministic: the context is already done before the
// subprocess is started, so markitdown need not be installed.
func TestConvert_ContextCanceled(t *testing.T) {
	c := newBareConverter(t, time.Minute)

	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte("# hi\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Convert(ctx, path)
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected error to wrap context.Canceled, got: %v", err)
	}
}

// The per-Converter timeout must produce a DeadlineExceeded error. A timeout
// of 1ns forces the deadline to fire before the (slow) CLI can finish.
// Markitdown need not be installed: the deadline is checked up front.
func TestConvert_TimeoutDeadline(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}

	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte("# hi\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	c := newBareConverter(t, 1*time.Nanosecond)

	_, err := c.Convert(context.Background(), path)
	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected error to wrap context.DeadlineExceeded, got: %v", err)
	}
}

// End-to-end happy path: a real markdown fixture is round-tripped through the
// markitdown CLI. Skipped when the CLI is not installed.
func TestConvert_MarkitdownRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}

	c := newBareConverter(t, 2*time.Minute)

	path := filepath.Join(t.TempDir(), "hello.md")
	content := "# Title\n\nHello **world**.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	md, err := c.Convert(context.Background(), path)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if !strings.Contains(md, "Title") {
		t.Errorf("expected markdown to contain 'Title', got:\n%s", md)
	}
	if !strings.Contains(md, "Hello **world**") {
		t.Errorf("expected markdown to preserve 'Hello **world**', got:\n%s", md)
	}
}

// Convert must trim surrounding whitespace from the CLI output.
func TestConvert_TrimsOutput(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}

	c := newBareConverter(t, 2*time.Minute)

	path := filepath.Join(t.TempDir(), "trim.txt")
	if err := os.WriteFile(path, []byte("plain text body\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	md, err := c.Convert(context.Background(), path)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	if md != strings.TrimSpace(md) {
		t.Errorf("expected trimmed output, got leading/trailing whitespace: %q", md)
	}
	if !strings.Contains(md, "plain text body") {
		t.Errorf("expected body text in output, got: %q", md)
	}
}
