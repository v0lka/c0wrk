package specs

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/v0lka/sp4rk/safeio"
)

// This file implements the content-parsing layer of the specs package: pure
// parsers over already-read file contents (unit-testable with plain string
// fixtures) and ParseSpecsRoot — the only function touching the filesystem,
// and it only ever reads (the core/research parser layout).

// sectionDirs is the walk order of the four section directories (META.md
// "File Organization"); Files is re-sorted by path afterwards.
var sectionDirs = []string{SectionArchitecture, SectionDomains, SectionContracts, SectionDecisions}

// maxSpecBytes bounds a single spec read: the tree is workspace content
// feeding read-only status RPCs, and safeio.ReadFileLimited refuses an
// oversized (hostile or accidental) file instead of reading it whole. 8 MiB
// is far above any real spec (the largest today is ~0.4 MiB).
const maxSpecBytes = 8 << 20

// markdownLinkRe matches an inline markdown link, capturing the target
// ("[text](target)"). Targets containing spaces or parentheses do not match
// — no documented cross-reference uses that shape (META.md: relative paths
// plus an optional #anchor).
var markdownLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^()\s]+)\)`)

// adrFileNameRe splits a decisions/ file name into number and slug
// ("105-specs-sidebar-panel.md" → "105", "specs-sidebar-panel").
var adrFileNameRe = regexp.MustCompile(`^(\d+)-(.+)\.md$`)

// adrTitleRe matches the template's ADR heading ("# ADR-NNN: Title"),
// capturing the title.
var adrTitleRe = regexp.MustCompile(`(?m)^#\s+ADR-\d+:\s*(.+)$`)

// bracketedADRRe matches a bracketed successor reference ("[084]",
// "[ADR-055]") in a raw status line. Only 1-3 digits, so a bracketed year
// ("[2026]") never matches.
var bracketedADRRe = regexp.MustCompile(`\[(?:ADR-)?(\d{1,3})\]`)

// adrRefRe matches a plain "ADR-055" successor reference in a raw status
// line.
var adrRefRe = regexp.MustCompile(`ADR-(\d{1,3})`)

// tableSeparatorCellRe matches one markdown alignment marker cell (":---",
// "---", "---:").
var tableSeparatorCellRe = regexp.MustCompile(`^:?-+:?$`)

// -- markdown primitives --

// atxHeading recognizes one ATX heading line and returns its text and
// level; anything else (including "#NoSpace") is not a heading.
func atxHeading(line string) (text string, level int, ok bool) {
	lvl := 0
	for lvl < len(line) && line[lvl] == '#' {
		lvl++
	}
	if lvl == 0 || lvl > 6 {
		return "", 0, false
	}
	rest := line[lvl:]
	if rest != "" && !strings.HasPrefix(rest, " ") {
		return "", 0, false
	}
	return strings.TrimSpace(rest), lvl, true
}

// plainText removes fenced code blocks (line-replaced) and inline code
// spans (span-replaced) from markdown content, preserving the line
// structure. Headings, links and table rows are read from the prose only —
// without this, META.md's inline link-format examples
// ("`[display text](relative/path.md)`") would parse as live
// cross-references.
func plainText(content string) string {
	lines := strings.Split(content, "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			lines[i] = ""
			continue
		}
		if inFence {
			lines[i] = ""
			continue
		}
		lines[i] = stripInlineCode(line)
	}
	return strings.Join(lines, "\n")
}

// stripInlineCode replaces every `...` span of the line with a space.
// Unmatched backticks swallow the rest of the line — acceptable for
// well-formed documents, and under-stripping can only miss links, never
// invent them.
func stripInlineCode(line string) string {
	var b strings.Builder
	inSpan := false
	for _, r := range line {
		switch {
		case r == '`':
			inSpan = !inSpan
			b.WriteRune(' ')
		case inSpan:
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ParseTitle returns the first ATX heading text of the document, outside
// code blocks; "" when there is none.
func ParseTitle(content string) string {
	for _, heading := range ParseHeadings(content) {
		return heading
	}
	return ""
}

// ParseHeadings returns every ATX heading text outside fenced code blocks,
// in document order — the anchor namespace for the CROSS_REF_BROKEN check.
// Inline code delimiters are stripped but their content is kept: a heading
// like "Mention gating (`manual` mode)" slugs to
// "mention-gating-manual-mode", exactly as renderers anchor it.
func ParseHeadings(content string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if heading, _, ok := atxHeading(trimmed); ok {
			out = append(out, stripBackticks(heading))
		}
	}
	return out
}

// stripBackticks removes every ` character, keeping the enclosed content.
func stripBackticks(s string) string {
	return strings.ReplaceAll(s, "`", "")
}

// SlugifyHeading converts heading text to its anchor slug the way markdown
// renderers do: lower-cased, everything that is not a letter, digit, space,
// hyphen or underscore dropped (an em-dash vanishes, leaving the two
// surrounding spaces as a double hyphen; parens and backticks vanish), then
// every space folded to a hyphen.
func SlugifyHeading(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == ' ', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return strings.ReplaceAll(b.String(), " ", "-")
}

// headingSlugs maps every heading of the content to its slug.
func headingSlugs(content string) map[string]bool {
	headings := ParseHeadings(content)
	if len(headings) == 0 {
		return nil
	}
	slugs := make(map[string]bool, len(headings))
	for _, h := range headings {
		slugs[SlugifyHeading(h)] = true
	}
	return slugs
}

// sectionBody returns the body of the first ATX section named name ("## Key
// Files" for "Key Files") at any level: the raw lines after the heading up
// to the next heading of the same or a higher level — sub-headings (###
// inside ##) belong to the section. Section boundaries are fence-aware, so
// a template heading inside a fenced block never ends a section; the body
// itself is returned raw (Key Files entries keep their backticks).
func sectionBody(content, name string) string {
	var out []string
	inSection := false
	inFence := false
	sectionLevel := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if inSection {
				out = append(out, line)
			}
			inFence = !inFence
			continue
		}
		heading, level, ok := atxHeading(trimmed)
		if !inFence && ok {
			if inSection {
				if level <= sectionLevel {
					break
				}
				continue // a deeper sub-heading stays inside the section
			}
			if strings.EqualFold(heading, name) {
				inSection = true
				sectionLevel = level
			}
			continue
		}
		if inSection {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// -- content parsers --

// ParseKeyFiles extracts the "## Key Files" section's backticked entries,
// verbatim, one per bullet line ("- `path` - role" → "path"). Prose lines,
// bullets without a backticked span, and lines inside fenced code blocks
// are ignored.
func ParseKeyFiles(content string) []string {
	body := sectionBody(content, "Key Files")
	if body == "" {
		return nil
	}
	var out []string
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || (!strings.HasPrefix(trimmed, "- ") && !strings.HasPrefix(trimmed, "* ")) {
			continue
		}
		if entry, ok := firstBackticked(trimmed); ok {
			out = append(out, entry)
		}
	}
	return out
}

// firstBackticked returns the first `...` span of the line.
func firstBackticked(line string) (string, bool) {
	start := strings.IndexByte(line, '`')
	if start < 0 {
		return "", false
	}
	rest := line[start+1:]
	end := strings.IndexByte(rest, '`')
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// ParseCrossRefs returns every relative markdown link target in document
// order, verbatim: a file path with an optional #anchor, or a pure #anchor
// (intra-file). Absolute and scheme'd URLs (http/https/mailto, //host) are
// not cross-references and are skipped. Links inside code blocks and inline
// code spans are skipped.
func ParseCrossRefs(content string) []string {
	matches := markdownLinkRe.FindAllStringSubmatch(plainText(content), -1)
	if len(matches) == 0 {
		return nil
	}
	var out []string
	for _, m := range matches {
		if isRelativeRef(m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// isRelativeRef reports whether a link target is a specs-relative reference
// (a path, a path#anchor, or a pure #anchor) rather than an absolute URL.
func isRelativeRef(target string) bool {
	if target == "" || strings.HasPrefix(target, "#") {
		return target != ""
	}
	if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "//") {
		return false
	}
	return true
}

// ParseADR extracts one ADR body's document metadata: the title, the Status
// classification and the successor number. Number and slug come from the
// file name and are filled in by the caller.
func ParseADR(content string) (title, status, supersededBy string) {
	title = ParseTitle(content)
	if m := adrTitleRe.FindStringSubmatch(plainText(content)); m != nil {
		title = strings.TrimSpace(m[1])
	}
	status, supersededBy = parseADRStatusLine(firstNonEmptyLine(sectionBody(content, "Status")))
	return title, status, supersededBy
}

// firstNonEmptyLine returns the first line that is not blank after
// trimming.
func firstNonEmptyLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// parseADRStatusLine classifies a raw status line. The first word lowers to
// the classification (accepted / superseded / anything else verbatim), and
// a superseded line's first successor reference — bracketed ("[084]",
// "[ADR-055]") or plain ("ADR-055") — yields supersededBy. Years never
// match: brackets accept only 1-3 digits and the plain form requires the
// ADR- prefix.
func parseADRStatusLine(line string) (status, supersededBy string) {
	if line == "" {
		return "", ""
	}
	words := strings.FieldsFunc(line, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) == 0 {
		return "", ""
	}
	status = strings.ToLower(words[0])
	if status != StatusSuperseded {
		return status, ""
	}
	if m := bracketedADRRe.FindStringSubmatch(line); m != nil {
		return status, m[1]
	}
	if m := adrRefRe.FindStringSubmatch(line); m != nil {
		return status, m[1]
	}
	return status, ""
}

// ParseIndex parses INDEX.md: the "## Navigation by Task" table into Tasks
// and the "## Directory Listing" section's relative .md links into
// ListedFiles. Always returns a non-nil index; an INDEX.md without either
// section parses to empty slices.
func ParseIndex(content string) *SpecsIndex {
	idx := &SpecsIndex{
		Tasks:       parseIndexTasks(sectionBody(content, "Navigation by Task")),
		ListedFiles: parseIndexListed(sectionBody(content, "Directory Listing")),
	}
	return idx
}

// parseIndexTasks reads the navigation table: pipe rows whose second cell
// carries at least one relative link. The header row (no links in "Read
// these specs") and separator rows drop out by that same rule.
func parseIndexTasks(body string) []IndexTask {
	var tasks []IndexTask
	for _, line := range strings.Split(plainText(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := splitTableCells(trimmed)
		if len(cells) < 2 || isTableSeparator(cells) {
			continue
		}
		specs := relativeLinks(cells[1])
		if len(specs) == 0 {
			continue
		}
		tasks = append(tasks, IndexTask{Task: cells[0], Specs: specs})
	}
	return tasks
}

// splitTableCells splits a pipe table row into trimmed cells.
func splitTableCells(row string) []string {
	row = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(row), "|"), "|")
	raw := strings.Split(row, "|")
	cells := make([]string, 0, len(raw))
	for _, c := range raw {
		cells = append(cells, strings.TrimSpace(c))
	}
	return cells
}

// isTableSeparator reports whether every cell of the row is a markdown
// alignment marker (":---", "---").
func isTableSeparator(cells []string) bool {
	sawMarker := false
	for _, c := range cells {
		if c == "" {
			continue
		}
		if !tableSeparatorCellRe.MatchString(c) {
			return false
		}
		sawMarker = true
	}
	return sawMarker
}

// parseIndexListed collects the directory listing's relative .md link
// targets, normalized and deduplicated, in document order.
func parseIndexListed(body string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, target := range relativeLinks(plainText(body)) {
		cleaned, ok := normalizeListedPath(target)
		if !ok || seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		out = append(out, cleaned)
	}
	return out
}

// normalizeListedPath reduces a link target to a specs-root-relative .md
// path. Links leaving the specs tree (../CODING_STANDARDS.md) and non-.md
// targets (directory links) are not listed spec files.
func normalizeListedPath(target string) (string, bool) {
	p, _ := splitRef(target)
	if !strings.HasSuffix(p, ".md") {
		return "", false
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	return cleaned, true
}

// relativeLinks returns the relative link targets of a text fragment, in
// order, verbatim.
func relativeLinks(text string) []string {
	matches := markdownLinkRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	var out []string
	for _, m := range matches {
		if isRelativeRef(m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// splitRef splits a cross-reference into its path and anchor parts
// ("../a.md#sec" → "../a.md", "sec"; "#sec" → "", "sec").
func splitRef(ref string) (p, anchor string) {
	if i := strings.IndexByte(ref, '#'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// -- filesystem orchestrator --

// ParseSpecsRoot parses a specs/ directory: INDEX.md, META.md and every .md
// file under the four section directories, plus the deterministic tier-1
// findings (ADR-105 D2/D5). It mirrors core/research.ParseResearchRoot's
// read-only tolerant shape but is neutral about a missing tree: a root that
// does not exist or is not a directory parses to Present=false, never an
// error; an unreadable or oversized individual file is skipped. Key Files
// entries and cross-references resolve against the parent of the specs root
// (the workspace root).
func ParseSpecsRoot(rootPath string) (*SpecsRoot, error) {
	root := &SpecsRoot{Path: rootPath}
	info, err := os.Stat(rootPath)
	if err != nil || !info.IsDir() {
		return root, nil //nolint:nilerr // a missing or non-directory root is a neutral present=false state, never an error (ADR-105 D2)
	}
	root.Present = true

	contents := make(map[string]string) // specs-root-relative path → content
	if content, ok := readSpec(filepath.Join(rootPath, "INDEX.md")); ok {
		contents["INDEX.md"] = content
		root.Index = ParseIndex(content)
	}
	if content, ok := readSpec(filepath.Join(rootPath, "META.md")); ok {
		contents["META.md"] = content
	}
	for _, section := range sectionDirs {
		loadSectionFiles(rootPath, section, contents)
	}

	for rel, content := range contents {
		root.Files = append(root.Files, newSpecFile(rel, content))
	}
	sort.Slice(root.Files, func(i, j int) bool { return root.Files[i].Path < root.Files[j].Path })

	root.ADRs = collectADRs(contents)
	root.Findings = computeFindings(root, contents, filepath.Dir(filepath.Clean(rootPath)))
	return root, nil
}

// readSpec reads one spec file tolerantly: missing, unreadable,
// non-regular or oversized files are ("", false) — a partial tree stays a
// valid state.
func readSpec(p string) (string, bool) {
	data, err := safeio.ReadFileLimited(p, maxSpecBytes)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// loadSectionFiles records every .md file under one section directory whose
// base name does not start with "_" (the template prefix, META.md) into
// contents. Walk errors degrade to skipping the subtree.
func loadSectionFiles(rootPath, section string, contents map[string]string) {
	_ = filepath.WalkDir(filepath.Join(rootPath, section), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is skipped, not fatal — partial trees stay valid
		}
		if d == nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, "_") {
			return nil
		}
		rel, relErr := filepath.Rel(rootPath, p)
		if relErr != nil {
			return nil //nolint:nilerr // an unrelativizable path cannot happen under the walked root; skip defensively
		}
		slash := filepath.ToSlash(rel)
		if _, dup := contents[slash]; dup {
			return nil
		}
		if content, ok := readSpec(p); ok {
			contents[slash] = content
		}
		return nil
	})
}

// newSpecFile builds the model entry for one parsed file.
func newSpecFile(rel, content string) SpecFile {
	return SpecFile{
		Path:      rel,
		Section:   sectionOf(rel),
		Title:     ParseTitle(content),
		KeyFiles:  ParseKeyFiles(content),
		CrossRefs: ParseCrossRefs(content),
	}
}

// sectionOf returns the file's top-level section directory, or "" for the
// root files.
func sectionOf(rel string) string {
	seg, _, _ := strings.Cut(rel, "/")
	switch seg {
	case SectionArchitecture, SectionDomains, SectionContracts, SectionDecisions:
		return seg
	}
	return ""
}

// collectADRs builds ADR metadata from the decisions/ files, sorted by
// number (numeric, file name as the tie-break so duplicates stay adjacent).
func collectADRs(contents map[string]string) []ADRMeta {
	var adrs []ADRMeta
	for rel, content := range contents {
		if !strings.HasPrefix(rel, SectionDecisions+"/") {
			continue
		}
		m := adrFileNameRe.FindStringSubmatch(path.Base(rel))
		if m == nil {
			continue
		}
		title, status, supersededBy := ParseADR(content)
		adrs = append(adrs, ADRMeta{
			Number:       m[1],
			Slug:         m[2],
			File:         rel,
			Title:        title,
			Status:       status,
			SupersededBy: supersededBy,
		})
	}
	sort.Slice(adrs, func(i, j int) bool {
		if c := compareADRNumbers(adrs[i].Number, adrs[j].Number); c != 0 {
			return c < 0
		}
		return adrs[i].File < adrs[j].File
	})
	return adrs
}

// compareADRNumbers orders ADR numbers numerically ("002" before "010"),
// reporting unpadded spellings of the same number ("002" and "2") as EQUAL
// (0) — the caller breaks equal numbers deterministically (collectADRs by
// file path, duplicateADRFindings as one duplicate group). Non-numeric
// numbers sort after all numeric ones, lexicographically.
func compareADRNumbers(a, b string) int {
	na, aerr := strconv.Atoi(a)
	nb, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		return cmp.Compare(na, nb)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// -- tier-1 findings (ADR-105 D5) --

// computeFindings runs the four deterministic checks. Every check
// under-approximates: entries that do not look like workspace paths,
// anchors in unreadable files, and a missing INDEX.md produce no findings.
func computeFindings(root *SpecsRoot, contents map[string]string, workspaceRoot string) []Finding {
	findings := make([]Finding, 0, len(root.Files))
	for _, f := range root.Files {
		findings = append(findings, pathMissingFindings(f, workspaceRoot)...)
	}
	findings = append(findings, crossRefFindings(root, contents)...)
	findings = append(findings, duplicateADRFindings(root.ADRs)...)
	findings = append(findings, indexStaleFindings(root)...)

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Kind != findings[j].Kind {
			return findings[i].Kind < findings[j].Kind
		}
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Detail < findings[j].Detail
	})
	return dedupeFindings(findings)
}

// dedupeFindings collapses identical findings — the same broken reference
// repeated across one file reports once.
func dedupeFindings(findings []Finding) []Finding {
	var out []Finding
	for i, f := range findings {
		if i > 0 && f == findings[i-1] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// pathMissingFindings flags Key Files entries that look like
// workspace-root-relative paths but do not exist. Only a definitive
// not-exist stat counts; other stat errors stay silent.
func pathMissingFindings(f SpecFile, workspaceRoot string) []Finding {
	var findings []Finding
	for _, entry := range f.KeyFiles {
		if !isWorkspacePathCandidate(entry, workspaceRoot) {
			continue
		}
		if _, err := os.Stat(filepath.Join(workspaceRoot, filepath.FromSlash(entry))); err == nil || !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		findings = append(findings, Finding{
			Kind:   FindingPathMissing,
			File:   f.Path,
			Detail: "key file does not exist: " + entry,
		})
	}
	return findings
}

// isWorkspacePathCandidate reports whether a Key Files entry looks like a
// workspace-root-relative path the PATH_MISSING check can stat: not home,
// absolute or placeholder-shaped, no glob metacharacters, and — when the
// entry has a directory part — rooted at a directory that actually exists
// under the workspace. The last rule keeps SDK-side entries written as bare
// module-relative paths ("sp4rk `agent/verify_on_edit.go`") out of the
// check: their first segment is not a workspace directory, so the entry is
// provably not a workspace path.
func isWorkspacePathCandidate(entry, workspaceRoot string) bool {
	if entry == "" {
		return false
	}
	if strings.HasPrefix(entry, "/") || strings.HasPrefix(entry, "~") || strings.HasPrefix(entry, "<") {
		return false
	}
	if strings.ContainsAny(entry, "*?[]") {
		return false
	}
	seg, rest, _ := strings.Cut(entry, "/")
	if rest == "" {
		return true // a file directly at the workspace root
	}
	info, err := os.Stat(filepath.Join(workspaceRoot, seg))
	return err == nil && info.IsDir()
}

// crossRefFindings flags relative links whose target file or anchor does
// not resolve. Targets resolve against the source file's directory and may
// legitimately leave the specs tree (../../docs/…, ../CODING_STANDARDS.md)
// — resolution happens in absolute space, because a cleaned relative join
// would silently drop the "../.." climb. Anchors are checked against the
// target's heading slugs; an anchor in a target that cannot be read
// resolves (under-approximation).
func crossRefFindings(root *SpecsRoot, contents map[string]string) []Finding {
	slugs := make(map[string]map[string]bool, len(contents))
	for rel, content := range contents {
		slugs[rel] = headingSlugs(content)
	}
	var findings []Finding
	for _, f := range root.Files {
		for _, ref := range f.CrossRefs {
			p, anchor := splitRef(ref)
			if p == "" { // pure intra-file anchor
				if !slugs[f.Path][anchor] {
					findings = append(findings, Finding{
						Kind:   FindingCrossRefBroken,
						File:   f.Path,
						Detail: "anchor does not resolve: " + ref,
					})
				}
				continue
			}
			dir := filepath.Dir(filepath.Join(root.Path, filepath.FromSlash(f.Path)))
			abs := filepath.Join(dir, filepath.FromSlash(p))
			if _, err := os.Stat(abs); err != nil {
				findings = append(findings, Finding{
					Kind:   FindingCrossRefBroken,
					File:   f.Path,
					Detail: "link does not resolve: " + ref,
				})
				continue
			}
			if anchor == "" {
				continue
			}
			rel, relErr := filepath.Rel(root.Path, abs)
			key := ""
			if relErr == nil {
				key = filepath.ToSlash(rel)
			}
			target, ok := slugs[key]
			if !ok {
				content, readable := readSpec(abs)
				if !readable {
					continue
				}
				target = headingSlugs(content)
				slugs[key] = target
			}
			if !target[anchor] {
				findings = append(findings, Finding{
					Kind:   FindingCrossRefBroken,
					File:   f.Path,
					Detail: "anchor does not resolve: " + ref,
				})
			}
		}
	}
	return findings
}

// duplicateADRFindings flags numbers carried by more than one decisions/
// file (ADRs are sorted, so a duplicate group is one adjacent run).
func duplicateADRFindings(adrs []ADRMeta) []Finding {
	var findings []Finding
	for i := 0; i < len(adrs); {
		j := i + 1
		// Group NUMERICALLY: the sort's comparator defines the equality
		// classes, and its zero includes unpadded spellings of the same
		// number ("002" == "2") — the very collision the check exists to
		// catch — so its zero is the group boundary, not string equality.
		for j < len(adrs) && compareADRNumbers(adrs[j].Number, adrs[i].Number) == 0 {
			j++
		}
		if group := adrs[i:j]; len(group) > 1 {
			files := make([]string, len(group))
			for k, a := range group {
				files[k] = a.File
			}
			findings = append(findings, Finding{
				Kind:   FindingDuplicateADRNumber,
				File:   group[0].File,
				Detail: "duplicate ADR number " + unpaddedADRNumber(group[0].Number) + ": " + strings.Join(files, ", "),
			})
		}
		i = j
	}
	return findings
}

// unpaddedADRNumber renders an ADR number without zero padding ("002" →
// "2") so a duplicate group spanning padded and unpadded spellings reports
// one canonical number. The file-name pattern admits digits only; the
// verbatim fallback covers a future pattern widening.
func unpaddedADRNumber(number string) string {
	if n, err := strconv.Atoi(number); err == nil {
		return strconv.Itoa(n)
	}
	return number
}

// indexStaleFindings flags section files INDEX.md does not know about. The
// baseline is the directory listing plus the task navigation table's spec
// targets — a file the index navigates to is not forgotten, wherever the
// link lives. Without a parsed INDEX.md there is no baseline to be stale
// against — a missing index produces no findings.
func indexStaleFindings(root *SpecsRoot) []Finding {
	if root.Index == nil {
		return nil
	}
	listed := make(map[string]bool, len(root.Index.ListedFiles))
	for _, p := range root.Index.ListedFiles {
		listed[p] = true
	}
	for _, task := range root.Index.Tasks {
		for _, s := range task.Specs {
			if cleaned, ok := normalizeListedPath(s); ok {
				listed[cleaned] = true
			}
		}
	}
	var findings []Finding
	for _, f := range root.Files {
		if f.Section == "" || listed[f.Path] {
			continue
		}
		findings = append(findings, Finding{
			Kind:   FindingIndexStale,
			File:   f.Path,
			Detail: "not listed in INDEX.md",
		})
	}
	return findings
}
