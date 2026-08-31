package toolmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestHTTPDownloader_CacheOnly covers the offline download contract: a
// verified cached archive satisfies the request with zero network I/O, and
// anything else fails fast with ErrCacheUnavailable instead of touching the
// network. The HTTP request counter doubles as the offline assertion — every
// CacheOnly branch below must leave it at zero.
func TestHTTPDownloader_CacheOnly(t *testing.T) {
	archiveBytes := []byte("deterministic archive bytes")
	sum := sha256.Sum256(archiveBytes)
	checksum := hex.EncodeToString(sum[:])

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(archiveBytes)
	}))
	defer srv.Close()

	tool := ToolSpec{
		Name:        "fixture",
		Version:     "1.0.0",
		Type:        StaticBinary,
		BinName:     "fixture",
		ArchiveName: "fixture-1.0.0.tar.gz",
		URLs:        map[string]string{Platform(): srv.URL},
		Checksums:   map[string]string{Platform(): checksum},
	}

	cacheDir := t.TempDir()
	d := NewHTTPDownloader(nil)

	// CacheOnly + cache miss → ErrCacheUnavailable, zero network I/O.
	if _, err := d.Download(context.Background(), tool, cacheDir, DownloadCacheOnly, nil); !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("cache-only miss: err = %v, want ErrCacheUnavailable", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("cache-only miss hit the network %d time(s); offline mode must never do I/O", hits.Load())
	}

	// CacheOnly + cache present but wrong bytes → ErrCacheUnavailable, zero
	// network I/O; the unverified file is removed.
	archivePath := filepath.Join(cacheDir, tool.ArchiveName)
	if err := os.WriteFile(archivePath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Download(context.Background(), tool, cacheDir, DownloadCacheOnly, nil); !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("cache-only checksum mismatch: err = %v, want ErrCacheUnavailable", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("cache-only mismatch hit the network %d time(s); offline mode must never do I/O", hits.Load())
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Error("unverified cached archive survived; it must be removed like the online path does")
	}

	// CacheOnly + verified cache → hit, zero network I/O.
	if err := os.WriteFile(archivePath, archiveBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := d.Download(context.Background(), tool, cacheDir, DownloadCacheOnly, nil)
	if err != nil {
		t.Fatalf("cache-only hit: %v", err)
	}
	if res.Downloaded {
		t.Error("a verified cache hit must be reported as Downloaded=false")
	}
	if hits.Load() != 0 {
		t.Fatalf("cache-only hit hit the network %d time(s); the cache must short-circuit", hits.Load())
	}

	// Online + no cache → downloads from the server and verifies.
	res, err = d.Download(context.Background(), tool, t.TempDir(), DownloadOnline, nil)
	if err != nil {
		t.Fatalf("online download: %v", err)
	}
	if !res.Downloaded {
		t.Error("an online download must be reported as Downloaded=true")
	}
	if hits.Load() != 1 {
		t.Fatalf("online download hit count = %d, want 1", hits.Load())
	}
}
