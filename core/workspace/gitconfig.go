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

	"github.com/v0lka/sp4rk/safeio"
)

// Exec-free reader/neutralizer for untrusted repositories' .git/config (the
// GitSpawn vulnerability class): parses the command-relevant INI subset,
// records include/includeIf directives without following them (only
// ResolveIncludes follows them — for trust fingerprinting, never key
// neutralization), and derives the per-repo `-c` neutralizing overrides
// GitCmdInRepo prepends to every git invocation. Posture: fail closed on
// anything unscannable, over-report on anything parsed leniently (git
// refuses malformed configs wholesale); malformed lines land in Errors.
// Threat model, scan scope, values: specs/decisions/033-git-subprocess-hardening.md;
// trust binding: specs/decisions/034-git-trust-opt-out.md,
// specs/decisions/070-git-semantic-trust.md.

// EmptyTreeSHA1 is the SHA-1 of the empty git tree — the attr.tree value that
// neutralizes every attribute-routed command vector. Only this exact hash
// qualifies: attr.tree="" is NOT safe and invalid hashes are silent no-ops
// (specs/decisions/033-git-subprocess-hardening.md).
const EmptyTreeSHA1 = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// EmptyTreeSHA256 is the attr.tree value for extensions.objectformat=sha256
// repositories, where the SHA-1 constant is a verified silent no-op;
// ScanGitConfig selects between the two via the resolved object format,
// fail-closed when unknown (specs/decisions/033-git-subprocess-hardening.md).
const EmptyTreeSHA256 = "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"

// maxGitConfigBytes caps how much of a .git/config (or gitdir pointer file)
// is read: real configs are a few KiB; anything larger is hostile input.
const maxGitConfigBytes = 4 << 20 // 4 MiB

const maxGitDirPointerBytes = 4096

// Include-resolution bounds for ResolveIncludes: a hostile repo can chain
// includes arbitrarily deep or wide, so these caps bound the trust/recheck
// scan's I/O — deliberately generous for real include trees, still
// terminating on adversarial input (per-file size: maxGitConfigBytes).
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
	GitConfigFindingSigning        = "signing"            // commit.gpgsign / gpg.format / gpg.program / user.signingkey
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

// gitProxyAllowKey gates the git:// transport family core.gitProxy hijacks:
// the proxy command has no neutralizing value (empty falls back to git's
// default proxy handling, not "none"); `-c protocol.git.allow=never` kills
// the whole transport (specs/decisions/033-git-subprocess-hardening.md).
const gitProxyAllowKey = "protocol.git.allow"

// gitProxyDenyValue is the only verified "no transport of this family may
// be used at all" value for gitProxyAllowKey.
const gitProxyDenyValue = "never"

// attributesFileKey names an additional attributes routing file; it has no
// dedicated "off" switch — an EMPTY -c value disables the source entirely,
// and -c beats the key no matter which file defined it (verified git 2.50.1;
// specs/decisions/033-git-subprocess-hardening.md).
const attributesFileKey = "core.attributesFile"

// GitConfigOverride is a single neutralizing `-c key=value` override for one
// command-bearing key discovered in a repository config.
type GitConfigOverride struct {
	Key   string // full lowercase config key, e.g. "filter.lfs.process"
	Value string // neutralizing value, e.g. "" or "cat"
}

// Argv renders the override as the single argv element that follows a `-c`
// flag ("key=value"); an empty Value renders as "key=" — exactly how an armed
// process filter is disarmed.
func (o GitConfigOverride) Argv() string {
	return o.Key + "=" + o.Value
}

// errUnrenderableOverrideKey marks a neutralizing override whose key cannot
// be expressed as a `-c` argv element: git splits a `-c` argument at the
// FIRST `=`, so a key containing `=` (a subsection like `[filter "a=b"]`)
// would land its pin under a prefix key and leave the command-bearing key
// armed (review [21], fail-open).
func errUnrenderableOverrideKey(key string) error {
	return fmt.Errorf("git config override key %q contains '=' (subsection with '='?) and cannot be expressed as a -c argv element; rename the config subsection or remove the command-bearing key", key)
}

// GitConfigInfo is the full result of parsing one .git/config.
type GitConfigInfo struct {
	// GitDir is the resolved git directory the config belongs to ("" when no
	// .git was found). For a linked worktree this is the per-worktree
	// directory (<main>/.git/worktrees/<name>); the shared state is CommonDir.
	GitDir string
	// CommonDir is the shared git directory (the worktree's commondir target);
	// equals GitDir for normal repositories, "" when no .git was found.
	CommonDir string
	// ConfigPath is the config file that was parsed ("" when none); with
	// worktree merge semantics this is the common config — a parsed
	// config.worktree overlay is reported in WorktreeConfigPath.
	ConfigPath string
	// WorktreeConfigPath is the per-worktree config.worktree overlay parsed
	// and merged on top of the common config ("" when absent, or when
	// extensions.worktreeConfig is disabled and git ignores the file).
	WorktreeConfigPath string
	// ObjectFormat is the repository's object format ("sha1"/"sha256"),
	// resolved by ScanGitConfig; it selects the attr.tree empty-tree
	// constant. Empty until the full pipeline validates the format.
	ObjectFormat string
	// Findings lists every dangerous key occurrence, in file order (merged
	// worktree-overlay last-wins for identical keys).
	Findings []GitConfigFinding
	// Includes lists include/includeIf directives (parsed, logged, not
	// followed).
	Includes []GitConfigInclude
	// Errors lists malformed constructs (the parser continues past them).
	Errors []GitConfigError

	// --- parser-captured state (not dangerous by itself; models how git
	// itself reads the repository; layers last-wins across config layers) ---

	// repositoryFormatVersion is core.repositoryformatversion (default 0).
	repositoryFormatVersion int64
	// repositoryFormatVersionSet reports whether core.repositoryformatversion
	// was parsed from THIS layer. Layering is presence-based, not value-based:
	// a later layer that omits the key must not clobber an earlier layer's
	// value with the zero default (git keeps the earlier value).
	repositoryFormatVersionSet bool
	// objectFormat is the lowercased extensions.objectformat ("" when unset).
	objectFormat string
	// objectFormatSet: parsed from this layer (presence layers — see
	// repositoryFormatVersionSet).
	objectFormatSet bool
	// worktreeConfigEnabled reports extensions.worktreeConfig=true — the
	// switch that makes git read config.worktree in linked worktrees.
	worktreeConfigEnabled bool
	// worktreeConfigEnabledSet: parsed from this layer (presence layers).
	worktreeConfigEnabledSet bool
	// attributesFilePath is the last core.attributesFile value ("", or the
	// verbatim path before ~/ and relative resolution).
	attributesFilePath string
	// attributesFilePathSet: parsed from this layer (presence layers).
	attributesFilePathSet bool

	// rawSources captures the raw bytes of every scanned source; the trust
	// snapshot/fingerprint bind to these bytes
	// (specs/decisions/034-git-trust-opt-out.md).
	rawSources []gitConfigSource

	// worktreeOverlay retains the standalone overlay parse (nil when none):
	// WorktreeOverlayClean judges it from this same read — no re-read, and
	// the include warning fires exactly once per scan.
	worktreeOverlay *GitConfigInfo

	// includeSources captures the raw bytes of every include target
	// ResolveIncludes resolved, populated lazily — only the trust/recheck
	// path follows includes (ADR-033). Serialized after rawSources
	// (specs/decisions/034-git-trust-opt-out.md).
	includeSources []gitConfigSource

	// records retains EVERY dispatched config entry; classified at
	// serialization time, only DANGEROUS ones enter the semantic identity
	// (specs/decisions/070-git-semantic-trust.md). Source = rawSources index
	// of the layer the record was parsed from.
	records []gitConfigRecord
}

// NeutralizingArgvErr is NeutralizingArgv with the spawn gate attached: a
// set whose key contains `=` is refused instead of silently
// under-neutralizing (see errUnrenderableOverrideKey).
func (info *GitConfigInfo) NeutralizingArgvErr() ([]string, error) {
	overrides := info.NeutralizingOverrides()
	if len(overrides) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(overrides)*2)
	for _, o := range overrides {
		if strings.Contains(o.Key, "=") {
			return nil, errUnrenderableOverrideKey(o.Key)
		}
		out = append(out, "-c", o.Argv())
	}
	return out, nil
}

// GitConfigFinding describes one dangerous key occurrence discovered in a
// repository's .git/config: every command-bearing key parsed from the
// in-scope section families produces a finding, override-worthy or not.
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
	// BaselineCovered reports that the sysproc.GitCmd baseline (-c
	// core.fsmonitor=false, core.hooksPath=<safe dir>, GIT_EDITOR=true)
	// already neutralizes the key on every invocation; no per-repo override.
	BaselineCovered bool
	// Inert reports the finding cannot execute: the signing siblings are
	// dead unless a layer arms commit.gpgsign — Clean() ignores them, intake
	// stays warning-free; they still render when another finding warns
	// (specs/domains/workspace.md, Git integration).
	Inert bool
	// Overrides are the verified per-repo `-c` neutralizations for this
	// finding; empty when none is needed or none is verified.
	Overrides []GitConfigOverride
}

// GitConfigInclude records an include/includeIf directive. The referenced
// file is deliberately not read: any recorded include means the visible
// config is an incomplete view — detection-grade, not mitigation
// (specs/decisions/034-git-trust-opt-out.md).
type GitConfigInclude struct {
	Conditional bool   // true for includeIf
	Condition   string // includeIf condition (e.g. "gitdir:~/x/**"); "" for include
	Path        string // unexpanded path value as written
	Line        int    // 1-based line number
	// SourceDir is the absolute directory of the config file containing the
	// directive (relative include paths resolve against it); empty from the
	// pure parser, filled by ScanGitConfigFile.
	SourceDir string
}

// GitConfigError is a malformed construct. git refuses malformed configs
// wholesale ("fatal: bad config line N"), but callers scanning untrusted
// workspaces should still fail closed.
type GitConfigError struct {
	Line    int // 1-based line number
	Message string
}

// gitConfigSource is one raw scanned source; trust binds to the exact bytes
// the user reviewed instead of re-reading (and re-racing) the filesystem
// (specs/decisions/034-git-trust-opt-out.md).
type gitConfigSource struct {
	// kind is a stable short label for the diff header ("config",
	// "config.worktree", "info/attributes", "core.attributesFile").
	kind string
	// path is the absolute path of the source (may be "" for synthetic).
	path string
	// data is the raw bytes read from the source.
	data []byte
}

// Clean reports the config is fully visible (no includes, no parse errors)
// and carries no dangerous keys; Inert (dead) findings are ignored. Only a
// Clean result lets the caller skip per-repo neutralization without
// suspicion (specs/decisions/033-git-subprocess-hardening.md).
func (info *GitConfigInfo) Clean() bool {
	if info == nil {
		return true
	}
	for i := range info.Findings {
		if !info.Findings[i].Inert {
			return false
		}
	}
	return len(info.Includes) == 0 && len(info.Errors) == 0
}

// WorktreeOverlayClean reports whether the worktree's own config.worktree
// overlay (read live whenever extensions.worktreeConfig is enabled)
// contributes nothing dangerous beyond the common configuration — no
// includes, no parse errors, no key live in the merged model. The spawn
// layer's gate for trusted repositories' linked worktrees: the trust
// snapshot scans the trust root and can never bind the overlay, which is
// plantable in the worktree's git dir at any time
// (specs/decisions/034-git-trust-opt-out.md). Judged from the standalone
// parse retained before merging; any key live in any layer is not clean,
// and a missing retained parse fails closed.
func (info *GitConfigInfo) WorktreeOverlayClean() bool {
	if info == nil || info.WorktreeConfigPath == "" {
		return true
	}
	wt := info.worktreeOverlay
	if wt == nil {
		return false
	}
	if len(wt.Includes) > 0 || len(wt.Errors) > 0 {
		return false
	}
	live := make(map[string]bool, len(info.Findings))
	for i := range info.Findings {
		if !info.Findings[i].Inert {
			live[info.Findings[i].FullKey] = true
		}
	}
	for i := range wt.Findings {
		if live[wt.Findings[i].FullKey] {
			return false
		}
	}
	return true
}

// Snapshot returns a canonical, diff-able serialization of every scanned
// source under a stable "===== kind (path) =====" header, so
// DiffGitConfigSnapshots can attribute changes; nil info → nil. This is
// what Fingerprint hashes (specs/decisions/034-git-trust-opt-out.md).
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

// writeSnapshotSource serializes one source under a stable
// "===== kind (path) =====" header, newline-terminated so the next header
// always starts on its own line.
func writeSnapshotSource(b *strings.Builder, src gitConfigSource) {
	fmt.Fprintf(b, "===== %s (%s) =====\n", src.kind, src.path)
	b.Write(src.data)
	if len(src.data) == 0 || src.data[len(src.data)-1] != '\n' {
		b.WriteByte('\n')
	}
}

// Fingerprint returns the SHA-256 hex digest of Snapshot(): the identity of
// the configuration the user reviewed — any drift in any scanned source
// changes it (specs/decisions/034-git-trust-opt-out.md).
func (info *GitConfigInfo) Fingerprint() string {
	sum := sha256.Sum256(info.Snapshot())
	return hex.EncodeToString(sum[:])
}

// DiffGitConfigSnapshots returns a unified diff between two snapshots (""
// when byte-identical), one stream over the canonical text covering every
// source (specs/decisions/034-git-trust-opt-out.md).
func DiffGitConfigSnapshots(previous, current []byte) string {
	if bytes.Equal(previous, current) {
		return ""
	}
	a := strings.Split(string(previous), "\n")
	b := strings.Split(string(current), "\n")
	return unifiedDiff(a, b)
}

// --- semantic snapshot: drift detection over classified records ---
//
// The byte-level identity makes ANY edit (including inert branch/alias
// churn) a trust-drift event; the semantic layer narrows it to what the
// configuration MEANS: only DANGEROUS classified records serialize, in
// Snapshot()'s header format so diffs attribute unchanged
// (specs/decisions/070-git-semantic-trust.md).

// Source-kind labels for the attribute routing sources and include targets
// (the strings written into the snapshot diff headers).
const (
	sourceKindInfoAttributes     = "info/attributes"
	sourceKindCoreAttributesFile = "core.attributesFile"
	sourceKindInclude            = "include"
	sourceKindIncludeUnreadable  = "include (unreadable)"
)

// gitConfigRecord is one retained config entry: every key line the parser
// dispatched, in file order — the raw material of the semantic snapshot.
type gitConfigRecord struct {
	// Section is the lowercased section name ("core", "branch", ...).
	Section string
	// Subsection is the verbatim quoted subsection name (case-sensitive, as
	// git treats it); empty for plain sections.
	Subsection string
	// Key is the lowercased key name within the section.
	Key string
	// Value is the parsed value ("" for bare boolean keys, with
	// Boolean=true).
	Value string
	// Boolean reports a bare key (no '='), which git reads as boolean true.
	Boolean bool
	// Line is the 1-based line number of the occurrence.
	Line int
	// Source is the rawSources index of the layer this record was parsed
	// from: always 0 from parseGitConfigData; mergeGitConfigInfo shifts the
	// overlay's records when it appends the overlay's source.
	Source int
}

// gitConfigRecordClass classifies a retained record for the semantic
// snapshot: INERT may be left out of the identity, DANGEROUS must appear.
type gitConfigRecordClass int

const (
	gitConfigRecordInert gitConfigRecordClass = iota
	gitConfigRecordDangerous
)

// semanticInertSections is the explicit allowlist of sections whose every
// key is inert: branch.* is remote-tracking bookkeeping, alias.* only
// expands when git is invoked through the alias name (c0wrk never does).
// Tiny and CLOSED — anything unlisted classifies DANGEROUS, fail-closed;
// it may only shrink (specs/decisions/070-git-semantic-trust.md).
var semanticInertSections = map[string]bool{
	"branch": true,
	"alias":  true,
}

// classifyGitConfigRecord applies the inert allowlist (case-insensitive
// section match, subsections ignored); every non-listed record is
// DANGEROUS (specs/decisions/070-git-semantic-trust.md).
func classifyGitConfigRecord(r gitConfigRecord) gitConfigRecordClass {
	if semanticInertSections[strings.ToLower(r.Section)] {
		return gitConfigRecordInert
	}
	return gitConfigRecordDangerous
}

// SemanticSnapshot returns a canonical, diff-able serialization of the
// DANGEROUS subset, in Snapshot()'s source-header format: one canonical
// line per DANGEROUS record for config layers, verbatim bytes for sources
// dangerous as a whole (attribute routing, include targets — not
// key-scanned). Every layer keeps its header; record lines are escaped, so
// a hostile value cannot span or forge lines; deterministic
// (specs/decisions/070-git-semantic-trust.md). Nil info → nil.
func (info *GitConfigInfo) SemanticSnapshot() []byte {
	if info == nil {
		return nil
	}
	var b strings.Builder
	if len(info.rawSources) == 0 {
		// A pure-parse result (no source attachment): serialize the
		// dangerous records under a synthetic header for a stable identity;
		// no records, no header — there is no source to name.
		if lines := semanticRecordLines(info.records, 0); len(lines) > 0 {
			writeSnapshotSource(&b, gitConfigSource{kind: "config", data: []byte(strings.Join(lines, "\n") + "\n")})
		}
		return []byte(b.String())
	}
	for i, src := range info.rawSources {
		if isAttributeRoutingSourceKind(src.kind) {
			// Dangerous as a whole: the raw bytes stay in the identity.
			writeSnapshotSource(&b, src)
			continue
		}
		// A config layer: the header always appears (stable diff
		// attribution), carrying only its DANGEROUS records.
		writeSnapshotSource(&b, gitConfigSource{
			kind: src.kind,
			path: src.path,
			data: []byte(strings.Join(semanticRecordLines(info.records, i), "\n") + "\n"),
		})
	}
	for _, src := range info.includeSources {
		// Include targets are deliberately not key-scanned (ADR-033): the
		// raw bytes stay in the identity.
		writeSnapshotSource(&b, src)
	}
	return []byte(b.String())
}

// SemanticFingerprint returns the SHA-256 hex digest of SemanticSnapshot():
// the identity the trust lifecycle keys on — semantic drift evicts, inert
// churn refreshes (specs/decisions/070-git-semantic-trust.md).
func (info *GitConfigInfo) SemanticFingerprint() string {
	sum := sha256.Sum256(info.SemanticSnapshot())
	return hex.EncodeToString(sum[:])
}

// semanticRecordLines renders the canonical lines of the given layer's
// DANGEROUS records in file order (inert and other layers' records left out).
func semanticRecordLines(records []gitConfigRecord, source int) []string {
	var lines []string
	for i := range records {
		r := records[i]
		if r.Source != source || classifyGitConfigRecord(r) == gitConfigRecordInert {
			continue
		}
		lines = append(lines, canonicalGitConfigRecordLine(r))
	}
	return lines
}

// canonicalGitConfigRecordLine renders one record as a single deterministic
// line `[section "sub"] key = value`; a bare boolean key renders without
// "= value" (distinct from an empty value, "key =").
func canonicalGitConfigRecordLine(r gitConfigRecord) string {
	var b strings.Builder
	b.WriteByte('[')
	b.WriteString(r.Section)
	if r.Subsection != "" {
		b.WriteString(" \"")
		b.WriteString(escapeSnapshotRecordText(r.Subsection))
		b.WriteByte('"')
	}
	b.WriteString("] ")
	b.WriteString(r.Key)
	if !r.Boolean {
		if r.Value == "" {
			b.WriteString(" =")
		} else {
			b.WriteString(" = ")
			b.WriteString(escapeSnapshotRecordText(r.Value))
		}
	}
	return b.String()
}

// escapeSnapshotRecordText escapes the bytes that would break the
// one-record-one-line invariant (backslash first, keeping the mapping
// reversible).
func escapeSnapshotRecordText(s string) string {
	if !strings.ContainsAny(s, "\\\n\r") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// isAttributeRoutingSourceKind: sources dangerous as a whole, serialized
// verbatim into the semantic snapshot.
func isAttributeRoutingSourceKind(kind string) bool {
	return kind == sourceKindInfoAttributes || kind == sourceKindCoreAttributesFile
}

// --- raw-snapshot recovery: v1→v2 trust-record migration ---
//
// v1 records carry only the raw fingerprint + snapshot; the semantic
// identity is recovered from the STORED snapshot — never by re-scanning the
// repository (specs/decisions/070-git-semantic-trust.md).

// isConfigLayerSourceKind: config layers serialize as canonical DANGEROUS
// record lines; everything else (attribute routing, includes) verbatim.
func isConfigLayerSourceKind(kind string) bool {
	return kind == "config" || kind == "config.worktree"
}

// isKnownSnapshotSourceKind: recovery fails closed on unknown kinds — a
// snapshot carrying one was not produced by this scanner.
func isKnownSnapshotSourceKind(kind string) bool {
	switch kind {
	case "config", "config.worktree",
		sourceKindInfoAttributes, sourceKindCoreAttributesFile,
		sourceKindInclude, sourceKindIncludeUnreadable:
		return true
	}
	return false
}

// parseSnapshotHeader parses one "===== kind (path) =====" line; any other
// line stays data. The kind may contain parentheses ("include
// (unreadable)"), so the split lands on the LAST " (".
func parseSnapshotHeader(line string) (kind, path string, ok bool) {
	const prefix = "===== "
	const suffix = " ====="
	if len(line) < len(prefix)+len(suffix) ||
		!strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
		return "", "", false
	}
	inner := line[len(prefix) : len(line)-len(suffix)]
	idx := strings.LastIndex(inner, " (")
	if idx < 0 || !strings.HasSuffix(inner, ")") {
		return "", "", false
	}
	return inner[:idx], inner[idx+2 : len(inner)-1], true
}

// snapshotSection is one source section recovered from a snapshot's text.
type snapshotSection struct {
	kind string
	path string
	data []byte
}

// splitSnapshotSections splits a snapshot into per-source sections; data
// bytes are reproduced verbatim. Errors on non-well-formed snapshots (fail
// closed — specs/decisions/070-git-semantic-trust.md).
func splitSnapshotSections(snapshot []byte) ([]snapshotSection, error) {
	var (
		sections []snapshotSection
		cur      *snapshotSection
		data     strings.Builder
	)
	flush := func() {
		if cur != nil {
			cur.data = []byte(data.String())
			sections = append(sections, *cur)
			cur = nil
			data.Reset()
		}
	}
	lines := strings.Split(string(snapshot), "\n")
	for i, line := range lines {
		if i == len(lines)-1 && line == "" {
			break // the artifact of a trailing newline, not content
		}
		if kind, path, ok := parseSnapshotHeader(line); ok {
			flush()
			cur = &snapshotSection{kind: kind, path: path}
			continue
		}
		if cur == nil {
			return nil, errors.New("snapshot does not start with a source header")
		}
		data.WriteString(line)
		data.WriteByte('\n')
	}
	flush()
	for _, s := range sections {
		if !isKnownSnapshotSourceKind(s.kind) {
			return nil, fmt.Errorf("snapshot source %q has an unknown kind %q", s.path, s.kind)
		}
	}
	return sections, nil
}

// semanticSnapshotFromSnapshot reconstructs the semantic snapshot of a
// stored RAW snapshot: config layers re-parse and re-serialize as DANGEROUS
// canonical lines, attribute routing and includes stay verbatim —
// byte-identical to SemanticSnapshot() for scanner-produced snapshots
// (specs/decisions/070-git-semantic-trust.md).
func semanticSnapshotFromSnapshot(raw []byte, loggers ...*slog.Logger) ([]byte, error) {
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	sections, err := splitSnapshotSections(raw)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, sec := range sections {
		if !isConfigLayerSourceKind(sec.kind) {
			// Dangerous as a whole: verbatim, like SemanticSnapshot().
			writeSnapshotSource(&b, gitConfigSource(sec))
			continue
		}
		parsed := parseGitConfigData(string(sec.data), logger)
		writeSnapshotSource(&b, gitConfigSource{
			kind: sec.kind,
			path: sec.path,
			data: []byte(strings.Join(semanticRecordLines(parsed.records, 0), "\n") + "\n"),
		})
	}
	return []byte(b.String()), nil
}

// SemanticFingerprintFromSnapshot recovers the semantic fingerprint of a
// stored RAW snapshot (the v1→v2 migration path), returning fingerprint +
// bytes. An error means fail closed and evict — never carry an
// unverifiable trust over (specs/decisions/070-git-semantic-trust.md).
func SemanticFingerprintFromSnapshot(raw []byte, loggers ...*slog.Logger) (fingerprint string, semantic []byte, err error) {
	semantic, err = semanticSnapshotFromSnapshot(raw, loggers...)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(semantic)
	return hex.EncodeToString(sum[:]), semantic, nil
}

// ResolveIncludes follows the config's include/includeIf directives for
// FINGERPRINTING ONLY: target bytes land in includeSources so
// Snapshot/Fingerprint bind trust to the included files too. It never parses
// an included file for keys, never changes NeutralizingOverrides, and runs
// only on the trust/recheck path — the spawn scan never follows includes
// (ADR-033; specs/decisions/034-git-trust-opt-out.md). Targets fingerprint
// regardless of condition (a change to a conditionally-inactive file must
// still revoke trust; reimplementing wildmatch() risks skipping a file git
// reads). Bounded; missing → empty source, unreadable → marker, never an
// error, so later changes still show as drift.
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

// includeResolver walks include directives depth-first (bounded depth, file
// count, and per-file size; cycles broken via the visited set).
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
		// A missing include is inert for git; an empty source makes its
		// later appearance still show as drift.
		r.info.includeSources = append(r.info.includeSources, gitConfigSource{kind: sourceKindInclude, path: target})
		return
	case err != nil:
		// Unreadable, oversized, or non-regular: record a marker so state
		// changes still show as drift, without failing the repo.
		r.logger.Warn("git config include target could not be fingerprinted", "path", target, "error", err)
		r.info.includeSources = append(r.info.includeSources, gitConfigSource{kind: sourceKindIncludeUnreadable, path: target})
		return
	}

	r.info.includeSources = append(r.info.includeSources, gitConfigSource{kind: sourceKindInclude, path: target, data: data})

	// Recurse into the included file's own includes (parsed for include
	// records only, never dangerous keys) so transitive changes revoke trust.
	sub := parseGitConfigData(string(data), r.logger)
	subDir := filepath.Dir(target)
	for i := range sub.Includes {
		sub.Includes[i].SourceDir = subDir
	}
	r.resolveIncludes(sub.Includes, depth+1)
}

// resolveIncludePath resolves an include path the way git does: a leading ~/
// expands against the home directory, a relative path against the containing
// config's directory. "" for a blank value; always filepath.Clean-ed.
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

// expandHomeTilde expands "~"/"~/" to the current user's home directory;
// unresolvable homes and "~user/" stay verbatim (a safe no-op: the target
// just will not exist).
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

// maxLCSCells bounds the O(mn) LCS table; beyond it the fallback
// common-prefix/suffix diff keeps the operation memory-safe and correct.
const maxLCSCells = 1_000_000

// maxDiffBytes caps the emitted diff, so drift can never bloat the warning
// payload (specs/decisions/034-git-trust-opt-out.md).
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

// trimDiffOps is the memory-safe fallback: common prefix/suffix kept,
// everything in between deleted+added — coarser but always correct.
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

// renderUnifiedDiff groups an edit script into hunks (3 context lines, @@
// range headers); changes whose context regions overlap or touch share one
// hunk, so context never repeats — standard unified-diff grouping.
func renderUnifiedDiff(ops []diffOp) string {
	const ctx = 3
	var out strings.Builder
	out.WriteString("--- previous\n+++ current\n")
	n := len(ops)
	for i := 0; i < n; {
		if ops[i].kind == diffEqual {
			i++
			continue
		}
		start, back := i, 0
		for start > 0 && ops[start-1].kind == diffEqual && back < ctx {
			start--
			back++
		}
		last := i
		for {
			end := last
			for end < n && ops[end].kind != diffEqual {
				end++
			}
			j := end
			for j < n && ops[j].kind == diffEqual {
				j++
			}
			if j >= n || j-end > ctx*2 {
				last = end - 1
				break
			}
			last = j
		}
		end := last + 1
		for fwd := 0; fwd < ctx && end < n && ops[end].kind == diffEqual; fwd++ {
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

// lineNumberBefore returns the 1-based old/new line number at ops[pos]
// (deletes count only old-ward, inserts only new-ward, equals both).
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
// findings — the Layer-2 additions on top of the sysproc.GitCmd baseline:
// per-name pins for every armed filter (process= + clean/smudge=cat), merge
// driver ("false %O %A %B"), textconv (cat) and external diff driver (empty
// — plain `git diff` executes these by default); the transport keys
// (core.sshCommand=ssh, core.askPass=, credential.helper= + per-URL pin);
// core.attributesFile= plus per-name pins for the attribute sources attr.tree
// does not cover; and the blanket attr.tree=<object-format empty tree> ONLY
// for include-bearing configs, where per-name pins cannot see every driver
// name (collateral disclosed via AttributesInterpretationDisabled).
// core.pager needs no override (pagers need a terminal); core.worktree has
// no -c beat (see NeedsWorkTreeEnvPin). Deduplicated, sorted; nil when clean.
// Values and canary verification: specs/decisions/033-git-subprocess-hardening.md.
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
			// Narrow mode ([56]): per-name pins cover every routing source,
			// so no blanket attr.tree kill (it would disable benign
			// eol/text attributes too).
			filterNames[f.Subsection] = true
		case GitConfigFindingMergeDriver:
			set["merge."+f.Subsection+".driver"] = neutralMergeDriverValue
		case GitConfigFindingTextConv:
			set["diff."+f.Subsection+".textconv"] = neutralPassthroughValue
		case GitConfigFindingDiffCommand:
			// External diff drivers execute on plain `git diff` by default
			// (--no-ext-diff is what disables them); the per-key empty kill
			// covers every routing source without attr.tree ([56]).
			if f.Subsection == "" {
				set["diff.external"] = ""
			} else {
				set["diff."+f.Subsection+".command"] = ""
			}
		case GitConfigFindingSSHCommand:
			// "ssh" restores the default binary and keeps remotes working;
			// an empty value would abort them ("cannot run :").
			set["core.sshCommand"] = "ssh"
		case GitConfigFindingAskPass:
			set["core.askPass"] = ""
		case GitConfigFindingGitProxy:
			// The proxy command cannot be neutralized; kill the transport
			// family instead — the operation fails closed.
			set[gitProxyAllowKey] = gitProxyDenyValue
		case GitConfigFindingCredential:
			// The -c layer is read after every file, so the empty reset
			// lands after all configured helpers; the per-URL pin is
			// belt-and-braces against url-match normalization subtleties.
			set["credential.helper"] = ""
			if f.Subsection != "" {
				set["credential."+f.Subsection+".helper"] = ""
			}
		case GitConfigFindingAttrTree:
			set[attrTreeKey] = info.emptyTreeHash()
		case GitConfigFindingAttributesFile:
			// Empty disables the routing source entirely (verified).
			set[attributesFileKey] = ""
		case GitConfigFindingAttrRouting:
			// Sources attr.tree cannot cover: pin the routed name (-c beats
			// file config wherever the driver is defined).
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
		// The one case per-name pins cannot cover: an included file may
		// define a driver whose name the scan never sees — the blanket kill
		// stays engaged, with its documented collateral (review [56]).
		set[attrTreeKey] = info.emptyTreeHash()
	}
	if len(info.Includes) > 0 {
		// The config is not fully visible: an included file may set any of
		// these keys unseen. Name-independent pins (same values the finding
		// path derives) — deliberately NOT unconditional, since a -c pin
		// beats the user's own global config. An include-hidden
		// core.gitProxy kills the transport family like the finding path
		// (review [147]).
		set[attributesFileKey] = ""
		set["core.sshCommand"] = "ssh"
		set["core.askPass"] = ""
		set["credential.helper"] = ""
		set["diff.external"] = ""
		set[gitProxyAllowKey] = gitProxyDenyValue
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

// AttributesInterpretationDisabled reports whether the derived set engages
// the blanket attr.tree kill — the review-[56] collateral where benign
// eol/text attributes stop applying. False in narrow mode; true only for
// include-bearing configs or a repo-set attr.tree
// (specs/decisions/033-git-subprocess-hardening.md).
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

// NeedsWorkTreeEnvPin reports whether the spawn environment's GIT_WORK_TREE
// must be pinned to the discovered work-tree root — the one channel that
// outranks the config key (no `-c` form beats core.worktree). Derived for a
// scanned core.worktree finding and for include-bearing configs (review
// [148]: an included file can hide the key and redirect tracked-file
// writes); a no-op for an honest repository
// (specs/decisions/033-git-subprocess-hardening.md).
func (info *GitConfigInfo) NeedsWorkTreeEnvPin() bool {
	if info == nil {
		return false
	}
	for i := range info.Findings {
		if info.Findings[i].Kind == GitConfigFindingWorkTree {
			return true
		}
	}
	return len(info.Includes) > 0
}

// emptyTreeHash returns the empty-tree hash matching the repository's object
// format (EmptyTreeSHA256 for sha256, EmptyTreeSHA1 otherwise); unknown
// formats are refused upstream, since an invalid hash is a silent no-op.
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

// ScanGitConfig safely reads and parses (text-only, never spawns) the config
// of the repository git would discover for repoRoot: the .git chain walks up
// (subdirectory workspaces covered), .git may be a directory or "gitdir:"
// pointer, no .git → empty result; linked worktrees scan the COMMON config
// (commondir file) plus the enabled config.worktree overlay, merged with
// git's layering ([2]a); attribute sources ([1]a) and the object format
// ([1]d) are validated as the attr.tree kill needs — unscannable source or
// unknown format fails the scan closed; missing/oversized config is an
// error; ignored includes log warnings
// (specs/decisions/033-git-subprocess-hardening.md).
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
		// config.worktree only matters when extensions.worktreeConfig is
		// enabled; a planted-but-disabled file is inert, not scanned.
		wtPath := filepath.Join(gitDir, "config.worktree")
		if _, statErr := os.Lstat(wtPath); statErr == nil {
			wt, wtErr := ScanGitConfigFile(wtPath, logger)
			if wtErr != nil {
				return nil, wtErr
			}
			mergeGitConfigInfo(info, wt)
			info.WorktreeConfigPath = wtPath
			// Retain the standalone parse for WorktreeOverlayClean — only
			// it attributes keys/includes/errors to the overlay alone.
			info.worktreeOverlay = wt
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

	// Runs after the overlay merge and attribute scan, so commit.gpgsign
	// armed in ANY layer keeps the siblings live (over-armed beats
	// under-armed; this only shapes the intake warning).
	markInertSigningSiblings(info)
	return info, nil
}

// signSiblingKeys are the baseline-covered signing keys that shape (or name)
// the signing program but never execute on their own: git runs a signing
// program only when commit.gpgsign is armed.
var signSiblingKeys = map[string]bool{
	"gpg.format":      true,
	"gpg.program":     true,
	"user.signingkey": true,
}

// markInertSigningSiblings flags the signing siblings Inert when no layer
// arms commit.gpgsign (specs/domains/workspace.md); commit.gpgsign itself
// stays unconditionally emitted (truthy-gated).
func markInertSigningSiblings(info *GitConfigInfo) {
	if info == nil {
		return
	}
	for i := range info.Findings {
		if info.Findings[i].Kind == GitConfigFindingSigning &&
			info.Findings[i].FullKey == "commit.gpgsign" {
			return // armed: the siblings stay live for this repository
		}
	}
	for i := range info.Findings {
		if info.Findings[i].Kind == GitConfigFindingSigning && signSiblingKeys[info.Findings[i].FullKey] {
			info.Findings[i].Inert = true
		}
	}
}

// resolveCommonGitDir resolves the shared git directory, mirroring
// `git rev-parse --git-common-dir`: a commondir file names it; without one
// the repository is self-contained. A worktree gitdir without a readable
// commondir fails closed (the hidden common config is what git executes).
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

// mergeGitConfigInfo folds the overlay (a later config layer) into base with
// git's layering semantics: overlay value wins on identical keys (findings
// deduped by FullKey), includes/errors append, and the repository-model
// state layers by KEY PRESENCE — the overlay overrides only keys it sets.
// Presence is the point: config.worktree routinely omits
// extensions.objectformat, and letting the omission zero the common "sha256"
// would downgrade the attr.tree hash to SHA-1 — a silent no-op kill there.
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
	// Records carry over with their source index shifted: the overlay's
	// records land at len(base.rawSources) once its sources are appended.
	shift := len(base.rawSources)
	base.rawSources = append(base.rawSources, overlay.rawSources...)
	for i := range overlay.records {
		r := overlay.records[i]
		r.Source += shift
		base.records = append(base.records, r)
	}
	if overlay.repositoryFormatVersionSet {
		base.repositoryFormatVersion = overlay.repositoryFormatVersion
		base.repositoryFormatVersionSet = true
	}
	if overlay.objectFormatSet {
		base.objectFormat = overlay.objectFormat
		base.objectFormatSet = true
	}
	if overlay.worktreeConfigEnabledSet {
		base.worktreeConfigEnabled = overlay.worktreeConfigEnabled
		base.worktreeConfigEnabledSet = true
	}
	if overlay.attributesFilePathSet {
		base.attributesFilePath = overlay.attributesFilePath
		base.attributesFilePathSet = true
	}
}

// validateRepoFormat fails closed on models whose attribute routing this
// scanner cannot reproduce (repositoryformatversion > 1, unknown
// extensions.objectformat): the format picks the attr.tree hash, and a
// wrong hash is a silent no-op (specs/decisions/033-git-subprocess-hardening.md).
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

// scanAttributeSources scans the attribute-routing files attr.tree cannot
// neutralize: <commonDir>/info/attributes (shared by every worktree) and the
// core.attributesFile target; routed names become findings pinned by NAME.
// No kill-switch exists: an unreadable source is a hard error (invisible
// routing is never an option); a missing source is inert
// (specs/decisions/033-git-subprocess-hardening.md).
func scanAttributeSources(repoRoot, commonDir string, info *GitConfigInfo, logger *slog.Logger) error {
	type source struct {
		path, label string
	}
	sources := []source{{filepath.Join(commonDir, "info", "attributes"), sourceKindInfoAttributes}}
	if raw := info.attributesFilePath; raw != "" {
		if resolved := resolveAttributesFilePath(raw, repoRoot); resolved != "" {
			sources = append(sources, source{resolved, sourceKindCoreAttributesFile})
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

// resolveAttributesFilePath resolves core.attributesFile like git resolves
// core.excludesFile: ~/ against home, relative against the directory git
// runs in (repoRoot — cmd.Dir); unresolvable home stays verbatim.
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

// parseAttributesRouting extracts command-routing directives (filter=/merge=/
// diff=<name>) from gitattributes-format files; macro lines covered.
// Disarming forms (-filter=x, !filter) are not findings: routing off, not on.
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
		// Both command-bearing keys are pinned: textconv and the external
		// command the git diff porcelain runs by default.
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

// ScanGitConfigFile parses one specific git config file (text-only): missing
// yields an empty result, unreadable/oversized an error (fail closed). See
// ScanGitConfig for details.
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
	// Record this file's directory: includes resolve relative paths against it.
	sourceDir := filepath.Dir(configPath)
	for i := range info.Includes {
		info.Includes[i].SourceDir = sourceDir
	}
	return info, nil
}

// sourceKind labels a config path for the snapshot diff header: the
// per-worktree overlay is distinguishable from the common config by basename.
func sourceKind(configPath string) string {
	if filepath.Base(configPath) == "config.worktree" {
		return "config.worktree"
	}
	return "config"
}

// resolveGitDir resolves the git directory whose config git itself would use
// for repoRoot: walks up (symlinks resolved, mirroring git's chdir-based
// discovery) until a .git entry — a .git-less workspace root must not
// silently skip neutralization while git runs the parent repository's
// config. "" when none (specs/decisions/033-git-subprocess-hardening.md).
func resolveGitDir(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", errors.New("cannot resolve git dir for an empty path")
	}
	// Symlinks resolved so the walk matches git's chdir-based discovery; on
	// error fall back to the lexical form (callers handle no-repo).
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

// ResolveWorkTreeRoot returns the work-tree root git would discover from
// path (first .git-carrying directory on the chain) — the trust-attribution
// form: warning and trusted_git_repos key on the repository root, not the
// scanned subdirectory (review [52]). Deliberately LEXICAL: the root stays
// in the path form the user knows, and attribution + trust list normalize
// through this one function so they agree. "" → callers fall back to path.
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

// ResolveGitTrustRoot returns the path form the git TRUST decision keys on:
// the repository's MAIN work-tree root. A linked worktree's commit surface
// (hooks, common config, signing keys) is shared by every worktree, so the
// decision must key on the root those programs come from — otherwise a
// trusted repository's worktree stays untrusted and "Trust & commit" loops
// (review [52]; specs/decisions/034-git-trust-opt-out.md). LEXICAL like
// ResolveWorkTreeRoot; every hop degrades to its form when the pointer
// chain cannot be resolved — nothing widens trust. "" when no .git exists.
func ResolveGitTrustRoot(path string) string {
	root := ResolveWorkTreeRoot(path)
	if root == "" {
		return ""
	}
	gitDir, err := resolveGitDirAt(root)
	if err != nil || gitDir == "" {
		return root
	}
	commonDir, isWorktree, err := resolveCommonGitDir(gitDir)
	if err != nil || !isWorktree {
		return root
	}
	// Only the standard layout maps back lexically (common dir at
	// <mainRoot>/.git); verify the .git entry before returning, so a
	// hostile commondir cannot redirect the trust key outside a repository.
	if filepath.Base(commonDir) != ".git" {
		return root
	}
	mainRoot := filepath.Dir(commonDir)
	if _, err := os.Lstat(filepath.Join(mainRoot, ".git")); err != nil {
		return root
	}
	return mainRoot
}

// canonicalizePathBestEffort returns the symlink-resolved spelling of path,
// or path itself when unresolvable — trust comparisons need both spellings
// (user-facing paths stay lexical; worktree chains carry canonical git paths).
func canonicalizePathBestEffort(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// TrustProbePaths returns every path form a trust decision for path may be
// stored or checked under: the cleaned path, its canonical spelling, both
// resolved roots (trust root, work-tree root) and their canonical spellings
// — deduped, first occurrence first. The backend trust list and the
// core/gittrust spawn registry probe ALL of them, so a trust recorded under
// any one form is honored under every other; every candidate is the same
// repository's root or the probed path itself, so no prefix/subtree matching
// ever crosses a repository boundary.
// See specs/domains/workspace.md (trust path probing).
func TrustProbePaths(path string) []string {
	cleaned := filepath.Clean(path)
	seen := make(map[string]struct{})
	out := make([]string, 0, 6)
	add := func(p string) {
		if p == "" {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	add(cleaned)
	add(canonicalizePathBestEffort(cleaned))
	for _, resolve := range []func(string) string{ResolveGitTrustRoot, ResolveWorkTreeRoot} {
		if root := resolve(cleaned); root != "" {
			add(root)
			add(canonicalizePathBestEffort(root))
		}
	}
	return out
}

// readCapped reads a regular file, refusing input beyond limit bytes.
// safeio.Open enforces regularity with NO Stat→Open window (review [14]): a
// racing FIFO would block the open forever on the synchronous SwitchProject
// scan path; non-regular files are refused by the open-descriptor fstat.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := safeio.Open(path)
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

// parseGitConfigData parses git-config text into findings; it never fails —
// malformed constructs land in info.Errors and parsing continues (git
// refuses such files wholesale, so continuing only over-reports).
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
// form; section/key names lowercase, quoted subsections stay verbatim and
// case-sensitive, dotted subsections lowercase (matching git).
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
		// git refuses entries outside any section — record it so Clean()
		// stays false and intake warns.
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
			// Error recorded; keep the partial value (over-report is safe).
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
		// Whitespace appends only once the value is non-empty (leading ws
		// after '=' disappears).
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
			// One escape switch in and out of quotes: the backslash
			// consumes the very next character. Backslash-newline is a
			// continuation that vanishes while surrounding ws is kept.
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
				// git refuses the file on unknown escapes; we record the
				// error and keep the characters (over-reporting is safe).
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

// parseQuotedSegment reads one "..." segment: git's escape switch applies
// inside quotes; a backslash before a raw newline continues the value.
// Unknown escapes are recorded and kept; an unterminated raw newline makes
// git refuse the file — it lands in Errors (over-report is the safe side).
func (p *gitConfigParser) parseQuotedSegment(b *strings.Builder) bool {
	p.pos++ // consume opening quote
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		switch c {
		case '"':
			p.pos++
			return true
		case '\n':
			// A raw newline makes git refuse the whole file, so it must
			// land in Errors — without it a config whose only anomaly is
			// this stays Clean() and intake never warns (review [63]).
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
	// Retain every dispatched entry BEFORE routing narrows the picture:
	// retention is unconditional, classification happens at serialization.
	p.info.records = append(p.info.records, gitConfigRecord{
		Section:    p.section,
		Subsection: p.subsection,
		Key:        key,
		Value:      value,
		Boolean:    boolean,
		Line:       line,
	})
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
				p.info.repositoryFormatVersionSet = true
			}
		case "attributesfile":
			if boolean {
				p.errorf(line, "core.attributesFile is not a boolean key")
				break
			}
			p.info.attributesFilePath = value
			p.info.attributesFilePathSet = true
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
			p.info.objectFormatSet = true
		case "worktreeconfig":
			p.info.worktreeConfigEnabled = parseBoolConfigValue(value, boolean)
			p.info.worktreeConfigEnabledSet = true
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
	case "commit":
		// Truthy-gated: gpgsign=false (the baseline's own pin) must keep a
		// config Clean; only an ARMED (true/yes/on/1/bare) commit.gpgsign
		// is reported, as a baseline-covered finding with no override.
		if key == "gpgsign" && parseBoolConfigValue(value, boolean) {
			kind = GitConfigFindingSigning
			baselineCovered = true
			description = "commit.gpgsign turns on commit signing, which makes git run the signing " +
				"program (gpg.program or gpg itself, consuming user.signingkey) during commit. " +
				"Neutralized by the baseline -c commit.gpgsign=false, which c0wrk forces on every git " +
				"invocation; reported so the intake picture shows the repo tried to sign commits."
		}
	case "gpg":
		if key == "format" || key == "program" {
			kind = GitConfigFindingSigning
			baselineCovered = true
			if key == "format" {
				description = "gpg.format selects the signing backend for commit and tag signing (openpgp, " +
					"x509, ssh); combined with an armed signing key it shapes which external program git " +
					"invokes. Neutralized by the baseline -c commit.gpgsign=false, which c0wrk forces on " +
					"every git invocation; reported so the intake picture shows the repo configured signing."
			} else {
				description = "gpg.program names the binary git executes to sign commits and tags — a " +
					"repository-configured command vector on signing operations. Neutralized by the " +
					"baseline -c commit.gpgsign=false, which c0wrk forces on every git invocation; reported " +
					"so the intake picture shows the repo configured signing."
			}
		}
	case "user":
		if key == "signingkey" {
			kind = GitConfigFindingSigning
			baselineCovered = true
			description = "user.signingkey names the key (or, for gpg.format=ssh, an arbitrary path or " +
				"literal key) git passes to the signing program on commit/tag signing. Neutralized by the " +
				"baseline -c commit.gpgsign=false, which c0wrk forces on every git invocation; reported so " +
				"the intake picture shows the repo configured signing."
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
		// The per-name pin is the whole neutralization (review [56]).
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
