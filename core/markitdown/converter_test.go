package markitdown

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
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

	_, err := NewConverter(Options{Logger: testLogger(), Timeout: time.Minute})
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

	c, err := NewConverter(Options{Logger: testLogger(), Timeout: time.Minute})
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

// TestConvertWithVision_DriverFallsBackToPlainCLI verifies the degradation
// contract: when the vision driver cannot complete (here: an unreachable
// endpoint inside the embedded client, or any driver-level failure),
// ConvertWithVision must still return a successful conversion via the plain
// CLI rather than surfacing an error — a broken vision configuration must
// never make document conversion worse than it is without vision.
//
// Runs with the real managed venv interpreter when available; otherwise the
// driver path is skipped by construction (pythonPath empty) and the test
// verifies the nil-vision pass-through instead.
func TestConvertWithVision_DriverFallsBackToPlainCLI(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}

	python := ""
	for _, cand := range []string{
		os.Getenv("HOME") + "/.c0wrk/tools/python/venv/bin/python3",
	} {
		if _, err := os.Stat(cand); err == nil {
			python = cand
			break
		}
	}

	// A tiny supported text document.
	path := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(path, []byte("plain vision fallback"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Converter{log: testLogger(), timeout: time.Minute, pythonPath: python}

	// Vision options pointing at a guaranteed-unreachable endpoint. The
	// embedded client will time out / fail to connect per image call; the
	// driver must still emit markdown (captioning failures are swallowed by
	// markitdown itself) — and if the driver as a whole fails, the plain CLI
	// fallback must rescue the conversion.
	vision := &VisionOptions{
		APIKey:  "test-key",
		BaseURL: "http://127.0.0.1:1/v1", // port 1: connection refused quickly
		Model:   "test-model",
	}

	markdown, err := c.ConvertWithVision(context.Background(), path, vision)
	if err != nil {
		t.Fatalf("ConvertWithVision with unreachable endpoint returned error: %v", err)
	}
	if !strings.Contains(markdown, "plain vision fallback") {
		t.Errorf("converted markdown missing source text: %q", markdown)
	}
}

// TestConvertWithVision_NilOptionsPassthrough pins the no-vision contract:
// nil options (and options without pythonPath) must behave exactly like
// Convert.
func TestConvertWithVision_NilOptionsPassthrough(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte("passthrough"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Converter{log: testLogger(), timeout: time.Minute}

	markdown, err := c.ConvertWithVision(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("nil vision options returned error: %v", err)
	}
	if !strings.Contains(markdown, "passthrough") {
		t.Errorf("markdown = %q, want passthrough content", markdown)
	}

	// Incomplete options are treated as "no vision" even with python present.
	incomplete := &VisionOptions{APIKey: "k"}
	if _, err := c.ConvertWithVision(context.Background(), path, incomplete); err != nil {
		t.Fatalf("incomplete vision options returned error: %v", err)
	}
}

// TestConvertWithVision_DriverCrashFallsBackToPlainCLI pins the wholesale
// driver-failure branch: when the driver process cannot even run (here: an
// interpreter path that does not exist), ConvertWithVision must fall back to
// the plain CLI and still convert successfully.
func TestConvertWithVision_DriverCrashFallsBackToPlainCLI(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte("crash fallback content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Interpreter path that cannot exist: the driver exec fails immediately.
	c := &Converter{log: testLogger(), timeout: time.Minute, pythonPath: filepath.Join(t.TempDir(), "no-such-python")}

	vision := &VisionOptions{APIKey: "k", BaseURL: "https://example.invalid/v1", Model: "m"}
	markdown, err := c.ConvertWithVision(context.Background(), path, vision)
	if err != nil {
		t.Fatalf("driver crash must degrade to plain conversion, got error: %v", err)
	}
	if !strings.Contains(markdown, "crash fallback content") {
		t.Errorf("markdown = %q, want plain CLI content", markdown)
	}
}

// ---------------------------------------------------------------------------
// Vision-assisted conversion E2E (embedded PDF / data-URI image captioning).
// ---------------------------------------------------------------------------

// venvPythonForTests resolves the managed venv interpreter used for
// vision-assisted conversion. Returns "" when unavailable — the E2E tests
// then skip (they require the real markitdown library plus pdfminer/Pillow).
func venvPythonForTests() string {
	cand := os.Getenv("HOME") + "/.c0wrk/tools/python/venv/bin/python3"
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	return ""
}

// mockVisionEndpoint starts an OpenAI Chat Completions-compatible HTTP server
// answering every POST with a fixed caption and counting requests. The driver
// appends "/chat/completions" to the returned base URL (hence the /v1 suffix).
func mockVisionEndpoint(t *testing.T) (baseURL string, calls *atomic.Int32) {
	t.Helper()
	calls = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"vision-e2e-model"`) {
			t.Errorf("vision request missing model, body: %.200s", body)
		}
		if !strings.Contains(string(body), `image_url`) {
			t.Errorf("vision request carries no image block, body: %.200s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"VISION E2E CAPTION: a red rectangle diagram"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", calls
}

// testJPEG renders a simple 400x300 image and encodes it as JPEG.
func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			if x > 50 && x < 350 && y > 50 && y < 250 {
				img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// buildPDFWithImage crafts a minimal one-page PDF whose content stream draws
// a text line and one DCTDecode (JPEG) image XObject.
func buildPDFWithImage(text string, jpegBytes []byte, w, h int) []byte {
	objects := map[int][]byte{}
	objects[1] = []byte("<< /Type /Catalog /Pages 2 0 R >>")
	objects[2] = []byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	objects[3] = []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
		"/Resources << /Font << /F1 5 0 R >> /XObject << /Im1 4 0 R >> >> /Contents 6 0 R >>")
	objects[4] = append([]byte(fmt.Sprintf(
		"<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB "+
			"/BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n", w, h, len(jpegBytes))),
		jpegBytes...)
	objects[4] = append(objects[4], []byte("\nendstream")...)
	objects[5] = []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	content := "BT /F1 24 Tf 72 700 Td (" + text + ") Tj ET\nq 400 0 0 300 72 350 cm /Im1 Do Q\n"
	objects[6] = append([]byte(fmt.Sprintf("<< /Length %d >>\nstream\n", len(content))),
		[]byte(content)...)
	objects[6] = append(objects[6], []byte("\nendstream")...)

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := map[int]int{}
	for n := 1; n <= 6; n++ {
		offsets[n] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", n)
		out.Write(objects[n])
		out.WriteString("\nendobj\n")
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 7\n0000000000 65535 f \n")
	for n := 1; n <= 6; n++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size 7 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", xref)
	return out.Bytes()
}

// TestConvertWithVision_PDFEmbeddedImageCaptioned pins the PDF image pass:
// markitdown 0.1.4 drops embedded PDF images entirely (pdfminer text only),
// so the driver must extract the DCTDecode XObject itself, caption it via the
// vision endpoint, and append an "## Embedded images" section to the text —
// otherwise the agent never sees image content from a PDF.
func TestConvertWithVision_PDFEmbeddedImageCaptioned(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}
	python := venvPythonForTests()
	if python == "" {
		t.Skip("managed venv python not available")
	}

	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(pdfPath, buildPDFWithImage("Quarterly revenue report", testJPEG(t), 400, 300), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL, calls := mockVisionEndpoint(t)
	c := &Converter{log: testLogger(), timeout: time.Minute, pythonPath: python}
	vision := &VisionOptions{APIKey: "e2e-key", BaseURL: baseURL, Model: "vision-e2e-model"}

	markdown, err := c.ConvertWithVision(context.Background(), pdfPath, vision)
	if err != nil {
		t.Fatalf("ConvertWithVision returned error: %v", err)
	}

	if !strings.Contains(markdown, "Quarterly revenue report") {
		t.Errorf("pdf text layer missing from output:\n%s", markdown)
	}
	if !strings.Contains(markdown, "## Embedded images") {
		t.Errorf("embedded images section missing from output:\n%s", markdown)
	}
	if !strings.Contains(markdown, "VISION E2E CAPTION: a red rectangle diagram") {
		t.Errorf("image caption missing from output:\n%s", markdown)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 captioning call, got %d", n)
	}
}

// zlibCompress compresses raw bytes the way a PDF FlateDecode stream stores
// them: a zlib wrapper (2-byte header + adler32) around the deflate payload —
// pdfminer feeds the bytes straight to zlib.decompress.
func zlibCompress(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// buildPDFWithFlateImage crafts a minimal one-page PDF whose image XObject
// uses FlateDecode with the GIVEN declared dimensions and the given (already
// zlib-compressed) stream bytes. Declared dimensions and actual stream
// content are independent on purpose: the bomb test relies on the mismatch.
func buildPDFWithFlateImage(text string, flateBytes []byte, w, h int) []byte {
	objects := map[int][]byte{}
	objects[1] = []byte("<< /Type /Catalog /Pages 2 0 R >>")
	objects[2] = []byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	objects[3] = []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
		"/Resources << /Font << /F1 5 0 R >> /XObject << /Im1 4 0 R >> >> /Contents 6 0 R >>")
	objects[4] = append([]byte(fmt.Sprintf(
		"<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB "+
			"/BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", w, h, len(flateBytes))),
		flateBytes...)
	objects[4] = append(objects[4], []byte("\nendstream")...)
	objects[5] = []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	content := "BT /F1 24 Tf 72 700 Td (" + text + ") Tj ET\nq 64 0 0 64 72 350 cm /Im1 Do Q\n"
	objects[6] = append([]byte(fmt.Sprintf("<< /Length %d >>\nstream\n", len(content))),
		[]byte(content)...)
	objects[6] = append(objects[6], []byte("\nendstream")...)

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := map[int]int{}
	for n := 1; n <= 6; n++ {
		offsets[n] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", n)
		out.Write(objects[n])
		out.WriteString("\nendobj\n")
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 7\n0000000000 65535 f \n")
	for n := 1; n <= 6; n++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size 7 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", xref)
	return out.Bytes()
}

// TestConvertWithVision_PDFFlateImageCaptioned pins the FlateDecode
// reconstruction path: a legitimate zlib-compressed RGB image XObject is
// reconstructed (bounded decompression must not reject honest streams) and
// captioned like a DCTDecode one.
func TestConvertWithVision_PDFFlateImageCaptioned(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}
	python := venvPythonForTests()
	if python == "" {
		t.Skip("managed venv python not available")
	}

	// 64x64 RGB (above the 32px decoration floor), gradient so the
	// compressed stream is non-trivial.
	const w, h = 64, 64
	raw := make([]byte, w*h*3)
	for i := 0; i < len(raw); i++ {
		raw[i] = byte(i * 7 % 251)
	}

	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "flate.pdf")
	if err := os.WriteFile(pdfPath, buildPDFWithFlateImage("Flate report", zlibCompress(t, raw), w, h), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL, calls := mockVisionEndpoint(t)
	c := &Converter{log: testLogger(), timeout: time.Minute, pythonPath: python}
	vision := &VisionOptions{APIKey: "e2e-key", BaseURL: baseURL, Model: "vision-e2e-model"}

	markdown, err := c.ConvertWithVision(context.Background(), pdfPath, vision)
	if err != nil {
		t.Fatalf("ConvertWithVision returned error: %v", err)
	}
	if !strings.Contains(markdown, "Flate report") {
		t.Errorf("pdf text layer missing from output:\n%s", markdown)
	}
	if !strings.Contains(markdown, "VISION E2E CAPTION") {
		t.Errorf("flate image caption missing from output:\n%s", markdown)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 captioning call, got %d", n)
	}
}

// TestConvertWithVision_PDFFlateBombSkipped pins the decompression-bomb
// guard: a stream declaring tiny dimensions (40x40 RGB needs 4800 bytes)
// whose zlib payload actually expands to 64 MiB must be skipped by the
// bounded decompressor — no caption entry, no OOM, no measurable stall —
// while the surrounding conversion still succeeds.
func TestConvertWithVision_PDFFlateBombSkipped(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}
	python := venvPythonForTests()
	if python == "" {
		t.Skip("managed venv python not available")
	}

	// 64 MiB of zeros compress to a few dozen KiB; the declared dimensions
	// claim only 4800 bytes of samples.
	bomb := zlibCompress(t, make([]byte, 64<<20))

	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "bomb.pdf")
	if err := os.WriteFile(pdfPath, buildPDFWithFlateImage("Bomb report", bomb, 40, 40), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL, calls := mockVisionEndpoint(t)
	c := &Converter{log: testLogger(), timeout: time.Minute, pythonPath: python}
	vision := &VisionOptions{APIKey: "e2e-key", BaseURL: baseURL, Model: "vision-e2e-model"}

	start := time.Now()
	markdown, err := c.ConvertWithVision(context.Background(), pdfPath, vision)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ConvertWithVision returned error: %v", err)
	}
	if !strings.Contains(markdown, "Bomb report") {
		t.Errorf("pdf text layer missing from output:\n%s", markdown)
	}
	if strings.Contains(markdown, "## Embedded images") {
		t.Errorf("bomb image must be skipped, got an embedded-images section:\n%s", markdown)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("bomb image must not be captioned, got %d calls", n)
	}
	// The bounded decompressor stops after the declared-size cap; even the
	// rlimit fallback aborts on first huge allocation. Generous CI margin.
	if elapsed > 30*time.Second {
		t.Errorf("bomb guard took %s; expected a fast skip", elapsed)
	}
}

// TestConvertWithVision_DataURIImageCaptioned pins the data-URI pass for
// html-family documents (html/docx/epub): the driver converts with
// keep_data_uris=True so embedded images survive, then replaces each base64
// blob with an inline caption — the vision model sees the image AND the raw
// payload never reaches the model context.
func TestConvertWithVision_DataURIImageCaptioned(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}
	python := venvPythonForTests()
	if python == "" {
		t.Skip("managed venv python not available")
	}

	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 200, 150))
	for y := 0; y < 150; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{R: 30, G: 30, B: 180, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBuf.Bytes())
	htmlPath := filepath.Join(dir, "page.html")
	html := "<html><body><h1>Report</h1><p>Text before</p><img src='" + dataURI + "' alt=''/><p>Text after</p></body></html>"
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL, calls := mockVisionEndpoint(t)
	c := &Converter{log: testLogger(), timeout: time.Minute, pythonPath: python}
	vision := &VisionOptions{APIKey: "e2e-key", BaseURL: baseURL, Model: "vision-e2e-model"}

	markdown, err := c.ConvertWithVision(context.Background(), htmlPath, vision)
	if err != nil {
		t.Fatalf("ConvertWithVision returned error: %v", err)
	}

	if !strings.Contains(markdown, "VISION E2E CAPTION: a red rectangle diagram") {
		t.Errorf("inline caption missing from output:\n%s", markdown)
	}
	if strings.Contains(markdown, "base64,") {
		t.Errorf("base64 image payload leaked into output:\n%.400s", markdown)
	}
	if !strings.Contains(markdown, "Text before") || !strings.Contains(markdown, "Text after") {
		t.Errorf("surrounding html text missing from output:\n%s", markdown)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 captioning call, got %d", n)
	}
}

// TestConvertWithVision_NonRasterDataURIStripped pins the data-URI pass's
// guarantee that NO embedded image payload leaks verbatim: markitdown emits
// data URIs for image mime types it does not caption (e.g. EMF metafiles and
// SVG), and those are outside the raster set Pillow can decode. The pass must
// still strip their base64 blobs — an EMF/SVG payload reaching the model
// context would defeat the entire purpose of the pass.
func TestConvertWithVision_NonRasterDataURIStripped(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}
	python := venvPythonForTests()
	if python == "" {
		t.Skip("managed venv python not available")
	}

	dir := t.TempDir()
	htmlPath := filepath.Join(dir, "page.html")
	// A valid base64 string that is NOT a decodable raster image; paired with
	// mime types the old regex would have skipped entirely (x-emf, svg+xml).
	payload := base64.StdEncoding.EncodeToString([]byte("not-a-raster-image"))
	html := "<html><body><p>emf</p><img src='data:image/x-emf;base64," + payload + "' alt=''/>" +
		"<p>svg</p><img src='data:image/svg+xml;base64," + payload + "' alt=''/></body></html>"
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL, calls := mockVisionEndpoint(t)
	c := &Converter{log: testLogger(), timeout: time.Minute, pythonPath: python}
	vision := &VisionOptions{APIKey: "e2e-key", BaseURL: baseURL, Model: "vision-e2e-model"}

	markdown, err := c.ConvertWithVision(context.Background(), htmlPath, vision)
	if err != nil {
		t.Fatalf("ConvertWithVision returned error: %v", err)
	}

	if strings.Contains(markdown, "base64,") {
		t.Errorf("non-raster base64 image payload leaked into output:\n%.400s", markdown)
	}
	if !strings.Contains(markdown, "emf") || !strings.Contains(markdown, "svg") {
		t.Errorf("surrounding text missing from output:\n%s", markdown)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("expected 0 captioning calls for non-decodable images, got %d", n)
	}
}

// TestConvertWithVision_DataURIAltReused pins the double-captioning fix:
// images that already carry alt text (markitdown's own pptx captioning, or a
// docx/html alt attribute) must NOT trigger a second LLM round-trip — the
// data-URI pass reuses the existing description and only strips the base64.
func TestConvertWithVision_DataURIAltReused(t *testing.T) {
	if _, err := exec.LookPath("markitdown"); err != nil {
		t.Skipf("markitdown CLI not available on PATH: %v", err)
	}
	python := venvPythonForTests()
	if python == "" {
		t.Skip("managed venv python not available")
	}

	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 120, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 120, B: 10, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBuf.Bytes())
	htmlPath := filepath.Join(dir, "page.html")
	html := "<html><body><img src='" + dataURI + "' alt='EXISTING ALT TEXT'/></body></html>"
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL, calls := mockVisionEndpoint(t)
	c := &Converter{log: testLogger(), timeout: time.Minute, pythonPath: python}
	vision := &VisionOptions{APIKey: "e2e-key", BaseURL: baseURL, Model: "vision-e2e-model"}

	markdown, err := c.ConvertWithVision(context.Background(), htmlPath, vision)
	if err != nil {
		t.Fatalf("ConvertWithVision returned error: %v", err)
	}

	if !strings.Contains(markdown, "EXISTING ALT TEXT") {
		t.Errorf("existing alt text lost from output:\n%s", markdown)
	}
	if strings.Contains(markdown, "base64,") {
		t.Errorf("base64 image payload leaked into output:\n%.400s", markdown)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("expected 0 captioning calls when alt text already present, got %d", n)
	}
}
