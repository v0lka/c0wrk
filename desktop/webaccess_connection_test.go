// SPDX-License-Identifier: Apache-2.0
//
// webaccess_connection_test.go — the renderer-side reader of the web-access
// section against a control-RPC writer. The connection/token pin lifecycle
// tests moved to the backend package with their code (P-11); this file keeps
// the GetWebAccessInfo race contract, which is a desktop binding surface.
// GetWebAccessInfo snapshots the section under the service's transition lock
// — the writers' lock — so the copy can never tear.

package desktop

import (
	"fmt"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
)

// configTokenOnDisk reloads config.yaml and returns the persisted web access
// token as-is — the shared helper for assertions on operator-typed
// credentials written through SetWebAccessToken (arbitrary strings within
// the validation bounds).
func configTokenOnDisk(t *testing.T, path string) string {
	t.Helper()
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	return reloaded.WebAccess.Token
}

// TestGetWebAccessInfo_RaceAgainstSectionWriters hammers the renderer-callable
// reader against a control-RPC writer. GetWebAccessInfo snapshots the section
// under the service's transition lock — the writers' lock — so the copy can
// never tear; before that snapshot existed the reader walked cfg.WebAccess
// field-by-field unlocked, and Token is a multi-word string header (UB-class
// under the Go memory model). Run under -race for the detection to bite (the
// stress legs do); without it the test only asserts both surfaces keep
// agreeing.
func TestGetWebAccessInfo_RaceAgainstSectionWriters(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: false\n")
	a := newWebAccessTestApp(t, cfg, path)

	const rounds = 50
	const lastToken = "race-token-00049"
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < rounds; i++ {
			if err := a.SetWebAccessToken(fmt.Sprintf("race-token-%05d", i)); err != nil {
				t.Errorf("SetWebAccessToken(%d): %v", i, err)
				return
			}
		}
	}()
	for i := 0; i < rounds; i++ {
		_ = a.GetWebAccessInfo()
	}
	<-done

	// After the writer settles the reader must agree with the disk state
	// (the sidecar is not running, so the token comes from the snapshot).
	if tok := configTokenOnDisk(t, path); tok != lastToken {
		t.Errorf("disk token = %q, want %q", tok, lastToken)
	}
	if info := a.GetWebAccessInfo(); info.Token != lastToken {
		t.Errorf("info.Token = %q, want %q", info.Token, lastToken)
	}
}
