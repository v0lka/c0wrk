package workspace

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
// command-relevant section families — core, attr, filter.<name>,
// merge.<name>, diff.<name>. include/includeIf directives are parsed, logged
// and recorded but deliberately NOT followed: the included files' contents are
// unknown, so a config with includes must be treated as partially invisible
// (NeutralizingOverrides compensates with the attribute-routing kill).
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
// equivalent, which requires a runtime probe outside this exec-free module)
// may be used.
const EmptyTreeSHA1 = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// maxGitConfigBytes caps how much of a .git/config (or .git gitdir pointer
// file) is read. Real-world configs are a few KiB; anything larger is hostile
// input, not configuration to be parsed.
const maxGitConfigBytes = 4 << 20 // 4 MiB

const maxGitDirPointerBytes = 4096

// Finding kinds. These strings are stable identifiers intended for UI
// rendering by the intake scanner.
const (
	GitConfigFindingFSMonitor   = "fsmonitor"      // core.fsmonitor / core.fsmonitorHook
	GitConfigFindingHooksPath   = "hooks_path"     // core.hooksPath
	GitConfigFindingEditor      = "editor"         // core.editor
	GitConfigFindingPager       = "pager"          // core.pager
	GitConfigFindingSSHCommand  = "ssh_command"    // core.sshCommand
	GitConfigFindingAskPass     = "askpass"        // core.askPass
	GitConfigFindingAttrTree    = "attr_tree"      // attr.tree
	GitConfigFindingFilter      = "filter_command" // filter.<n>.process|clean|smudge
	GitConfigFindingMergeDriver = "merge_driver"   // merge.<n>.driver
	GitConfigFindingTextConv    = "textconv"       // diff.<n>.textconv
	GitConfigFindingDiffCommand = "diff_command"   // diff.<n>.command / diff.external
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
	// .git was found).
	GitDir string
	// ConfigPath is the config file that was parsed ("" when none was found).
	ConfigPath string
	// Findings lists every dangerous key occurrence, in file order.
	Findings []GitConfigFinding
	// Includes lists include/includeIf directives (parsed, logged, not
	// followed).
	Includes []GitConfigInclude
	// Errors lists malformed constructs (the parser continues past them).
	Errors []GitConfigError
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

// NeutralizingOverrides derives the per-repo neutralizing `-c` set from the
// findings — the Layer-2 additions that go on top of the unconditional
// sysproc.GitCmd baseline when running git inside this repository:
//
//   - one filter.<n>.process= + filter.<n>.clean=cat + filter.<n>.smudge=cat
//     triple per armed filter name (process= alone would fall back to
//     attacker-supplied clean/smudge),
//   - merge.<n>.driver="false %O %A %B" per custom merge driver,
//   - diff.<n>.textconv=cat per armed textconv,
//   - attr.tree=<EmptyTreeSHA1> whenever any attribute-routed vector exists
//     (verified to kill worktree-.gitattributes routing for all of them) and
//     additionally when include directives were recorded (config not fully
//     visible — unknown filter names can only be covered by the routing kill),
//   - the attacker's own attr.tree, if set, is beaten by our attr.tree
//     override (-c wins over file config).
//
// Keys that the GitCmd baseline already covers and keys with no verified
// per-key neutralization (pager, sshCommand, askPass, external diff drivers —
// all unreachable from c0wrk's local, piped git usage) produce no overrides.
// The result is deduplicated and sorted by key for deterministic output; nil
// for a clean config.
func (info *GitConfigInfo) NeutralizingOverrides() []GitConfigOverride {
	if info == nil {
		return nil
	}
	set := map[string]string{}
	needAttrTree := false
	filterNames := map[string]bool{}
	for i := range info.Findings {
		f := &info.Findings[i]
		switch f.Kind {
		case GitConfigFindingFilter:
			filterNames[f.Subsection] = true
			needAttrTree = true
		case GitConfigFindingMergeDriver:
			set["merge."+f.Subsection+".driver"] = neutralMergeDriverValue
			needAttrTree = true
		case GitConfigFindingTextConv:
			set["diff."+f.Subsection+".textconv"] = neutralPassthroughValue
			needAttrTree = true
		case GitConfigFindingDiffCommand:
			// External diff drivers only run with --ext-diff (never passed
			// by c0wrk), but their attribute routing is killed by
			// attr.tree=<empty tree> for defense in depth.
			needAttrTree = true
		case GitConfigFindingAttrTree:
			set[attrTreeKey] = EmptyTreeSHA1
		}
	}
	for name := range filterNames {
		set["filter."+name+".process"] = ""
		set["filter."+name+".clean"] = neutralPassthroughValue
		set["filter."+name+".smudge"] = neutralPassthroughValue
	}
	if needAttrTree || len(info.Includes) > 0 {
		set[attrTreeKey] = EmptyTreeSHA1
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

// ScanGitConfig safely reads and parses the git config of the repository
// git itself would discover for repoRoot (text-only; it never spawns a
// process). Discovery mirrors `git -C repoRoot`: the .git chain is walked up
// from repoRoot, so a workspace rooted at a subdirectory of a repository is
// covered too. .git may be a directory or a "gitdir: ..." pointer file
// (worktrees; relative pointers resolve against the directory holding the
// .git entry); no .git anywhere on the chain yields an empty result, not an
// error. A missing or oversized config file inside a resolved git dir is
// reported as an error so callers can fail closed. The optional logger
// receives a warning line for every ignored include/includeIf directive.
func ScanGitConfig(repoRoot string, loggers ...*slog.Logger) (*GitConfigInfo, error) {
	gitDir, err := resolveGitDir(repoRoot)
	if err != nil {
		return nil, err
	}
	if gitDir == "" {
		return &GitConfigInfo{}, nil
	}
	return ScanGitConfigFile(filepath.Join(gitDir, "config"), loggers...)
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
	return info, nil
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

// readCapped reads a regular file, refusing input beyond limit bytes.
// Anything that is not a regular file (FIFO, device, socket, directory) is
// refused before opening: a FIFO planted as .git/config (or as the .git
// pointer file) would otherwise block the open forever, hanging the
// synchronous intake scan on the SwitchProject RPC path.
func readCapped(path string, limit int64) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
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
				"'false %O %A %B' (a real conflict is recorded without running attacker code); the " +
				"attr.tree override also removes the attribute routing."
		}
	case "diff":
		if p.subsection != "" && (key == "textconv" || key == "command") ||
			(p.subsection == "" && key == "external") {
			if key == "textconv" {
				kind = GitConfigFindingTextConv
				description = "diff." + p.subsection + ".textconv is executed to render blob content and runs " +
					"by default in plain git diff (no flag needed). Neutralized by forcing textconv=cat; " +
					"the attr.tree override also removes the attribute routing."
			} else {
				kind = GitConfigFindingDiffCommand
				driver := p.subsection
				if driver == "" {
					driver = "external"
				}
				description = "diff." + driver + " is an external diff driver; it only executes with the " +
					"--ext-diff flag, which c0wrk never passes, and its attribute routing is killed by the " +
					"attr.tree override. No per-key -c neutralization is required."
			}
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
	case GitConfigFindingMergeDriver:
		finding.Overrides = []GitConfigOverride{{Key: "merge." + p.subsection + ".driver", Value: neutralMergeDriverValue}}
	case GitConfigFindingTextConv:
		finding.Overrides = []GitConfigOverride{{Key: "diff." + p.subsection + ".textconv", Value: neutralPassthroughValue}}
		// The verified filter recipe also requires the cat fallbacks and the
		// routing kill; those are assembled centrally in NeutralizingOverrides
		// per subsection.
	case GitConfigFindingFilter:
		finding.Overrides = []GitConfigOverride{
			{Key: "filter." + p.subsection + ".process", Value: ""},
			{Key: "filter." + p.subsection + ".clean", Value: neutralPassthroughValue},
			{Key: "filter." + p.subsection + ".smudge", Value: neutralPassthroughValue},
		}
	}
	p.info.Findings = append(p.info.Findings, finding)
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
			"core.fsmonitorHook is a deprecated alias of core.fsmonitor holding an external monitor " +
				"command executed on index refresh. Covered by the c0wrk git baseline (-c core.fsmonitor=false).",
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
			"core.sshCommand names the SSH binary/command used by network transport (fetch/push/clone). " +
				"c0wrk only runs local git operations against workspaces, so it is not reachable; no -c " +
				"override is required.",
			false
	case "askpass":
		return GitConfigFindingAskPass,
			"core.askPass names a password-prompt helper executed by network transports on authentication. " +
				"c0wrk only runs local git operations against workspaces, so it is not reachable; no -c " +
				"override is required.",
			false
	}
	return "", "", false
}

// filterKeyDescription explains one filter sub-key.
func filterKeyDescription(key, name string) string {
	switch key {
	case "process":
		return "filter." + name + ".process runs a long-running external filter process during check-in and " +
			"check-out — on add, on commit (even --allow-empty on a clean tree), and on status/diff whenever " +
			"hashing is forced. Neutralized by forcing process= (empty): git then falls back to clean/smudge, " +
			"which the accompanying clean=cat/smudge=cat overrides pin to a safe passthrough; the attr.tree " +
			"override additionally removes the attribute routing that activates the filter."
	case "clean":
		return "filter." + name + ".clean rewrites file content on check-in hashing (add/commit). Neutralized " +
			"by forcing clean=cat alongside process=; the attr.tree override removes the attribute routing " +
			"that activates the filter."
	default: // smudge
		return "filter." + name + ".smudge rewrites blob content on check-out. Neutralized by forcing " +
			"smudge=cat alongside process=; the attr.tree override removes the attribute routing that " +
			"activates the filter."
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
