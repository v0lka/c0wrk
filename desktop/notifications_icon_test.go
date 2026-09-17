package desktop

import (
	"bytes"
	"os"
	"testing"
)

// pngSignature is the leading 8 bytes of every PNG stream (RFC 2083 §11.1
// file signature), used to sanity-check the embedded asset before the
// byte-for-byte drift comparison.
var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// TestNotificationIconPNG_MatchesBuildAppicon guards the embedded copy at
// desktop/icon/appicon.png against drift from build/appicon.png — the
// source of truth Wails uses for packaged builds. Both files live in the
// repository, so a divergence (the appicon was replaced without refreshing
// the embedded copy, or the copy was edited by hand) fails here instead of
// shipping a stale banner icon in dev mode / standalone binaries.
func TestNotificationIconPNG_MatchesBuildAppicon(t *testing.T) {
	embedded := notificationIconPNG()
	if len(embedded) < len(pngSignature) {
		t.Fatalf("notificationIconPNG() returned only %d bytes; the embed directive is broken", len(embedded))
	}
	if !bytes.HasPrefix(embedded, pngSignature) {
		t.Fatalf("notificationIconPNG() is not a PNG: first bytes % x", embedded[:len(pngSignature)])
	}

	// The test binary's working directory is the package dir (desktop/),
	// so the canonical icon sits one level up.
	onDisk, err := os.ReadFile("../build/appicon.png")
	if err != nil {
		t.Fatalf("reading build/appicon.png: %v", err)
	}
	if !bytes.Equal(embedded, onDisk) {
		t.Errorf("embedded icon desktop/icon/appicon.png drifted from build/appicon.png "+
			"(embedded %d bytes, build/appicon.png %d bytes); re-copy the file", len(embedded), len(onDisk))
	}
}
