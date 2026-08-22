package updater

import (
	"errors"
	"testing"
)

func TestAssetNameForPlatform(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		goos    string
		goarch  string
		want    string
		wantErr error
	}{
		{name: "darwin/arm64", goos: "darwin", goarch: "arm64", want: "c0wrk-desktop-macos-arm64.zip"},
		{name: "linux/amd64", goos: "linux", goarch: "amd64", want: "c0wrk-desktop-linux-amd64.tar.gz"},
		{name: "windows/amd64", goos: "windows", goarch: "amd64", want: "c0wrk-desktop-windows-amd64.zip"},
		{name: "linux/arm64", goos: "linux", goarch: "arm64", want: "c0wrk-desktop-linux-arm64.tar.gz"},
		{name: "unsupported linux/riscv64", goos: "linux", goarch: "riscv64", wantErr: ErrNoAssetForPlatform},
		{name: "unsupported darwin/amd64", goos: "darwin", goarch: "amd64", wantErr: ErrNoAssetForPlatform},
		{name: "empty platform", goos: "", goarch: "", wantErr: ErrNoAssetForPlatform},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := AssetNameForPlatform(tc.goos, tc.goarch)
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
				t.Fatalf("AssetNameForPlatform(%s,%s) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
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
		got, err := SelectAsset(assets, "darwin", "arm64")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-macos-arm64.zip" {
			t.Fatalf("got %q, want macos zip", got.Name)
		}
	})

	t.Run("selects linux/amd64", func(t *testing.T) {
		t.Parallel()
		got, err := SelectAsset(assets, "linux", "amd64")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-linux-amd64.tar.gz" {
			t.Fatalf("got %q, want linux tar.gz", got.Name)
		}
	})

	t.Run("selects windows/amd64", func(t *testing.T) {
		t.Parallel()
		got, err := SelectAsset(assets, "windows", "amd64")
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
		got, err := SelectAsset(urlOnly, "darwin", "arm64")
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
		if _, err := SelectAsset(noMac, "darwin", "arm64"); !errors.Is(err, ErrNoAssetForPlatform) {
			t.Fatalf("expected ErrNoAssetForPlatform, got %v", err)
		}
	})

	t.Run("unsupported platform errors", func(t *testing.T) {
		t.Parallel()
		if _, err := SelectAsset(assets, "linux", "arm64"); !errors.Is(err, ErrNoAssetForPlatform) {
			t.Fatalf("expected ErrNoAssetForPlatform, got %v", err)
		}
	})

	t.Run("empty asset list errors", func(t *testing.T) {
		t.Parallel()
		if _, err := SelectAsset(nil, "darwin", "arm64"); !errors.Is(err, ErrNoAssetForPlatform) {
			t.Fatalf("expected ErrNoAssetForPlatform, got %v", err)
		}
	})

	t.Run("case-insensitive match", func(t *testing.T) {
		t.Parallel()
		upper := []ReleaseAsset{{Name: "C0WRK-DESKTOP-MACOS-ARM64.ZIP"}}
		got, err := SelectAsset(upper, "darwin", "arm64")
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
		got, err := SelectAsset(withCompanion, "darwin", "arm64")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "c0wrk-desktop-macos-arm64.zip" {
			t.Fatalf("got %q, want the archive asset, not a companion file", got.Name)
		}
	})
}
