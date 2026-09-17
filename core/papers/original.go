// Package papers — original-HTML ("paper.html") fetcher layer.
//
// This file downloads a paper's best HTML rendition (arxiv.org/html/<id>,
// falling back to ar5iv.labs.arxiv.org/html/<id>), sanitizes the document,
// localizes its images, and persists paper.html plus assets/ into the paper's
// directory. Persistence mirrors writer.go: every target is symlink-resolved
// and containment-checked against the library root through pathutil BEFORE a
// staging directory is created, and the staged files are renamed into place
// only after everything is staged — so a fetch or staging failure leaves the
// prior paper.html byte-for-byte unchanged and a symlinked paper directory can
// never redirect a write outside the library.
//
// The fetcher is stateless (no package-level mutable state), so it needs no
// mutex of its own; callers that mutate a paper library concurrently must
// serialize the mutation through their own per-research-root mutex (the
// backend's researchMutationMu), exactly as they do for WritePaper.
package papers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/html"
)

// Artifact names for the fetched original. The library layout gains
// <libraryRoot>/<slug>/paper.html plus <libraryRoot>/<slug>/assets/<image>.
const (
	// OriginalHTMLFileName is the localized HTML rendition of the paper.
	OriginalHTMLFileName = "paper.html"

	// OriginalAssetsDirName is the directory holding the paper's localized
	// images, referenced from paper.html as assets/<name>.
	OriginalAssetsDirName = "assets"
)

// Rendition endpoints, in preference order: arXiv's native LaTeXML HTML first,
// the ar5iv mirror as fallback. The hosts of these endpoints are also the hard
// allowlist: a redirect (or an image URL) that leaves them is refused, so the
// fetcher can never be steered at an arbitrary host.
const (
	arxivHTMLEndpoint = "https://arxiv.org/html/"
	ar5ivHTMLEndpoint = "https://ar5iv.labs.arxiv.org/html/"
)

// Fetch caps and timeouts. The HTML and per-image byte caps are enforced while
// streaming the body (LimitReader), so an oversized response is cut off, not
// buffered; the image-count cap skips (and de-references) images beyond the
// limit rather than failing the whole document.
const (
	originalDocTimeout   = 60 * time.Second
	originalImageTimeout = 20 * time.Second
	maxOriginalHTMLBytes = 8 << 20 // 8 MiB
	maxOriginalImageSize = 2 << 20 // 2 MiB per image
	maxOriginalImages    = 200     // images localized per document
	maxOriginalRedirects = 5       // redirect hops per request
)

// FetchStatus classifies the outcome of a paper.html fetch. The zero value is
// not meaningful — always construct results through FetchOriginalHTML.
type FetchStatus string

const (
	// FetchStatusOK: a rendition was downloaded, localized, and persisted.
	FetchStatusOK FetchStatus = "ok"

	// FetchStatusNotFound: no HTML rendition exists on any endpoint (404/410
	// everywhere reachable); nothing was written.
	FetchStatusNotFound FetchStatus = "not_found"

	// FetchStatusNoArxiv: the record carries no usable arXiv identifier, so
	// there is nothing to fetch; nothing was written.
	FetchStatusNoArxiv FetchStatus = "no_arxiv"

	// FetchStatusOffline: no endpoint could be reached at the network level
	// (DNS, dial, timeout); nothing was written.
	FetchStatusOffline FetchStatus = "offline"

	// FetchStatusError: the fetch failed for another reason (unexpected HTTP
	// status, oversized/undecodable document, rejected slug or containment
	// violation, filesystem failure). FetchResult.Err carries the cause.
	FetchStatusError FetchStatus = "error"
)

// FetchResult is the typed outcome of FetchOriginalHTML.
type FetchResult struct {
	// Status is the classification; Err is non-nil exactly for
	// FetchStatusOffline and FetchStatusError.
	Status FetchStatus `json:"status"`

	// ArxivID is the normalized identifier the fetch used; empty for
	// FetchStatusNoArxiv.
	ArxivID string `json:"arxiv_id,omitempty"`

	// ResolvedURL is the URL that actually produced the document (after
	// redirects); empty unless Status is FetchStatusOK (or a fetch got far
	// enough to have one before failing).
	ResolvedURL string `json:"resolved_url,omitempty"`

	// AssetsKept counts images downloaded and written under assets/.
	AssetsKept int `json:"assets_kept"`

	// AssetsSkipped counts images whose src was removed instead — failed,
	// oversized, beyond the count cap, or off-allowlist.
	AssetsSkipped int `json:"assets_skipped"`

	// Err is the underlying error for offline/error statuses.
	Err error `json:"-"`
}

// FetchOriginalHTML fetches rec's best HTML rendition from arXiv (native
// arxiv.org/html, falling back to the ar5iv mirror), sanitizes it, localizes
// its images, and persists paper.html plus assets/ into
// libraryRoot/<rec.ResolvedSlug()>/ atomically and containment-checked against
// libraryRoot. client may be nil (a default bounded client is built); when
// given it is cloned, so the caller's value is never mutated.
//
// The outcome is typed rather than a bare error: not_found, no_arxiv, and
// offline are expected conditions, not failures. A record with no usable
// arXiv identifier yields FetchStatusNoArxiv without touching disk or network.
//
// Callers that mutate the paper library concurrently must serialize this with
// their per-research-root mutation mutex, as they do for WritePaper.
func FetchOriginalHTML(ctx context.Context, client *http.Client, libraryRoot string, rec PaperRecord) FetchResult {
	return fetchOriginalHTML(ctx, client, []string{arxivHTMLEndpoint, ar5ivHTMLEndpoint}, libraryRoot, rec)
}

// fetchOriginalHTML is FetchOriginalHTML with injectable endpoints (tests run
// it against httptest servers). The endpoints' hosts form the hard allowlist.
func fetchOriginalHTML(ctx context.Context, client *http.Client, endpoints []string, libraryRoot string, rec PaperRecord) FetchResult {
	id := NormalizeArxivID(rec.Identifiers)
	if id == "" {
		return FetchResult{Status: FetchStatusNoArxiv}
	}
	slug := rec.ResolvedSlug()
	if slug == "" {
		return FetchResult{
			Status:  FetchStatusError,
			ArxivID: id,
			Err:     errors.New("paper.html fetch rejected: record has neither a valid slug, title, nor id"),
		}
	}

	allowed := allowedHostsOf(endpoints)
	docClient := boundedClient(client, allowed, originalDocTimeout)
	imgClient := boundedClient(client, allowed, originalImageTimeout)

	var offlineErr error
	for _, ep := range endpoints {
		if err := ctx.Err(); err != nil {
			return FetchResult{Status: FetchStatusError, ArxivID: id, Err: err}
		}
		target := strings.TrimSuffix(ep, "/") + "/" + id
		body, finalURL, err := fetchBounded(ctx, docClient, target, maxOriginalHTMLBytes)
		if err != nil {
			var status *httpStatusError
			switch {
			case errors.As(err, &status) && (status.Status == http.StatusNotFound || status.Status == http.StatusGone):
				continue // no rendition here — try the next endpoint
			case isOfflineErr(err):
				if offlineErr == nil {
					offlineErr = err
				}
				continue
			default:
				return FetchResult{Status: FetchStatusError, ArxivID: id, Err: err}
			}
		}

		doc, assets, kept, skipped, err := localizeOriginal(ctx, imgClient, body, finalURL, allowed)
		if err != nil {
			return FetchResult{Status: FetchStatusError, ArxivID: id, ResolvedURL: finalURL, Err: fmt.Errorf("localizing %q: %w", finalURL, err)}
		}
		if err := persistOriginal(libraryRoot, slug, doc, assets); err != nil {
			return FetchResult{Status: FetchStatusError, ArxivID: id, ResolvedURL: finalURL, Err: err}
		}
		return FetchResult{
			Status:        FetchStatusOK,
			ArxivID:       id,
			ResolvedURL:   finalURL,
			AssetsKept:    kept,
			AssetsSkipped: skipped,
		}
	}
	// Every endpoint was tried without a document. A network-level failure
	// anywhere means completeness is unknowable, so offline wins over
	// not_found.
	if offlineErr != nil {
		return FetchResult{Status: FetchStatusOffline, ArxivID: id, Err: offlineErr}
	}
	return FetchResult{Status: FetchStatusNotFound, ArxivID: id}
}

// ---------------------------------------------------------------------------
// arXiv identifier normalization
// ---------------------------------------------------------------------------

// arxivIDNewRe matches a modern arXiv id: YYMM.NNNNN with an optional version
// suffix (1706.03762, 2401.12345v2).
var arxivIDNewRe = regexp.MustCompile(`^\d{4}\.\d{4,5}(v\d+)?$`)

// arxivIDOldRe matches a legacy arXiv id: subject-area/YYMMNNN with an
// optional version suffix (hep-th/9901001, cs.CL/0301012v1).
var arxivIDOldRe = regexp.MustCompile(`^[a-z-]+(\.[A-Z]{2})?/\d{7}(v\d+)?$`)

// arxivURLPathPrefixes are the arXiv URL path forms an id may be embedded in
// (/abs/, /html/, /pdf/).
var arxivURLPathPrefixes = []string{"abs", "html", "pdf"}

// NormalizeArxivID extracts and normalizes the arXiv identifier from a
// record's identifiers: the first identifier whose scheme folds to "arxiv".
// The value is accepted in bare form ("1706.03762"), prefixed form
// ("arXiv:1706.03762"), and URL form ("https://arxiv.org/abs/1706.03762v2",
// "arxiv.org/html/hep-th/9901001"); an optional version suffix (vN) is kept.
// The identifier text itself is never fetched — it is only used to build URLs
// onto the fixed allowlisted endpoints — so a hostile URL-shaped value cannot
// steer the fetch. Input that carries no well-formed id yields "".
func NormalizeArxivID(idents []Identifier) string {
	for _, ident := range idents {
		if strings.ToLower(strings.TrimSpace(ident.Scheme)) != "arxiv" {
			continue
		}
		if id := normalizeArxivIDValue(strings.TrimSpace(ident.Value)); id != "" {
			return id
		}
	}
	return ""
}

// normalizeArxivIDValue normalizes a single raw identifier value; see
// NormalizeArxivID.
func normalizeArxivIDValue(raw string) string {
	v := raw
	// URL form: absolutize a bare host prefix, then strip the path down to
	// the id segment.
	if !strings.Contains(v, "://") {
		if host := "arxiv.org/"; strings.HasPrefix(strings.ToLower(v), host) {
			v = "https://" + v
		}
	}
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil {
			return ""
		}
		segments := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segments) == 0 {
			return ""
		}
		if len(segments) > 1 && slicesContains(arxivURLPathPrefixes, segments[0]) {
			segments = segments[1:]
		}
		v = strings.Join(segments, "/")
	}
	// Prefixed form: strip a case-insensitive "arXiv:" prefix.
	if rest, found := strings.CutPrefix(strings.ToLower(v), "arxiv:"); found {
		v = v[len(v)-len(rest):]
	}
	v = strings.TrimSpace(v)
	if arxivIDNewRe.MatchString(v) || arxivIDOldRe.MatchString(v) {
		return v
	}
	return ""
}

// slicesContains reports whether list contains s (a tiny local helper to
// avoid importing the slices package for one call).
func slicesContains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Bounded fetching
// ---------------------------------------------------------------------------

// httpStatusError marks a non-200 response so the rendition loop can tell a
// missing document (404/410) from other failures.
type httpStatusError struct {
	URL    string
	Status int
}

func (e *httpStatusError) Error() string {
	return "fetching " + strconv.Quote(e.URL) + ": unexpected HTTP status " + strconv.Itoa(e.Status)
}

// allowedHostsOf derives the hard host allowlist from the endpoints' hosts.
func allowedHostsOf(endpoints []string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(endpoints))
	for _, ep := range endpoints {
		if u, err := url.Parse(ep); err == nil && u.Hostname() != "" {
			allowed[u.Hostname()] = struct{}{}
		}
	}
	return allowed
}

// boundedClient clones base (or starts from a default client), then forces a
// per-request timeout and a redirect policy that bounds hops and refuses any
// host outside allowed. The base client is never mutated.
func boundedClient(base *http.Client, allowed map[string]struct{}, timeout time.Duration) *http.Client {
	var c *http.Client
	if base == nil {
		c = &http.Client{}
	} else {
		cp := *base // shallow copy: shares Transport/Jar, never mutates the caller's value
		c = &cp
	}
	c.Timeout = timeout
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxOriginalRedirects {
			return fmt.Errorf("refusing to follow more than %d redirects fetching %q", maxOriginalRedirects, req.URL)
		}
		if _, ok := allowed[req.URL.Hostname()]; !ok {
			return fmt.Errorf("refusing redirect to non-allowlisted host %q", req.URL.Hostname())
		}
		return nil
	}
	return c
}

// fetchBounded GETs rawURL with c, enforcing the hard byte cap while reading,
// and returns the body plus the FINAL URL (after redirects) — the base the
// document's relative image URLs must resolve against. A non-200 status is an
// *httpStatusError; transport failures surface as-is for classification.
func fetchBounded(ctx context.Context, c *http.Client, rawURL string, limit int64) (body []byte, finalURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "c0wrk-papers/1.0")
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close() //nolint:errcheck // body of a GET we fully drain or abandon

	if resp.StatusCode != http.StatusOK {
		// Drain (best-effort) so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, "", &httpStatusError{URL: rawURL, Status: resp.StatusCode}
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > limit {
		return nil, "", fmt.Errorf("fetching %q: response exceeds the %d-byte cap", rawURL, limit)
	}
	return body, resp.Request.URL.String(), nil
}

// isOfflineErr reports whether err is a network-level failure (DNS, dial,
// refused, reset, timeout) as opposed to an HTTP-status or policy error. A
// *url.Error counts only when its CAUSE is network-level, so a redirect-policy
// rejection (wrapped in *url.Error by the transport) is not misread as offline.
func isOfflineErr(err error) bool {
	if err == nil {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isOfflineCause(urlErr.Err)
	}
	return isOfflineCause(err)
}

// isOfflineCause classifies an unwrapped error cause.
func isOfflineCause(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var timeouter interface{ Timeout() bool }
	if errors.As(err, &timeouter) && timeouter.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// ---------------------------------------------------------------------------
// Sanitization + image localization
// ---------------------------------------------------------------------------

// strippedElements are removed wholesale from the fetched document. They are
// defense in depth: the saved copy is meant to be self-contained and inert, so
// scripts, stylesheets, resource links, and nested browsing contexts go away
// even though the bytes are already local.
var strippedElements = map[string]struct{}{
	"script": {},
	"style":  {},
	"link":   {},
	"iframe": {},
}

// arxivDocumentClass is the LaTeXML class of the paper's root <article>
// element — the actual document, as opposed to arXiv's site chrome around it.
const arxivDocumentClass = "ltx_document"

// stripArxivChrome reduces the document to the paper's own content. arXiv's
// HTML renditions wrap <article class="ltx_document"> in heavy site chrome: a
// report-issue <dialog>, an announcement banner, the sticky
// arxiv-html-header menu, the table-of-contents navbar, and an
// arxiv-html-footer (ar5iv mirrors the layout with ltx_page_header /
// ltx_page_footer wrappers). The saved paper.html must begin at the paper
// itself (its title heading), so every body-level subtree except the article
// is dropped — which also spares the asset pass the chrome's logo/banner
// images. A document with no recognizable article is left untouched
// (fail-soft: unknown shapes keep the whole-document behavior).
func stripArxivChrome(tree *html.Node) {
	var body, article *html.Node
	var find func(n *html.Node)
	find = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				switch {
				case c.Data == "body" && body == nil:
					body = c
				case c.Data == "article" && article == nil && hasClassToken(c, arxivDocumentClass):
					article = c
				}
			}
			find(c)
		}
	}
	find(tree)
	if body == nil || article == nil {
		return
	}
	if article.Parent != nil {
		article.Parent.RemoveChild(article)
	}
	for c := body.FirstChild; c != nil; {
		next := c.NextSibling
		body.RemoveChild(c)
		c = next
	}
	body.AppendChild(article)
}

// hasClassToken reports whether n carries token in its class attribute,
// matched as a whitespace-separated token ("ltx_document ltx_authors_1line"
// contains "ltx_document"; "ltx_documentation" does not).
func hasClassToken(n *html.Node, token string) bool {
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for _, t := range strings.Fields(a.Val) {
			if t == token {
				return true
			}
		}
	}
	return false
}

// localizeOriginal parses the fetched HTML, cuts the arXiv site chrome away,
// strips the banned elements, rewrites every <img src> to a local
// assets/<name>, and downloads each image with c (bounded by
// maxOriginalImageSize). Relative URLs resolve against the FINAL document
// URL; absolute URLs must stay on the allowed hosts. An image that fails,
// exceeds its cap, exceeds the count cap, or leaves the allowlist keeps its
// <img> element but loses its src — an honest missing placeholder, never a
// broken remote URL. It returns the rendered document, the downloaded
// assets keyed by file name, and the kept/skipped counts.
func localizeOriginal(ctx context.Context, c *http.Client, body []byte, docURL string, allowed map[string]struct{}) (doc []byte, assets map[string][]byte, kept, skipped int, err error) {
	base, err := url.Parse(docURL)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("document URL %q unparseable: %w", docURL, err)
	}
	tree, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("parsing HTML: %w", err)
	}
	stripArxivChrome(tree)

	l := &localizer{
		ctx:      ctx,
		client:   c,
		base:     base,
		allowed:  allowed,
		byURL:    make(map[string]string),
		usedName: make(map[string]struct{}),
		assets:   make(map[string][]byte),
	}
	l.walk(tree)

	var out bytes.Buffer
	if err := html.Render(&out, tree); err != nil {
		return nil, nil, 0, 0, fmt.Errorf("rendering localized HTML: %w", err)
	}
	return out.Bytes(), l.assets, l.kept, l.skipped, nil
}

// localizer carries the localization pass's state.
type localizer struct {
	ctx     context.Context
	client  *http.Client
	base    *url.URL
	allowed map[string]struct{}

	byURL    map[string]string   // resolved image URL → asset name (dedup)
	usedName map[string]struct{} // taken asset names
	assets   map[string][]byte   // asset name → bytes

	seen    int // <img> elements encountered (cap counter)
	kept    int
	skipped int
}

// walk visits node's subtree, stripping banned elements and localizing imgs.
func (l *localizer) walk(node *html.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		l.visit(child, node)
		child = next
	}
}

// visit processes one node against its parent, then recurses into survivors.
func (l *localizer) visit(n, parent *html.Node) {
	if n.Type == html.ElementNode {
		if _, bad := strippedElements[n.Data]; bad {
			parent.RemoveChild(n)
			return
		}
		if n.Data == "img" {
			l.localizeImg(n)
		}
	}
	l.walk(n)
}

// localizeImg rewrites (or removes) one <img> tag's src. srcset is always
// dropped: it can only point at remote renditions.
func (l *localizer) localizeImg(n *html.Node) {
	removeAttr(n, "srcset")
	src := getAttr(n, "src")
	if src == "" {
		return // nothing to rewrite; an <img> without src is already honest
	}
	l.seen++
	if l.seen > maxOriginalImages {
		removeAttr(n, "src")
		l.skipped++
		return
	}

	resolved, name, ok := l.resolve(src)
	if !ok {
		removeAttr(n, "src")
		l.skipped++
		return
	}
	if name != "" { // already downloaded on an earlier reference
		setAttr(n, "src", OriginalAssetsDirName+"/"+name)
		return
	}

	body, _, err := fetchBounded(l.ctx, l.client, resolved, maxOriginalImageSize)
	if err != nil {
		removeAttr(n, "src")
		l.skipped++
		return
	}
	name = l.assignName(resolved)
	l.assets[name] = body
	l.kept++
	setAttr(n, "src", OriginalAssetsDirName+"/"+name)
}

// resolve absolutizes src against the document URL and checks it against the
// allowlist. When the URL was already localized, it returns the assigned name
// with ok=true and name non-empty (the dedup fast path); when the URL is
// unusable, ok=false.
func (l *localizer) resolve(src string) (resolved, name string, ok bool) {
	u, err := l.base.Parse(src)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", false
	}
	if _, allowed := l.allowed[u.Hostname()]; !allowed {
		return "", "", false
	}
	resolved = u.String()
	if assigned, dup := l.byURL[resolved]; dup {
		return resolved, assigned, true
	}
	return resolved, "", true
}

// assignName derives a safe unique assets/ file name for a resolved image URL
// and records the URL→name mapping.
func (l *localizer) assignName(resolved string) string {
	u, err := url.Parse(resolved)
	if err != nil {
		u = &url.URL{Path: resolved}
	}
	base := sanitizeAssetName(path.Base(u.Path))
	name := base
	for i := 2; ; i++ {
		if _, taken := l.usedName[name]; !taken {
			break
		}
		ext := filepath.Ext(base)
		name = strings.TrimSuffix(base, ext) + "-" + strconv.Itoa(i) + ext
	}
	l.usedName[name] = struct{}{}
	l.byURL[resolved] = name
	return name
}

// sanitizeAssetName folds a URL path segment into a safe single path
// component: [A-Za-z0-9._-] kept, everything else collapsed to "_", no
// leading dot (no hidden files), length-capped preserving the extension.
func sanitizeAssetName(base string) string {
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.TrimLeft(b.String(), ".")
	if s == "" {
		return "image"
	}
	if len(s) > 80 {
		ext := filepath.Ext(s)
		if len(ext) > 16 {
			ext = ""
		}
		s = s[:80-len(ext)] + ext
	}
	return s
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func removeAttr(n *html.Node, key string) {
	kept := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != key {
			kept = append(kept, a)
		}
	}
	n.Attr = kept
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// persistOriginal stages doc plus assets into a temp directory inside the
// paper's own directory (same filesystem, so renames are atomic), then swaps
// them into place: stale assets are removed, the new assets directory is
// renamed in, and paper.html is renamed LAST as the commit point. Every final
// target is symlink-resolved and containment-checked against libraryRoot
// BEFORE staging (resolveTargetWithinRoot / pathutil.IsWithinPath), so a
// symlinked paper directory can never redirect a write outside the library and
// any failure before the first rename leaves the prior content untouched.
func persistOriginal(libraryRoot, slug string, doc []byte, assets map[string][]byte) error {
	paperDir, err := ensurePaperDir(libraryRoot, slug)
	if err != nil {
		return err
	}
	htmlTarget, err := resolveTargetWithinRoot(libraryRoot, filepath.Join(paperDir, OriginalHTMLFileName))
	if err != nil {
		return err
	}
	assetsTarget, err := resolveTargetWithinRoot(libraryRoot, filepath.Join(paperDir, OriginalAssetsDirName))
	if err != nil {
		return err
	}

	staging, err := os.MkdirTemp(paperDir, ".original-*")
	if err != nil {
		return fmt.Errorf("staging paper.html for %q: %w", slug, err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := os.WriteFile(filepath.Join(staging, OriginalHTMLFileName), doc, 0o644); err != nil {
		return fmt.Errorf("staging paper.html for %q: %w", slug, err)
	}
	if len(assets) > 0 {
		stagedAssets := filepath.Join(staging, OriginalAssetsDirName)
		if err := os.Mkdir(stagedAssets, 0o755); err != nil {
			return fmt.Errorf("staging assets for %q: %w", slug, err)
		}
		names := make([]string, 0, len(assets))
		for name := range assets {
			names = append(names, name)
		}
		sort.Strings(names) // deterministic staging order
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(stagedAssets, name), assets[name], 0o644); err != nil {
				return fmt.Errorf("staging asset %q for %q: %w", name, slug, err)
			}
		}
		if err := os.RemoveAll(assetsTarget); err != nil {
			return fmt.Errorf("replacing assets for %q: %w", slug, err)
		}
		if err := os.Rename(stagedAssets, assetsTarget); err != nil {
			return fmt.Errorf("moving assets in for %q: %w", slug, err)
		}
	} else if isDir(assetsTarget) {
		// A rendition with no images still clears stale assets from a
		// previous fetch.
		if err := os.RemoveAll(assetsTarget); err != nil {
			return fmt.Errorf("clearing stale assets for %q: %w", slug, err)
		}
	}

	if err := os.Rename(filepath.Join(staging, OriginalHTMLFileName), htmlTarget); err != nil {
		return fmt.Errorf("moving paper.html in for %q: %w", slug, err)
	}
	return nil
}

// isDir reports whether dir exists and is a directory.
func isDir(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
