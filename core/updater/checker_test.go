package updater

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/proxy"
)

// releasePayload builds a GitHub releases/latest JSON body for tests.
func releasePayload(tag string, assets ...ReleaseAsset) string {
	assetJSON := "[]"
	if len(assets) > 0 {
		var b strings.Builder
		b.WriteByte('[')
		for i, a := range assets {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"name":"` + a.Name + `","browser_download_url":"` + a.BrowserDownloadURL + `","content_type":"` + a.ContentType + `","size":1}`)
		}
		b.WriteByte(']')
		assetJSON = b.String()
	}
	return `{
		"tag_name":"` + tag + `",
		"name":"Release ` + tag + `",
		"body":"## What's new\n- stuff",
		"html_url":"https://github.com/v0lka/c0wrk/releases/` + tag + `",
		"published_at":"2025-01-02T03:04:05Z",
		"assets":` + assetJSON + `
	}`
}

func allPlatformAssets(tag string) []ReleaseAsset {
	base := "https://github.com/v0lka/c0wrk/releases/download/" + tag + "/"
	return []ReleaseAsset{
		{Name: "c0wrk-desktop-macos-arm64.zip", BrowserDownloadURL: base + "c0wrk-desktop-macos-arm64.zip"},
		{Name: "c0wrk-desktop-linux-amd64.tar.gz", BrowserDownloadURL: base + "c0wrk-desktop-linux-amd64.tar.gz"},
		{Name: "c0wrk-desktop-linux-arm64.tar.gz", BrowserDownloadURL: base + "c0wrk-desktop-linux-arm64.tar.gz"},
		{Name: "c0wrk-desktop-windows-amd64.zip", BrowserDownloadURL: base + "c0wrk-desktop-windows-amd64.zip"},
	}
}

// newTestChecker wires a Checker whose baseURL and HTTP client target the
// given mock server.
func newTestChecker(t *testing.T, ts *httptest.Server, current, skipped string) *Checker {
	t.Helper()
	c := NewChecker(Config{CurrentVersion: current, SkippedVersion: skipped}, ts.Client(), nil)
	c.baseURL = ts.URL
	c.WithPlatform("darwin", "arm64") // deterministic regardless of CI host
	return c
}

func TestCheck_NewVersionAvailable(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("request missing User-Agent header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(releasePayload("v1.2.4", allPlatformAssets("v1.2.4")...)))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.3", "")
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Available {
		t.Fatal("expected Available=true")
	}
	if res.LatestVersion != "v1.2.4" {
		t.Errorf("LatestVersion = %q, want v1.2.4", res.LatestVersion)
	}
	if res.CurrentVersion != "v1.2.3" {
		t.Errorf("CurrentVersion = %q, want v1.2.3", res.CurrentVersion)
	}
	if res.AssetName != "c0wrk-desktop-macos-arm64.zip" {
		t.Errorf("AssetName = %q, want macos zip", res.AssetName)
	}
	if !strings.HasSuffix(res.AssetURL, "c0wrk-desktop-macos-arm64.zip") {
		t.Errorf("AssetURL = %q, want to end with macos zip", res.AssetURL)
	}
	if res.ReleaseNotes == "" {
		t.Error("expected non-empty ReleaseNotes")
	}
	if res.PublishedAt != "2025-01-02T03:04:05Z" {
		t.Errorf("PublishedAt = %q", res.PublishedAt)
	}
}

func TestCheck_SameVersionNotAvailable(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(releasePayload("v1.2.3", allPlatformAssets("v1.2.3")...)))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.3", "")
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Available {
		t.Fatal("expected Available=false for same version")
	}
	if res.AssetURL != "" {
		t.Errorf("expected empty AssetURL when not available, got %q", res.AssetURL)
	}
}

func TestCheck_OlderVersionNotAvailable(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(releasePayload("v1.2.2", allPlatformAssets("v1.2.2")...)))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.3", "")
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Available {
		t.Fatal("expected Available=false when latest is older than current")
	}
}

func TestCheck_PreReleaseOlderThanStable(t *testing.T) {
	t.Parallel()
	// Current is a pre-release; stable latest is newer → available.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(releasePayload("v1.2.4", allPlatformAssets("v1.2.4")...)))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.4-rc1", "")
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Available {
		t.Fatal("expected v1.2.4 to be newer than v1.2.4-rc1")
	}
}

func TestCheck_StableNotNewerThanSamePreRelease(t *testing.T) {
	t.Parallel()
	// Latest is a pre-release of the same base; current stable is NOT older.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(releasePayload("v1.2.4-rc1", allPlatformAssets("v1.2.4-rc1")...)))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.4", "")
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Available {
		t.Fatal("pre-release v1.2.4-rc1 must not be newer than stable v1.2.4")
	}
}

func TestCheck_DevBuildTreatedAsAvailable(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(releasePayload("v1.2.3", allPlatformAssets("v1.2.3")...)))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "dev", "")
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Available {
		t.Fatal("dev build should be offered an update when a valid release exists")
	}
}

func TestCheck_SkippedVersionSuppressesUpdate(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(releasePayload("v1.2.4", allPlatformAssets("v1.2.4")...)))
	}))
	defer ts.Close()

	// Latest is newer than current, but the user skipped exactly v1.2.4.
	c := newTestChecker(t, ts, "v1.2.3", "v1.2.4")
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Available {
		t.Fatal("expected Available=false when latest == skipped version")
	}
}

func TestCheck_SkippedVersionNormalization(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(releasePayload("v1.2.4", allPlatformAssets("v1.2.4")...)))
	}))
	defer ts.Close()

	// Skip stored without leading "v" still matches.
	c := newTestChecker(t, ts, "v1.2.3", "1.2.4")
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Available {
		t.Fatal("expected skipped (1.2.4) to match latest (v1.2.4)")
	}
}

func TestCheck_Http404NoRelease(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.3", "")
	_, err := c.Check(context.Background())
	if !errors.Is(err, ErrNoRelease) {
		t.Fatalf("expected ErrNoRelease, got %v", err)
	}
}

func TestCheck_Http403RateLimited(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1735900000")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.3", "")
	_, err := c.Check(context.Background())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

func TestCheck_Http403Forbidden(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// No rate-limit header → a genuine forbidden, distinct from rate limiting.
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.3", "")
	_, err := c.Check(context.Background())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestCheck_MalformedJSON(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not valid json`))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.3", "")
	_, err := c.Check(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "parse release JSON") {
		t.Errorf("expected JSON parse error, got %v", err)
	}
}

func TestCheck_OtherHTTPStatusErrors(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.3", "")
	_, err := c.Check(context.Background())
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestCheck_NoAssetForPlatform(t *testing.T) {
	t.Parallel()
	// Release exists and is newer, but ships no asset for the platform.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(releasePayload("v1.2.4",
			ReleaseAsset{Name: "c0wrk-desktop-windows-amd64.zip", BrowserDownloadURL: "https://x/c0wrk-desktop-windows-amd64.zip"},
		)))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.3", "")
	c.WithPlatform("darwin", "arm64") // only windows asset present → no match
	_, err := c.Check(context.Background())
	if !errors.Is(err, ErrNoAssetForPlatform) {
		t.Fatalf("expected ErrNoAssetForPlatform, got %v", err)
	}
}

func TestCheck_SelectsCorrectAssetPerPlatform(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goos, goarch, wantAsset string
	}{
		{"darwin", "arm64", "c0wrk-desktop-macos-arm64.zip"},
		{"linux", "amd64", "c0wrk-desktop-linux-amd64.tar.gz"},
		{"linux", "arm64", "c0wrk-desktop-linux-arm64.tar.gz"},
		{"windows", "amd64", "c0wrk-desktop-windows-amd64.zip"},
	}
	for _, tc := range cases {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			t.Parallel()
			// Each parallel subtest owns its server lifecycle: a parent-managed
			// server would be closed before parallel subtests actually execute.
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(releasePayload("v2.0.0", allPlatformAssets("v2.0.0")...)))
			}))
			defer ts.Close()

			c := newTestChecker(t, ts, "v1.0.0", "")
			c.WithPlatform(tc.goos, tc.goarch)
			res, err := c.Check(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.AssetName != tc.wantAsset {
				t.Errorf("AssetName = %q, want %q", res.AssetName, tc.wantAsset)
			}
		})
	}
}

func TestCheck_RespectsContextCancellation(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(releasePayload("v1.2.4", allPlatformAssets("v1.2.4")...)))
	}))
	defer ts.Close()

	c := newTestChecker(t, ts, "v1.2.3", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	_, err := c.Check(ctx)
	if err == nil {
		t.Fatal("expected error when context is cancelled before response")
	}
}

// --- pure helper unit tests (semver edge cases) ---

func TestIsUpdateAvailable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		current, latest string
		want            bool
	}{
		{"patch newer", "v1.2.3", "v1.2.4", true},
		{"minor newer", "v1.2.3", "v1.3.0", true},
		{"major newer", "v1.2.3", "v2.0.0", true},
		{"equal", "v1.2.3", "v1.2.3", false},
		{"older latest", "v1.2.4", "v1.2.3", false},
		{"prerelease < stable", "v1.2.3", "v1.2.3-rc1", false},
		{"stable > prerelease", "v1.2.3-rc1", "v1.2.3", true},
		{"two prereleases", "v1.2.3-rc1", "v1.2.3-rc2", true},
		{"dev current, valid latest", "dev", "v1.2.3", true},
		{"dev current, invalid latest", "dev", "notaversion", false},
		{"current valid, latest invalid", "v1.2.3", "broken", false},
		{"without v prefix normalized", "1.2.3", "1.2.4", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isUpdateAvailable(tc.current, tc.latest); got != tc.want {
				t.Errorf("isUpdateAvailable(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}

func TestIsSkippedVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		latest  string
		skipped string
		want    bool
	}{
		{"exact match", "v1.2.3", "v1.2.3", true},
		{"no v normalization", "v1.2.3", "1.2.3", true},
		{"different", "v1.2.3", "v1.2.4", false},
		{"empty skipped", "v1.2.3", "", false},
		{"canonical ignores build metadata", "v1.2.3+build1", "v1.2.3", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isSkippedVersion(tc.latest, tc.skipped); got != tc.want {
				t.Errorf("isSkippedVersion(%q, %q) = %v, want %v", tc.latest, tc.skipped, got, tc.want)
			}
		})
	}
}

func TestNewCheckerWithProxy_DisabledUsesDefaultClient(t *testing.T) {
	t.Parallel()
	// Proxy disabled → BuildClient returns (nil,nil); checker must still get a
	// working default client.
	c, err := NewCheckerWithProxy(Config{CurrentVersion: "v1.0.0"}, proxy.Config{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil checker")
	}
	if c.client == nil {
		t.Fatal("expected non-nil client when proxy disabled")
	}
	if c.baseURL != "https://api.github.com" {
		t.Errorf("baseURL = %q, want https://api.github.com", c.baseURL)
	}
}

func TestNewCheckerWithProxy_InvalidProxyURLErrors(t *testing.T) {
	t.Parallel()
	// An enabled proxy with an invalid URL must surface a build error so the
	// corporate-proxy setting is never silently dropped.
	_, err := NewCheckerWithProxy(Config{CurrentVersion: "v1.0.0"}, proxy.Config{Enabled: true, URL: "ftp://bad"}, nil)
	if err == nil {
		t.Fatal("expected error for invalid proxy URL")
	}
	if !strings.Contains(err.Error(), "proxy") {
		t.Errorf("expected proxy-related error, got %v", err)
	}
}
