package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestReadAgentsMD_CapsReadAtAgentsMDMaxBytes pins the per-file read bound:
// a huge workspace-controlled AGENTS.md must never be fully materialized
// before the cap is applied (the cap is enforced AT READ TIME via
// io.LimitReader, not after an unbounded safeio.ReadFile).
func TestReadAgentsMD_CapsReadAtAgentsMDMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	original := 4 * DefaultAgentsMDMaxBytes
	big := strings.Repeat("A", original)
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	o := &Orchestrator{config: OrchestratorConfig{AgentsMDMaxBytes: 1024}}
	got, err := o.readAgentsMD(path)
	if err != nil {
		t.Fatalf("readAgentsMD failed: %v", err)
	}
	// An oversized file is clipped WITH the same truncation note the combined
	// cap produces, reporting the file's TRUE on-disk size (not the clipped
	// read length) — a silently clipped prompt is the regression this pins.
	wantMarker := fmt.Sprintf("[…AGENTS.md truncated at 1024 bytes; original was %d bytes]", original)
	if !strings.HasSuffix(got, wantMarker) {
		t.Fatalf("expected the truncation note with the true original size, got suffix %q", got[max(0, len(got)-120):])
	}
	body := strings.TrimSuffix(got, "\n\n"+wantMarker)
	if len(body) > 1024 {
		t.Fatalf("expected the body to respect the 1024-byte cap, got %d bytes", len(body))
	}
	if !strings.HasPrefix(big, body) {
		t.Fatalf("clipped body is not a prefix of the original (mid-rune or mid-line cut): %q", body[:min(64, len(body))])
	}
	// The cached entry holds the truncated content too (no full copy survives).
	if cached := o.agentsMDCache[path].content; cached != got {
		t.Errorf("expected the cache to hold the truncated content, got %d bytes", len(cached))
	}
}

// TestReadAgentsMD_DefaultCapApplies: a 0 cap resolves to
// DefaultAgentsMDMaxBytes.
func TestReadAgentsMD_DefaultCapApplies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	original := DefaultAgentsMDMaxBytes + 4096
	if err := os.WriteFile(path, []byte(strings.Repeat("B", original)), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	o := &Orchestrator{}
	got, err := o.readAgentsMD(path)
	if err != nil {
		t.Fatalf("readAgentsMD failed: %v", err)
	}
	want := fmt.Sprintf("[…AGENTS.md truncated at %d bytes; original was %d bytes]", DefaultAgentsMDMaxBytes, original)
	if !strings.HasSuffix(got, want) {
		t.Fatalf("expected the default cap (%d) to bound the read WITH the truncation note, got suffix %q", DefaultAgentsMDMaxBytes, got[max(0, len(got)-140):])
	}
}

// TestReadAgentsMD_NegativeCapDisablesCapping: a negative cap is the explicit
// opt-out (mirroring capAgentsMD) — the file is read in full.
func TestReadAgentsMD_NegativeCapDisablesCapping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("small but uncapped"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	o := &Orchestrator{config: OrchestratorConfig{AgentsMDMaxBytes: -1}}
	got, err := o.readAgentsMD(path)
	if err != nil {
		t.Fatalf("readAgentsMD failed: %v", err)
	}
	if got != "small but uncapped" {
		t.Errorf("expected the full content with capping disabled, got %q", got)
	}
}

// TestReadAgentsMD_CapStillTruncatesMultibyteSafely: the per-file read cap can
// split a multibyte rune at the byte boundary; capAgentsMD remains the
// rune-safe final trim. The read must not error on such content.
func TestReadAgentsMD_MultibyteContentUnderCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("инструкция: тестируй"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	o := &Orchestrator{config: OrchestratorConfig{AgentsMDMaxBytes: 1024}}
	got, err := o.readAgentsMD(path)
	if err != nil {
		t.Fatalf("readAgentsMD failed: %v", err)
	}
	if got != "инструкция: тестируй" {
		t.Errorf("expected verbatim content under the cap, got %q", got)
	}
}

// TestReadAgentsMD_TruncationSnapsToLineBoundary: the per-source clip must go
// through the same UTF-8/line-boundary snapping as capAgentsMD — the body
// before the truncation note ends at a '\n' and is an exact prefix of the
// original (never a mid-line fragment).
func TestReadAgentsMD_TruncationSnapsToLineBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	line := "0123456789\n" // 11 bytes
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(line)
	}
	original := b.String()
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	o := &Orchestrator{config: OrchestratorConfig{AgentsMDMaxBytes: 100}}
	got, err := o.readAgentsMD(path)
	if err != nil {
		t.Fatalf("readAgentsMD failed: %v", err)
	}
	marker := "[…AGENTS.md truncated at 100 bytes; original was 2200 bytes]"
	if !strings.HasSuffix(got, marker) {
		t.Fatalf("missing truncation note, suffix %q", got[max(0, len(got)-100):])
	}
	body := strings.TrimSuffix(got, "\n\n"+marker)
	if !strings.HasSuffix(body, "\n") {
		t.Fatalf("clipped body does not end at a line boundary: %q", body[max(0, len(body)-40):])
	}
	if !strings.HasPrefix(original, body) {
		t.Fatalf("clipped body is not a prefix of the original")
	}
}

// agentsMDMarker renders the marker text (without the leading "\n\n") the
// production note carries; tests assert against the exact same format.
func agentsMDMarker(maxBytes, originalSize int64) string {
	return fmt.Sprintf("[…AGENTS.md truncated at %d bytes; original was %d bytes]", maxBytes, originalSize)
}

// TestReadAgentsMD_TruncationNoteFitsWithinCap pins the note-reservation
// invariant end to end: when the last line inside the old full-cap window is
// shorter than the truncation note, a clip that spent the whole window would
// push the note past the cap — and capAgentsMD would re-truncate the note
// into a second marker carrying a false (inflated) "original was" size. The
// per-file clip must reserve the note's own size from the cap so the result
// fits once, note included, and capAgentsMD passes it through untouched.
func TestReadAgentsMD_TruncationNoteFitsWithinCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	line := "0123456789\n" // 11 bytes
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(line)
	}
	original := b.String() // 2200 bytes
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	const capBytes = 1000
	o := &Orchestrator{config: OrchestratorConfig{AgentsMDMaxBytes: capBytes}}
	got, err := o.readAgentsMD(path)
	if err != nil {
		t.Fatalf("readAgentsMD failed: %v", err)
	}

	// THE invariant: the capped content — note included — fits the cap.
	if len(got) > capBytes {
		t.Fatalf("capped content with the note exceeds the cap: %d > %d bytes", len(got), capBytes)
	}
	marker := agentsMDMarker(capBytes, int64(len(original)))
	if n := strings.Count(got, "[…AGENTS.md truncated"); n != 1 {
		t.Fatalf("expected exactly one truncation marker, found %d", n)
	}
	if !strings.HasSuffix(got, marker) {
		t.Fatalf("marker truncated or false size reported, suffix %q", got[max(0, len(got)-90):])
	}
	body := strings.TrimSuffix(got, "\n\n"+marker)
	if !strings.HasSuffix(body, "\n") {
		t.Fatalf("clipped body does not end at a line boundary: %q", body[max(0, len(body)-40):])
	}
	if !strings.HasPrefix(original, body) {
		t.Fatalf("clipped body is not a prefix of the original")
	}
	// Pass-through: the combined cap must NOT re-truncate the per-file note
	// (a second pass would duplicate the marker with an inflated size).
	if again := o.capAgentsMD(got); again != got {
		t.Fatalf("capAgentsMD re-truncated already-capped content: %d -> %d bytes", len(got), len(again))
	}
}

// TestCapAgentsMD_TruncationNoteFitsWithinCap pins the same note-reservation
// invariant on the combined path: a combined content whose last line inside
// the full cap window is shorter than the note must come back within the cap
// with exactly one intact marker reporting the TRUE combined size, and the
// truncation must be idempotent.
func TestCapAgentsMD_TruncationNoteFitsWithinCap(t *testing.T) {
	const capBytes = 100
	line := "0123456789\n" // 11 bytes
	var b strings.Builder
	for i := 0; i < 15; i++ {
		b.WriteString(line)
	}
	content := b.String() // 165 bytes

	o := &Orchestrator{config: OrchestratorConfig{AgentsMDMaxBytes: capBytes}}
	got := o.capAgentsMD(content)

	if len(got) > capBytes {
		t.Fatalf("capped combined content with the note exceeds the cap: %d > %d bytes", len(got), capBytes)
	}
	marker := agentsMDMarker(capBytes, int64(len(content)))
	if n := strings.Count(got, "[…AGENTS.md truncated"); n != 1 {
		t.Fatalf("expected exactly one truncation marker, found %d", n)
	}
	if !strings.HasSuffix(got, marker) {
		t.Fatalf("marker truncated or false size reported, suffix %q", got[max(0, len(got)-90):])
	}
	if body := strings.TrimSuffix(got, "\n\n"+marker); !strings.HasSuffix(body, "\n") {
		t.Fatalf("clipped body does not end at a line boundary: %q", body[max(0, len(body)-40):])
	}
	// Idempotence: re-capping changes nothing (the note is never re-truncated).
	if again := o.capAgentsMD(got); again != got {
		t.Fatalf("capAgentsMD is not idempotent: %d -> %d bytes", len(got), len(again))
	}
}

// TestCapAgentsMD_BytePreciseForMultibyte pins the byte cap on the combined
// path: a rune-counting trim would return multibyte content with fewer runes
// than the cap but MORE bytes than the cap, breaching it by up to 4x. The
// snap is byte-precise and rune-safe.
func TestCapAgentsMD_BytePreciseForMultibyte(t *testing.T) {
	const capBytes = 1024
	content := strings.Repeat("ж", 600) // 1200 bytes, 600 runes

	o := &Orchestrator{config: OrchestratorConfig{AgentsMDMaxBytes: capBytes}}
	got := o.capAgentsMD(content)

	if len(got) > capBytes {
		t.Fatalf("multibyte content overflows the byte cap: %d > %d bytes", len(got), capBytes)
	}
	marker := agentsMDMarker(capBytes, int64(len(content)))
	if !strings.HasSuffix(got, marker) {
		t.Fatalf("missing truncation note with the true original size, suffix %q", got[max(0, len(got)-90):])
	}
	if !utf8.ValidString(got) || strings.Contains(strings.TrimSuffix(got, "\n\n"+marker), "\uFFFD") {
		t.Fatalf("clipped body splits a multibyte rune: %q", got[max(0, len(got)-90):])
	}
}

// TestCapAgentsMD_CapSmallerThanNote documents the pathological floor: a cap
// smaller than the truncation note itself cannot hold content AND note, so
// the note alone is returned — content is never silently dropped without a
// marker, and exactly one marker survives.
func TestCapAgentsMD_CapSmallerThanNote(t *testing.T) {
	const capBytes = 10
	content := strings.Repeat("x", 50)

	o := &Orchestrator{config: OrchestratorConfig{AgentsMDMaxBytes: capBytes}}
	got := o.capAgentsMD(content)

	if want := "\n\n" + agentsMDMarker(capBytes, int64(len(content))); got != want {
		t.Fatalf("expected the note alone for a cap smaller than the note, got %q", got)
	}
}
