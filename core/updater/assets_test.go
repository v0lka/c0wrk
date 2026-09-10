package updater

import (
	"errors"
	"testing"
)

func TestAssetNameFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		goos    string
		goarch  string
		flavor  Flavor
		want    string
		wantErr error
	}{
		{name: "darwin/arm64", goos: "darwin", goarch: "arm64", flavor: FlavorCPU, want: "c0wrk-desktop-macos-arm64.zip"},
		{name: "linux/amd64 cpu", goos: "linux", goarch: "amd64", flavor: FlavorCPU, want: "c0wrk-desktop-linux-amd64.tar.gz"},
		{name: "linux/amd64 cuda13", goos: "linux", goarch: "amd64", flavor: FlavorCUDA13, want: "c0wrk-desktop-linux-amd64-cuda13.tar.gz"},
		{name: "windows/amd64", goos: "windows", goarch: "amd64", flavor: FlavorCPU, want: "c0wrk-desktop-windows-amd64.zip"},
		{name: "linux/arm64", goos: "linux", goarch: "arm64", flavor: FlavorCPU, want: "c0wrk-desktop-linux-arm64.tar.gz"},
		{name: "linux/arm64 cuda13 unsupported", goos: "linux", goarch: "arm64", flavor: FlavorCUDA13, wantErr: ErrNoAssetForPlatform},
		{name: "darwin/arm64 cuda13 unsupported", goos: "darwin", goarch: "arm64", flavor: FlavorCUDA13, wantErr: ErrNoAssetForPlatform},
		{name: "windows/amd64 cuda13 unsupported", goos: "windows", goarch: "amd64", flavor: FlavorCUDA13, wantErr: ErrNoAssetForPlatform},
		{name: "unsupported linux/riscv64", goos: "linux", goarch: "riscv64", flavor: FlavorCPU, wantErr: ErrNoAssetForPlatform},
		{name: "unsupported darwin/amd64", goos: "darwin", goarch: "amd64", flavor: FlavorCPU, wantErr: ErrNoAssetForPlatform},
		{name: "empty platform", goos: "", goarch: "", flavor: FlavorCPU, wantErr: ErrNoAssetForPlatform},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := AssetNameFor(tc.goos, tc.goarch, tc.flavor)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected error %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("AssetNameFor(%s,%s,%s) = %q, want %q", tc.goos, tc.goarch, tc.flavor, got, tc.want)
			}
		})
	}
}

func TestSelectAsset(t *testing.T) {
	t.Parallel()
	assets := []ReleaseAsset{
		{Name: "c0wrk-desktop-macos-arm64.zip", BrowserDownloadURL: "https://github.com/v0lka/c0wrk/releases/download/v1.2.3/c0wrk-desktop-macos-arm64.zip"},
		{Name: "c0wrk-desktop-linux-amd64.tar.gz", BrowserDownloadURL: "https://github.com/v0lka/c0wrk/releases/download/v1.2.3/c0wrk-desktop-linux-amd64.tar.gz"},
		{Name: "c0wrk-desktop-windows-amd64.zip", BrowserDownloadURL: "https://github.com/v0lka/c0wrk/releases/download/v1.2.3/c0wrk-desktop-windows-amd64.zip"},
		{Name: "c0wrk-desktop-v1.2.3_SHA256SUMS.txt", BrowserDownloadURL: "https://github.com/v0lka/c0wrk/releases/download/v1.2.3/c0wrk-desktop-v1.2.3_SHA256SUMS.txt"},
	}

	t.Run("selects darwin/arm64 by filename", func(t *testing.T) {
		t.Parallel()
		got, err := SelectAsset(assets, "darwin", "arm64", FlavorCPU)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-macos-arm64.zip" {
			t.Fatalf("got %q, want macos zip", got.Name)
		}
	})

	t.Run("selects linux/amd64", func(t *testing.T) {
		t.Parallel()
		got, err := SelectAsset(assets, "linux", "amd64", FlavorCPU)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-linux-amd64.tar.gz" {
			t.Fatalf("got %q, want linux tar.gz", got.Name)
		}
	})

	t.Run("selects windows/amd64", func(t *testing.T) {
		t.Parallel()
		got, err := SelectAsset(assets, "windows", "amd64", FlavorCPU)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-windows-amd64.zip" {
			t.Fatalf("got %q, want windows zip", got.Name)
		}
	})

	t.Run("matches via URL when name is empty", func(t *testing.T) {
		t.Parallel()
		urlOnly := []ReleaseAsset{
			{Name: "", BrowserDownloadURL: "https://github.com/v0lka/c0wrk/releases/download/v1.2.3/c0wrk-desktop-macos-arm64.zip"},
		}
		got, err := SelectAsset(urlOnly, "darwin", "arm64", FlavorCPU)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.BrowserDownloadURL == "" {
			t.Fatal("expected non-empty URL")
		}
	})

	t.Run("skips checksums and unrelated assets", func(t *testing.T) {
		t.Parallel()
		noMac := []ReleaseAsset{
			{Name: "c0wrk-desktop-v1.2.3_SHA256SUMS.txt", BrowserDownloadURL: "https://github.com/v0lka/c0wrk/releases/download/v1.2.3/c0wrk-desktop-v1.2.3_SHA256SUMS.txt"},
			{Name: "Source code.zip", BrowserDownloadURL: "https://github.com/v0lka/c0wrk/archive/v1.2.3.zip"},
		}
		if _, err := SelectAsset(noMac, "darwin", "arm64", FlavorCPU); !errors.Is(err, ErrNoAssetForPlatform) {
			t.Fatalf("expected ErrNoAssetForPlatform, got %v", err)
		}
	})

	t.Run("unsupported platform errors", func(t *testing.T) {
		t.Parallel()
		if _, err := SelectAsset(assets, "linux", "arm64", FlavorCPU); !errors.Is(err, ErrNoAssetForPlatform) {
			t.Fatalf("expected ErrNoAssetForPlatform, got %v", err)
		}
	})

	t.Run("empty asset list errors", func(t *testing.T) {
		t.Parallel()
		if _, err := SelectAsset(nil, "darwin", "arm64", FlavorCPU); !errors.Is(err, ErrNoAssetForPlatform) {
			t.Fatalf("expected ErrNoAssetForPlatform, got %v", err)
		}
	})

	t.Run("case-insensitive match", func(t *testing.T) {
		t.Parallel()
		upper := []ReleaseAsset{{Name: "C0WRK-DESKTOP-MACOS-ARM64.ZIP"}}
		got, err := SelectAsset(upper, "darwin", "arm64", FlavorCPU)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name == "" {
			t.Fatal("expected a match despite uppercase filename")
		}
	})

	t.Run("prefers archive over companion assets", func(t *testing.T) {
		t.Parallel()
		withCompanion := []ReleaseAsset{
			{Name: "c0wrk-desktop-macos-arm64.zip.sig", BrowserDownloadURL: "https://github.com/v0lka/c0wrk/releases/download/v1.2.3/c0wrk-desktop-macos-arm64.zip.sig"},
			{Name: "c0wrk-desktop-macos-arm64.zip.asc", BrowserDownloadURL: "https://github.com/v0lka/c0wrk/releases/download/v1.2.3/c0wrk-desktop-macos-arm64.zip.asc"},
			{Name: "c0wrk-desktop-macos-arm64.zip", BrowserDownloadURL: "https://github.com/v0lka/c0wrk/releases/download/v1.2.3/c0wrk-desktop-macos-arm64.zip"},
		}
		got, err := SelectAsset(withCompanion, "darwin", "arm64", FlavorCPU)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-macos-arm64.zip" {
			t.Fatalf("got %q, want the archive asset, not a companion file", got.Name)
		}
	})
}

// linuxAssetsBothFlavors is the release asset list for linux/amd64 carrying
// BOTH flavors under their canonical names. Both names are canonical, so a
// CPU-flavored lookup must win via the exact-name pass.
func linuxAssetsBothFlavors() []ReleaseAsset {
	return []ReleaseAsset{
		{Name: "c0wrk-desktop-linux-amd64.tar.gz", BrowserDownloadURL: "https://x/c0wrk-desktop-linux-amd64.tar.gz"},
		{Name: "c0wrk-desktop-linux-amd64-cuda13.tar.gz", BrowserDownloadURL: "https://x/c0wrk-desktop-linux-amd64-cuda13.tar.gz"},
	}
}

// TestSelectAsset_CPUFlavorNeverPicksCUDA is the core regression: on
// linux/amd64 the CPU token "linux-amd64" is a substring of the CUDA asset
// name "…-linux-amd64-cuda13.tar.gz", so the old Contains-based fallback
// (and even asset order) could hand a CPU installation a CUDA archive.
// Both the exact-name pass and the anchored fallback must keep them apart.
func TestSelectAsset_CPUFlavorNeverPicksCUDA(t *testing.T) {
	t.Parallel()

	t.Run("both canonical assets present → CPU archive wins for CPU flavor", func(t *testing.T) {
		t.Parallel()
		got, err := SelectAsset(linuxAssetsBothFlavors(), "linux", "amd64", FlavorCPU)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-linux-amd64.tar.gz" {
			t.Fatalf("got %q, want the plain CPU archive", got.Name)
		}
	})

	t.Run("cuda13 asset listed first does not shadow the CPU archive", func(t *testing.T) {
		t.Parallel()
		reordered := []ReleaseAsset{
			{Name: "c0wrk-desktop-linux-amd64-cuda13.tar.gz", BrowserDownloadURL: "https://x/c0wrk-desktop-linux-amd64-cuda13.tar.gz"},
			{Name: "c0wrk-desktop-linux-amd64.tar.gz", BrowserDownloadURL: "https://x/c0wrk-desktop-linux-amd64.tar.gz"},
		}
		got, err := SelectAsset(reordered, "linux", "amd64", FlavorCPU)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-linux-amd64.tar.gz" {
			t.Fatalf("got %q, want the plain CPU archive despite cuda13 listing first", got.Name)
		}
	})

	// Regression against the real bug in the fallback: with NO canonical CPU
	// name in the release, the old substring match happily accepted the
	// *-cuda13.tar.gz asset for the CPU flavor. The anchored suffix
	// "<token><ext>" must reject it.
	t.Run("fallback: only a cuda13 archive present → CPU flavor errors, never cross-picks", func(t *testing.T) {
		t.Parallel()
		cudaOnly := []ReleaseAsset{
			{Name: "c0wrk-desktop-linux-amd64-cuda13.tar.gz", BrowserDownloadURL: "https://x/c0wrk-desktop-linux-amd64-cuda13.tar.gz"},
		}
		if _, err := SelectAsset(cudaOnly, "linux", "amd64", FlavorCPU); !errors.Is(err, ErrNoAssetForPlatform) {
			t.Fatalf("expected ErrNoAssetForPlatform for CPU flavor with only a cuda13 asset, got %v", err)
		}
	})

	// Fallback without canonical names at all: version-embedded filenames
	// matched purely via the anchored token+ext suffix.
	t.Run("fallback: versioned names → CPU flavor does not match *-cuda13.tar.gz", func(t *testing.T) {
		t.Parallel()
		versioned := []ReleaseAsset{
			{Name: "c0wrk-desktop-v2.0.0-linux-amd64.tar.gz", BrowserDownloadURL: "https://x/c0wrk-desktop-v2.0.0-linux-amd64.tar.gz"},
			{Name: "c0wrk-desktop-v2.0.0-linux-amd64-cuda13.tar.gz", BrowserDownloadURL: "https://x/c0wrk-desktop-v2.0.0-linux-amd64-cuda13.tar.gz"},
		}
		got, err := SelectAsset(versioned, "linux", "amd64", FlavorCPU)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-v2.0.0-linux-amd64.tar.gz" {
			t.Fatalf("got %q, want the plain versioned CPU archive", got.Name)
		}
	})
}

// TestSelectAsset_CUDA13Flavor verifies the GPU side of flavor selection: the
// canonical exact name and the token-based fallback both resolve to the
// cuda13 archive, and a release without a cuda13 asset fails closed with
// ErrNoAssetForPlatform instead of silently downgrading to the CPU archive.
func TestSelectAsset_CUDA13Flavor(t *testing.T) {
	t.Parallel()

	t.Run("canonical exact name", func(t *testing.T) {
		t.Parallel()
		got, err := SelectAsset(linuxAssetsBothFlavors(), "linux", "amd64", FlavorCUDA13)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-linux-amd64-cuda13.tar.gz" {
			t.Fatalf("got %q, want the cuda13 archive", got.Name)
		}
	})

	t.Run("token fallback with versioned names", func(t *testing.T) {
		t.Parallel()
		versioned := []ReleaseAsset{
			{Name: "c0wrk-desktop-v2.0.0-linux-amd64.tar.gz", BrowserDownloadURL: "https://x/c0wrk-desktop-v2.0.0-linux-amd64.tar.gz"},
			{Name: "c0wrk-desktop-v2.0.0-linux-amd64-cuda13.tar.gz", BrowserDownloadURL: "https://x/c0wrk-desktop-v2.0.0-linux-amd64-cuda13.tar.gz"},
		}
		got, err := SelectAsset(versioned, "linux", "amd64", FlavorCUDA13)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-v2.0.0-linux-amd64-cuda13.tar.gz" {
			t.Fatalf("got %q, want the versioned cuda13 archive", got.Name)
		}
	})

	t.Run("no cuda13 asset → ErrNoAssetForPlatform, never downgrades to CPU", func(t *testing.T) {
		t.Parallel()
		cpuOnly := []ReleaseAsset{
			{Name: "c0wrk-desktop-linux-amd64.tar.gz", BrowserDownloadURL: "https://x/c0wrk-desktop-linux-amd64.tar.gz"},
		}
		if _, err := SelectAsset(cpuOnly, "linux", "amd64", FlavorCUDA13); !errors.Is(err, ErrNoAssetForPlatform) {
			t.Fatalf("expected ErrNoAssetForPlatform for cuda13 flavor with only a CPU asset, got %v", err)
		}
	})

	t.Run("empty asset list → ErrNoAssetForPlatform", func(t *testing.T) {
		t.Parallel()
		if _, err := SelectAsset(nil, "linux", "amd64", FlavorCUDA13); !errors.Is(err, ErrNoAssetForPlatform) {
			t.Fatalf("expected ErrNoAssetForPlatform, got %v", err)
		}
	})
}

// TestMatchesTokenExt pins the anchored-suffix semantics of the fallback
// matcher itself: the token must sit flush against the archive extension.
func TestMatchesTokenExt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"c0wrk-desktop-linux-amd64.tar.gz", "linux-amd64", true},
		{"c0wrk-desktop-linux-amd64-cuda13.tar.gz", "linux-amd64", false}, // the substring trap
		{"c0wrk-desktop-linux-amd64-cuda13.tar.gz", "linux-amd64-cuda13", true},
		{"c0wrk-desktop-v2-linux-amd64.tar.gz", "linux-amd64", true},
		{"c0wrk-desktop-linux-amd64.tar.gz.sig", "linux-amd64", false}, // companion, not archive
		{"c0wrk-desktop-linux-amd64-cuda13.tar.gz.sig", "linux-amd64-cuda13", false},
		{"c0wrk-desktop-macos-arm64.zip", "macos-arm64", true},
		{"c0wrk-desktop-macos-arm64.zip.sig", "macos-arm64", false},
		{"c0wrk-desktop-linux-arm64.tar.gz", "linux-arm64", true},
		{"c0wrk-desktop-windows-amd64.zip", "windows-amd64", true},
		{"c0wrk-desktop-linux-amd64.tar.gz", "linux-arm64", false}, // wrong arch token
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.token, func(t *testing.T) {
			t.Parallel()
			if got := matchesTokenExt(tc.name, tc.token); got != tc.want {
				t.Fatalf("matchesTokenExt(%q, %q) = %v, want %v", tc.name, tc.token, got, tc.want)
			}
		})
	}
}
