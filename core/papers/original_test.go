package papers

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// fetchTestRecord builds a minimal record with an arXiv identifier, mirroring
// the library's card shape (scheme "arxiv", possibly prefixed value).
func fetchTestRecord(slug string) PaperRecord {
	return PaperRecord{
		Slug:  slug,
		Title: "Fetched Paper",
		Identifiers: []Identifier{
			{Scheme: "doi", Value: "10.0000/example"},
			{Scheme: "arxiv", Value: "arXiv:1706.03762"},
		},
	}
}

// fetchPaperHTML is a rendition document with two images plus the stripped
// elements (script/style/link/iframe) that must not survive localization.
const fetchPaperHTML = `<!DOCTYPE html>
<html><head><title>T</title>
<script>alert("x")</script>
<style>body{color:red}</style>
<link rel="stylesheet" href="remote.css">
</head>
<body>
<h1>Attention</h1>
<p>body text</p>
<img src="x1.png" alt="first">
<img src="assets/fig2.svg" alt="second">
<iframe src="https://evil.example/frame"></iframe>
</body></html>
`

// fetchTestPNG / fetchTestSVG are the image payloads served alongside the
// rendition document.
var (
	fetchTestPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	fetchTestSVG = []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")
)

// serveOriginal stands up a rendition server: the document at /html/<id> and
// the two images it references.
func serveOriginal(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/html/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, fetchPaperHTML)
	})
	mux.HandleFunc("/html/x1.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fetchTestPNG)
	})
	mux.HandleFunc("/html/assets/fig2.svg", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fetchTestSVG)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fetchEndpoints(srv *httptest.Server) []string {
	return []string{srv.URL + "/html/"}
}

// TestNormalizeArxivID covers the identifier normalization table: bare,
// prefixed, URL, and legacy forms in; malformed values out.
func TestNormalizeArxivID(t *testing.T) {
	cases := []struct {
		name   string
		idents []Identifier
		want   string
	}{
		{"bare", []Identifier{{Scheme: "arxiv", Value: "1706.03762"}}, "1706.03762"},
		{"prefix", []Identifier{{Scheme: "arxiv", Value: "arXiv:1706.03762"}}, "1706.03762"},
		{"lowercase prefix", []Identifier{{Scheme: "arxiv", Value: "arxiv:2401.12345"}}, "2401.12345"},
		{"versioned", []Identifier{{Scheme: "arxiv", Value: "1706.03762v7"}}, "1706.03762v7"},
		{"https abs url", []Identifier{{Scheme: "arxiv", Value: "https://arxiv.org/abs/1706.03762"}}, "1706.03762"},
		{"http abs url versioned", []Identifier{{Scheme: "arxiv", Value: "http://arxiv.org/html/2401.12345v2"}}, "2401.12345v2"},
		{"bare host url", []Identifier{{Scheme: "arxiv", Value: "arxiv.org/abs/1706.03762v1"}}, "1706.03762v1"},
		{"pdf url", []Identifier{{Scheme: "arxiv", Value: "https://arxiv.org/pdf/1706.03762"}}, "1706.03762"},
		{"legacy id", []Identifier{{Scheme: "arxiv", Value: "hep-th/9901001"}}, "hep-th/9901001"},
		{"legacy id from url", []Identifier{{Scheme: "arxiv", Value: "https://arxiv.org/abs/cs.CL/0301012v1"}}, "cs.CL/0301012v1"},
		{"first arxiv wins", []Identifier{
			{Scheme: "doi", Value: "10.1/x"},
			{Scheme: "ArXiv", Value: "1706.03762"},
			{Scheme: "arxiv", Value: "2401.99999"},
		}, "1706.03762"},
		{"doi only", []Identifier{{Scheme: "doi", Value: "10.1/x"}}, ""},
		{"garbage", []Identifier{{Scheme: "arxiv", Value: "10.1016/j.nonsense"}}, ""},
		{"empty", []Identifier{{Scheme: "arxiv", Value: ""}}, ""},
		{"url without id", []Identifier{{Scheme: "arxiv", Value: "https://arxiv.org/"}}, ""},
	}
	for _, tc := range cases {
		if got := NormalizeArxivID(tc.idents); got != tc.want {
			t.Errorf("%s: NormalizeArxivID = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestFetchOriginalWritesHTMLAndAssets is the happy path: a 200 rendition with
// two images yields paper.html with rewritten local srcs plus both assets on
// disk, and the stripped elements are gone.
func TestFetchOriginalWritesHTMLAndAssets(t *testing.T) {
	srv := serveOriginal(t)
	root := t.TempDir()

	res := fetchOriginalHTML(context.Background(), srv.Client(), fetchEndpoints(srv), root, fetchTestRecord("fetched-paper"))
	if res.Status != FetchStatusOK {
		t.Fatalf("Status = %q, want ok (Err: %v)", res.Status, res.Err)
	}
	if res.ArxivID != "1706.03762" {
		t.Errorf("ArxivID = %q, want 1706.03762", res.ArxivID)
	}
	if res.ResolvedURL != srv.URL+"/html/1706.03762" {
		t.Errorf("ResolvedURL = %q, want the served document URL", res.ResolvedURL)
	}
	if res.AssetsKept != 2 || res.AssetsSkipped != 0 {
		t.Errorf("kept/skipped = %d/%d, want 2/0", res.AssetsKept, res.AssetsSkipped)
	}

	doc, err := os.ReadFile(filepath.Join(root, "fetched-paper", OriginalHTMLFileName))
	if err != nil {
		t.Fatalf("paper.html missing: %v", err)
	}
	html := string(doc)
	for _, want := range []string{`src="assets/x1.png"`, `src="assets/fig2.svg"`} {
		if !strings.Contains(html, want) {
			t.Errorf("paper.html missing rewritten src %q:\n%s", want, html)
		}
	}
	for _, banned := range []string{"<script", "<style", "<link", "<iframe", "srcset"} {
		if strings.Contains(html, banned) {
			t.Errorf("paper.html still contains stripped element %q:\n%s", banned, html)
		}
	}
	for name, payload := range map[string][]byte{"x1.png": fetchTestPNG, "fig2.svg": fetchTestSVG} {
		info, err := os.Stat(filepath.Join(root, "fetched-paper", OriginalAssetsDirName, name))
		if err != nil {
			t.Fatalf("asset %s missing: %v", name, err)
		}
		if info.Size() != int64(len(payload)) {
			t.Errorf("asset %s size = %d, want %d", name, info.Size(), len(payload))
		}
		got, err := os.ReadFile(filepath.Join(root, "fetched-paper", OriginalAssetsDirName, name))
		if err != nil || !bytes.Equal(got, payload) {
			t.Errorf("asset %s bytes = %q, %v; want the served payload", name, got, err)
		}
	}
}

// fetchArxivChromeHTML models arXiv's native rendition shape: the paper's
// <article class="ltx_document"> wrapped in site chrome (report-issue dialog,
// announcement banner, arxiv-html-header menu, TOC navbar, LaTeXML footer).
const fetchArxivChromeHTML = `<!DOCTYPE html>
<html><head><title>T</title><link rel="stylesheet" href="chrome.css"></head>
<body>
<dialog id="modal-form"><form><button>Report an Issue</button></form></dialog>
<div class="ds-announcement" id="announcement-banner"><img src="banner.png" alt="">arXiv is now a nonprofit</div>
<header class="arxiv-html-header"><nav class="html-header-nav"><a href="/">arXiv</a><img src="logo.svg" alt="logo"></nav></header>
<nav class="ltx_page_navbar"><nav class="ltx_TOC"><ol class="ltx_toclist"><li><a href="#S1">1 Introduction</a></li></ol></nav></nav>
<div class="ltx_page_main"><div class="ltx_page_content">
<article class="ltx_document ltx_authors_1line">
<h1 class="ltx_title ltx_title_document">Attention Is All You Need</h1>
<p class="ltx_p">body text</p>
<img src="fig1.png" alt="figure">
</article>
</div></div>
<footer class="arxiv-html-footer">Generated by LaTeXML</footer>
</body></html>
`

// TestFetchOriginalStripsArxivChrome: the saved paper.html keeps only the
// paper's own <article> — the document begins at the title heading, the site
// chrome (dialog, banner, header menu, TOC navbar, footer) is gone, and only
// the article's image is localized.
func TestFetchOriginalStripsArxivChrome(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/html/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, fetchArxivChromeHTML)
	})
	mux.HandleFunc("/html/fig1.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fetchTestPNG)
	})
	// Chrome images must never be requested once the chrome is cut; a canary
	// that fails the test if hit.
	for _, chrome := range []string{"banner.png", "logo.svg"} {
		mux.HandleFunc("/html/"+chrome, func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("chrome resource %q was fetched though its element is stripped", chrome)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	root := t.TempDir()

	res := fetchOriginalHTML(context.Background(), srv.Client(), fetchEndpoints(srv), root, fetchTestRecord("chrome-paper"))
	if res.Status != FetchStatusOK {
		t.Fatalf("Status = %q, want ok (Err: %v)", res.Status, res.Err)
	}
	if res.AssetsKept != 1 || res.AssetsSkipped != 0 {
		t.Errorf("kept/skipped = %d/%d, want 1/0 — only the article's image is localized", res.AssetsKept, res.AssetsSkipped)
	}

	doc, err := os.ReadFile(filepath.Join(root, "chrome-paper", OriginalHTMLFileName))
	if err != nil {
		t.Fatalf("paper.html missing: %v", err)
	}
	html := string(doc)
	// The document begins at the paper itself: the article (title heading and
	// body) survived intact…
	for _, want := range []string{
		"<article", "ltx_document", "Attention Is All You Need", "body text", `src="assets/fig1.png"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("paper.html missing the paper content %q:\n%s", want, html)
		}
	}
	// …while every chrome landmark is gone.
	for _, banned := range []string{
		"modal-form", "ds-announcement", "announcement-banner", "arxiv-html-header",
		"html-header-nav", "ltx_page_navbar", "ltx_TOC", "ltx_toclist",
		"arxiv-html-footer", "banner.png", "logo.svg", "Generated by LaTeXML",
	} {
		if strings.Contains(html, banned) {
			t.Errorf("paper.html still contains arXiv chrome %q:\n%s", banned, html)
		}
	}
	// The article's image is the only asset on disk.
	entries, err := os.ReadDir(filepath.Join(root, "chrome-paper", OriginalAssetsDirName))
	if err != nil {
		t.Fatalf("assets dir missing: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "fig1.png" {
		t.Errorf("assets = %v, want exactly [fig1.png]", entries)
	}
}

// TestFetchOriginalStripsAr5ivChrome: the ar5iv mirror wraps the same
// ltx_document article in ltx_page_header / ltx_page_footer divs — the cut
// keeps only the article there too.
func TestFetchOriginalStripsAr5ivChrome(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/html/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body>
<div class="ltx_page_main">
<div class="ltx_page_header">header chrome</div>
<div class="ltx_page_content">
<article class="ltx_document"><h1 class="ltx_title">Mirror Title</h1><p>mirror text</p></article>
</div>
<div class="ltx_page_footer">footer chrome</div>
</div>
</body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	root := t.TempDir()

	res := fetchOriginalHTML(context.Background(), srv.Client(), fetchEndpoints(srv), root, fetchTestRecord("ar5iv-chrome"))
	if res.Status != FetchStatusOK {
		t.Fatalf("Status = %q, want ok (Err: %v)", res.Status, res.Err)
	}
	doc, err := os.ReadFile(filepath.Join(root, "ar5iv-chrome", OriginalHTMLFileName))
	if err != nil {
		t.Fatalf("paper.html missing: %v", err)
	}
	html := string(doc)
	for _, want := range []string{"<article", "ltx_document", "Mirror Title", "mirror text"} {
		if !strings.Contains(html, want) {
			t.Errorf("paper.html missing the paper content %q:\n%s", want, html)
		}
	}
	for _, banned := range []string{"ltx_page_header", "ltx_page_footer", "header chrome", "footer chrome", "ltx_page_main"} {
		if strings.Contains(html, banned) {
			t.Errorf("paper.html still contains ar5iv chrome %q:\n%s", banned, html)
		}
	}
}

// TestFetchOriginalFallsThroughToAr5iv: a 404 on arxiv.org/html falls through
// to the ar5iv mirror; a 404 on both yields not_found with nothing written.
func TestFetchOriginalFallsThroughToAr5iv(t *testing.T) {
	png := []byte("png")
	mux := http.NewServeMux()
	mux.HandleFunc("/arxiv/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/ar5iv/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<html><body><img src=\"x1.png\"></body></html>")
	})
	mux.HandleFunc("/ar5iv/x1.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(png)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	endpoints := []string{srv.URL + "/arxiv/", srv.URL + "/ar5iv/"}

	t.Run("falls through to mirror", func(t *testing.T) {
		root := t.TempDir()
		res := fetchOriginalHTML(context.Background(), srv.Client(), endpoints, root, fetchTestRecord("mirror-paper"))
		if res.Status != FetchStatusOK {
			t.Fatalf("Status = %q, want ok (Err: %v)", res.Status, res.Err)
		}
		if !strings.HasSuffix(res.ResolvedURL, "/ar5iv/1706.03762") {
			t.Errorf("ResolvedURL = %q, want the ar5iv document", res.ResolvedURL)
		}
		if _, err := os.Stat(filepath.Join(root, "mirror-paper", OriginalHTMLFileName)); err != nil {
			t.Errorf("paper.html missing: %v", err)
		}
	})

	t.Run("both missing yields not_found", func(t *testing.T) {
		empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		t.Cleanup(empty.Close)
		root := t.TempDir()
		res := fetchOriginalHTML(context.Background(), empty.Client(),
			[]string{empty.URL + "/html/", empty.URL + "/html/"}, root, fetchTestRecord("missing-paper"))
		if res.Status != FetchStatusNotFound {
			t.Fatalf("Status = %q, want not_found (Err: %v)", res.Status, res.Err)
		}
		if res.Err != nil {
			t.Errorf("Err = %v, want nil for not_found", res.Err)
		}
		if _, err := os.Stat(filepath.Join(root, "missing-paper", OriginalHTMLFileName)); err == nil {
			t.Error("paper.html written for a not-found paper")
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("not_found created entries %v, want none", entries)
		}
	})
}

// TestFetchOriginalNoArxivIdentifier: a record without an arXiv identifier
// yields no_arxiv without touching disk or network.
func TestFetchOriginalNoArxivIdentifier(t *testing.T) {
	root := t.TempDir()
	rec := PaperRecord{Slug: "no-arxiv", Identifiers: []Identifier{{Scheme: "doi", Value: "10.1/x"}}}

	// The exported entry uses production endpoints; no_arxiv must be decided
	// before any request, so this never touches the network.
	res := FetchOriginalHTML(context.Background(), nil, root, rec)
	if res.Status != FetchStatusNoArxiv {
		t.Fatalf("Status = %q, want no_arxiv", res.Status)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("no_arxiv created entries %v, want none", entries)
	}
}

// TestFetchOriginalOffline: an unreachable endpoint yields offline.
func TestFetchOriginalOffline(t *testing.T) {
	// Reserve a port and close it so dialing is guaranteed refused.
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	res := fetchOriginalHTML(context.Background(), nil,
		[]string{"http://" + addr + "/html/"}, root, fetchTestRecord("offline-paper"))
	if res.Status != FetchStatusOffline {
		t.Fatalf("Status = %q, want offline (Err: %v)", res.Status, res.Err)
	}
	if res.Err == nil {
		t.Error("Err = nil, want the underlying network error")
	}
	if _, err := os.Stat(filepath.Join(root, "offline-paper", OriginalHTMLFileName)); err == nil {
		t.Error("paper.html written for an offline fetch")
	}
}

// TestFetchOriginalSkipsFailedImages: a failed, an oversize, an off-allowlist,
// and a duplicate-name image leave honest src-less <img> placeholders while
// the healthy images stay intact and deduped.
func TestFetchOriginalSkipsFailedImages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/html/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body>
<img src="good.png" alt="good">
<img src="missing.png" alt="missing">
<img src="huge.png" alt="huge">
<img src="https://other.example/evil.png" alt="evil">
<img src="sub/good.png" alt="same-name">
</body></html>`)
	})
	mux.HandleFunc("/html/good.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("good-bytes"))
	})
	mux.HandleFunc("/html/missing.png", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/html/huge.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxOriginalImageSize+1))
	})
	mux.HandleFunc("/html/sub/good.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("sub-bytes"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	root := t.TempDir()

	res := fetchOriginalHTML(context.Background(), srv.Client(), fetchEndpoints(srv), root, fetchTestRecord("skipped-images"))
	if res.Status != FetchStatusOK {
		t.Fatalf("Status = %q, want ok (Err: %v)", res.Status, res.Err)
	}
	if res.AssetsKept != 2 || res.AssetsSkipped != 3 {
		t.Fatalf("kept/skipped = %d/%d, want 2/3", res.AssetsKept, res.AssetsSkipped)
	}

	doc, err := os.ReadFile(filepath.Join(root, "skipped-images", OriginalHTMLFileName))
	if err != nil {
		t.Fatal(err)
	}
	html := string(doc)
	if !strings.Contains(html, `src="assets/good.png"`) {
		t.Errorf("healthy image not localized:\n%s", html)
	}
	if !strings.Contains(html, `src="assets/good-2.png"`) {
		t.Errorf("same-name image not deduplicated:\n%s", html)
	}
	// The failed/oversize/off-allowlist images keep their <img alt> placeholders
	// but lose their src — never a broken remote URL.
	for _, banned := range []string{"missing.png", "huge.png", "other.example", "sub/good.png"} {
		if strings.Contains(html, banned) {
			t.Errorf("paper.html still references skipped image %q:\n%s", banned, html)
		}
	}
	for _, placeholder := range []string{`alt="missing"`, `alt="huge"`, `alt="evil"`} {
		if !strings.Contains(html, placeholder) {
			t.Errorf("placeholder <img> %s removed entirely:\n%s", placeholder, html)
		}
	}

	// Both kept assets are on disk with their own bytes.
	first, err := os.ReadFile(filepath.Join(root, "skipped-images", OriginalAssetsDirName, "good.png"))
	if err != nil || string(first) != "good-bytes" {
		t.Errorf("assets/good.png = %q, %v; want good-bytes", first, err)
	}
	second, err := os.ReadFile(filepath.Join(root, "skipped-images", OriginalAssetsDirName, "good-2.png"))
	if err != nil || string(second) != "sub-bytes" {
		t.Errorf("assets/good-2.png = %q, %v; want sub-bytes", second, err)
	}
}

// TestFetchOriginalImageCap: images beyond the count cap keep their element
// but lose their src, and are never fetched.
func TestFetchOriginalImageCap(t *testing.T) {
	total := maxOriginalImages + 5
	var fetched atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/html/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString("<!DOCTYPE html><html><body>")
		for i := 0; i < total; i++ {
			fmt.Fprintf(&b, "<img src=\"img%03d.png\" alt=\"i%d\">", i, i)
		}
		b.WriteString("</body></html>")
		_, _ = fmt.Fprint(w, b.String())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		_, _ = w.Write([]byte("x"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	root := t.TempDir()

	res := fetchOriginalHTML(context.Background(), srv.Client(), fetchEndpoints(srv), root, fetchTestRecord("capped-images"))
	if res.Status != FetchStatusOK {
		t.Fatalf("Status = %q, want ok (Err: %v)", res.Status, res.Err)
	}
	if res.AssetsKept != maxOriginalImages || res.AssetsSkipped != 5 {
		t.Fatalf("kept/skipped = %d/%d, want %d/5", res.AssetsKept, res.AssetsSkipped, maxOriginalImages)
	}
	if got := fetched.Load(); got != int64(maxOriginalImages) {
		t.Errorf("image requests = %d, want exactly the cap %d (extras must not be fetched)", got, maxOriginalImages)
	}
	if !strings.Contains(mustRead(t, filepath.Join(root, "capped-images", OriginalHTMLFileName)), `src="assets/img000.png"`) {
		t.Error("first image not localized")
	}
	if strings.Contains(mustRead(t, filepath.Join(root, "capped-images", OriginalHTMLFileName)), "img204") {
		t.Error("image beyond the cap still referenced")
	}
}

// TestFetchOriginalRedirectPolicy: same-host redirects are followed (and the
// resolved URL reflects them); off-allowlist redirect targets are refused.
func TestFetchOriginalRedirectPolicy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/html/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final/1706.03762", http.StatusFound)
	})
	mux.HandleFunc("/final/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<html><body>final</body></html>")
	})
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("off-allowlist host was fetched")
	}))
	t.Cleanup(evil.Close)
	// The allowlist is host-based, so the refused target must carry a
	// different HOSTNAME than the endpoint (httptest servers all listen on
	// 127.0.0.1 and differ only by port).
	evilHost := strings.Replace(evil.URL, "127.0.0.1", "localhost", 1)
	mux.HandleFunc("/evil/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evilHost+"/gotcha", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Run("same host followed", func(t *testing.T) {
		root := t.TempDir()
		res := fetchOriginalHTML(context.Background(), srv.Client(), fetchEndpoints(srv), root, fetchTestRecord("redir-ok"))
		if res.Status != FetchStatusOK {
			t.Fatalf("Status = %q, want ok (Err: %v)", res.Status, res.Err)
		}
		if !strings.HasSuffix(res.ResolvedURL, "/final/1706.03762") {
			t.Errorf("ResolvedURL = %q, want the post-redirect URL", res.ResolvedURL)
		}
	})

	t.Run("off allowlist refused", func(t *testing.T) {
		root := t.TempDir()
		res := fetchOriginalHTML(context.Background(), srv.Client(),
			[]string{srv.URL + "/evil/"}, root, fetchTestRecord("redir-evil"))
		if res.Status != FetchStatusError {
			t.Fatalf("Status = %q, want error (Err: %v)", res.Status, res.Err)
		}
		if _, err := os.Stat(filepath.Join(root, "redir-evil", OriginalHTMLFileName)); err == nil {
			t.Error("paper.html written despite refused redirect")
		}
	})
}

// TestFetchOriginalRejectsUnsafeSlug: a record with no usable slug is
// rejected before anything is fetched or written.
func TestFetchOriginalRejectsUnsafeSlug(t *testing.T) {
	srv := serveOriginal(t)
	root := t.TempDir()
	rec := fetchTestRecord("")
	rec.Title = ""

	res := fetchOriginalHTML(context.Background(), srv.Client(), fetchEndpoints(srv), root, rec)
	if res.Status != FetchStatusError {
		t.Fatalf("Status = %q, want error", res.Status)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Errorf("rejected record created entries %v", entries)
	}
}

// TestFetchOriginalContainmentSymlinkEscape: a paper directory that is a
// symlink pointing outside the library root is rejected before anything is
// written, even though the fetch itself succeeds.
func TestFetchOriginalContainmentSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	srv := serveOriginal(t)
	base := t.TempDir()
	libRoot := filepath.Join(base, "papers")
	if err := os.MkdirAll(libRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(libRoot, "evil")); err != nil {
		t.Fatal(err)
	}

	res := fetchOriginalHTML(context.Background(), srv.Client(), fetchEndpoints(srv), libRoot, fetchTestRecord("evil"))
	if res.Status != FetchStatusError {
		t.Fatalf("Status = %q, want error (Err: %v)", res.Status, res.Err)
	}
	if _, err := os.Stat(filepath.Join(outside, OriginalHTMLFileName)); err == nil {
		t.Error("paper.html escaped the library root")
	}
	if _, err := os.Stat(filepath.Join(outside, OriginalAssetsDirName)); err == nil {
		t.Error("assets escaped the library root")
	}
}

// TestFetchOriginalReplacesAndPreserves: a successful fetch atomically
// replaces an existing paper.html and swaps stale assets; a subsequent failed
// fetch leaves the current paper.html byte-for-byte intact.
func TestFetchOriginalReplacesAndPreserves(t *testing.T) {
	good := serveOriginal(t)
	root := t.TempDir()
	dir := filepath.Join(root, "fetched-paper")

	// Pre-existing content from an earlier fetch.
	if err := os.MkdirAll(filepath.Join(dir, OriginalAssetsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	oldHTML := []byte("<html><body>OLD RENDITION</body></html>")
	if err := os.WriteFile(filepath.Join(dir, OriginalHTMLFileName), oldHTML, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, OriginalAssetsDirName, "stale.png"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Successful re-fetch replaces both artifacts.
	res := fetchOriginalHTML(context.Background(), good.Client(), fetchEndpoints(good), root, fetchTestRecord("fetched-paper"))
	if res.Status != FetchStatusOK {
		t.Fatalf("Status = %q, want ok (Err: %v)", res.Status, res.Err)
	}
	fresh, err := os.ReadFile(filepath.Join(dir, OriginalHTMLFileName))
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(fresh, oldHTML) {
		t.Fatal("paper.html not replaced")
	}
	if _, err := os.Stat(filepath.Join(dir, OriginalAssetsDirName, "stale.png")); err == nil {
		t.Error("stale asset survived the re-fetch")
	}
	if _, err := os.Stat(filepath.Join(dir, OriginalAssetsDirName, "x1.png")); err != nil {
		t.Errorf("new asset missing: %v", err)
	}

	// A failed fetch (both renditions gone) must not disturb the file.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(dead.Close)
	res = fetchOriginalHTML(context.Background(), dead.Client(),
		[]string{dead.URL + "/a/", dead.URL + "/b/"}, root, fetchTestRecord("fetched-paper"))
	if res.Status != FetchStatusNotFound {
		t.Fatalf("Status = %q, want not_found (Err: %v)", res.Status, res.Err)
	}
	after, err := os.ReadFile(filepath.Join(dir, OriginalHTMLFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, fresh) {
		t.Errorf("paper.html changed after a failed fetch:\n got %q\nwant %q", after, fresh)
	}
}

// TestFetchOriginalNoImagesClearsStaleAssets: a rendition with no images
// removes a stale assets directory rather than orphaning it.
func TestFetchOriginalNoImagesClearsStaleAssets(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/html/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<html><body>text only</body></html>")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	root := t.TempDir()
	dir := filepath.Join(root, "text-paper")
	if err := os.MkdirAll(filepath.Join(dir, OriginalAssetsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, OriginalAssetsDirName, "old.png"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := fetchOriginalHTML(context.Background(), srv.Client(), fetchEndpoints(srv), root, fetchTestRecord("text-paper"))
	if res.Status != FetchStatusOK {
		t.Fatalf("Status = %q, want ok (Err: %v)", res.Status, res.Err)
	}
	if _, err := os.Stat(filepath.Join(dir, OriginalAssetsDirName)); err == nil {
		t.Error("stale assets directory survived an image-less re-fetch")
	}
}

// TestFetchOriginalHTMLBytesCap: an oversized document is refused rather than
// truncated to a corrupt copy.
func TestFetchOriginalHTMLBytesCap(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/html/1706.03762", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxOriginalHTMLBytes+1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	root := t.TempDir()

	res := fetchOriginalHTML(context.Background(), srv.Client(), fetchEndpoints(srv), root, fetchTestRecord("huge-paper"))
	if res.Status != FetchStatusError {
		t.Fatalf("Status = %q, want error (Err: %v)", res.Status, res.Err)
	}
	if _, err := os.Stat(filepath.Join(root, "huge-paper", OriginalHTMLFileName)); err == nil {
		t.Error("oversized document was written")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
