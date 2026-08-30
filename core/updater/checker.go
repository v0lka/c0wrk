package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/core/proxy"
	"golang.org/x/mod/semver"
)

// GitHub API endpoints are HTTPS-only; the scheme is hard-coded so a
// misconfigured proxy can never downgrade the update check to plain HTTP.
const (
	defaultOwner       = "v0lka"
	defaultRepo        = "c0wrk"
	defaultHTTPTimeout = 15 * time.Second
	githubAccept       = "application/vnd.github+json"
	userAgent          = "c0wrk-desktop-updater"
)

// Sentinel errors for well-known check outcomes.
var (
	// ErrNoRelease is returned when GitHub reports no published release
	// (HTTP 404 from /releases/latest).
	ErrNoRelease = errors.New("no published release found")
	// ErrRateLimited is returned when the GitHub API rejects the request
	// because the unauthenticated rate limit was exceeded (HTTP 403 with
	// X-RateLimit-Remaining: 0).
	ErrRateLimited = errors.New("github API rate limited")
	// ErrForbidden is returned when the GitHub API returns a genuine 403 that
	// is not a rate-limit response, so callers can distinguish it from
	// ErrRateLimited.
	ErrForbidden = errors.New("github API forbidden")
)

// ReleaseAsset is a single downloadable file attached to a GitHub release.
type ReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	ContentType        string `json:"content_type"`
	Size               int64  `json:"size"`
}

// Result is the outcome of a single update check.
type Result struct {
	// Available is true when a strictly newer release exists, has not been
	// skipped by the user, and ships an asset for the current platform.
	Available bool
	// LatestVersion is the tag_name of the newest release (e.g. "v1.2.4").
	LatestVersion string
	// CurrentVersion is the version the running binary reports.
	CurrentVersion string
	// AssetURL is the browser_download_url of the selected asset (empty when
	// not available).
	AssetURL string
	// AssetName is the filename of the selected asset.
	AssetName string
	// ReleaseNotes is the release body (markdown) for the latest release.
	ReleaseNotes string
	// PublishedAt is the RFC3339 timestamp the release was published.
	PublishedAt string
	// HTMLURL is the human-readable release page.
	HTMLURL string
}

// Config parameterises a Checker. All fields have sensible defaults applied
// by NewChecker / NewCheckerWithProxy.
type Config struct {
	Owner          string        // GitHub repo owner, defaults to "v0lka"
	Repo           string        // GitHub repo name, defaults to "c0wrk"
	CurrentVersion string        // version of the running binary (e.g. "v1.2.3" or "dev")
	SkippedVersion string        // tag the user dismissed; suppresses that exact release
	HTTPTimeout    time.Duration // per-request timeout; defaults to 15s
}

// Checker queries GitHub for the latest release and decides whether an update
// is available for a given platform. It is safe for concurrent use after
// construction.
type Checker struct {
	cfg     Config
	client  *http.Client
	logger  *slog.Logger
	goos    string
	goarch  string
	baseURL string // API origin; defaults to "https://api.github.com" (HTTPS-only). Overridable by in-package tests.
}

// NewChecker returns a Checker backed by the given HTTP client. A nil client
// is replaced by a default client honouring Config.HTTPTimeout, which is how a
// caller wires in a proxy-aware client built via the proxy package
// (proxy.BuildClient) — corporate proxies are therefore never bypassed.
//
// The detected runtime platform is used by default; override it with
// WithPlatform (mainly for tests).
func NewChecker(cfg Config, client *http.Client, logger *slog.Logger) *Checker {
	cfg.Owner = defaultString(cfg.Owner, defaultOwner)
	cfg.Repo = defaultString(cfg.Repo, defaultRepo)
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = defaultHTTPTimeout
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.HTTPTimeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	goos, goarch := CurrentPlatform()
	return &Checker{
		cfg:     cfg,
		client:  client,
		logger:  logger,
		goos:    goos,
		goarch:  goarch,
		baseURL: "https://api.github.com",
	}
}

// NewCheckerWithProxy builds an HTTP client from a proxy.Config (so enterprise
// proxies and custom CA bundles are respected) and returns a Checker using it.
// When the proxy is disabled, a plain client with Config.HTTPTimeout is used.
func NewCheckerWithProxy(cfg Config, proxyCfg proxy.Config, logger *slog.Logger) (*Checker, error) {
	client, err := proxy.BuildClient(proxyCfg, cfg.HTTPTimeout, logger)
	if err != nil {
		return nil, fmt.Errorf("build proxy client: %w", err)
	}
	// proxy.BuildClient returns (nil, nil) when the proxy is disabled; fall
	// back to a default client so the checker always has a working transport.
	if client == nil {
		client = &http.Client{Timeout: cfg.HTTPTimeout}
	}
	return NewChecker(cfg, client, logger), nil
}

// WithPlatform overrides the GOOS/GOARCH used for asset selection. Returns the
// receiver for chaining.
func (c *Checker) WithPlatform(goos, goarch string) *Checker {
	c.goos = goos
	c.goarch = goarch
	return c
}

// Check performs a single update check against the GitHub releases/latest
// endpoint. A non-nil error means the check could not be completed (network
// failure, rate limit, malformed response); a successful check with no update
// returns Result{Available: false}, nil.
func (c *Checker) Check(ctx context.Context) (Result, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.baseURL, c.cfg.Owner, c.cfg.Repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return Result{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", githubAccept)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("request latest release: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return Result{}, c.classifyStatus(resp)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap (verbose release notes)
	if err != nil {
		return Result{}, fmt.Errorf("read release body: %w", err)
	}

	var rel githubRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return Result{}, fmt.Errorf("parse release JSON: %w", err)
	}

	return c.evaluate(rel)
}

// classifyStatus maps a non-200 GitHub response to a sentinel error.
func (c *Checker) classifyStatus(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusNotFound:
		return ErrNoRelease
	case http.StatusForbidden:
		// GitHub sets X-RateLimit-Remaining: 0 when the rate limit is hit.
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return ErrRateLimited
		}
		return ErrForbidden
	default:
		return fmt.Errorf("github API returned status %d", resp.StatusCode)
	}
}

// evaluate turns a parsed release into a Result: compares versions, honours the
// skipped-version gate, and selects the platform asset when an update exists.
func (c *Checker) evaluate(rel githubRelease) (Result, error) {
	res := Result{
		LatestVersion:  rel.TagName,
		CurrentVersion: c.cfg.CurrentVersion,
		ReleaseNotes:   rel.Body,
		PublishedAt:    rel.PublishedAt,
		HTMLURL:        rel.HTMLURL,
	}

	if !isUpdateAvailable(c.cfg.CurrentVersion, rel.TagName) {
		return res, nil
	}
	if isSkippedVersion(rel.TagName, c.cfg.SkippedVersion) {
		// The user explicitly dismissed this exact release.
		return res, nil
	}

	asset, err := SelectAsset(rel.Assets, c.goos, c.goarch)
	if err != nil {
		return res, fmt.Errorf("select asset for %s/%s: %w", c.goos, c.goarch, err)
	}

	res.Available = true
	res.AssetURL = asset.BrowserDownloadURL
	res.AssetName = asset.Name
	return res, nil
}

// githubRelease is the subset of the GitHub releases/latest payload we use.
type githubRelease struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	Body        string         `json:"body"`
	HTMLURL     string         `json:"html_url"`
	PublishedAt string         `json:"published_at"`
	Assets      []ReleaseAsset `json:"assets"`
}

// isUpdateAvailable reports whether latest is strictly newer than current.
//
// Both tags are normalised to the strict vMAJOR.MINOR.PATCH form that
// golang.org/x/mod/semver requires (see normalizeTag). A current version that
// is not valid semver (e.g. the "dev" default for local builds) is treated as
// "unknown", so any valid published release is offered.
func isUpdateAvailable(current, latest string) bool {
	cur := normalizeTag(current)
	lat := normalizeTag(latest)
	if !semver.IsValid(lat) {
		return false
	}
	if !semver.IsValid(cur) {
		// Dev/local build: cannot compare precisely → offer the release.
		return true
	}
	return semver.Compare(lat, cur) > 0
}

// isSkippedVersion reports whether latest matches the version the user chose to
// skip. Comparison uses canonical semver forms so "1.2.0" and "v1.2.0" are
// treated identically.
func isSkippedVersion(latest, skipped string) bool {
	if skipped == "" {
		return false
	}
	lat := semver.Canonical(normalizeTag(latest))
	skip := semver.Canonical(normalizeTag(skipped))
	return lat == skip
}

// normalizeTag converts a release tag to the strict vMAJOR.MINOR.PATCH form
// that golang.org/x/mod/semver requires: it adds the leading "v" when missing
// and pads numeric tags lacking the minor/patch components with ".0"
// (c0wrk release tags look like "v0.7-beta", which semver.IsValid rejects as
// "v0.7" has no patch). Non-numeric strings (e.g. the "dev" default for local
// builds) stay invalid semver and keep their "unknown version" semantics. The
// empty string is passed through unchanged.
func normalizeTag(s string) string {
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "v") {
		s = "v" + s
	}
	return padShortVersion(s)
}

// shortVersionTag matches numeric tags with optional minor/patch components
// and an optional pre-release ("-beta") or build ("+meta") suffix.
var shortVersionTag = regexp.MustCompile(`^v(\d+)(?:\.(\d+))?(?:\.(\d+))?([-+].*)?$`)

// padShortVersion expands a numeric tag missing the minor/patch components
// with ".0" so it satisfies strict semver ("v0.7-beta" → "v0.7.0-beta",
// "v1" → "v1.0.0"). Already-complete and non-numeric tags are returned
// unchanged.
func padShortVersion(v string) string {
	m := shortVersionTag.FindStringSubmatch(v)
	if m == nil {
		return v
	}
	major, minor, patch, suffix := m[1], m[2], m[3], m[4]
	if minor == "" {
		minor = "0"
	}
	if patch == "" {
		patch = "0"
	}
	return "v" + major + "." + minor + "." + patch + suffix
}

func defaultString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
