// Package papers — original-HTML ("paper.html") fetcher layer: fetch,
// sanitize, localize images, persist atomically and containment-checked
// (specs/domains/papers.md). Concurrent mutators serialize through their
// per-research-root mutex, as for WritePaper.
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

	// originalAssetsBackupDirName names the hidden sibling directory the
	// previous assets/ tree is renamed into during persistOriginal's swap, so
	// a failed paper.html rename can restore it.
	originalAssetsBackupDirName = ".assets-old"
)

// Rendition endpoints (native LaTeXML HTML first, ar5iv mirror fallback);
// their hosts are the hard allowlist — a redirect or image URL that leaves
// them is refused.
const (
	arxivHTMLEndpoint = "https://arxiv.org/html/"
	ar5ivHTMLEndpoint = "https://ar5iv.labs.arxiv.org/html/"
)

// Fetch caps and timeouts; the byte caps are enforced while streaming (an
// oversized response is cut off, not buffered) and the image-count cap
// de-references rather than fails the document (specs/domains/papers.md).
const (
	originalDocTimeout   = 60 * time.Second
	originalImageTimeout = 20 * time.Second
	maxOriginalHTMLBytes = 8 << 20 // 8 MiB
	maxOriginalImageSize = 2 << 20 // 2 MiB per image
	maxOriginalImages    = 200     // images localized per document
	maxOriginalRedirects = 5       // redirect hops per request

	// maxOriginalDataURIBytes caps an inline `data:` image URI kept verbatim
	// (base64 ≈ 4/3 the decoded size); over the cap it is dropped.
	maxOriginalDataURIBytes = 2 << 20 // 2 MiB
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

	// FetchStatusError: any other failure (HTTP status, oversize/undecodable
	// document, rejected slug, containment, filesystem); Err carries the cause.
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

	// ResolvedURL is the post-redirect URL that produced the document; empty
	// unless a fetch got far enough to have one.
	ResolvedURL string `json:"resolved_url,omitempty"`

	// AssetsKept counts images downloaded and written under assets/.
	AssetsKept int `json:"assets_kept"`

	// AssetsSkipped counts images whose src was removed instead — failed,
	// oversized, beyond the count cap, or off-allowlist.
	AssetsSkipped int `json:"assets_skipped"`

	// Err is the underlying error for offline/error statuses.
	Err error `json:"-"`
}

// FetchOriginalHTML fetches, sanitizes, localizes, and persists the best
// rendition; the typed outcome treats not_found / no_arxiv / offline as
// expected conditions (no id → no_arxiv, no disk/network). client is
// cloned, never mutated (specs/domains/papers.md).
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
	// A network-level failure anywhere means completeness is unknowable, so
	// offline wins over not_found.
	if offlineErr != nil {
		return FetchResult{Status: FetchStatusOffline, ArxivID: id, Err: offlineErr}
	}
	return FetchResult{Status: FetchStatusNotFound, ArxivID: id}
}

// --- arXiv identifier normalization ---

// arxivIDNewRe matches a modern arXiv id: YYMM.NNNNN with an optional version
// suffix (1706.03762, 2401.12345v2).
var arxivIDNewRe = regexp.MustCompile(`^\d{4}\.\d{4,5}(v\d+)?$`)

// arxivIDOldRe matches a legacy arXiv id: subject-area/YYMMNNN with an
// optional version suffix (hep-th/9901001, cs.CL/0301012v1).
var arxivIDOldRe = regexp.MustCompile(`^[a-z-]+(\.[A-Z]{2})?/\d{7}(v\d+)?$`)

// arxivURLPathPrefixes are the arXiv URL path forms an id may be embedded in
// (/abs/, /html/, /pdf/).
var arxivURLPathPrefixes = []string{"abs", "html", "pdf"}

// NormalizeArxivID extracts the first "arxiv"-scheme identifier (bare,
// prefixed, or URL form); the text only ever builds URLs onto the fixed
// allowlisted endpoints, so a hostile value cannot steer the fetch.
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

// --- Bounded fetching ---

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

// boundedClient clones base (never mutating it) and forces the per-request
// timeout plus a redirect policy bounded to the allowlisted hosts.
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

// fetchBounded GETs rawURL under the byte cap, returning the body plus the
// FINAL (post-redirect) URL — the base relative image URLs resolve against.
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

// isOfflineErr reports network-level failures (DNS, dial, reset, timeout);
// a *url.Error counts only when its CAUSE is network-level, so a
// redirect-policy rejection is not misread as offline.
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

// --- Sanitization + image localization ---

// strippedElements are removed wholesale — the saved copy must be inert
// (scripts, stylesheets, resource links, nested browsing contexts), even
// though the bytes are already local.
var strippedElements = map[string]struct{}{
	"script": {},
	"style":  {},
	"link":   {},
	"iframe": {},
}

// arxivDocumentClass is the LaTeXML class of the paper's root <article>
// element — the actual document, as opposed to arXiv's site chrome around it.
const arxivDocumentClass = "ltx_document"

// stripArxivChrome reduces the document to its own <article>: the arXiv/
// ar5iv site chrome is dropped body-level (sparing the asset pass its
// images); no recognizable article → untouched, fail-soft (specs/domains/
// papers.md).
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

// hasClassToken reports whether n's class attribute carries token as a
// whitespace-separated token ("ltx_documentation" does not match).
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

// localizeOriginal cuts the site chrome, strips banned elements, and
// rewrites embedded resources (<img src>, <object data>, <embed src>) to
// local assets/<name>. A failing/capped/off-allowlist resource loses its
// attribute but keeps its element — an honest placeholder, never a broken
// remote URL.
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

	seen    int // resource elements encountered (cap counter)
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
		switch n.Data {
		case "img":
			l.localizeResource(n, "src")
		case "object":
			// LaTeXML emits SVG figures as <object data="…">.
			l.localizeResource(n, "data")
		case "embed":
			l.localizeResource(n, "src")
		}
	}
	l.walk(n)
}

// localizeResource rewrites one resource attribute to assets/<name>; a
// `data:` URI is self-contained (kept verbatim, bounded, never fetched);
// <img srcset> is always dropped.
func (l *localizer) localizeResource(n *html.Node, attr string) {
	if n.Data == "img" {
		removeAttr(n, "srcset")
	}
	raw := getAttr(n, attr)
	if raw == "" {
		return // nothing to rewrite; an element without the attribute is already honest
	}
	l.seen++
	if l.seen > maxOriginalImages {
		removeAttr(n, attr)
		l.skipped++
		return
	}
	if isDataURI(raw) {
		if len(raw) > maxOriginalDataURIBytes {
			removeAttr(n, attr)
			l.skipped++
			return
		}
		l.kept++ // inline self-contained bytes: kept verbatim, no fetch
		return
	}

	resolved, name, ok := l.resolve(raw)
	if !ok {
		removeAttr(n, attr)
		l.skipped++
		return
	}
	if name != "" { // already downloaded on an earlier reference
		setAttr(n, attr, OriginalAssetsDirName+"/"+name)
		return
	}

	body, _, err := fetchBounded(l.ctx, l.client, resolved, maxOriginalImageSize)
	if err != nil {
		removeAttr(n, attr)
		l.skipped++
		return
	}
	name = l.assignName(resolved)
	l.assets[name] = body
	l.kept++
	setAttr(n, attr, OriginalAssetsDirName+"/"+name)
}

// isDataURI reports whether src is a `data:` URI — inline, self-contained
// bytes that need no download and no host check.
func isDataURI(src string) bool {
	u, err := url.Parse(src)
	return err == nil && u.Scheme == "data"
}

// resolve absolutizes src against the document URL and checks the
// allowlist; an already-localized URL returns its assigned name (dedup
// fast path), an unusable one ok=false.
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

// sanitizeAssetName folds a URL path segment into one safe component
// ([A-Za-z0-9._-], no leading dot, length-capped keeping the extension).
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

// --- Persistence ---

// persistOriginal stages into a temp dir inside the paper dir (same
// filesystem → atomic renames) and swaps in with a backup: assets/ renamed
// aside, new assets in, paper.html renamed LAST as the commit point (its
// failure rolls the swap back). Targets are containment-checked before
// staging (specs/domains/papers.md).
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
	stagedAssets := ""
	if len(assets) > 0 {
		stagedAssets = filepath.Join(staging, OriginalAssetsDirName)
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
	} else if !isDir(assetsTarget) {
		// No new assets and nothing stale to replace: the document rename is
		// the only remaining step.
		if err := os.Rename(filepath.Join(staging, OriginalHTMLFileName), htmlTarget); err != nil {
			return fmt.Errorf("moving paper.html in for %q: %w", slug, err)
		}
		return nil
	}

	// The existing assets dir is renamed aside (never deleted up front) so a
	// failed paper.html commit below can restore it.
	assetsBackup := filepath.Join(paperDir, originalAssetsBackupDirName)
	hadOldAssets := isDir(assetsTarget)
	if hadOldAssets {
		if err := os.RemoveAll(assetsBackup); err != nil {
			return fmt.Errorf("replacing assets for %q: %w", slug, err)
		}
		if err := os.Rename(assetsTarget, assetsBackup); err != nil {
			return fmt.Errorf("replacing assets for %q: %w", slug, err)
		}
	}
	if stagedAssets != "" {
		if err := os.Rename(stagedAssets, assetsTarget); err != nil {
			if hadOldAssets {
				_ = os.Rename(assetsBackup, assetsTarget) // best-effort rollback
			}
			return fmt.Errorf("moving assets in for %q: %w", slug, err)
		}
	}

	// paper.html is renamed LAST — the commit point. On its failure the assets
	// swap is rolled back so the still-current document keeps its own images.
	if err := os.Rename(filepath.Join(staging, OriginalHTMLFileName), htmlTarget); err != nil {
		_ = os.RemoveAll(assetsTarget)
		if hadOldAssets {
			_ = os.Rename(assetsBackup, assetsTarget) // best-effort rollback
		}
		return fmt.Errorf("moving paper.html in for %q: %w", slug, err)
	}
	if hadOldAssets {
		if err := os.RemoveAll(assetsBackup); err != nil {
			return fmt.Errorf("removing assets backup for %q: %w", slug, err)
		}
	}
	return nil
}

// isDir reports whether dir exists and is a directory.
func isDir(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
