package rtk

import (
	"encoding/json"
	"testing"
)

func TestBuildDownloadURL(t *testing.T) {
	tests := []struct {
		name        string
		goos        string
		goarch      string
		wantTarget  string
		wantExt     string
		wantErr     bool
	}{
		{
			name:       "darwin arm64",
			goos:       "darwin",
			goarch:     "arm64",
			wantTarget: "aarch64-apple-darwin",
			wantExt:    ".tar.gz",
		},
		{
			name:       "darwin amd64",
			goos:       "darwin",
			goarch:     "amd64",
			wantTarget: "x86_64-apple-darwin",
			wantExt:    ".tar.gz",
		},
		{
			name:       "linux amd64",
			goos:       "linux",
			goarch:     "amd64",
			wantTarget: "x86_64-unknown-linux-musl",
			wantExt:    ".tar.gz",
		},
		{
			name:       "linux arm64",
			goos:       "linux",
			goarch:     "arm64",
			wantTarget: "aarch64-unknown-linux-gnu",
			wantExt:    ".tar.gz",
		},
		{
			name:       "windows amd64",
			goos:       "windows",
			goarch:     "amd64",
			wantTarget: "x86_64-pc-windows-msvc",
			wantExt:    ".zip",
		},
		{
			name:    "unsupported OS",
			goos:    "freebsd",
			goarch:  "amd64",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, ext, err := buildDownloadURL(tt.goos, tt.goarch)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %s/%s, got nil", tt.goos, tt.goarch)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got := "." + ext; got != tt.wantExt {
				t.Errorf("expected extension %q, got %q", tt.wantExt, got)
			}

			if url == "" {
				t.Fatal("expected non-empty URL")
			}

			if !contains(url, tt.wantTarget) {
				t.Errorf("expected URL to contain %q, got %q", tt.wantTarget, url)
			}

			if !contains(url, tt.wantExt) {
				t.Errorf("expected URL to contain %q, got %q", tt.wantExt, url)
			}
		})
	}
}

// contains is a small helper so we don't import strings just for this.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestCheckRtk_NotInPath(t *testing.T) {
	// Override PATH to ensure rtk is not found.
	t.Setenv("PATH", t.TempDir())
	// Override HOME so the ~/.local/bin/rtk fallback doesn't find a real binary.
	t.Setenv("HOME", t.TempDir())

	status := CheckRtk()

	if status.Installed {
		t.Errorf("expected Installed=false when rtk is not in PATH, got true")
	}
	if status.Path != "" {
		t.Errorf("expected empty Path, got %q", status.Path)
	}
	if status.Version != "" {
		t.Errorf("expected empty Version, got %q", status.Version)
	}
}

func TestCheckRtkStatusFields(t *testing.T) {
	original := RtkStatus{
		Installed: true,
		Path:      "/usr/local/bin/rtk",
		Version:   "1.2.3",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Verify JSON keys
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	for _, key := range []string{"installed", "path", "version"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("expected JSON key %q, not found in %s", key, string(data))
		}
	}

	// Round-trip
	var decoded RtkStatus
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded != original {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, original)
	}
}
