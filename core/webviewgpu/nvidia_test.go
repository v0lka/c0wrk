package webviewgpu

import (
	"errors"
	"testing"
)

func TestApplyNvidiaExplicitSyncWorkaround(t *testing.T) {
	cases := []struct {
		name    string
		goos    string
		preset  map[string]string
		wantSet bool
	}{
		{"linux, unset", "linux", map[string]string{}, true},
		{"linux, operator re-enabled explicit sync", "linux", map[string]string{nvidiaDisableExplicitSyncEnvVar: "0"}, false},
		{"linux, operator set it already", "linux", map[string]string{nvidiaDisableExplicitSyncEnvVar: "1"}, false},
		{"non-linux", "darwin", map[string]string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var set []string
			lookup := func(k string) (string, bool) { v, ok := tc.preset[k]; return v, ok }
			setenv := func(k, v string) error { set = append(set, k+"="+v); return nil }

			applyNvidiaExplicitSyncWorkaround(tc.goos, lookup, setenv, nil)

			if tc.wantSet {
				if len(set) != 1 || set[0] != nvidiaDisableExplicitSyncEnvVar+"=1" {
					t.Fatalf("setenv calls = %v, want [%s=1]", set, nvidiaDisableExplicitSyncEnvVar)
				}
			} else if len(set) != 0 {
				t.Fatalf("setenv must not be called, got %v", set)
			}
		})
	}
}

func TestApplyNvidiaExplicitSyncWorkaroundSetenvError(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	setenv := func(string, string) error { return errors.New("boom") }
	// Must not panic; the failure is only logged.
	applyNvidiaExplicitSyncWorkaround("linux", lookup, setenv, nil)
}
