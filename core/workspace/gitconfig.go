package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// This file implements a safe, exec-free reader for a repository's .git/config
// (see the GitSpawn vulnerability class: a repo that arrives as files with its
// .git directory intact can carry command-bearing config keys that git executes
// on ordinary operations — status/diff/add/commit/merge — without any user
// interaction, because `git clone` deliberately does not transfer config but
// archive/USB/shared-drive distribution does).
//
// Scope (per the GitSpawn remediation plan): the INI subset under the
// command-relevant section families — core, attr, extensions (model-only:
// objectformat/worktreeConfig), filter.<name>, merge.<name>, diff.<name>.
// include/includeIf directives are parsed, logged and recorded but
// deliberately NOT followed by the key scanner: the included files' contents
// are unknown, so a config with includes must be treated as partially
// invisible. (ResolveIncludes is the one exception — it follows the directives
// for trust fingerprinting only, never for key neutralization; see its doc.)
// NeutralizingOverrides compensates on three fronts: per-name -c pins for
// every command-bearing driver key visible in the scanned config (a pinned
// driver is a fixed benign command no matter which routing source — the
// in-tree .gitattributes included — routes paths to it); core.attributesFile=
// (empty) for that routing source, plus the attribute-routing FILES git
// reads without any kill-switch — .git/info/attributes and the
// core.attributesFile target — which are themselves scanned and every routed
// driver name they contain is pinned the same way (-c overrides beat file
// config no matter where, including an included file, the driver is
// defined); and attr.tree (object-format aware) as the narrow fallback for
// the one case per-name pins cannot cover — include directives that may
// hide driver definitions whose names the scan never sees. An attribute
// source that exists but cannot be scanned makes the whole scan fail closed
// (see review-post-v0.7.3 finding [1]); the attr.tree fallback's collateral
// (benign eol/text attributes disabled) is documented with finding [56].
//
// Linked worktrees are scanned with merge semantics ([2] of the same
// review): the worktree gitdir's commondir file resolves the common dir,
// whose config — plus the optional config.worktree overlay when
// extensions.worktreeConfig is enabled — is exactly what git reads there.
//
// The network-transport keys the Git panel's remote RPCs (pull/push/fetch)
// reach — core.sshCommand, core.askPass, core.gitProxy, credential.helper
// and credential.<url>.helper — and the external diff drivers
// (diff.external, diff.<n>.command), which plain `git diff` executes by
// default, are in scope as well with per-key neutralizations
// (post-v0.7.3 review findings [40]/[55]; every value canary-verified on
// git 2.50.1). core.gitProxy has no neutralizable value of its own (empty
// included) and is killed through the transport family instead
// (-c protocol.git.allow=never); core.worktree redirects tracked-file
// writes outside the workspace and no -c form beats it, so it is
// neutralized by the GIT_WORK_TREE env pin GitCmdInRepo applies when the
// finding is present (the one channel that outranks the config key). The
// only command-bearing core key left without any override is core.pager:
// c0wrk always pipes git output and a pager only runs on a terminal.
// Include-bearing configs additionally derive name-independent pins
// (core.sshCommand=ssh, core.askPass=, credential.helper=, diff.external=)
// for the keys an included file may set invisibly, and GitCmdInRepo fails
// closed for them on git older than 2.45, where attr.tree — the only cover
// for include-hidden attribute-routed drivers — is silently ignored.
//
// Out of scope, with rationale: commit.gpgsign / gpg.program (signing vectors)
// are neutralized unconditionally by the sysproc.GitCmd baseline
// (-c commit.gpgsign=false + GIT_EDITOR=true, see step_2 of the remediation),
// so per-repo detection adds nothing actionable for c0wrk's local operation
// set. Findings for baseline-covered keys are still reported (fsmonitor,
// hooksPath, editor) with BaselineCovered=true so the intake scanner shows the
// full picture from this single source.
//
// Neutralization values are not invented here — every emitted override comes
// from the canary-verified semantics of the GitSpawn neutralization study
// (git 2.50.1): attr.tree=<empty tree> kills ALL attribute-routed vectors;
// filter.<n>.process= (empty) plus clean=cat/smudge=cat disarms a named filter
// (process= alone would fall back to attacker-supplied clean/smudge);
// merge.<n>.driver="false %O %A %B" resolves conflicts without executing the
// attacker driver; diff.<n>.textconv=cat renders diffs as passthrough.
//
// Guarantees of the parser: it never spawns a process, never follows symlinks
// beyond opening the single config file, caps the input size, never panics on
// malformed input, and over-reports rather than under-reports on constructs it
// is lenient about (git itself refuses malformed configs wholesale, so a
// construct we mis-read is one git would not execute either — and every
// malformed line is recorded in Errors so callers can fail closed).

// EmptyTreeSHA1 is the SHA-1 of the empty git tree. Forcing
// -c attr.tree=<EmptyTreeSHA1> makes git read .gitattributes from the empty
// tree instead of the worktree, which neutralizes every attribute-routed
// command vector (clean/smudge/process filters, merge drivers, textconv,
// external diff drivers). Verified: attr.tree="" (empty) is NOT safe, and
// invalid hashes are silent no-ops — only this exact hash (or the SHA-256
// equivalent, see EmptyTreeSHA256) may be used.
const EmptyTreeSHA1 = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// EmptyTreeSHA256 is the SHA-256 of the empty git tree — the attr.tree value
// that neutralizes attribute routing on repositories created with
// extensions.objectformat=sha256. Verified against git 2.50.1: the SHA-1
// constant is a silent no-op there (invalid hash → no attr.tree behavior at
// all), while this hash kills worktree .gitattributes routing exactly like
// EmptyTreeSHA1 does on SHA-1 repositories. ScanGitConfig selects between
// the two via the repository's extensions.objectformat (fail-closed when the
// format is unknown — see [1]d of the post-v0.7.3 review).
const EmptyTreeSHA256 = "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"

// maxGitConfigBytes caps how much of a .git/config (or .git gitdir pointer
// file) is read. Real-world configs are a few KiB; anything larger is hostile
// input, not configuration to be parsed.
const maxGitConfigBytes = 4 << 20 // 4 MiB

const maxGitDirPointerBytes = 4096

// Include-resolution bounds for ResolveIncludes: a hostile repo can chain
// includes arbitrarily deep or wide. The depth cap, the total file count cap,
// and the per-file size cap (maxGitConfigBytes, shared with the primary
// config read) together bound the work so a trust/recheck scan can never be
// driven into unbounded I/O. These are deliberately generous — real include
// trees are shallow and small — while still terminating on adversarial input.
const (
	maxIncludeDepth = 10
	maxIncludeFiles = 64
)

// Finding kinds. These strings are stable identifiers intended for UI
// rendering by the intake scanner.
const (
	GitConfigFindingFSMonitor      = "fsmonitor"          // core.fsmonitor / core.fsmonitorHook
	GitConfigFindingHooksPath      = "hooks_path"         // core.hooksPath
	GitConfigFindingEditor         = "editor"             // core.editor
	GitConfigFindingPager          = "pager"              // core.pager
	GitConfigFindingSSHCommand     = "ssh_command"        // core.sshCommand
	GitConfigFindingAskPass        = "askpass"            // core.askPass
	GitConfigFindingGitProxy       = "git_proxy"          // core.gitProxy
	GitConfigFindingWorkTree       = "worktree"           // core.worktree
	GitConfigFindingAttrTree       = "attr_tree"          // attr.tree
	GitConfigFindingFilter         = "filter_command"     // filter.<n>.process|clean|smudge
	GitConfigFindingMergeDriver    = "merge_driver"       // merge.<n>.driver
	GitConfigFindingTextConv       = "textconv"           // diff.<n>.textconv
	GitConfigFindingDiffCommand    = "diff_command"       // diff.<n>.command / diff.external
	GitConfigFindingAttributesFile = "attributes_file"    // core.attributesFile
	GitConfigFindingAttrRouting    = "attributes_routing" // filter/merge/diff routing in .git/info/attributes or core.attributesFile
	GitConfigFindingCredential     = "credential_helper"  // credential.helper / credential.<url>.helper
)

// Verified neutralizing override values (GitSpawn neutralization study).
const (
	// neutralMergeDriverValue resolves a conflict without running the
	// attacker's driver: git records a real conflict (UU, worktree keeps
	// our version) and the driver command executes `false`.
	neutralMergeDriverValue = "false %O %A %B"
	// neutralPassthroughValue is a benign passthrough command for
	// textconv / clean / smudge fallbacks.
	neutralPassthroughValue = "cat"
)

const attrTreeKey = "attr.tree"

// gitProxyAllowKey is the protocol-allowlist key that gates the git://
// transport family core.gitProxy hijacks. Unlike the driver keys there is
// no way to neutralize the proxy command itself (an empty core.gitProxy
// falls back to the environment/git's default proxy handling, not to
// "none"), but `-c protocol.git.allow=never` kills the whole transport
// (verified on git 2.50.1: `git fetch` fails with "transport 'git' not
// allowed" and the armed proxy never executes). ext:: and other exotic
// transports are already blocked by git's own defaults.
const gitProxyAllowKey = "protocol.git.allow"

// gitProxyDenyValue is the only verified "no transport of this family may
// be used at all" value for gitProxyAllowKey.
const gitProxyDenyValue = "never"

// attributesFileKey is the config key git reads an additional attributes
// routing file from (core.attributesFile). Unlike hooks or fsmonitor it has
// no dedicated "off" switch, but an EMPTY value set via -c disables the
// source entirely (verified against git 2.50.1) — and -c beats the key no
// matter which file (config, included file) defined it.
const attributesFileKey = "core.attributesFile"

// GitConfigOverride is a single neutralizing `-c key=value` override for one
// command-bearing key discovered in a repository config.
type GitConfigOverride struct {
	Key   string // full lowercase config key, e.g. "filter.lfs.process"
	Value string // neutralizing value, e.g. "" or "cat"
}

// Argv renders the override as the single argv element that follows a `-c`
// flag ("key=value"). An empty Value renders as "key=" (empty-string value),
// which is exactly how an armed process filter is disarmed.
func (o GitConfigOverride) Argv() string {
	return o.Key + "=" + o.Value
}

// GitConfigFinding describes one dangerous key occurrence discovered in a
// repository's .git/config. It is the single source of truth for the intake
// scanner: every command-bearing key parsed from the in-scope section families
// produces a finding, whether or not it needs a per-repo override.
type GitConfigFinding struct {
	// Kind classifies the vector; one of the GitConfigFinding* constants.
	Kind string
	// Section is the lowercased section name ("core", "filter", ...).
	Section string
	// Subsection is the verbatim subsection name for [filter "lfs"]-style
	// headers (case-sensitive, as git treats them); empty for plain sections.
	Subsection string
	// Key is the lowercased key name within the section ("process", ...).
	Key string
	// FullKey is the complete dotted key ("filter.lfs.process").
	FullKey string
	// Value is the parsed value ("" for bare boolean keys, with Boolean=true).
	Value string
	// Boolean reports a bare key (no '='), which git reads as boolean true.
	Boolean bool
	// Line is the 1-based line number of the occurrence.
	Line int
	// Description is a human-readable explanation of what the key does, its
	// reachability from c0wrk's git usage, and how it is neutralized.
	Description string
	// BaselineCovered reports that the key is already neutralized on every
	// c0wrk git invocation by the unconditional sysproc.GitCmd baseline
	// (-c core.fsmonitor=false, -c core.hooksPath=<safe dir>,
	// GIT_EDITOR=true), so it needs no per-repo override.
	BaselineCovered bool
	// Overrides are the verified per-repo `-c` neutralizations for this
	// finding. Empty when none is needed or none is verified (the
	// description explains which case applies).
	Overrides []GitConfigOverride
}

// GitConfigInclude records an include/includeIf directive. The referenced file
// is deliberately not read: an included config can define additional
// command-bearing keys, so any recorded include means the visible config is an
// incomplete view and the workspace must be treated with corresponding
// suspicion (NeutralizingOverrides compensates with the attribute-routing
// kill, but unknown filter names remain unneutralizable — this is a
// detection-grade signal, not a mitigation).
type GitConfigInclude struct {
	Conditional bool   // true for includeIf
	Condition   string // includeIf condition (e.g. "gitdir:~/x/**"); "" for include
	Path        string // unexpanded path value as written
	Line        int    // 1-based line number
	// SourceDir is the absolute directory of the config file that contained
	// the directive (git resolves a relative include path against it). Empty
	// for results produced by the pure parser (parseGitConfigData), which has
	// no file of its own; ScanGitConfigFile fills it.
	SourceDir string
}

// GitConfigError is a malformed construct. git itself refuses to run with a
// malformed config ("fatal: bad config line N"), so Errors non-empty means the
// file would fail git wholesale — but callers scanning untrusted workspaces
// should still fail closed.
type GitConfigError struct {
	Line    int // 1-based line number
	Message string
}

// GitConfigInfo is the full result of parsing one .git/config.
type GitConfigInfo struct {
	// GitDir is the resolved git directory the config belongs to ("" when no
	// .git was found). For a linked worktree this is the per-worktree
	// directory (<main>/.git/worktrees/<name>); the shared repository state
	// lives in CommonDir.
	GitDir string
	// CommonDir is the shared git directory (the target of the worktree's
	// commondir file). It equals GitDir for normal repositories and is ""
	// when no .git was found at all.
	CommonDir string
	// ConfigPath is the config file that was parsed ("" when none was found).
	// With worktree merge semantics this is the common config; a parsed
	// config.worktree overlay is reported in WorktreeConfigPath.
	ConfigPath string
	// WorktreeConfigPath is the per-worktree config.worktree overlay that was
	// parsed and merged on top of the common config ("" when absent or when
	// extensions.worktreeConfig is disabled, in which case git ignores the
	// file entirely).
	WorktreeConfigPath string
	// ObjectFormat is the repository's object format resolved from
	// core.repositoryformatversion + extensions.objectformat ("sha1" or
	// "sha256"). It selects the empty-tree constant used for the attr.tree
	// kill (see EmptyTreeSHA1 / EmptyTreeSHA256). Empty until the full
	// ScanGitConfig pipeline has validated the format.
	ObjectFormat string
	// Findings lists every dangerous key occurrence, in file order (merged
	// worktree-overlay last-wins for identical keys).
	Findings []GitConfigFinding
	// Includes lists include/includeIf directives (parsed, logged, not
	// followed).
	Includes []GitConfigInclude
	// Errors lists malformed constructs (the parser continues past them).
	Errors []GitConfigError

	// --- parser-captured state (not dangerous by itself, needed to model
	// how git itself reads the repository; merged with last-wins semantics
	// across config layers) ---

	// repositoryFormatVersion is core.repositoryformatversion (default 0).
	repositoryFormatVersion int64
	// objectFormat is the lowercased extensions.objectformat ("" when unset).
	objectFormat string
	// worktreeConfigEnabled reports extensions.worktreeConfig=true, the
	// switch that makes git read config.worktree in linked worktrees.
	worktreeConfigEnabled bool
	// attributesFilePath is the last core.attributesFile value ("", or the
	// verbatim path before ~/ and relative resolution).
	attributesFilePath string

	// rawSources captures the raw bytes of every source the scan read, in a
	// stable order (common config, config.worktree overlay, then the
	// attribute routing sources). Snapshot/Fingerprint serialize these into
	// a canonical diff-able identity for trust-time snapshots.
	rawSources []gitConfigSource

	// includeSources captures the raw bytes of every include/includeIf
	// target resolved by ResolveIncludes, in a stable, deterministic order
	// (directive order, depth-first). It is populated lazily — only the
	// trust/recheck path calls ResolveIncludes — so the per-invocation spawn
	// scan never pays for following includes (ADR-033 rejects following them
	// there). Snapshot/Fingerprint serialize these after rawSources, binding
	// the trust decision to the included files' bytes as well.
	includeSources []gitConfigSource
}

// gitConfigSource is one raw config/routing source read by the scan, kept for
// Snapshot/Fingerprint so a trust decision can be bound to the exact bytes
// the user reviewed rather than re-read (and re-race) the filesystem later.
type gitConfigSource struct {
	// kind is a stable short label for the diff header ("config",
	// "config.worktree", "info/attributes", "core.attributesFile").
	kind string
	// path is the absolute path of the source (may be "" for synthetic).
	path string
	// data is the raw bytes read from the source.
	data []byte
}

// Clean reports that the config is fully visible (no include directives, no
// parse errors) and carries no dangerous keys. Only a Clean result allows the
// caller to skip per-repo neutralization without suspicion.
func (info *GitConfigInfo) Clean() bool {
	if info == nil {
		return true
	}
	return len(info.Findings) == 0 && len(info.Includes) == 0 && len(info.Errors) == 0
}

// Snapshot returns a canonical, diff-able byte representation of every source
// the scan read — the common config, the config.worktree overlay, and the
// attribute routing sources (info/attributes plus any core.attributesFile
// target), followed by any include/includeIf targets resolved by
// ResolveIncludes. Each source is prefixed with a stable header naming its
// kind and path, so DiffGitConfigSnapshots can attribute a change to the
// right file. A nil info yields nil. The representation is deterministic for
// a given set of sources and is what Fingerprint hashes.
func (info *GitConfigInfo) Snapshot() []byte {
	if info == nil {
		return nil
	}
	var b strings.Builder
	for _, src := range info.rawSources {
		writeSnapshotSource(&b, src)
	}
	for _, src := range info.includeSources {
		writeSnapshotSource(&b, src)
	}
	return []byte(b.String())
}

// writeSnapshotSource serializes one source under a stable "===== kind (path)
// =====" header, ensuring a trailing newline so the next header always starts
// on its own line.
func writeSnapshotSource(b *strings.Builder, src gitConfigSource) {
	fmt.Fprintf(b, "===== %s (%s) =====\n", src.kind, src.path)
	b.Write(src.data)
	if len(src.data) == 0 || src.data[len(src.data)-1] != '\n' {
		b.WriteByte('\n')
	}
}

// Fingerprint returns the SHA-256 hex digest of Snapshot(): a stable identity
// for the exact configuration the user reviewed at trust time. Comparing the
// fingerprint of a later scan against the stored value detects any drift in
// the config, its worktree overlay, the attribute routing sources, or any
// resolved include target.
func (info *GitConfigInfo) Fingerprint() string {
	sum := sha256.Sum256(info.Snapshot())
	return hex.EncodeToString(sum[:])
}

// DiffGitConfigSnapshots returns a human-readable unified diff between two
// snapshots (the previously-trusted state and the current scan). It is "" when
// the two snapshots are byte-identical. The diff is line-based over the whole
// canonical snapshot text, so it naturally covers the config, the worktree
// overlay, and the attribute routing sources in one stream.
func DiffGitConfigSnapshots(previous, current []byte) string {
	if bytes.Equal(previous, current) {
		return ""
	}
	a := strings.Split(string(previous), "\n")
	b := strings.Split(string(current), "\n")
	return unifiedDiff(a, b)
}

// ResolveIncludes follows the config's include/includeIf directives for
// fingerprinting only, reading each target's raw bytes into includeSources so
// Snapshot/Fingerprint bind the trust decision to the included files too.
//
// Scope: unlike the key scanner, which deliberately never follows includes
// (ADR-033 — an included file's command-bearing keys stay invisible and are
// compensated by the attr.tree kill), this reads included files purely to
// fingerprint their bytes, closing the include-hidden drift gap: a post-trust
// change to any file git reads revokes the trust. It never parses an included
// file for dangerous keys, never changes NeutralizingOverrides, and is called
// only from the trust/recheck RPCs — the per-invocation spawn scan stays free
// of include-following (a hostile repo cannot amplify the hot path's I/O).
//
// Condition handling is deliberately conservative (fail-closed): both include
// and includeIf targets are fingerprinted regardless of their condition. An
// includeIf whose condition is currently false still contributes its bytes, so
// a change to it revokes trust — over-reporting in the safe direction, exactly
// matching the parser's over-report-rather-than-under-report guarantee. This
// avoids reimplementing git's wildmatch() for the gitdir/onbranch/hasconfig
// conditions, a wrong implementation of which would SKIP a file git actually
// reads (the dangerous direction).
//
// Bounds and failure handling: recursion is depth-limited (maxIncludeDepth),
// the total file count is limited (maxIncludeFiles), and each file is size-
// capped (maxGitConfigBytes) and must be a regular file (openRegularFile
// refuses FIFOs/devices without blocking). A missing target contributes an
// empty source so its later appearance changes the fingerprint; an unreadable,
// oversized, or non-regular target contributes a fixed marker source. None of
// these is an error — a repo with an unreadable include remains scannable and
// trustable, and any later change to that target's state still shows as drift.
func (info *GitConfigInfo) ResolveIncludes(loggers ...*slog.Logger) {
	if info == nil {
		return
	}
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	r := &includeResolver{
		logger:  logger,
		visited: make(map[string]bool, len(info.Includes)),
		info:    info,
	}
	r.resolveIncludes(info.Includes, 0)
}

// includeResolver walks include directives depth-first, reading targets and
// recursing into an included file's own include records, while bounding depth,
// file count, and per-file size and breaking cycles via a visited set.
type includeResolver struct {
	logger  *slog.Logger
	visited map[string]bool // absolute resolved target paths already handled
	count   int             // total distinct targets read
	info    *GitConfigInfo  // top-level info whose includeSources accumulates
}

func (r *includeResolver) resolveIncludes(includes []GitConfigInclude, depth int) {
	for i := range includes {
		r.read(&includes[i], depth)
	}
}

func (r *includeResolver) read(inc *GitConfigInclude, depth int) {
	if depth >= maxIncludeDepth {
		r.logger.Warn("git config include resolution hit the depth limit; nested includes are not fingerprinted",
			"path", inc.Path, "line", inc.Line)
		return
	}
	if r.count >= maxIncludeFiles {
		r.logger.Warn("git config include resolution hit the file-count limit; further includes are not fingerprinted",
			"path", inc.Path)
		return
	}
	target := resolveIncludePath(inc.Path, inc.SourceDir)
	if target == "" {
		return
	}
	if r.visited[target] {
		return
	}
	r.visited[target] = true
	r.count++

	data, err := readCapped(target, maxGitConfigBytes)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// A missing include is inert (git ignores it); record an empty
		// source so its later appearance still changes the fingerprint.
		r.info.includeSources = append(r.info.includeSources, gitConfigSource{kind: "include", path: target})
		return
	case err != nil:
		// Unreadable, oversized, or non-regular: record a marker so a
		// change to the target's state still shows as drift, without making
		// the repo untrustable.
		r.logger.Warn("git config include target could not be fingerprinted", "path", target, "error", err)
		r.info.includeSources = append(r.info.includeSources, gitConfigSource{kind: "include (unreadable)", path: target})
		return
	}

	r.info.includeSources = append(r.info.includeSources, gitConfigSource{kind: "include", path: target, data: data})

	// Recurse into the included file's own include directives so a transitive
	// change anywhere in the tree revokes trust. The included file is parsed
	// only for its include records, never for dangerous keys.
	sub := parseGitConfigData(string(data), r.logger)
	subDir := filepath.Dir(target)
	for i := range sub.Includes {
		sub.Includes[i].SourceDir = subDir
	}
	r.resolveIncludes(sub.Includes, depth+1)
}

// resolveIncludePath resolves an include directive's path value the way git
// does for include.path/includeIf.<condition>.path: a leading ~/ expands
// against the home directory, and a relative path resolves against the
// directory of the config file that contained the directive. Returns "" for a
// blank value; the result is always filepath.Clean-ed.
func resolveIncludePath(path, sourceDir string) string {
	value := strings.TrimSpace(path)
	if value == "" {
		return ""
	}
	value = expandHomeTilde(value)
	if !filepath.IsAbs(value) && sourceDir != "" {
		value = filepath.Join(sourceDir, value)
	}
	return filepath.Clean(value)
}

// expandHomeTilde expands a leading "~" or "~/" to the current user's home
// directory. Unresolvable homes (and the "~user/" form, which requires a user
// database lookup) are left verbatim — the target simply will not exist, which
// is a safe no-op for fingerprinting. Mirrors git's tilde expansion for the
// current user only.
func expandHomeTilde(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
		return path
	}
	return path
}

// diffOp is one operation in a line-level edit script.
type diffOp struct {
	kind diffKind
	text string
}

type diffKind uint8

const (
	diffEqual diffKind = iota
	diffDelete
	diffInsert
)

// maxLCSCells bounds the O(mn) LCS table for the minimal diff. Git configs are
// small (tens to hundreds of lines); beyond this the fallback
// common-prefix/suffix diff keeps the operation memory-safe and still correct.
const maxLCSCells = 1_000_000

// maxDiffBytes caps the emitted diff so a pathological config change can never
// turn the warning payload into a megabyte blob.
const maxDiffBytes = 32 << 10

// unifiedDiff renders a unified diff (context 3, truncated to maxDiffBytes)
// between two line slices.
func unifiedDiff(a, b []string) string {
	var ops []diffOp
	if len(a)*len(b) <= maxLCSCells {
		ops = lcsDiffOps(a, b)
	} else {
		ops = trimDiffOps(a, b)
	}
	diff := renderUnifiedDiff(ops)
	if len(diff) > maxDiffBytes {
		diff = diff[:maxDiffBytes] + "\n... (diff truncated)\n"
	}
	return diff
}

// lcsDiffOps builds a minimal edit script via a longest-common-subsequence
// dynamic program. O(mn) time and memory — bounded by the caller (maxLCSCells).
func lcsDiffOps(a, b []string) []diffOp {
	m, n := len(a), len(b)
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	ops := make([]diffOp, 0, m+n)
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{diffEqual, a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{diffDelete, a[i]})
			i++
		default:
			ops = append(ops, diffOp{diffInsert, b[j]})
			j++
		}
	}
	for ; i < m; i++ {
		ops = append(ops, diffOp{diffDelete, a[i]})
	}
	for ; j < n; j++ {
		ops = append(ops, diffOp{diffInsert, b[j]})
	}
	return ops
}

// trimDiffOps is the memory-safe fallback for very large inputs: it keeps the
// common prefix and suffix and treats everything in between as deleted+added.
// Coarser than the LCS diff but always correct.
func trimDiffOps(a, b []string) []diffOp {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	ops := make([]diffOp, 0, len(a)+len(b))
	for i := 0; i < p; i++ {
		ops = append(ops, diffOp{diffEqual, a[i]})
	}
	for i := p; i < len(a)-s; i++ {
		ops = append(ops, diffOp{diffDelete, a[i]})
	}
	for j := p; j < len(b)-s; j++ {
		ops = append(ops, diffOp{diffInsert, b[j]})
	}
	for i := len(a) - s; i < len(a); i++ {
		ops = append(ops, diffOp{diffEqual, a[i]})
	}
	return ops
}

// renderUnifiedDiff groups an edit script into hunks (3 lines of context) and
// renders them as a unified diff with @@ range headers.
func renderUnifiedDiff(ops []diffOp) string {
	const ctx = 3
	changed := make([]bool, len(ops))
	for i, op := range ops {
		changed[i] = op.kind != diffEqual
	}
	var out strings.Builder
	out.WriteString("--- previous\n+++ current\n")
	for i := 0; i < len(ops); {
		if !changed[i] {
			i++
			continue
		}
		start, back := i, 0
		for start > 0 && ops[start-1].kind == diffEqual && back < ctx {
			start--
			back++
		}
		end := i
		for end < len(ops) && ops[end].kind != diffEqual {
			end++
		}
		for fwd := 0; fwd < ctx && end < len(ops) && ops[end].kind == diffEqual; fwd++ {
			end++
		}
		oldStart := lineNumberBefore(ops, start, true)
		newStart := lineNumberBefore(ops, start, false)
		oldCount, newCount := 0, 0
		for k := start; k < end; k++ {
			switch ops[k].kind {
			case diffInsert:
				newCount++
			case diffDelete:
				oldCount++
			default:
				oldCount++
				newCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for k := start; k < end; k++ {
			switch ops[k].kind {
			case diffDelete:
				out.WriteString("-" + ops[k].text + "\n")
			case diffInsert:
				out.WriteString("+" + ops[k].text + "\n")
			default:
				out.WriteString(" " + ops[k].text + "\n")
			}
		}
		i = end
	}
	return out.String()
}

// lineNumberBefore returns the 1-based line number (in the old or new file,
// per isOld) of the line at ops[pos]. Delete lines count only toward the old
// numbering, insert lines only toward the new; equal lines count toward both.
func lineNumberBefore(ops []diffOp, pos int, isOld bool) int {
	n := 1
	for k := 0; k < pos; k++ {
		switch ops[k].kind {
		case diffDelete:
			if isOld {
				n++
			}
		case diffInsert:
			if !isOld {
				n++
			}
		default:
			n++
		}
	}
	return n
}

// NeutralizingOverrides derives the per-repo neutralizing `-c` set from the
// findings — the Layer-2 additions that go on top of the unconditional
// sysproc.GitCmd baseline when running git inside this repository:
//
//   - one filter.<n>.process= + filter.<n>.clean=cat + filter.<n>.smudge=cat
//     triple per armed filter name (process= alone would fall back to
//     attacker-supplied clean/smudge),
//
//   - merge.<n>.driver="false %O %A %B" per custom merge driver,
//
//   - diff.<n>.textconv=cat per armed textconv,
//
//   - attr.tree=<empty tree> ONLY when include directives were recorded
//     (post-v0.7.3 review [56]): a per-name pin neutralizes every KNOWN
//     driver wherever it is routed from — the in-tree .gitattributes source
//     included — so the blanket kill is not needed for visible keys, and it
//     disables benign attributes too (eol/text normalization), making CRLF
//     repositories report falsely-modified files and whole-file numstat
//     diffs (empirically confirmed on git 2.50.1 with a `* text=auto`
//     repository). Includes are the one case per-name pins cannot cover: an
//     included file may define a filter whose name the scan never sees,
//     routed from the in-tree .gitattributes — only attr.tree kills that
//     source (the info/attributes and core.attributesFile sources are
//     scanned and pinned instead). The empty-tree constant matches the
//     repository's object format (SHA-1 or SHA-256); on SHA-256 repositories
//     the SHA-1 hash is a silent no-op, so ScanGitConfig resolves
//     ObjectFormat (fail-closed) and this derivation selects EmptyTreeSHA256
//     accordingly.
//
//   - core.attributesFile= (empty) whenever the key is set or any include
//     directive was recorded: attr.tree does NOT cover this routing source,
//     and an empty value disables it (verified: git 2.50.1 respects
//     core.attributesFile from repository config, and the -c override beats
//     it no matter which file — including an included one — set it).
//
//   - for every filter/merge/diff routing discovered in .git/info/attributes
//     or the core.attributesFile file (sources attr.tree does not cover),
//     the same per-name neutralization as for visible config keys: the -c
//     overrides beat file config regardless of where the routed driver is
//     defined — including an included file the scanner cannot read.
//
//   - diff.external= (empty) per configured default external diff command:
//     plain `git diff` executes it BY DEFAULT — --ext-diff is NOT required
//     and --no-ext-diff is what disables it (verified on git 2.50.1). The
//     empty kill is fail-closed: an invocation without --no-ext-diff errors
//     instead of executing the armed value; c0wrk's patch-producing diff
//     call sites pass --no-ext-diff, so their output stays usable.
//
//   - diff.<n>.command= (empty) per named external diff driver, which the
//     git diff porcelain likewise executes by default for paths routed to
//     it via the diff attribute. The per-key empty kill is the whole story:
//     -c beats file config wherever the driver is defined, so every routing
//     source — in-tree .gitattributes, info/attributes,
//     core.attributesFile — is closed without attr.tree ([56]).
//
//   - the transport keys the Git panel's remote RPCs reach:
//     core.sshCommand=ssh restores the default ssh binary for ssh://
//     remotes (an empty value aborts the fetch with "cannot run :" —
//     "ssh" keeps remote operations working); core.askPass= disables the
//     askpass helper; credential.helper= (empty) resets the accumulated
//     helper list — an empty value resets, and the -c layer is read after
//     every file, so the reset lands after all file-configured helpers —
//     plus a per-URL credential.<url>.helper= pin as belt-and-braces
//     against url-match normalization subtleties.
//
// Only core.pager and core.worktree produce no -c override: the pager is
// never spawned on a terminal (c0wrk always pipes git output), and the
// worktree key cannot be beaten by -c at all — GitCmdInRepo neutralizes it
// through the GIT_WORK_TREE env pin when the finding is present. The
// result is deduplicated and sorted by key for deterministic output; nil
// for a clean config.
func (info *GitConfigInfo) NeutralizingOverrides() []GitConfigOverride {
	if info == nil {
		return nil
	}
	set := map[string]string{}
	filterNames := map[string]bool{}
	for i := range info.Findings {
		f := &info.Findings[i]
		switch f.Kind {
		case GitConfigFindingFilter:
			// Narrow mode ([56]): the per-name pins below neutralize the
			// driver wherever it is routed from — the in-tree .gitattributes
			// source included — so no blanket attr.tree kill is derived for
			// visible filters (it would disable benign eol/text attributes
			// too; see the attr.tree bullet above).
			filterNames[f.Subsection] = true
		case GitConfigFindingMergeDriver:
			set["merge."+f.Subsection+".driver"] = neutralMergeDriverValue
		case GitConfigFindingTextConv:
			set["diff."+f.Subsection+".textconv"] = neutralPassthroughValue
		case GitConfigFindingDiffCommand:
			// External diff drivers execute on plain `git diff` by default
			// (verified on git 2.50.1: --ext-diff is NOT what gates them —
			// --no-ext-diff is what disables them). diff.external is
			// config-driven, not attribute-routed, so the per-key empty
			// kill is its whole story; named diff.<n>.command drivers are
			// likewise fully covered by the per-key empty kill: the -c
			// override beats file config wherever the driver is defined,
			// so every routing source is closed without attr.tree ([56]).
			if f.Subsection == "" {
				set["diff.external"] = ""
			} else {
				set["diff."+f.Subsection+".command"] = ""
			}
		case GitConfigFindingSSHCommand:
			// Verified (git 2.50.1): "ssh" restores the default binary and
			// a fetch proceeds (connection attempts come from the real
			// ssh); an empty value aborts with "cannot run :" instead.
			set["core.sshCommand"] = "ssh"
		case GitConfigFindingAskPass:
			set["core.askPass"] = ""
		case GitConfigFindingGitProxy:
			// The proxy command itself cannot be neutralized (any value,
			// empty included, still resolves to a command path); the
			// verified kill is the transport-family allowlist entry, which
			// makes the git:// operation fail closed instead.
			set[gitProxyAllowKey] = gitProxyDenyValue
		case GitConfigFindingCredential:
			// An empty value resets the accumulated helper list and the
			// -c layer is read after every file, so the reset covers
			// generic and URL-specific helpers alike; the per-URL pin is
			// belt-and-braces against url-match normalization subtleties.
			set["credential.helper"] = ""
			if f.Subsection != "" {
				set["credential."+f.Subsection+".helper"] = ""
			}
		case GitConfigFindingAttrTree:
			set[attrTreeKey] = info.emptyTreeHash()
		case GitConfigFindingAttributesFile:
			// Verified (git 2.50.1): an empty value disables the
			// core.attributesFile routing source entirely.
			set[attributesFileKey] = ""
		case GitConfigFindingAttrRouting:
			// Routing sources attr.tree cannot cover are closed by pinning
			// the routed name: -c overrides beat file config wherever the
			// driver is defined (config, included file, or nowhere).
			switch f.Key {
			case "filter":
				filterNames[f.Subsection] = true
			case "merge":
				set["merge."+f.Subsection+".driver"] = neutralMergeDriverValue
			case "diff":
				set["diff."+f.Subsection+".textconv"] = neutralPassthroughValue
				set["diff."+f.Subsection+".command"] = ""
			}
		}
	}
	for name := range filterNames {
		set["filter."+name+".process"] = ""
		set["filter."+name+".clean"] = neutralPassthroughValue
		set["filter."+name+".smudge"] = neutralPassthroughValue
	}
	if len(info.Includes) > 0 {
		// The only case where driver names may be unknown to the scan: an
		// included file can define a filter whose name the scanner never
		// sees, routed from the in-tree .gitattributes — a routing source
		// with no per-name pin possible. The blanket kill stays engaged for
		// it, with the documented benign-attribute collateral (review [56]:
		// CRLF/eol normalization off → falsely-modified statuses and
		// inflated numstat on text-normalized repositories).
		set[attrTreeKey] = info.emptyTreeHash()
	}
	if len(info.Includes) > 0 {
		// The config is not fully visible: an included file may set
		// core.attributesFile where the scan cannot see it. The empty -c
		// override beats every file-level definition of the key.
		set[attributesFileKey] = ""
		// Name-independent kills for the command-bearing keys an included
		// file may arm invisibly (post-v0.7.3-style reachability): the
		// values reuse exactly what the finding-driven path pins for each
		// key. Deliberately NOT unconditional — a -c pin beats the user's
		// own global config too, so these fire only for include-bearing
		// configs, where the alternative is executing unseen commands.
		// credential.helper= (empty) resets the accumulated list and
		// covers URL-specific helpers set in included files as well.
		set["core.sshCommand"] = "ssh"
		set["core.askPass"] = ""
		set["credential.helper"] = ""
		set["diff.external"] = ""
	}
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]GitConfigOverride, 0, len(keys))
	for _, k := range keys {
		out = append(out, GitConfigOverride{Key: k, Value: set[k]})
	}
	return out
}

// AttributesInterpretationDisabled reports whether the derived neutralizing
// set engages the blanket attr.tree kill — the state where benign attributes
// (eol/text normalization) stop applying for c0wrk's git operations (the
// review-[56] collateral: CRLF repositories show falsely-modified files and
// whole-file numstat diffs). In narrow mode this is false: visible drivers
// are pinned by name and attributes keep working. True only while include
// directives make driver names unknowable, or the repository's own config
// sets attr.tree (which the override then pins to the empty tree).
func (info *GitConfigInfo) AttributesInterpretationDisabled() bool {
	if info == nil {
		return false
	}
	for _, o := range info.NeutralizingOverrides() {
		if o.Key == attrTreeKey {
			return true
		}
	}
	return false
}

// NeedsWorkTreeEnvPin reports whether the scanned config carries a
// core.worktree finding — the one vector no `-c` override can neutralize
// (verified on git 2.50.1: both a value and an empty core.worktree lose to
// the file config). GitCmdInRepo consults this to pin the spawn
// environment's GIT_WORK_TREE to the discovered repository root instead,
// the one channel that outranks the config key. Deliberately finding-gated:
// a blanket pin would impose our discovery on every repository, breaking
// legitimate core.worktree-using setups; only an attacker-observable
// occurrence in the scanned config derives the pin.
func (info *GitConfigInfo) NeedsWorkTreeEnvPin() bool {
	if info == nil {
		return false
	}
	for i := range info.Findings {
		if info.Findings[i].Kind == GitConfigFindingWorkTree {
			return true
		}
	}
	return false
}

// emptyTreeHash returns the empty-tree hash matching the repository's object
// format: EmptyTreeSHA256 when extensions.objectformat=sha256 was parsed or
// resolved, EmptyTreeSHA1 otherwise. Invalid hashes are silent no-ops for
// git, so the full ScanGitConfig pipeline refuses unknown formats (fail
// closed) rather than falling back silently.
func (info *GitConfigInfo) emptyTreeHash() string {
	if info.ObjectFormat == "sha256" || info.objectFormat == "sha256" {
		return EmptyTreeSHA256
	}
	return EmptyTreeSHA1
}

// NeutralizingArgv renders NeutralizingOverrides as flat "-c", "key=value"
// argv elements, ready to append to the arguments of a sysproc.GitCmd call.
func (info *GitConfigInfo) NeutralizingArgv() []string {
	overrides := info.NeutralizingOverrides()
	if len(overrides) == 0 {
		return nil
	}
	out := make([]string, 0, len(overrides)*2)
	for _, o := range overrides {
		out = append(out, "-c", o.Argv())
	}
	return out
}

// ScanGitConfig safely reads and parses the git configuration of the
// repository git itself would discover for repoRoot (text-only; it never
// spawns a process). Discovery mirrors `git -C repoRoot`: the .git chain is
// walked up from repoRoot, so a workspace rooted at a subdirectory of a
// repository is covered too. .git may be a directory or a "gitdir: ..."
// pointer file (worktrees; relative pointers resolve against the directory
// holding the .git entry); no .git anywhere on the chain yields an empty
// result, not an error.
//
// Linked worktrees ([2]a of the post-v0.7.3 review): a worktree's .git
// points at <main>/.git/worktrees/<name>, which carries no config of its
// own — git reads the COMMON config (<main>/.git/config) plus, when
// extensions.worktreeConfig is enabled, the config.worktree overlay. Both
// are scanned and merged with git's layering semantics (the worktree value
// wins for identical keys; includes/errors union). The common dir is
// resolved via the worktree's commondir file, mirroring
// `git rev-parse --git-common-dir`.
//
// Attribute routing sources ([1]a): attr.tree only replaces the in-tree
// .gitattributes source, so .git/info/attributes and the file named by
// core.attributesFile are scanned as well (see scanAttributeSources). The
// attributes mechanism has no config kill-switch, which is exactly why the
// scan must succeed: an attribute source that exists but cannot be read
// makes the whole scan fail closed instead of running git with invisible
// routing.
//
// Object format ([1]d): core.repositoryformatversion +
// extensions.objectformat select the empty-tree constant used for the
// attr.tree kill (SHA-1 vs SHA-256); an unknown format or version refuses
// the scan (fail closed) because an invalid attr.tree value is a silent
// no-op for git.
//
// A missing or oversized config file inside a resolved git dir is
// reported as an error so callers can fail closed. The optional logger
// receives a warning line for every ignored include/includeIf directive.
func ScanGitConfig(repoRoot string, loggers ...*slog.Logger) (*GitConfigInfo, error) {
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	gitDir, err := resolveGitDir(repoRoot)
	if err != nil {
		return nil, err
	}
	if gitDir == "" {
		return &GitConfigInfo{}, nil
	}

	commonDir, isWorktree, err := resolveCommonGitDir(gitDir)
	if err != nil {
		return nil, err
	}

	info, err := ScanGitConfigFile(filepath.Join(commonDir, "config"), logger)
	if err != nil {
		return nil, err
	}
	info.GitDir = gitDir
	info.CommonDir = commonDir

	if isWorktree && info.worktreeConfigEnabled {
		// git reads config.worktree only when extensions.worktreeConfig is
		// enabled in the common config; a planted-but-disabled file is inert
		// and deliberately not scanned (no vector, no false findings).
		wtPath := filepath.Join(gitDir, "config.worktree")
		if _, statErr := os.Lstat(wtPath); statErr == nil {
			wt, wtErr := ScanGitConfigFile(wtPath, logger)
			if wtErr != nil {
				return nil, wtErr
			}
			mergeGitConfigInfo(info, wt)
			info.WorktreeConfigPath = wtPath
		}
	}

	if err := info.validateRepoFormat(); err != nil {
		return nil, err
	}
	if info.objectFormat == "sha256" {
		info.ObjectFormat = "sha256"
	} else {
		info.ObjectFormat = "sha1"
	}
	// The attr.tree finding's override was rendered with the parse-time
	// default hash; pin it to the now-resolved object format.
	emptyTree := info.emptyTreeHash()
	for i := range info.Findings {
		if info.Findings[i].Kind == GitConfigFindingAttrTree && len(info.Findings[i].Overrides) == 1 {
			info.Findings[i].Overrides[0].Value = emptyTree
		}
	}

	if err := scanAttributeSources(repoRoot, commonDir, info, logger); err != nil {
		return nil, err
	}
	return info, nil
}

// resolveCommonGitDir resolves the shared git directory for gitDir, mirroring
// `git rev-parse --git-common-dir`: a commondir file inside gitDir names the
// common dir (usually relative to gitDir); without one the repository is
// self-contained and the common dir is gitDir itself. A worktree gitdir is
// recognizable by its gitdir file (pointing back at the worktree root); one
// without a readable commondir is corrupted or hostile and fails closed —
// the common config it hides is exactly what git would execute.
func resolveCommonGitDir(gitDir string) (commonDir string, isWorktree bool, err error) {
	commonPath := filepath.Join(gitDir, "commondir")
	if _, err := os.Stat(commonPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if fileExists(filepath.Join(gitDir, "gitdir")) {
				return "", false, fmt.Errorf("worktree git dir %s has a gitdir marker but no commondir file: cannot resolve the common config (fail closed)", gitDir)
			}
			return gitDir, false, nil
		}
		return "", false, fmt.Errorf("stat commondir: %w", err)
	}
	data, err := readCapped(commonPath, maxGitDirPointerBytes)
	if err != nil {
		return "", false, fmt.Errorf("read commondir (fail closed): %w", err)
	}
	target := strings.TrimSpace(string(data))
	if target == "" {
		return "", false, fmt.Errorf("empty commondir file in %s (fail closed)", gitDir)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(gitDir, target)
	}
	cst, err := os.Stat(target)
	if err != nil {
		return "", false, fmt.Errorf("common dir %s does not exist (fail closed): %w", target, err)
	}
	if !cst.IsDir() {
		return "", false, fmt.Errorf("common dir %s is not a directory (fail closed)", target)
	}
	return target, true, nil
}

// mergeGitConfigInfo folds the overlay (a later config layer, e.g. a
// worktree's config.worktree) into base with git's layering semantics: for
// identical keys the overlay value wins, everything else unions. Findings
// are deduplicated by FullKey (the surviving entry keeps the overlay's value
// and line), includes and parse errors append (both files were really read),
// and the parser-captured repository-model state takes the overlay's value.
func mergeGitConfigInfo(base, overlay *GitConfigInfo) {
	idx := make(map[string]int, len(base.Findings))
	for i := range base.Findings {
		idx[base.Findings[i].FullKey] = i
	}
	for i := range overlay.Findings {
		f := overlay.Findings[i]
		if j, ok := idx[f.FullKey]; ok {
			base.Findings[j] = f
		} else {
			base.Findings = append(base.Findings, f)
			idx[f.FullKey] = len(base.Findings) - 1
		}
	}
	base.Includes = append(base.Includes, overlay.Includes...)
	base.Errors = append(base.Errors, overlay.Errors...)
	base.rawSources = append(base.rawSources, overlay.rawSources...)
	base.repositoryFormatVersion = overlay.repositoryFormatVersion
	base.objectFormat = overlay.objectFormat
	base.worktreeConfigEnabled = overlay.worktreeConfigEnabled
	base.attributesFilePath = overlay.attributesFilePath
}

// validateRepoFormat checks that the parsed repository model is one whose
// attribute-routing behavior this scanner can reproduce. git refuses
// repositoryformatversion > 1 and unknown extensions.objectformat values
// outright; the scanner fails closed the same way, because the object format
// decides which empty-tree hash the attr.tree kill must use and a wrong hash
// is a silent no-op (invalid attr.tree values disable the mechanism without
// any error).
func (info *GitConfigInfo) validateRepoFormat() error {
	if info.repositoryFormatVersion < 0 || info.repositoryFormatVersion > 1 {
		return fmt.Errorf("unsupported core.repositoryformatversion %d (fail closed: cannot model the repository)", info.repositoryFormatVersion)
	}
	switch info.objectFormat {
	case "", "sha1", "sha256":
		return nil
	default:
		return fmt.Errorf("unknown extensions.objectformat %q (fail closed: cannot select the attr.tree empty-tree hash)", info.objectFormat)
	}
}

// scanAttributeSources scans the attribute-routing files that attr.tree
// cannot neutralize: <commonDir>/info/attributes (which git reads for every
// worktree of the repository) and the file named by core.attributesFile
// (repo config; verified live on git 2.50.1). Routing directives found there
// become findings whose neutralization pins the routed driver NAME — the -c
// overrides beat file config regardless of where the driver is defined.
// There is no config kill-switch for these sources, so a source that exists
// but cannot be read is a hard error: running git with invisible routing is
// never an option. A missing source is inert (git ignores it) and skipped.
func scanAttributeSources(repoRoot, commonDir string, info *GitConfigInfo, logger *slog.Logger) error {
	type source struct {
		path, label string
	}
	sources := []source{{filepath.Join(commonDir, "info", "attributes"), "info/attributes"}}
	if raw := info.attributesFilePath; raw != "" {
		if resolved := resolveAttributesFilePath(raw, repoRoot); resolved != "" {
			sources = append(sources, source{resolved, "core.attributesFile"})
		}
	}
	for _, src := range sources {
		data, err := readCapped(src.path, maxGitConfigBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue // a missing routing source is inert
		}
		if err != nil {
			return fmt.Errorf("cannot scan attribute routing source %s (no kill switch exists; fail closed): %w", src.path, err)
		}
		info.Findings = append(info.Findings, parseAttributesRouting(string(data), src.label, src.path)...)
		info.rawSources = append(info.rawSources, gitConfigSource{kind: src.label, path: src.path, data: data})
	}
	return nil
}

// resolveAttributesFilePath resolves a core.attributesFile value the way git
// resolves it for core.excludesFile: a leading ~/ expands against the home
// directory, a relative path resolves against the working directory git runs
// in (c0wrk always runs repo-scoped git with cmd.Dir = the repository root,
// so repoRoot is that anchor). An unresolvable home leaves the path verbatim
// (it will simply not exist).
func resolveAttributesFilePath(raw, repoRoot string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	value = expandHomeTilde(value)
	if !filepath.IsAbs(value) {
		value = filepath.Join(repoRoot, value)
	}
	return filepath.Clean(value)
}

// parseAttributesRouting extracts command-routing directives
// (filter=<name>, merge=<name>, diff=<name>) from a gitattributes-format
// file. Macro definition lines (e.g. "lfs filter=lfs -text") are covered by
// the same token scan, so a name that only a macro expansion would route is
// still pinned. Explicitly DISARMING forms (-filter=x, !filter) are not
// findings: they turn routing off, not on.
func parseAttributesRouting(data, label, path string) []GitConfigFinding {
	var findings []GitConfigFinding
	for lineNo, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tokens := strings.Fields(line)
		if len(tokens) < 2 {
			continue // a bare pattern routes nothing
		}
		pattern := strings.Trim(tokens[0], `"`)
		for _, tok := range tokens[1:] {
			if strings.HasPrefix(tok, "#") {
				break // comment token: rest of the line is commentary
			}
			name, value, hasValue := strings.Cut(tok, "=")
			if !hasValue {
				continue // bare attr (e.g. "text", "binary"): no driver name
			}
			if name != "filter" && name != "merge" && name != "diff" {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"`)
			if value == "" {
				continue
			}
			findings = append(findings, GitConfigFinding{
				Kind:        GitConfigFindingAttrRouting,
				Section:     "attributes",
				Subsection:  value,
				Key:         name,
				FullKey:     label + ":" + name + "=" + value,
				Value:       pattern,
				Line:        lineNo + 1,
				Description: attrRoutingDescription(label, path, pattern, name, value),
				Overrides:   attrRoutingOverrides(name, value),
			})
		}
	}
	return findings
}

func attrRoutingDescription(label, path, pattern, attr, name string) string {
	return label + " (" + path + ") routes \"" + pattern + "\" to " + attr + " driver \"" + name +
		"\". attr.tree does not cover this routing source, so the routed name is pinned instead: " +
		"the -c overrides beat file config no matter where the driver is defined — including an " +
		"included config file the scanner cannot read."
}

func attrRoutingOverrides(attr, name string) []GitConfigOverride {
	switch attr {
	case "filter":
		return []GitConfigOverride{
			{Key: "filter." + name + ".process", Value: ""},
			{Key: "filter." + name + ".clean", Value: neutralPassthroughValue},
			{Key: "filter." + name + ".smudge", Value: neutralPassthroughValue},
		}
	case "merge":
		return []GitConfigOverride{{Key: "merge." + name + ".driver", Value: neutralMergeDriverValue}}
	default: // diff
		// The named diff driver is pinned on BOTH of its command-bearing
		// keys: textconv (content rendering) and command (the external
		// driver the git diff porcelain runs by default — verified on git
		// 2.50.1).
		return []GitConfigOverride{
			{Key: "diff." + name + ".textconv", Value: neutralPassthroughValue},
			{Key: "diff." + name + ".command", Value: ""},
		}
	}
}

// fileExists reports whether path exists (following symlinks).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ScanGitConfigFile parses one specific git config file (text-only). A missing
// file yields an empty result; an unreadable or oversized file yields an error
// (fail closed). See ScanGitConfig for details.
func ScanGitConfigFile(configPath string, loggers ...*slog.Logger) (*GitConfigInfo, error) {
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	data, err := readCapped(configPath, maxGitConfigBytes)
	if errors.Is(err, os.ErrNotExist) {
		return &GitConfigInfo{ConfigPath: configPath}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read git config: %w", err)
	}
	info := parseGitConfigData(string(data), logger)
	info.ConfigPath = configPath
	info.GitDir = filepath.Dir(configPath)
	info.rawSources = []gitConfigSource{{kind: sourceKind(configPath), path: configPath, data: data}}
	// Every include directive in this file resolves its (possibly relative)
	// path against this file's directory, so record it for ResolveIncludes.
	sourceDir := filepath.Dir(configPath)
	for i := range info.Includes {
		info.Includes[i].SourceDir = sourceDir
	}
	return info, nil
}

// sourceKind labels a config file path for the snapshot diff header: the
// per-worktree overlay is distinguishable from the common config by its
// basename; every other config file reads as the common config.
func sourceKind(configPath string) string {
	if filepath.Base(configPath) == "config.worktree" {
		return "config.worktree"
	}
	return "config"
}

// resolveGitDir resolves the git directory whose config git itself would use
// for repoRoot: it walks up from repoRoot (after resolving symlinks, mirroring
// git's physical discovery from the working directory) until it finds a .git
// entry — exactly what `git -C repoRoot rev-parse` does when the workspace
// root is a subdirectory of the repository. Skipping the walk-up would let a
// .git-less workspace root silently skip neutralization while git happily
// executes the parent repository's config-driven programs. .git may be a
// directory or a "gitdir: ..." pointer file (worktrees); relative pointers
// are resolved against the directory containing the .git entry. Returns ""
// when no .git is found anywhere on the chain.
func resolveGitDir(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", errors.New("cannot resolve git dir for an empty path")
	}
	// Evaluate symlinks so the walk matches git's discovery, which runs from
	// the physical working directory after chdir. On error (path missing or
	// inaccessible) fall back to the lexical form: discovery then reports
	// the missing .git chain as no-repo, which callers already handle.
	root, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		root = repoRoot
	}
	dir := root
	for {
		gitDir, err := resolveGitDirAt(dir)
		if err != nil {
			return "", err
		}
		if gitDir != "" {
			return gitDir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Filesystem root reached: no .git on the whole chain.
			return "", nil
		}
		dir = parent
	}
}

// resolveGitDirAt resolves the .git entry at dir itself: "" when dir carries
// no .git at all, the .git directory when present, or the target of a
// "gitdir: ..." pointer file.
func resolveGitDirAt(dir string) (string, error) {
	dotGit := filepath.Join(dir, ".git")
	st, err := os.Stat(dotGit)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stat .git: %w", err)
	}
	if st.IsDir() {
		return dotGit, nil
	}
	data, err := readCapped(dotGit, maxGitDirPointerBytes)
	if err != nil {
		return "", fmt.Errorf("read .git pointer: %w", err)
	}
	pointer, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return "", fmt.Errorf(".git is not a directory and not a gitdir pointer file: %s", dotGit)
	}
	target := strings.TrimSpace(pointer)
	if target == "" {
		return "", fmt.Errorf(".git pointer file has empty gitdir: %s", dotGit)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return target, nil
}

// ResolveWorkTreeRoot returns the work-tree root of the repository git
// would discover from path: the first directory on path's chain that
// carries a .git entry (a directory for plain repositories, a "gitdir:"
// pointer file for linked worktrees) — the path `git rev-parse
// --show-toplevel` reports, derived exec-free with the same walk-up
// discovery as the config scan. It exists for trust attribution and
// warning display (review [52]): the scan walks up from a subdirectory
// workspace to the parent repository's config, so the intake warning and
// the security.trusted_git_repos list must key on that repository root,
// not on the scanned subdirectory path.
//
// The walk is deliberately LEXICAL (no symlink evaluation): the stored and
// displayed root stays in the path form the user knows (macOS $TMPDIR
// symlinks, symlinked workspace mounts) instead of its physical
// /private/... spelling, and both the warning attribution and the trust
// list normalize through this one function, so they always agree.
// Returns "" when no .git exists anywhere on the chain — callers fall back
// to the given path, which keeps the fail-closed pairing (an unresolvable
// chain is warned and trusted under the exact path the user saw).
func ResolveWorkTreeRoot(path string) string {
	if path == "" {
		return ""
	}
	dir := filepath.Clean(path)
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Filesystem root reached: no .git on the whole chain.
			return ""
		}
		dir = parent
	}
}

// readCapped reads a regular file, refusing input beyond limit bytes.
// Regularity is enforced by openRegularFile with NO Stat→Open window
// (review [14]): a FIFO swapped in between a stat and an open by a racing
// local adversary would otherwise defeat the guard and block the open
// forever, hanging the synchronous intake scan on the SwitchProject RPC
// path. Anything that is not a regular file (FIFO, device, socket,
// directory) is refused by the fstat on the already-open descriptor.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := openRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes, refusing to parse", path, limit)
	}
	return data, nil
}

// --- pure INI-subset parser (no I/O beyond the caller-provided string) ---

type gitConfigParser struct {
	data       []byte
	pos        int
	line       int // 1-based
	logger     *slog.Logger
	info       *GitConfigInfo
	section    string
	subsection string
}

// parseGitConfigData parses git-config text into findings. It never fails:
// malformed constructs are recorded in info.Errors and parsing continues
// (git refuses such files wholesale, so continuing can only over-report,
// never under-report relative to what git would execute).
func parseGitConfigData(data string, logger *slog.Logger) *GitConfigInfo {
	info := &GitConfigInfo{}
	// Strip a UTF-8 BOM if present (byte-level to keep the parser ASCII).
	data = strings.TrimPrefix(data, "\ufeff")
	p := &gitConfigParser{data: []byte(data), line: 1, logger: logger, info: info}
	for {
		p.skipBlank()
		if p.pos >= len(p.data) {
			break
		}
		if p.data[p.pos] == '[' {
			p.parseHeader()
		} else {
			p.parseEntry()
		}
	}
	return info
}

func (p *gitConfigParser) errorf(line int, format string, args ...any) {
	p.info.Errors = append(p.info.Errors, GitConfigError{
		Line:    line,
		Message: fmt.Sprintf(format, args...),
	})
}

// skipBlank consumes whitespace, newlines and whole comment lines.
func (p *gitConfigParser) skipBlank() {
	for p.pos < len(p.data) {
		switch c := p.data[p.pos]; c {
		case ' ', '\t', '\r':
			p.pos++
		case '\n':
			p.pos++
			p.line++
		case '#', ';':
			p.skipLine()
		default:
			return
		}
	}
}

// skipInlineWs consumes spaces, tabs and CR (not newlines).
func (p *gitConfigParser) skipInlineWs() {
	for p.pos < len(p.data) {
		if c := p.data[p.pos]; c == ' ' || c == '\t' || c == '\r' {
			p.pos++
		} else {
			return
		}
	}
}

// skipLine consumes through the next newline (inclusive), counting it.
func (p *gitConfigParser) skipLine() {
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		if c == '\n' {
			p.line++
			return
		}
	}
}

// atLineEnd reports that only a comment or nothing remains on the line.
func (p *gitConfigParser) atLineEnd() bool {
	p.skipInlineWs()
	if p.pos >= len(p.data) {
		return true
	}
	switch p.data[p.pos] {
	case '\n', '#', ';':
		return true
	}
	return false
}

// parseHeader parses "[section]", "[section \"sub\"]" or the old dotted
// "[section.sub]" form. Section and key names are case-insensitive
// (lowercased here); a quoted subsection is case-sensitive and preserved
// verbatim; a dotted subsection is lowercased, matching git.
func (p *gitConfigParser) parseHeader() {
	startLine := p.line
	p.pos++ // consume '['
	nameStart := p.pos
	for p.pos < len(p.data) && isHeaderNameByte(p.data[p.pos]) {
		p.pos++
	}
	name := strings.ToLower(string(p.data[nameStart:p.pos]))
	if name == "" {
		p.errorf(startLine, "empty section name")
		p.skipLine()
		return
	}
	// git requires section names to start with a letter.
	if !isKeyStartByte(p.data[nameStart]) {
		p.errorf(startLine, "section name %q must start with a letter", name)
		p.skipLine()
		return
	}
	p.skipInlineWs()
	if p.pos >= len(p.data) {
		p.errorf(startLine, "unterminated section header")
		return
	}
	sub := ""
	switch p.data[p.pos] {
	case ']':
		p.pos++
	case '"':
		var ok bool
		if sub, ok = p.parseQuotedSubsection(); !ok {
			p.skipLine()
			return
		}
		p.skipInlineWs()
		if p.pos >= len(p.data) || p.data[p.pos] != ']' {
			p.errorf(startLine, "expected ']' after subsection")
			p.skipLine()
			return
		}
		p.pos++
	default:
		p.errorf(startLine, "unexpected character %q in section header", rune(p.data[p.pos]))
		p.skipLine()
		return
	}
	// Old dotted syntax: [filter.lfs] means section "filter", subsection
	// "lfs" (git lowercases the whole dotted header).
	if sub == "" {
		if base, rest, ok := strings.Cut(name, "."); ok {
			name, sub = base, rest
		}
	}
	p.section, p.subsection = name, sub
}

// parseQuotedSubsection reads the quoted subsection of a header, honoring \"
// and \\ escapes.
func (p *gitConfigParser) parseQuotedSubsection() (string, bool) {
	p.pos++ // consume '"'
	var b strings.Builder
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		switch c {
		case '"':
			p.pos++
			return b.String(), true
		case '\n':
			p.errorf(p.line, "unterminated subsection name")
			return "", false
		case '\\':
			p.pos++
			if p.pos >= len(p.data) {
				p.errorf(p.line, "unterminated escape in subsection name")
				return "", false
			}
			b.WriteByte(p.data[p.pos])
			p.pos++
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	p.errorf(p.line, "unterminated subsection name")
	return "", false
}

// parseEntry parses "key = value" or a bare "key" (boolean true) within the
// current section.
func (p *gitConfigParser) parseEntry() {
	startLine := p.line
	if p.section == "" {
		// git refuses config entries outside of any section ("bad config
		// line") — record it so Clean() stays false and intake warns.
		p.errorf(startLine, "config entry outside of any section")
		p.skipLine()
		return
	}
	if !isKeyStartByte(p.data[p.pos]) {
		p.errorf(startLine, "invalid config syntax")
		p.skipLine()
		return
	}
	keyStart := p.pos
	p.pos++
	for p.pos < len(p.data) && isKeyByte(p.data[p.pos]) {
		p.pos++
	}
	key := strings.ToLower(string(p.data[keyStart:p.pos]))
	value := ""
	boolean := false
	p.skipInlineWs()
	if p.pos < len(p.data) && p.data[p.pos] == '=' {
		p.pos++
		v, ok := p.parseValue()
		value = v
		if !ok {
			// The error is already recorded; keep the partial value so
			// the key is still reported (over-report is the safe direction).
			p.skipLine()
		}
	} else {
		boolean = true
		if !p.atLineEnd() {
			p.errorf(startLine, "invalid syntax after key %q", key)
			p.skipLine()
			return
		}
	}
	p.dispatchEntry(startLine, key, value, boolean)
}

// parseValue reads a config value: partial quoting, backslash-newline
// continuation, and trailing-unquoted-whitespace trimming, matching git's
// value semantics. Returns the value and whether it terminated cleanly.
func (p *gitConfigParser) parseValue() (string, bool) {
	var b strings.Builder
	var pending strings.Builder // whitespace outside quotes, dropped while no value content exists
	flushPending := func() {
		// git only appends whitespace once the value buffer is non-empty,
		// which is how leading whitespace after '=' disappears.
		if b.Len() > 0 {
			b.WriteString(pending.String())
		}
		pending.Reset()
	}
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		switch c {
		case '\n':
			p.pos++
			p.line++
			return b.String(), true
		case '#', ';':
			p.skipLine()
			return b.String(), true
		case '"':
			flushPending()
			if !p.parseQuotedSegment(&b) {
				return b.String(), false
			}
		case '\\':
			// git applies one escape switch in and out of quotes: the
			// backslash is consumed together with the very next character
			// (no whitespace skipping). Backslash-newline is a line
			// continuation that vanishes while surrounding whitespace is
			// kept (verified against git: `line1 \` + indented `line2`
			// yields "line1 \t…line2" with all intervening ws intact).
			p.pos++
			if p.pos >= len(p.data) {
				p.errorf(p.line, "unterminated escape in value")
				flushPending()
				b.WriteByte('\\')
				return b.String(), false
			}
			e := p.data[p.pos]
			p.pos++
			switch e {
			case '\n':
				p.line++
			case 't':
				flushPending()
				b.WriteByte('\t')
			case 'b':
				flushPending()
				b.WriteByte('\b')
			case 'n':
				flushPending()
				b.WriteByte('\n')
			case '\\', '"':
				flushPending()
				b.WriteByte(e)
			default:
				// git refuses the whole file on unknown escapes; we
				// record the error and keep the characters literally
				// (over-reporting is the safe direction).
				p.errorf(p.line, "unknown escape \\%c in value", e)
				flushPending()
				b.WriteByte('\\')
				b.WriteByte(e)
			}
		case ' ', '\t', '\r':
			pending.WriteByte(c)
			p.pos++
		default:
			flushPending()
			b.WriteByte(c)
			p.pos++
		}
	}
	return b.String(), true
}

// parseQuotedSegment reads one "..." segment of a value into b. git applies
// its escape switch inside quotes too: \", \\, \n, \t, \b, and a backslash
// before a raw newline continues the quoted value on the next line. Unknown
// escapes are recorded as errors and kept literally (git refuses the whole
// file there; over-reporting is the safe direction). An unterminated raw
// newline (no backslash) makes git refuse the file; we preserve the newline
// and keep reading, which can only over-report.
func (p *gitConfigParser) parseQuotedSegment(b *strings.Builder) bool {
	p.pos++ // consume opening quote
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		switch c {
		case '"':
			p.pos++
			return true
		case '\n':
			// A raw newline inside a quoted value makes git refuse the
			// whole file ("fatal: bad config line N" — verified on git
			// 2.50.1), so it must land in Errors: without it a config whose
			// only anomaly is this construct stays Clean() and the intake
			// never warns about a config git considers broken (review
			// [63]). The newline is kept and reading continues —
			// over-reporting findings stays the safe direction.
			p.errorf(p.line, "newline inside quoted value (git refuses this config)")
			b.WriteByte('\n')
			p.pos++
			p.line++
		case '\\':
			p.pos++
			if p.pos >= len(p.data) {
				p.errorf(p.line, "unterminated escape in quoted value")
				return false
			}
			e := p.data[p.pos]
			p.pos++
			switch e {
			case '\n':
				p.line++
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			case 'n':
				b.WriteByte('\n')
			case '\\', '"':
				b.WriteByte(e)
			default:
				p.errorf(p.line, "unknown escape \\%c in quoted value", e)
				b.WriteByte('\\')
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	p.errorf(p.line, "unterminated quoted value")
	return false
}

// dispatchEntry routes a parsed key to findings, include records, or silence.
func (p *gitConfigParser) dispatchEntry(line int, key, value string, boolean bool) {
	if p.section == "include" || p.section == "includeif" {
		inc := GitConfigInclude{
			Conditional: p.section == "includeif",
			Condition:   p.subsection,
			Path:        value,
			Line:        line,
		}
		p.info.Includes = append(p.info.Includes, inc)
		p.logger.Warn("git config include directive ignored (not followed); config is an incomplete view",
			"line", line,
			"conditional", inc.Conditional,
			"condition", inc.Condition,
			"path", inc.Path)
		return
	}
	var kind, description string
	var baselineCovered bool
	switch p.section {
	case "core":
		kind, description, baselineCovered = coreKeyFinding(key)
		switch key {
		case "repositoryformatversion":
			v, convErr := parseIntConfigValue(value)
			if convErr != nil {
				p.errorf(line, "core.repositoryformatversion %q is not an integer: %v", value, convErr)
			} else {
				p.info.repositoryFormatVersion = v
			}
		case "attributesfile":
			if boolean {
				p.errorf(line, "core.attributesFile is not a boolean key")
				break
			}
			p.info.attributesFilePath = value
			kind = GitConfigFindingAttributesFile
			description = "core.attributesFile names an additional attributes routing file. Verified live " +
				"on git 2.50.1: the value is respected from repository config, and attr.tree does not cover " +
				"this routing source. Neutralized by forcing core.attributesFile= (empty), which disables " +
				"the source entirely; the file itself is also scanned for routed driver names, which are " +
				"pinned by name (the -c overrides beat file config wherever the driver is defined)."
		}
	case "extensions":
		// extensions.* decides how git itself interprets the repository
		// (object format, worktree config); capture for the model.
		switch key {
		case "objectformat":
			p.info.objectFormat = strings.ToLower(value)
		case "worktreeconfig":
			p.info.worktreeConfigEnabled = parseBoolConfigValue(value, boolean)
		}
	case "attr":
		if key == "tree" {
			kind = GitConfigFindingAttrTree
			description = "attr.tree redirects where git reads .gitattributes from; an attacker-chosen tree " +
				"arms filter/merge/textconv vectors repo-wide. Neutralized by overriding attr.tree to the " +
				"empty tree, which git's -c command line beats file config on."
		}
	case "filter":
		if p.subsection != "" && (key == "process" || key == "clean" || key == "smudge") {
			kind = GitConfigFindingFilter
			description = filterKeyDescription(key, p.subsection)
		}
	case "merge":
		if p.subsection != "" && key == "driver" {
			kind = GitConfigFindingMergeDriver
			description = "merge." + p.subsection + ".driver is executed by git to resolve merge conflicts " +
				"for paths routed to it via the merge attribute. Neutralized by forcing the driver to " +
				"'false %O %A %B' (a real conflict is recorded without running attacker code); the -c " +
				"override beats file config wherever the driver is defined, so every routing source — " +
				"the in-tree .gitattributes included — is covered without disabling benign attributes."
		}
	case "diff":
		if p.subsection != "" && (key == "textconv" || key == "command") ||
			(p.subsection == "" && key == "external") {
			switch {
			case key == "textconv":
				kind = GitConfigFindingTextConv
				description = "diff." + p.subsection + ".textconv is executed to render blob content and runs " +
					"by default in plain git diff (no flag needed). Neutralized by forcing textconv=cat; " +
					"the -c override beats file config wherever the driver is defined, so every routing " +
					"source — the in-tree .gitattributes included — is covered without disabling benign " +
					"attributes."
			case p.subsection == "":
				kind = GitConfigFindingDiffCommand
				description = "diff.external is an external diff command that plain git diff executes BY " +
					"DEFAULT — no --ext-diff flag is required (--no-ext-diff is what disables it; verified " +
					"on git 2.50.1) — so it runs from the Git panel's diff surfaces on the first change " +
					"they render. Neutralized by forcing diff.external= (empty): the armed value can never " +
					"execute, and c0wrk's patch-producing diff call sites additionally pass --no-ext-diff."
			default:
				kind = GitConfigFindingDiffCommand
				description = "diff." + p.subsection + ".command is an external diff driver that plain git diff " +
					"executes by default for paths routed to it via the diff attribute (verified on git " +
					"2.50.1: --ext-diff is not required on the git diff porcelain). Neutralized by forcing " +
					"diff." + p.subsection + ".command= (empty), which covers every routing source (the -c " +
					"override beats file config wherever the driver is defined); c0wrk's patch-producing " +
					"diff call sites additionally pass --no-ext-diff."
			}
		}
	case "credential":
		if key == "helper" {
			kind = GitConfigFindingCredential
			description = credentialHelperDescription(p.subsection)
		}
	}
	if kind == "" {
		return
	}
	fullKey := p.section
	if p.subsection != "" {
		fullKey += "." + p.subsection
	}
	fullKey += "." + key
	finding := GitConfigFinding{
		Kind:            kind,
		Section:         p.section,
		Subsection:      p.subsection,
		Key:             key,
		FullKey:         fullKey,
		Value:           value,
		Boolean:         boolean,
		Line:            line,
		Description:     description,
		BaselineCovered: baselineCovered,
	}
	switch kind {
	case GitConfigFindingAttrTree:
		finding.Overrides = []GitConfigOverride{{Key: attrTreeKey, Value: EmptyTreeSHA1}}
	case GitConfigFindingAttributesFile:
		finding.Overrides = []GitConfigOverride{{Key: attributesFileKey, Value: ""}}
	case GitConfigFindingMergeDriver:
		finding.Overrides = []GitConfigOverride{{Key: "merge." + p.subsection + ".driver", Value: neutralMergeDriverValue}}
	case GitConfigFindingTextConv:
		finding.Overrides = []GitConfigOverride{{Key: "diff." + p.subsection + ".textconv", Value: neutralPassthroughValue}}
		// The per-name pin is the whole neutralization: the -c override
		// beats file config wherever the driver is defined, so every
		// routing source is covered without attr.tree (review [56]).
	case GitConfigFindingFilter:
		finding.Overrides = []GitConfigOverride{
			{Key: "filter." + p.subsection + ".process", Value: ""},
			{Key: "filter." + p.subsection + ".clean", Value: neutralPassthroughValue},
			{Key: "filter." + p.subsection + ".smudge", Value: neutralPassthroughValue},
		}
	case GitConfigFindingSSHCommand:
		finding.Overrides = []GitConfigOverride{{Key: "core.sshCommand", Value: "ssh"}}
	case GitConfigFindingAskPass:
		finding.Overrides = []GitConfigOverride{{Key: "core.askPass", Value: ""}}
	case GitConfigFindingGitProxy:
		finding.Overrides = []GitConfigOverride{{Key: gitProxyAllowKey, Value: gitProxyDenyValue}}
	case GitConfigFindingCredential:
		ov := []GitConfigOverride{{Key: "credential.helper", Value: ""}}
		if p.subsection != "" {
			ov = append(ov, GitConfigOverride{Key: "credential." + p.subsection + ".helper", Value: ""})
		}
		finding.Overrides = ov
	case GitConfigFindingDiffCommand:
		if p.subsection == "" {
			finding.Overrides = []GitConfigOverride{{Key: "diff.external", Value: ""}}
		} else {
			finding.Overrides = []GitConfigOverride{{Key: "diff." + p.subsection + ".command", Value: ""}}
		}
	}
	p.info.Findings = append(p.info.Findings, finding)
}

// parseBoolConfigValue interprets a git boolean config value: a bare key is
// true, and true/yes/on/1 (case-insensitive) are true; everything else is
// false.
func parseBoolConfigValue(value string, boolean bool) bool {
	if boolean {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// parseIntConfigValue parses a git integer config value.
func parseIntConfigValue(value string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}

// coreKeyFinding classifies command-bearing core.* keys.
func coreKeyFinding(key string) (kind, description string, baselineCovered bool) {
	switch key {
	case "fsmonitor":
		return GitConfigFindingFSMonitor,
			"core.fsmonitor runs a filesystem-monitor command (or the built-in FSMonitor when boolean) " +
				"during index refresh on status/diff/add. Covered by the c0wrk git baseline, which forces " +
				"-c core.fsmonitor=false on every invocation.",
			true
	case "fsmonitorhook":
		return GitConfigFindingFSMonitor,
			"core.fsmonitorHook is a legacy key that current git ignores entirely (verified on git " +
				"2.50.1: a command planted there never executes — the key is dead in modern git, not an " +
				"executing alias of core.fsmonitor). Reported belt-and-braces only: should any git version " +
				"ever honor the legacy key again, the c0wrk git baseline (-c core.fsmonitor=false on every " +
				"invocation) keeps it inert.",
			true
	case "hookspath":
		return GitConfigFindingHooksPath,
			"core.hooksPath redirects the directory git runs hooks from (pre-commit etc.) without user " +
				"interaction. Covered by the c0wrk git baseline, which points hooks at an empty safe directory.",
			true
	case "editor":
		return GitConfigFindingEditor,
			"core.editor names the editor command git may execute (e.g. for a commit message). Covered by " +
				"the c0wrk git baseline: GIT_EDITOR=true wins over repo config, so nothing executes.",
			true
	case "pager":
		return GitConfigFindingPager,
			"core.pager names a pager command git may execute — but only when stdout is a terminal. " +
				"c0wrk always pipes git output, so the pager is never spawned; no -c override is required.",
			false
	case "sshcommand":
		return GitConfigFindingSSHCommand,
			"core.sshCommand names the SSH binary/command every ssh://-style remote operation runs " +
				"(fetch/push/pull — reachable from the Git panel's remote RPCs; it wholly replaces the ssh " +
				"binary and executes before any network I/O). Neutralized by forcing core.sshCommand=ssh, " +
				"which restores the default ssh binary (verified on git 2.50.1: remote operations proceed " +
				"with it; an empty value would abort them with a spawn error instead).",
			false
	case "askpass":
		return GitConfigFindingAskPass,
			"core.askPass names a helper git executes to prompt for credentials whenever a transport " +
				"needs them (an authenticated fetch/push — reachable from the Git panel's remote RPCs). " +
				"Neutralized by forcing core.askPass= (empty), which disables the helper: authentication " +
				"fails closed instead of executing repository-defined code.",
			false
	case "gitproxy":
		return GitConfigFindingGitProxy,
			"core.gitProxy names the command git executes as a proxy for EVERY git:// transport operation " +
				"(fetch/push/pull — reachable from the Git panel's remote RPCs; verified on git 2.50.1: an " +
				"armed proxy runs even under a fully hardened baseline argv, and no core.gitProxy value " +
				"neutralizes it — empty included). Neutralized by forcing protocol.git.allow=never, which " +
				"forbids the git:// transport family outright: the operation fails closed with 'transport " +
				"not allowed' instead of executing the repository's proxy (ext:: and similar exotic " +
				"transports are already denied by git's own protocol defaults).",
			false
	case "worktree":
		return GitConfigFindingWorkTree,
			"core.worktree redirects where git materializes tracked files: a checkout or reset --hard in " +
				"this repository writes tracked content to the configured absolute path — outside the " +
				"workspace the user pointed c0wrk at (verified on git 2.50.1; no -c form beats the " +
				"config-defined worktree — an empty value is ignored too). Neutralized by pinning the " +
				"spawn environment's GIT_WORK_TREE to the repository root git would discover (the env var " +
				"outranks core.worktree), applied by GitCmdInRepo only when this finding is present.",
			false
	}
	return "", "", false
}

// credentialHelperDescription explains one credential-helper occurrence.
// The subsection is the URL match for credential.<url>.helper ("" for the
// generic credential.helper key).
func credentialHelperDescription(url string) string {
	key := "credential.helper"
	if url != "" {
		key = "credential." + url + ".helper"
	}
	desc := key + " names a credential helper git executes when a transport asks for credentials " +
		"(an authenticated fetch/push — reachable from the Git panel's remote RPCs; verified with git " +
		"credential fill on git 2.50.1). Neutralized by forcing credential.helper= (empty), which resets " +
		"the accumulated helper list — an empty value resets, and the -c command-line layer is read after " +
		"every file, so the reset lands after all file-configured helpers, generic and URL-specific alike"
	if url == "" {
		return desc + "."
	}
	return desc + ", plus a credential." + url + ".helper= (empty) pin for this URL."
}

// filterKeyDescription explains one filter sub-key.
func filterKeyDescription(key, name string) string {
	switch key {
	case "process":
		return "filter." + name + ".process runs a long-running external filter process during check-in and " +
			"check-out — on add, on commit (even --allow-empty on a clean tree), and on status/diff whenever " +
			"hashing is forced. Neutralized by forcing process= (empty): git then falls back to clean/smudge, " +
			"which the accompanying clean=cat/smudge=cat overrides pin to a safe passthrough; the -c overrides " +
			"beat file config wherever the driver is defined, so every routing source — the in-tree " +
			".gitattributes included — is covered without disabling benign attributes."
	case "clean":
		return "filter." + name + ".clean rewrites file content on check-in hashing (add/commit). Neutralized " +
			"by forcing clean=cat alongside process=; the -c override beats file config wherever the driver " +
			"is defined, covering every routing source without disabling benign attributes."
	default: // smudge
		return "filter." + name + ".smudge rewrites blob content on check-out. Neutralized by forcing " +
			"smudge=cat alongside process=; the -c override beats file config wherever the driver is " +
			"defined, covering every routing source without disabling benign attributes."
	}
}

func isHeaderNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.'
}

func isKeyStartByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isKeyByte(c byte) bool {
	return isKeyStartByte(c) || c >= '0' && c <= '9' || c == '-'
}
