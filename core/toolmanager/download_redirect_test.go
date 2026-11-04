package toolmanager

// Redirect-policy tests: the HTTPS-only transport invariant must hold for
// every redirect hop of a tool archive download, not just the initial URL.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func redirectTestSpec(rawURL, checksum string) ToolSpec {
	return ToolSpec{
		Name:        "fixture",
		Version:     "1.0.0",
		Type:        StaticBinary,
		BinName:     "fixture",
		ArchiveName: "fixture-1.0.0.tar.gz",
		URLs:        map[string]string{Platform(): rawURL},
		Checksums:   map[string]string{Platform(): checksum},
	}
}

func TestNewHTTPDownloader_NonLoopbackHTTPHopRefused(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/tool.tar.gz", http.StatusFound)
	}))
	defer origin.Close()

	d := NewHTTPDownloader(nil)
	_, err := d.Download(context.Background(), redirectTestSpec(origin.URL, "unused"), t.TempDir(), DownloadOnline, nil)
	if err == nil {
		t.Fatal("download redirected to a non-loopback http origin = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "must use HTTPS") {
		t.Errorf("error = %v, want it to contain %q", err, "must use HTTPS")
	}
}

func TestNewHTTPDownloader_LoopbackRedirectFollowed(t *testing.T) {
	archiveBytes := []byte("deterministic archive bytes")
	sum := sha256.Sum256(archiveBytes)
	checksum := hex.EncodeToString(sum[:])

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveBytes)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/get", http.StatusFound)
	}))
	defer origin.Close()

	d := NewHTTPDownloader(nil)
	res, err := d.Download(context.Background(), redirectTestSpec(origin.URL, checksum), t.TempDir(), DownloadOnline, nil)
	if err != nil {
		t.Fatalf("Download via a loopback redirect = %v, want nil", err)
	}
	if !res.Downloaded {
		t.Error("Downloaded = false, want true (the archive must come from the redirect target)")
	}
}

func TestNewHTTPDownloader_RedirectCap(t *testing.T) {
	var originURL string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, originURL+"/next", http.StatusFound)
	}))
	defer origin.Close()
	originURL = origin.URL

	d := NewHTTPDownloader(nil)
	_, err := d.Download(context.Background(), redirectTestSpec(origin.URL, "unused"), t.TempDir(), DownloadOnline, nil)
	if err == nil {
		t.Fatal("self-redirecting endpoint = nil error, want the redirect cap")
	}
	if !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("error = %v, want it to contain %q", err, "stopped after 10 redirects")
	}
}

func TestNewHTTPDownloader_CallerClientNotMutated(t *testing.T) {
	caller := &http.Client{Timeout: time.Second}
	d := NewHTTPDownloader(caller)
	if d.Client == caller {
		t.Error("downloader aliases the caller's client; the policy must land on a copy")
	}
	if d.Client.CheckRedirect == nil {
		t.Error("downloader client is missing the redirect policy")
	}
	if caller.CheckRedirect != nil {
		t.Error("caller client was mutated; CheckRedirect must remain nil on the original")
	}
}

func TestNewHTTPDownloader_InitialURLMustBeHTTPS(t *testing.T) {
	// A non-loopback plain-http spec URL must fail the transport check before
	// any network I/O — the redirect policy alone never sees the initial hop
	// (there are no redirects on a direct fetch).
	d := NewHTTPDownloader(nil)
	_, err := d.Download(context.Background(), redirectTestSpec("http://example.invalid/fixture.tar.gz", "unused"), t.TempDir(), DownloadOnline, nil)
	if err == nil {
		t.Fatal("Download with a non-loopback http:// URL = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "must use HTTPS") {
		t.Errorf("error = %v, want it to contain %q", err, "must use HTTPS")
	}
}
