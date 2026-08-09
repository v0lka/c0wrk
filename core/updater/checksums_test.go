package updater

import (
	"errors"
	"testing"
)

// validSums is a hand-crafted SHA256SUMS body exercising both text mode
// (two spaces) and binary mode (space + asterisk) as emitted by GNU sha256sum.
const validSums = `abc123def4567890abc123def4567890abc123def4567890abc123def4567890  c0wrk-desktop-macos-arm64.zip
fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321 *c0wrk-desktop-linux-amd64.tar.gz
0000000000000000000000000000000000000000000000000000000000000000  c0wrk-desktop-windows-amd64.zip
`

// TestParseChecksums_Valid parses a well-formed body and asserts every entry
// is captured with its filename verbatim (including the binary-mode line).
func TestParseChecksums_Valid(t *testing.T) {
	got, err := ParseChecksums([]byte(validSums))
	if err != nil {
		t.Fatalf("ParseChecksums returned error: %v", err)
	}
	want := map[string]string{
		"c0wrk-desktop-macos-arm64.zip":    "abc123def4567890abc123def4567890abc123def4567890abc123def4567890",
		"c0wrk-desktop-linux-amd64.tar.gz": "fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321",
		"c0wrk-desktop-windows-amd64.zip":  "0000000000000000000000000000000000000000000000000000000000000000",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d (%+v)", len(got), len(want), got)
	}
	for name, digest := range want {
		if got[name] != digest {
			t.Errorf("entry %q = %q, want %q", name, got[name], digest)
		}
	}
}

// TestParseChecksums_Empty covers the empty-sums acceptance criterion: an
// empty or whitespace-only file yields an empty map with a nil error (the
// fail-closed failure surfaces later at FindChecksum/Verify time).
func TestParseChecksums_Empty(t *testing.T) {
	for name, in := range map[string]string{
		"empty":       "",
		"blank lines": "\n\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseChecksums([]byte(in))
			if err != nil {
				t.Fatalf("ParseChecksums returned error: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("expected empty map, got %+v", got)
			}
		})
	}
}

// TestParseChecksums_MalformedLine covers the malformed-string acceptance
// criterion: a single bad line aborts the whole parse (fail-closed) rather
// than silently dropping an entry that might be the one we need.
func TestParseChecksums_MalformedLine(t *testing.T) {
	cases := map[string]string{
		"too short":        "deadbeef  not-a-full-digest.zip\n",
		"non-hex digest":   "zzzz123def4567890abc123def4567890abc123def4567890abc123def4567890  asset.zip\n",
		"missing filename": "abc123def4567890abc123def4567890abc123def4567890abc123def4567890  \n",
		"bad separator":    "abc123def4567890abc123def4567890abc123def4567890abc123def4567890-asset.zip\n",
		"bad indicator":    "abc123def4567890abc123def4567890abc123def4567890abc123def4567890 +asset.zip\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseChecksums([]byte(in))
			if !errors.Is(err, ErrMalformedChecksumLine) {
				t.Fatalf("expected ErrMalformedChecksumLine, got %v", err)
			}
		})
	}
}

// TestParseChecksums_RejectsUppercase ensures uppercase hex is rejected: GNU
// sha256sum emits lowercase, and accepting uppercase would introduce ambiguity
// that could mask a real mismatch.
func TestParseChecksums_RejectsUppercase(t *testing.T) {
	in := []byte("ABC123DEF4567890ABC123DEF4567890ABC123DEF4567890ABC123DEF4567890  asset.zip\n")
	if _, err := ParseChecksums(in); !errors.Is(err, ErrMalformedChecksumLine) {
		t.Fatalf("expected ErrMalformedChecksumLine for uppercase hex, got %v", err)
	}
}

// TestParseChecksums_FilenameWithSpaces confirms the filename is taken verbatim
// to end of line so spaces survive (not split by Fields).
func TestParseChecksums_FilenameWithSpaces(t *testing.T) {
	in := []byte("abc123def4567890abc123def4567890abc123def4567890abc123def4567890  my asset.zip\n")
	got, err := ParseChecksums(in)
	if err != nil {
		t.Fatalf("ParseChecksums returned error: %v", err)
	}
	if _, ok := got["my asset.zip"]; !ok {
		t.Fatalf("expected entry for %q, got %+v", "my asset.zip", got)
	}
}

// TestFindChecksum_Found looks up a known asset.
func TestFindChecksum_Found(t *testing.T) {
	digest, err := FindChecksum([]byte(validSums), "c0wrk-desktop-macos-arm64.zip")
	if err != nil {
		t.Fatalf("FindChecksum returned error: %v", err)
	}
	want := "abc123def4567890abc123def4567890abc123def4567890abc123def4567890"
	if digest != want {
		t.Errorf("got %q, want %q", digest, want)
	}
}

// TestFindChecksum_NotFound covers the missing-string acceptance criterion: an
// asset absent from the sums fails with ErrChecksumNotFound (fail-closed).
func TestFindChecksum_NotFound(t *testing.T) {
	_, err := FindChecksum([]byte(validSums), "does-not-exist.zip")
	if !errors.Is(err, ErrChecksumNotFound) {
		t.Fatalf("expected ErrChecksumNotFound, got %v", err)
	}
}

// TestFindChecksum_EmptySums confirms an empty sums body yields not-found for
// any asset (the fail-closed outcome for a missing entry).
func TestFindChecksum_EmptySums(t *testing.T) {
	_, err := FindChecksum([]byte(""), "any.zip")
	if !errors.Is(err, ErrChecksumNotFound) {
		t.Fatalf("expected ErrChecksumNotFound, got %v", err)
	}
}
