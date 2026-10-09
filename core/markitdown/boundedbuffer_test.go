package markitdown

import (
	"strings"
	"testing"
)

// TestBoundedBuffer_CapsAtMax pins the subprocess-output capture cap: the
// buffer must accumulate under the cap, fail the write that crosses it (which
// is what breaks the exec copy and surfaces through cmd.Run), flag the
// overflow for a precise error, and refuse further writes.
func TestBoundedBuffer_CapsAtMax(t *testing.T) {
	b := &boundedBuffer{max: 10}

	if n, err := b.Write([]byte("0123456789")); err != nil || n != 10 {
		t.Fatalf("write under cap = (%d, %v), want (10, nil)", n, err)
	}
	if b.overflow {
		t.Error("overflow flagged while still under the cap")
	}

	if _, err := b.Write([]byte("x")); err == nil {
		t.Fatal("write crossing the cap = nil error, want an error")
	}
	if !b.overflow {
		t.Error("overflow not flagged after the cap was crossed")
	}
	if got := b.String(); got != "0123456789" {
		t.Errorf("buffer = %q, want the capped content only", got)
	}

	if _, err := b.Write([]byte("y")); err == nil || !strings.Contains(err.Error(), "capture cap") {
		t.Fatalf("write after overflow = (%q), want a capture-cap error", err)
	}
}
