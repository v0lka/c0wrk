package sdd

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/safeio"
	"gopkg.in/yaml.v3"
)

// Reading a specification tree.
//
// ParseTree is the one filesystem entry point of this package. It reads a
// workspace through a Framework and nothing else: the framework's declared
// roots are walked, every path is validated as contained inside the workspace,
// every path is classified against the declared concepts, every matching
// document is parsed for exactly the metadata its concept declares, the
// declared views are rendered, and the declared checks are run. A file no
// concept matches never enters the model, and a finding exists only because a
// declared check failed — this file invents neither documents nor findings.
//
// The read is tolerant by construction. A root that does not exist parses to
// the neutral not-present state, an individual unreadable or oversized
// document is skipped, and an entry whose real path escapes the workspace (a
// symlink out of the tree) is excluded — none of these is an error. Reading is
// also strictly read-only: nothing under the workspace is created, modified or
// removed, which is why the same call serves the panel on every refresh.
//
// Parsing of document content is pure: the helpers below consume strings, so
// every extraction is unit-testable without a filesystem.
//
// The model carries no environment metadata. A record's git-derived add date
// is an enrichment of the wire boundary, not tree content: it stays in the
// backend DTO, so this package never runs git and never reports a date that the
// working tree does not carry (ADR-108, ADR-109).

// maxDocumentBytes bounds one document read. A specification tree is workspace
// content feeding read-only status RPCs, and safeio.ReadFileLimited refuses an
// oversized (hostile or accidental) file instead of reading it whole. 8 MiB is
// far above any real document.
const maxDocumentBytes = 8 << 20

// The parse budget bounds one ParseTree, not just one file: a clone controls
// how MANY matched documents exist and how big they are in total, so a
// per-file limit alone would let a few hundred near-limit documents turn
// every panel refresh into a multi-hundred-MiB retention spike. Beyond the
// budget the walk stops reading — documents past it are excluded whole (never
// parsed half-content, which would invent findings) and the skip is named in
// Warnings. Package vars so tests can shrink the budget; never tuned at
// runtime.
var (
	maxParseTotalBytes     int64 = 64 << 20
	maxParseTotalDocuments       = 2000
	// maxParseEntries bounds the WORK one ParseTree does, not only the content
	// it retains: every entry the walk visits costs one unit — a matched
	// document, a directory instance, a sibling that matches no concept, an
	// unreadable or oversized file. The content budget alone would leave all of
	// those free, because it is charged only after a successful read, so a root
	// as broad as "." would still visit and classify every entry of a .git store
	// or a dependency directory on every panel refresh. Past the budget the walk
	// stops descending and the skip is named once in Warnings.
	maxParseEntries = 50_000
	// maxParseWarnings bounds the diagnostics themselves. A warning line is part
	// of the model, crosses the RPC boundary on every status refresh and is
	// joined into one title attribute, so its count must not be linear in the
	// number of unreadable entries: past the cap the remainder collapses into
	// one counted line.
	maxParseWarnings = 20
	// maxParseFindings bounds the findings themselves, for the same reason
	// maxParseWarnings bounds the diagnostics: a finding is part of the model,
	// crosses the RPC boundary on every status refresh and is rendered as a
	// list, so its count must not be linear in the number of failed checks. A
	// reference-dense clone packs one failed check per few dozen bytes, which
	// the content budget alone leaves effectively unbounded (millions of
	// findings at the 64 MiB cap). Past the cap the remainder collapses into
	// one counted finding, so the model, the DTO and the sort stay bounded by
	// construction.
	maxParseFindings = 500
	// maxParseCrossRefs bounds the WORK cross-reference checks spend: every
	// reference costs one stat (and, with an anchor, one slug build of the
	// target), and a single 8 MiB document can hold tens of thousands of them.
	// Findings are capped above, but the stat cost is incurred BEFORE a finding
	// is recorded, so the budget has to sit on the resolution path. Past it
	// references are not judged at all and the skip is named once in Warnings.
	maxParseCrossRefs = 20_000
)

// prunedDirNames are directories the walk never descends into. A VCS store and
// a dependency directory never hold a project's own specifications, and because
// a lone `path: "."` root is legitimate (the whole workspace as one subtree),
// descending into them is not a theoretical case: it would classify npm
// READMEs as specification documents. Pruning happens BEFORE classification, so
// neither the model nor the entry budget sees them.
var prunedDirNames = map[string]bool{
	".git":         true,
	"node_modules": true,
}

// Status classifications the `status` directive reduces a "## Status"
// section's first line to. Real status lines are free prose ("Accepted —
// amended by [ADR-052 ...]"), so the classification keys off the first word;
// any other first word is reported verbatim, lower-cased.
const (
	StatusAccepted   = "accepted"
	StatusSuperseded = "superseded"
)

var (
	// markdownLinkRe matches an inline markdown link and captures the target.
	// Targets containing spaces or parentheses do not match — no documented
	// document format uses that shape.
	markdownLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^()\s]+)\)`)
	// leadingNumberRe splits a name into its leading number and its slug
	// ("106-specs-sidebar-panel" → "106", "specs-sidebar-panel").
	leadingNumberRe = regexp.MustCompile(`^(\d+)-(.+)$`)
	// bracketedNumberRe matches a bracketed successor reference ("[084]",
	// "[ADR-055]", "[1024]") in a raw status line. The digit run is the
	// record number, capped at six digits — enough for any plausible record
	// numbering past 999. A calendar year spelled "[2024]" is rejected at
	// resolution time (see unresolvedYearToken: 1900–2099), not by
	// truncating the reference.
	bracketedNumberRe = regexp.MustCompile(`\[(?:ADR-)?(\d{1,6})\]`)
	// plainNumberRe matches a plain "ADR-055" successor reference. Like the
	// bracketed form it caps at six digits; the resolution-time year guard
	// keeps calendar years from becoming false missing-target findings.
	plainNumberRe = regexp.MustCompile(`ADR-(\d{1,6})`)
	// tableSeparatorCellRe matches one markdown alignment marker cell.
	tableSeparatorCellRe = regexp.MustCompile(`^:?-+:?$`)
	// taskItemRe matches a checkbox list item and captures its state and text.
	taskItemRe = regexp.MustCompile(`^[-*+]\s+\[([ xX])\]\s+(.*)$`)
	// orderedItemRe matches an ordered-list item marker ("12. ", "3) ").
	orderedItemRe = regexp.MustCompile(`^\d{1,9}[.)] `)
	// deltaHeadingRe matches a delta spec's requirements-section heading.
	deltaHeadingRe = regexp.MustCompile(`^(ADDED|MODIFIED|REMOVED|RENAMED) Requirements$`)
	// frontmatterRe captures a leading YAML frontmatter block.
	frontmatterRe = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---[ \t]*(?:\r?\n|\z)`)
	// atxClosingRe matches a CommonMark ATX closing sequence: a trailing run
	// of #s preceded by whitespace ("Heading ##" → "Heading"). A # glued to
	// the last word ("foo#") or inside the text is content, not a closer.
	atxClosingRe = regexp.MustCompile(`\s+#+\s*$`)
)

// -- markdown primitives --

// fenceTracker tracks fenced code blocks the way CommonMark defines them: a
// fence opens on a line of three or more backticks or tildes (a backtick
// fence's info string may not carry a backtick) and closes only on a line of
// the same character of at least the same length. A plain boolean cannot do
// this: the inner ``` of a ```` example would flip the state and the rest of
// the document would parse as the opposite of what it is — inventing findings
// from example content and swallowing real ones after the block.
type fenceTracker struct {
	active bool
	char   byte
	length int
}

// fenceLine reports whether the (already trimmed) line is a fence delimiter
// and advances the tracker: an opening delimiter starts a block, a matching
// delimiter closes it, and any non-delimiter line leaves the state alone.
func (f *fenceTracker) fenceLine(trimmed string) bool {
	char, count, info := fenceDelimiter(trimmed)
	if count < 3 {
		return false
	}
	if f.active {
		// A closing fence is the opening run's character, at least its length,
		// followed only by spaces or tabs: any info text keeps the block open.
		if char == f.char && count >= f.length && info == "" {
			f.active = false
			return true
		}
		return false
	}
	// A backtick fence's info string may not hold a backtick, so such a line is
	// plain prose rather than an opening fence. Tilde fences carry no such
	// restriction, which is why `~~~ ~~~` opens a block instead of closing one.
	if char == '`' && strings.ContainsRune(info, '`') {
		return false
	}
	f.active, f.char, f.length = true, char, count
	return true
}

// fenceDelimiter decomposes a trimmed line into its fence character, run length
// and the info string that follows the run; a first character that is neither
// ` nor ~, or a run shorter than three, means the line is not a fence at all.
// The info string is returned verbatim — deciding whether a delimiter opens or
// closes is fenceLine's job, not this decomposition's.
func fenceDelimiter(trimmed string) (char byte, count int, info string) {
	if trimmed == "" || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, ""
	}
	char = trimmed[0]
	for count < len(trimmed) && trimmed[count] == char {
		count++
	}
	if count < 3 {
		return 0, 0, ""
	}
	return char, count, strings.TrimSpace(trimmed[count:])
}

// atxHeading recognizes one ATX heading line and returns its text and level;
// anything else (including "#NoSpace") is not a heading. A CommonMark closing
// sequence is stripped so the text matches what a renderer renders.
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
	content := atxClosingRe.ReplaceAllString(strings.TrimSpace(rest), "")
	return stripBackticks(content), lvl, true
}

// stripBackticks removes every ` character, keeping the enclosed content.
func stripBackticks(s string) string {
	return strings.ReplaceAll(s, "`", "")
}

// stripInlineCode replaces every `...` span of the line with spaces.
// Unmatched backticks swallow the rest of the line — under-stripping can only
// miss links, never invent them.
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

// bodyOf strips a leading frontmatter block, returning the markdown body.
// Frontmatter is metadata, not prose: every heading, link, table and list
// extraction reads the body only, so a YAML block scalar can never contribute
// a document heading or a live cross-reference.
func bodyOf(content string) string {
	if m := frontmatterRe.FindStringSubmatchIndex(content); m != nil {
		return content[m[1]:]
	}
	return content
}

// plainText removes fenced code blocks and inline code spans from markdown,
// preserving line structure: headings, links and table rows are read from the
// prose only, so a format example never parses as a live cross-reference —
// even a nested one (a ``` inside a ```` block stays inside the example).
func plainText(content string) string {
	lines := strings.Split(content, "\n")
	var fence fenceTracker
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if fence.fenceLine(trimmed) {
			lines[i] = ""
			continue
		}
		if fence.active {
			lines[i] = ""
			continue
		}
		lines[i] = stripInlineCode(line)
	}
	return strings.Join(lines, "\n")
}

// headingsOf returns every ATX heading outside fenced code blocks, in document
// order, with inline code delimiters stripped.
func headingsOf(content string) []string {
	var out []string
	var fence fenceTracker
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence.fenceLine(trimmed) {
			continue
		}
		if fence.active {
			continue
		}
		if heading, _, ok := atxHeading(trimmed); ok {
			out = append(out, heading)
		}
	}
	return out
}

// parseTitle returns the first ATX heading of the document, outside code
// blocks; "" when there is none.
func parseTitle(content string) string {
	for _, heading := range headingsOf(content) {
		return heading
	}
	return ""
}

// documentTitle returns the display title of the document at name: its first
// ATX heading with the record's own identity prefix removed — "# ADR-001: One"
// in 001-one.md is "One", because the identifier already travels beside the
// record as its number. The prefix is a "<word>-<digits>: " lead, stripped only
// when the name itself carries a number; a heading without such a lead, and a
// document without a number, keep their title verbatim.
func documentTitle(name, content string) string {
	title := parseTitle(content)
	number, _ := splitName(name)
	if title == "" || number == "" {
		return title
	}
	lead, rest, ok := strings.Cut(title, ":")
	if !ok {
		return title
	}
	word, token, ok := strings.Cut(lead, "-")
	if !ok || word == "" || token == "" {
		return title
	}
	for _, r := range word {
		if !unicode.IsLetter(r) {
			return title
		}
	}
	for _, r := range token {
		if !unicode.IsDigit(r) {
			return title
		}
	}
	return strings.TrimSpace(rest)
}

// slugifyHeading converts heading text to its anchor slug the way markdown
// renderers do: lower-cased, everything that is not a letter, digit, space,
// hyphen or underscore dropped, then every space folded to a hyphen.
func slugifyHeading(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == ' ', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return strings.ReplaceAll(b.String(), " ", "-")
}

// anchorSlugs maps every heading of the content to the anchor slugs a
// markdown renderer would produce. The namespace is renderer semantics, not
// just ATX text: setext headings ("Title" over "====" / "----") carry anchors
// too, and a repeated heading gets the GitHub-style suffixes ("-1", "-2", …
// for its second and later occurrences) beside its plain slug. Used only to
// judge cross-reference anchors — the prose model (headingsOf) stays ATX-only,
// which is what extraction directives document.
func anchorSlugs(content string) map[string]bool {
	var slugs []string
	var fence fenceTracker
	prevProse := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence.fenceLine(trimmed) {
			prevProse = ""
			continue
		}
		if fence.active {
			prevProse = ""
			continue
		}
		if heading, _, ok := atxHeading(trimmed); ok {
			slugs = append(slugs, heading)
			prevProse = ""
			continue
		}
		// A run of "=" or "-" directly under a non-blank prose line is a
		// setext underline (CommonMark); under a blank line it is nothing.
		if prevProse != "" && trimmed != "" {
			if strings.TrimLeft(trimmed, "=") == "" || strings.TrimLeft(trimmed, "-") == "" {
				slugs = append(slugs, prevProse)
				prevProse = ""
				continue
			}
		}
		if trimmed == "" {
			prevProse = ""
			continue
		}
		prevProse = stripInlineCode(trimmed)
	}
	if len(slugs) == 0 {
		return nil
	}
	out := make(map[string]bool, len(slugs))
	seen := make(map[string]int, len(slugs))
	for _, heading := range slugs {
		slug := slugifyHeading(heading)
		if slug == "" {
			continue
		}
		n := seen[slug]
		seen[slug] = n + 1
		out[slug] = true
		if n > 0 {
			out[slug+"-"+strconv.Itoa(n)] = true
		}
	}
	return out
}

// sectionBodyWhere returns the body of the first ATX section whose heading
// satisfies match, at any level: the raw lines after the heading up to the
// next heading of the same or a higher level, so sub-headings stay inside the
// section. Boundaries are fence-aware; the body is returned raw.
func sectionBodyWhere(content string, match func(string) bool) string {
	var out []string
	inSection := false
	var fence fenceTracker
	sectionLevel := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence.fenceLine(trimmed) {
			if inSection {
				out = append(out, line)
			}
			continue
		}
		heading, level, ok := atxHeading(trimmed)
		if !fence.active && ok {
			if inSection {
				if level <= sectionLevel {
					break
				}
				continue // a deeper sub-heading stays inside the section
			}
			if match(heading) {
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

// headingContains matches a section heading carrying a word, case-insensitively
// — the tolerant dialect rule a topic section is found by.
func headingContains(word string) func(string) bool {
	lowered := strings.ToLower(word)
	return func(heading string) bool {
		return strings.Contains(strings.ToLower(heading), lowered)
	}
}

// firstNonEmptyLine returns the first line that is not blank after trimming.
func firstNonEmptyLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
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

// bulletText strips a list marker from a line — the CommonMark bullet markers
// ("- ", "* ", "+ ") and the ordered forms ("12. ", "3) ") — reporting both
// the text and whether the line was a list item at all.
func bulletText(line string) (string, bool) {
	for _, marker := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, marker) {
			return strings.TrimSpace(line[len(marker):]), true
		}
	}
	if m := orderedItemRe.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(line[len(m[0]):]), true
	}
	return "", false
}

// frontmatterOf parses a document's leading YAML frontmatter into flat string
// values. The block is decoded as YAML (so quoted keys, nested keys and block
// scalars keep their real structure — a block scalar's indented lines never
// become top-level fields), scalars are stringified, and a composite value is
// rendered with its YAML shape so its key still counts as present. A document
// without frontmatter — or with a block that is not a mapping — yields nil,
// and the block never enters the prose a heading or link scan sees.
func frontmatterOf(content string) map[string]string {
	m := frontmatterRe.FindStringSubmatch(content)
	if m == nil {
		return nil
	}
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(m[1]), &raw); err != nil || raw == nil {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		out[key] = frontmatterValue(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// frontmatterValue stringifies one YAML node for the flat string model.
func frontmatterValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case map[string]any, []any:
		data, err := yaml.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.TrimRight(string(data), "\n")
	default:
		return fmt.Sprint(v)
	}
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
		// A CommonMark destination may be wrapped in angle brackets
		// ("[x](<./a.md>)"); the brackets delimit the destination and are not
		// part of the target, so they never reach splitRef or stat.
		target := strings.TrimSuffix(strings.TrimPrefix(m[1], "<"), ">")
		if isRelativeRef(target) {
			out = append(out, target)
		}
	}
	return out
}

// isRelativeRef reports whether a link target is a tree-relative reference (a
// path, a path#anchor, or a pure #anchor) rather than an absolute URL or a
// site-root-relative path.
func isRelativeRef(target string) bool {
	if target == "" {
		return false
	}
	if strings.HasPrefix(target, "#") {
		return true
	}
	// A leading slash names a site-root-relative destination ("/specs/x.md"):
	// it resolves against the site root, not the document's directory, so
	// appending it there would stat a path that never exists. This also covers
	// the protocol-relative "//host" form.
	if strings.HasPrefix(target, "/") {
		return false
	}
	return !strings.Contains(target, "://") && !strings.HasPrefix(target, "mailto:")
}

// splitRef splits a cross-reference into its path and anchor parts
// ("../a.md#sec" → "../a.md", "sec"; "#sec" → "", "sec").
func splitRef(ref string) (p, anchor string) {
	if i := strings.IndexByte(ref, '#'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
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

// inlineCodeSpans returns the content of every `...` span of the line.
func inlineCodeSpans(line string) []string {
	var out []string
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			return out
		}
		rest := line[start+1:]
		end := strings.IndexByte(rest, '`')
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		line = rest[end+1:]
	}
}

// -- extraction directives --

// parseKeyFiles extracts the "## Key Files" section's backticked entries,
// verbatim, one per bullet line ("- `path` - role" → "path").
func parseKeyFiles(content string) []string {
	body := sectionBodyWhere(content, func(heading string) bool {
		return strings.EqualFold(heading, "Key Files")
	})
	if body == "" {
		return nil
	}
	var out []string
	var fence fenceTracker
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence.fenceLine(trimmed) {
			continue
		}
		if fence.active {
			continue
		}
		if text, ok := bulletText(trimmed); ok {
			if entry, ok := firstBackticked(text); ok {
				out = append(out, entry)
			}
		}
	}
	return out
}

// parseCrossRefs returns every relative markdown link target in document
// order, verbatim. Absolute and scheme'd URLs are not cross-references, and
// links inside code blocks and inline code spans are skipped.
func parseCrossRefs(content string) []string {
	return relativeLinks(plainText(content))
}

// parseStatus classifies a document's "## Status" section: the first word of
// its first non-empty line, lower-cased, plus the successor token a superseded
// line names ("[084]", "[ADR-055]", "ADR-055" — the digit run is capped at six digits).
// A token that never resolves as a record is judged at resolution time, so a
// bracketed calendar year does not become a false missing-target finding.
func parseStatus(content string) (status, supersededBy string) {
	body := sectionBodyWhere(content, func(heading string) bool {
		return strings.EqualFold(heading, "Status")
	})
	line := firstNonEmptyLine(body)
	if line == "" {
		return "", ""
	}
	words := strings.FieldsFunc(line, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return "", ""
	}
	status = strings.ToLower(words[0])
	if status != StatusSuperseded {
		return status, ""
	}
	if m := bracketedNumberRe.FindStringSubmatch(line); m != nil {
		return status, m[1]
	}
	if m := plainNumberRe.FindStringSubmatch(line); m != nil {
		return status, m[1]
	}
	return status, ""
}

// topicBody returns the trimmed body of the first section whose heading
// carries the word, at any level; "" when the document has no such section.
func topicBody(content, word string) string {
	return strings.TrimSpace(sectionBodyWhere(content, headingContains(word)))
}

// topicItems collects the entries a document makes about one topic: every ATX
// heading starting with the word (so an OpenSpec "### Requirement: X" is one
// entry), followed by the list items of the first section whose heading
// carries the word (so a Spec Kit "### Functional Requirements" bullet list is
// enumerated). Entries are verbatim, in document order within each group.
// The topic's own section heading is a container, not an entry: a heading that
// is exactly the topic word or its plural form ("Requirements", "User
// Stories" — stor + "ies") is skipped, however the plural is spelled.
func topicItems(content, word string) []string {
	var out []string
	for _, heading := range headingsOf(content) {
		if !hasWordPrefix(heading, word) {
			continue
		}
		if isTopicContainerHeading(heading, word) {
			continue
		}
		out = append(out, heading)
	}
	// Only the CONTAINER form contributes bare entries. When the first matching
	// heading is a leaf ENTRY ("### User Story 1 …"), the heading itself is the
	// entry and its bullets are that entry's detail (its acceptance scenarios),
	// not siblings of it — so the bullet scan is restricted to container
	// headings and never picks up a leaf entry's own list.
	container := func(heading string) bool { return isTopicContainerHeading(heading, word) }
	return append(out, sectionBullets(content, container)...)
}

// isTopicContainerHeading reports whether a heading carrying word is that
// section's CONTAINER form — the word plus a plural marker ("## User Stories",
// "## Requirements") — rather than an entry ("### User Story 1 …"). The word is
// located wherever it appears, so a container heading with a prefix
// ("Open Questions on User Stories") is recognized too.
func isTopicContainerHeading(heading, word string) bool {
	lowered := strings.ToLower(heading)
	loweredWord := strings.ToLower(word)
	idx := strings.Index(lowered, loweredWord)
	if idx < 0 {
		return false
	}
	return isTopicContainer(strings.TrimSpace(lowered[idx+len(loweredWord):]))
}

// isTopicContainer reports whether the text after a topic word is just the
// word's plural marker ("" / "s" / "es" / "ies" / "y") — the section heading's
// own shape, never an entry.
func isTopicContainer(rest string) bool {
	switch rest {
	case "", "s", "es", "ies", "y":
		return true
	}
	return false
}

// sectionBullets returns the list items a section owns directly: the bullets
// between the first heading satisfying match and the next heading of the same
// or a higher level. Items under a deeper heading belong to that sub-section
// and are excluded, so a requirement's scenario steps never read as
// requirements of their own.
func sectionBullets(content string, match func(string) bool) []string {
	var out []string
	active, level := false, 0
	var fence fenceTracker
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence.fenceLine(trimmed) {
			continue
		}
		if fence.active {
			continue
		}
		if heading, headingLevel, ok := atxHeading(trimmed); ok {
			switch {
			case active && headingLevel <= level:
				return out // the section ended
			case active:
				active = false // a deeper heading owns what follows
			case level == 0 && match(heading):
				active, level = true, headingLevel
			}
			continue
		}
		if !active {
			continue
		}
		if text, ok := bulletText(trimmed); ok {
			out = append(out, text)
		}
	}
	return out
}

// hasWordPrefix reports whether text begins with word, case-insensitively. A
// prefix (rather than a whole word) is what a topic needs: "user stor" must
// cover both "User Story 1" and "User Stories", and "requirement" must cover
// "Requirements".
func hasWordPrefix(text, word string) bool {
	return strings.HasPrefix(strings.ToLower(text), strings.ToLower(word))
}

// deltaEntries returns the delta sections of a delta spec ("## ADDED
// Requirements" → "ADDED"), in document order.
func deltaEntries(content string) []string {
	var out []string
	for _, heading := range headingsOf(content) {
		if m := deltaHeadingRe.FindStringSubmatch(heading); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// taskItems returns the checkbox items of a task list, in document order.
func taskItems(content string) []TaskItem {
	var out []TaskItem
	var fence fenceTracker
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence.fenceLine(trimmed) {
			continue
		}
		if fence.active {
			continue
		}
		m := taskItemRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		out = append(out, TaskItem{Text: strings.TrimSpace(m[2]), Done: strings.EqualFold(m[1], "x")})
	}
	return out
}

// indexTasks parses a navigation index: the first section whose heading carries
// the word "task" contributes its pipe rows, and a row whose second cell
// carries at least one relative link is a task entry. The header row (no links
// in that cell), separator rows and external-only rows drop out by that same
// rule.
func indexTasks(content string) []IndexTask {
	body := sectionBodyWhere(content, func(heading string) bool {
		return strings.Contains(strings.ToLower(heading), "task")
	})
	if body == "" {
		return nil
	}
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

// splitName splits a base name into its leading number and slug.
func splitName(name string) (number, slug string) {
	base := strings.TrimSuffix(name, path.Ext(name))
	if m := leadingNumberRe.FindStringSubmatch(base); m != nil {
		return m[1], m[2]
	}
	return "", base
}

// topSection returns a document's top-level directory inside its root, or ""
// for a document sitting directly in the root.
func topSection(rel string) string {
	section, _, nested := strings.Cut(rel, "/")
	if !nested {
		return ""
	}
	return section
}

// valuesFor parses one extraction directive on demand — the shape a check uses
// when it declares its own input (`required_files.from`), independent of what
// the concept chose to keep in the model. The directive reads the document's
// markdown body; frontmatter is metadata, not prose.
func valuesFor(content, directive string) []string {
	body := bodyOf(content)
	switch directive {
	case ExtractKeyFiles:
		return parseKeyFiles(body)
	case ExtractCrossRefs:
		return parseCrossRefs(body)
	case ExtractHeadings:
		return headingsOf(body)
	case ExtractRequirements:
		return topicItems(body, "requirement")
	case ExtractScenarios:
		return topicItems(body, "scenario")
	case ExtractPrinciples:
		return topicItems(body, "principle")
	case ExtractUserStories:
		return topicItems(body, "user stor")
	case ExtractDeltas:
		return deltaEntries(body)
	}
	return nil
}

// fieldValue parses one extraction directive as a single scalar — the value a
// `unique` or `references` check compares. name is the instance's base name,
// which the number directive reads.
func fieldValue(name, content, directive string) string {
	body := bodyOf(content)
	switch directive {
	case ExtractNumber:
		number, _ := splitName(name)
		return number
	case ExtractTitle:
		return parseTitle(body)
	case ExtractPurpose:
		return topicBody(body, "purpose")
	case ExtractStatus:
		status, _ := parseStatus(body)
		return status
	case ExtractSupersededBy:
		_, supersededBy := parseStatus(body)
		return supersededBy
	}
	return ""
}

// indexMentions scans a navigation index for every path-shaped relative token
// it mentions: link targets and inline-code fields in prose, whitespace fields
// in fenced blocks (a directory tree names its leaves below their directory
// entries). Tokens with a directory part land in exact; bare file names
// additionally land in names. Tokens are returned twice — filtered to .md
// shapes (the document vocabulary) and as every path-shaped token (raw) — so a
// directory concept's instances can be matched against the index too: an
// index may list `bundles/a` with no .md suffix at all.
func indexMentions(content string) (exact, names, raw map[string]bool) {
	exact = make(map[string]bool)
	names = make(map[string]bool)
	raw = make(map[string]bool)
	add := func(field string) {
		refPath, _ := splitRef(field)
		token, ok := cleanIndexToken(refPath)
		if !ok {
			return
		}
		cleaned := path.Clean(token)
		if cleaned == "." || cleaned == ".." || cleaned == "/" ||
			strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
			return
		}
		exact[cleaned] = true
		if !strings.Contains(cleaned, "/") {
			names[cleaned] = true
		}
	}
	addRaw := func(field string) {
		token := trimTokenMarkup(field)
		if token == "" || strings.ContainsAny(token, "[](){}<>\"'`*?") ||
			strings.Contains(token, "://") || strings.HasPrefix(token, "mailto:") ||
			strings.HasPrefix(token, "/") || strings.HasPrefix(token, "~") {
			return
		}
		cleaned := path.Clean(token)
		if cleaned == "." || cleaned == ".." || cleaned == "/" ||
			strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
			return
		}
		raw[cleaned] = true
	}
	inFence := fenceTracker{}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if inFence.fenceLine(trimmed) {
			continue
		}
		if inFence.active {
			for _, field := range strings.Fields(trimmed) {
				add(field)
				addRaw(field)
			}
			continue
		}
		prose := stripInlineCode(line)
		for _, target := range relativeLinks(prose) {
			add(target)
			addRaw(target)
		}
		for _, span := range inlineCodeSpans(line) {
			for _, field := range strings.Fields(span) {
				add(field)
				addRaw(field)
			}
		}
		for _, field := range strings.Fields(prose) {
			add(field)
			addRaw(field)
		}
	}
	return exact, names, raw
}

// trimTokenMarkup strips the markup a token collects from its context. A '.' is
// END punctuation only: a leading dot is a path segment, so `./pages/a.md` and
// `.archive/a.md` reach path.Clean with their prefix intact instead of being
// rewritten into a different path. Both token vocabularies (the .md document
// one and the raw path one) go through this, so they cannot diverge.
func trimTokenMarkup(field string) string {
	return strings.TrimLeft(
		strings.TrimRight(field, "`\"'[](){}<>,.;:!?"),
		"`\"'[](){}<>,;:!?",
	)
}

// cleanIndexToken strips the markup a token collects from its context and
// rejects non-path shapes: inner markup debris, scheme'd URLs, absolute and
// home paths, glob metacharacters, non-.md suffixes.
func cleanIndexToken(field string) (string, bool) {
	token := trimTokenMarkup(field)
	if token == "" || strings.ContainsAny(token, "[](){}<>\"'`") {
		return "", false
	}
	if strings.Contains(token, "://") || strings.HasPrefix(token, "mailto:") {
		return "", false
	}
	if strings.HasPrefix(token, "/") || strings.HasPrefix(token, "~") || strings.ContainsAny(token, "*?") {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(token), ".md") {
		return "", false
	}
	return token, true
}

// -- concept matching --

// matchPattern matches a slash-separated pattern against a slash-separated
// relative path: `*` and `?` match within one segment, `**` matches zero or
// more segments (so `domains/**/*.md` covers both `domains/x.md` and
// `domains/backend/x.md`). The `**` recursion is memoized on the
// (pattern-segment, path-segment) pair: without it a pattern of repeated `**`
// re-explores every split of the remaining path, and one hostile glob turned
// every panel refresh into seconds of combinatorial backtracking.
func matchPattern(pattern, rel string) bool {
	patternSegs := strings.Split(pattern, "/")
	segments := strings.Split(rel, "/")
	memo := make([]map[int]bool, len(patternSegs)+1)
	var match func(pi, si int) bool
	match = func(pi, si int) bool {
		if pi == len(patternSegs) {
			return si == len(segments)
		}
		if memo[pi] == nil {
			memo[pi] = make(map[int]bool)
		}
		if cached, ok := memo[pi][si]; ok {
			return cached
		}
		var result bool
		switch {
		case patternSegs[pi] == "**":
			for i := si; i <= len(segments); i++ {
				if match(pi+1, i) {
					result = true
					break
				}
			}
		case si == len(segments):
			result = false
		default:
			ok, err := path.Match(patternSegs[pi], segments[si])
			result = err == nil && ok && match(pi+1, si+1)
		}
		memo[pi][si] = result
		return result
	}
	return match(0, 0)
}

// patternSpecificity scores a pattern for the overlap rule: the depth of its
// fixed (wildcard-free) leading prefix, then its length.
func patternSpecificity(pattern string) (depth, length int) {
	for _, segment := range strings.Split(pattern, "/") {
		if strings.ContainsAny(segment, "*?[") {
			break
		}
		depth++
	}
	return depth, len(pattern)
}

// classify returns the concept matching one path inside a root, applying the
// overlap rule: the most specific glob wins — the deepest fixed prefix, then
// the longest pattern, then declaration order. A concept's declared `exclude`
// globs remove a matched candidate from that concept (an actual-documents
// concept sheds its section's templates this way). A directory path matches
// only directory concepts and a file path only file concepts, which is how a
// container concept (`changes/*`) coexists with the files inside it.
func classify(fw *Framework, rootID, rel string, dir bool) *Concept {
	var best *Concept
	bestDepth, bestLength := -1, -1
	for i := range fw.Concepts {
		concept := &fw.Concepts[i]
		if concept.Match.Root != rootID || concept.Match.Dir != dir {
			continue
		}
		if !matchPattern(concept.Match.Glob, rel) {
			continue
		}
		excluded := false
		for _, pattern := range concept.Match.Exclude {
			if matchPattern(pattern, rel) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		depth, length := patternSpecificity(concept.Match.Glob)
		if depth > bestDepth || (depth == bestDepth && length > bestLength) {
			best, bestDepth, bestLength = concept, depth, length
		}
	}
	return best
}

// -- the reader --

// instance is one matched path: a concept instance plus what was read from it.
type instance struct {
	concept  *Concept
	rootID   string
	rootPath string // the root's declared workspace-relative path
	path     string // workspace-relative, slash separated
	rel      string // root-relative, slash separated
	abs      string // absolute, containment-validated
	dir      bool
	content  string
}

// slugResult caches one file's anchor namespace; ok is false when the file
// could not be read, in which case an anchor is treated as resolving.
type slugResult struct {
	slugs map[string]bool
	ok    bool
}

type parser struct {
	framework Framework
	tree      *Tree
	absRoot   string

	instances []*instance
	byConcept map[string][]*instance

	regexCache map[string]*regexp.Regexp
	slugCache  map[string]slugResult

	// Parse-budget accounting across the whole tree: bytes of content read,
	// documents read and entries VISITED (a directory, an unreadable file and a
	// non-matching sibling all cost the walk real time without reaching the
	// content budget). budgetWarned/entriesWarned keep each exhaustion notice to
	// one line, and suppressedWarnings counts diagnostics past the cap so
	// finish can collapse them into a single counted line.
	budgetBytes        int64
	budgetDocs         int
	budgetEntries      int
	budgetWarned       bool
	entriesWarned      bool
	suppressedWarnings int

	// Findings are bounded the same way (maxParseFindings): the first findings
	// in deterministic emission order are kept, the rest are counted and
	// collapsed into one summary by finish. refsLeft is the cross-reference
	// resolution budget (maxParseCrossRefs); refsWarned keeps its exhaustion
	// notice to one line.
	suppressedFindings int
	lastSuppressed     Finding
	refsLeft           int
	refsWarned         bool
}

// ParseTree reads one workspace through one framework and returns its model.
//
// A workspace missing every declared root parses to Present=false with empty
// collections, and no error: an absent framework is a state, not a failure. An
// error is returned only when the workspace root itself cannot be resolved to
// an absolute path, which no caller of this package should ever hit.
func ParseTree(workspaceRoot string, fw Framework) (*Tree, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		// An empty workspace root must not silently resolve to the process
		// CWD (filepath.Abs("") does): a caller without a workspace has no
		// tree to read, and reading the directory the app was launched from
		// would present a stranger's files as this project's specifications.
		return nil, errors.New("sdd: ParseTree needs a workspace root, got an empty path")
	}
	fw = normalizeViews(fw)
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("sdd: resolving workspace root %q: %w", workspaceRoot, err)
	}
	p := &parser{
		framework:  fw,
		absRoot:    absRoot,
		tree:       &Tree{Path: workspaceRoot, Framework: fw.ID, Label: fw.Label},
		byConcept:  make(map[string][]*instance, len(fw.Concepts)),
		regexCache: make(map[string]*regexp.Regexp, len(fw.Checks)),
		slugCache:  make(map[string]slugResult),
		refsLeft:   maxParseCrossRefs,
	}
	p.collect()
	p.buildDocuments()
	p.buildNodes()
	p.runChecks()
	p.finish()
	return p.tree, nil
}

// normalizeViews applies the documented fallback for a framework without
// views: one implicit tree section over every declared concept. Without it
// such a framework recognized its documents but rendered an empty panel —
// the nodes exist only for concepts a tree section names.
func normalizeViews(fw Framework) Framework {
	if len(fw.Views.Sections) > 0 || len(fw.Concepts) == 0 {
		return fw
	}
	kinds := make([]string, 0, len(fw.Concepts))
	for _, c := range fw.Concepts {
		kinds = append(kinds, c.Kind)
	}
	fw.Views.Sections = []Section{{ID: "tree", Label: fw.Label, Kind: SectionTree, Concepts: kinds}}
	return fw
}

// resolve maps a workspace-relative path to its absolute form after validating
// that the real path stays inside the workspace. A symlink pointing out of the
// tree fails this test and is excluded, so no read ever leaves the workspace.
func (p *parser) resolve(rel string) (string, bool) {
	abs := filepath.Join(p.absRoot, filepath.FromSlash(rel))
	ok, err := pathutil.IsWithinPath(p.absRoot, abs)
	if err != nil || !ok {
		return abs, false
	}
	return abs, true
}

// warnf records a non-fatal read diagnostic, bounded by maxParseWarnings. The
// first maxParseWarnings messages are kept in order; everything past them is
// counted and collapsed into one line by finish, so a tree with thousands of
// unreadable entries cannot make the diagnostics themselves the unbounded
// artefact. The two budget notices bypass this cap — they are appended
// directly, because they are the explanation for why the model stops growing
// and must never be the message that got dropped.
func (p *parser) warnf(format string, args ...any) {
	if len(p.tree.Warnings) >= maxParseWarnings {
		p.suppressedWarnings++
		return
	}
	p.tree.Warnings = append(p.tree.Warnings, "sdd: "+fmt.Sprintf(format, args...))
}

// spendCrossRef charges one cross-reference resolution against
// maxParseCrossRefs and reports whether it may proceed. Every reference costs a
// stat (and often a slug build), which is work spent BEFORE any finding is
// recorded, so the findings cap alone would not bound it. The exhaustion notice
// is appended directly rather than through warnf: it explains why the rest of
// the links are unjudged and must never be the line the diagnostics cap drops.
func (p *parser) spendCrossRef() bool {
	if p.refsLeft > 0 {
		p.refsLeft--
		return true
	}
	if !p.refsWarned {
		p.refsWarned = true
		p.tree.Warnings = append(p.tree.Warnings, fmt.Sprintf(
			"sdd: cross-reference budget exhausted after %d resolved references; the remaining links are not judged (shrink the tree or the concept globs)", maxParseCrossRefs))
	}
	return false
}

// collect walks every declared root and records the concept instances.
// Present means what the model documents say it means: at least one declared
// root exists as a directory AND every root the framework marked required
// exists — a framework whose required root is missing is not present, and the
// missing root is named in Warnings so the empty state can say why. A root
// that resolves through a workspace-internal symlink is walked through its
// physical target (WalkDir does not descend a symlinked root), while the
// instance paths stay in the root's declared namespace.
func (p *parser) collect() {
	anyPresent, allRequiredPresent := false, true
	for _, root := range p.framework.Roots {
		state := RootState{ID: root.ID, Path: root.Path, Label: root.Label, Required: root.Required}
		absRoot, ok := p.resolve(root.Path)
		if !ok {
			if root.Required {
				allRequiredPresent = false
				p.warnf("required root %q (%s) is unavailable: escapes the workspace", root.ID, root.Path)
			} else {
				p.warnf("root %q (%s) escapes the workspace, skipped", root.ID, root.Path)
			}
			p.tree.Roots = append(p.tree.Roots, state)
			continue
		}
		info, err := os.Stat(absRoot)
		if err != nil || !info.IsDir() {
			if root.Required {
				allRequiredPresent = false
				p.warnf("required root %q (%s) is missing", root.ID, root.Path)
			}
			p.tree.Roots = append(p.tree.Roots, state)
			continue
		}
		// Walk the PHYSICAL root: os.Stat follows the entry's symlink, but
		// WalkDir would see a non-directory root entry and skip everything,
		// silently producing an empty (yet "present") tree for a valid
		// workspace-internal symlink. The walked tree keeps validating every
		// entry against the workspace, so an escaping link inside the root is
		// still excluded.
		physical, err := filepath.EvalSymlinks(absRoot)
		if err != nil || !p.withinWorkspace(physical) {
			// A root that does not resolve inside the workspace contributes
			// nothing, so it must not count as present — including for a
			// REQUIRED root, whose unusability fails the tree's Present gate
			// exactly like a missing required root does.
			if root.Required {
				allRequiredPresent = false
				p.warnf("required root %q (%s) is unavailable: does not resolve inside the workspace", root.ID, root.Path)
			} else {
				p.warnf("root %q (%s) does not resolve inside the workspace, skipped", root.ID, root.Path)
			}
			p.tree.Roots = append(p.tree.Roots, state)
			continue
		}
		state.Present = true
		anyPresent = true
		p.tree.Roots = append(p.tree.Roots, state)
		p.walk(root, physical)
	}
	p.tree.Present = anyPresent && allRequiredPresent

	sort.SliceStable(p.instances, func(i, j int) bool { return p.instances[i].path < p.instances[j].path })
	for _, inst := range p.instances {
		p.byConcept[inst.concept.Kind] = append(p.byConcept[inst.concept.Kind], inst)
	}
}

// withinWorkspace reports whether an absolute path stays inside the workspace.
func (p *parser) withinWorkspace(abs string) bool {
	ok, err := pathutil.IsWithinPath(p.absRoot, abs)
	return err == nil && ok
}

// walk records the concept instances under one present root.
func (p *parser) walk(root Root, absRoot string) {
	_ = filepath.WalkDir(absRoot, func(pth string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry == nil || pth == absRoot {
			return nil //nolint:nilerr // an unreadable entry is skipped; a partial tree stays valid
		}
		// Charge the visit before doing any work on it. An unreadable file, a
		// non-matching sibling and a directory instance all cost the walk real
		// time while none of them reaches the content budget, so the walk needs
		// its own bound or the retained-content cap says nothing about work.
		if p.entryBudgetExhausted() {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() && prunedDirNames[entry.Name()] {
			return fs.SkipDir // a VCS store or dependency tree never holds specs
		}
		relOS, relErr := filepath.Rel(absRoot, pth)
		if relErr != nil {
			return nil //nolint:nilerr // an unrelativizable path cannot occur under the walked root
		}
		rel := filepath.ToSlash(relOS)
		wpath := path.Join(root.Path, rel)
		abs, ok := p.resolve(wpath)
		if !ok {
			p.warnf("%s escapes the workspace, skipped", wpath)
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		concept := classify(&p.framework, root.ID, rel, entry.IsDir())
		if concept == nil {
			return nil // outside the declared concepts: not part of the model
		}
		inst := &instance{
			concept:  concept,
			rootID:   root.ID,
			rootPath: root.Path,
			path:     wpath,
			rel:      rel,
			abs:      abs,
			dir:      entry.IsDir(),
		}
		if !inst.dir {
			if p.budgetExhausted() {
				return nil // past the budget: excluded whole, one warning said so
			}
			content, readOK := readDocument(abs)
			if !readOK {
				p.warnf("%s cannot be read, skipped", wpath)
				return nil
			}
			p.budgetDocs++
			p.budgetBytes += int64(len(content))
			inst.content = content
		}
		p.instances = append(p.instances, inst)
		return nil
	})
}

// entryBudgetExhausted reports whether the walk has spent its entry budget,
// warning once when it crosses. Past it the walk stops descending instead of
// visiting and classifying the rest of the tree.
func (p *parser) entryBudgetExhausted() bool {
	if p.budgetEntries >= maxParseEntries {
		if !p.entriesWarned {
			p.entriesWarned = true
			p.tree.Warnings = append(p.tree.Warnings, fmt.Sprintf(
				"sdd: walk budget exhausted after %d entries; the remaining entries are not classified (shrink the roots or the concept globs)", maxParseEntries))
		}
		return true
	}
	p.budgetEntries++
	return false
}

// budgetExhausted reports whether the parse budget is spent, warning once
// when it crosses. A document past the budget is excluded WHOLE — a
// contentless instance would turn every content check into a false finding.
func (p *parser) budgetExhausted() bool {
	if p.budgetDocs >= maxParseTotalDocuments || p.budgetBytes >= maxParseTotalBytes {
		if !p.budgetWarned {
			p.budgetWarned = true
			p.tree.Warnings = append(p.tree.Warnings, fmt.Sprintf(
				"sdd: parse budget exhausted after %d documents / %d MiB of content; documents past it are excluded whole and cross-reference targets beyond it resolve unjudged (raise nothing — shrink the tree or the concept globs)",
				p.budgetDocs, p.budgetBytes>>20))
		}
		return true
	}
	return false
}

// readDocument reads one document, bounded and normalized: a missing,
// unreadable, non-regular or oversized file is skipped rather than read, and
// the bytes that ARE read are normalized once — a leading BOM stripped and
// CRLF (or a lone CR) folded to LF — so every consumer (frontmatter, regex
// checks, prose scans) sees the same text regardless of the checkout's line
// endings. Without this a Windows checkout would defeat every `$`-anchored
// content_regex and hide a BOM-led frontmatter block.
func readDocument(abs string) (string, bool) {
	data, err := safeio.ReadFileLimited(abs, maxDocumentBytes)
	if err != nil {
		return "", false
	}
	return normalizeDocumentText(data), true
}

// normalizeDocumentText strips one leading UTF-8 BOM and folds CRLF / CR
// endings to LF. It is applied exactly once, at read time.
func normalizeDocumentText(data []byte) string {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !bytes.ContainsRune(data, '\r') {
		return string(data)
	}
	return strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
}

// buildDocuments turns file instances into model documents, extracting exactly
// the metadata each concept declares.
func (p *parser) buildDocuments() {
	for _, inst := range p.instances {
		if inst.dir {
			continue
		}
		p.tree.Documents = append(p.tree.Documents, p.document(inst))
	}
}

// document builds one model document. Number and Slug are name-derived
// structure and are always populated; every other field appears only because
// the concept declared the corresponding extraction directive.
func (p *parser) document(inst *instance) Document {
	doc := Document{
		Path:    inst.path,
		Root:    inst.rootID,
		Concept: inst.concept.Kind,
		Label:   inst.concept.Label,
		Section: topSection(inst.rel),
		Name:    path.Base(inst.rel),
	}
	doc.Number, doc.Slug = splitName(doc.Name)
	body := bodyOf(inst.content) // metadata is not prose (see bodyOf)
	for _, directive := range inst.concept.Extract {
		switch directive {
		case ExtractTitle:
			doc.Title = parseTitle(body)
			doc.DisplayTitle = documentTitle(doc.Name, body)
		case ExtractHeadings:
			doc.Headings = headingsOf(body)
		case ExtractKeyFiles:
			doc.KeyFiles = parseKeyFiles(body)
		case ExtractCrossRefs:
			doc.CrossRefs = parseCrossRefs(body)
		case ExtractFrontmatter:
			doc.Frontmatter = frontmatterOf(inst.content)
		case ExtractPurpose:
			doc.Purpose = topicBody(body, "purpose")
		case ExtractRequirements:
			doc.Requirements = topicItems(body, "requirement")
		case ExtractScenarios:
			doc.Scenarios = topicItems(body, "scenario")
		case ExtractDeltas:
			doc.Deltas = deltaEntries(body)
		case ExtractPrinciples:
			doc.Principles = topicItems(body, "principle")
		case ExtractUserStories:
			doc.UserStories = topicItems(body, "user stor")
		case ExtractTaskItems:
			doc.TaskItems = taskItems(body)
		case ExtractIndexTasks:
			doc.Tasks = indexTasks(body)
		case ExtractStatus:
			doc.Status, _ = parseStatus(body)
		case ExtractSupersededBy:
			_, doc.SupersededBy = parseStatus(body)
		case ExtractNumber:
			// devNull: the number is name-derived and already populated.
		}
	}
	doc.Missing = unsatisfiedRequirements(inst)
	return doc
}

// unsatisfiedRequirements reports the concept's declared `required` tokens the
// document does not satisfy, in declaration order.
func unsatisfiedRequirements(inst *instance) []string {
	var missing []string
	for _, token := range inst.concept.Required {
		if !requirementMet(inst, token) {
			missing = append(missing, token)
		}
	}
	return missing
}

// requirementMet evaluates one `required` token against the document content.
func requirementMet(inst *instance, token string) bool {
	body := bodyOf(inst.content)
	switch token {
	case RequireNonEmpty:
		return strings.TrimSpace(inst.content) != ""
	case RequireTitle:
		return parseTitle(body) != ""
	case RequireStatus:
		status, _ := parseStatus(body)
		return status != ""
	case RequirePurpose:
		return topicBody(body, "purpose") != ""
	case RequireRequirements:
		return len(topicItems(body, "requirement")) > 0
	case RequireScenarios:
		return len(topicItems(body, "scenario")) > 0
	case RequireTasks:
		return len(taskItems(body)) > 0
	case RequirePrinciples:
		return len(topicItems(body, "principle")) > 0
	case RequireUserStories:
		return len(topicItems(body, "user stor")) > 0
	}
	return false
}

// buildNodes renders the declared tree views: a shared forest of folders and
// instances, plus one entry per declared section pointing at its top nodes.
func (p *parser) buildNodes() {
	viewed := make(map[string]bool, len(p.framework.Concepts))
	for _, section := range p.framework.Views.Sections {
		if section.Kind != SectionTree {
			continue
		}
		for _, kind := range section.Concepts {
			viewed[kind] = true
		}
	}

	nodes := make(map[string]*Node, len(p.instances))
	var created []string

	ensure := func(wpath, kind string) *Node {
		if node, ok := nodes[wpath]; ok {
			if node.Kind == NodeFolder && kind != NodeFolder {
				node.Kind = kind
			}
			return node
		}
		node := &Node{ID: wpath, Kind: kind, Path: wpath, Name: path.Base(wpath), Label: path.Base(wpath)}
		nodes[wpath] = node
		created = append(created, wpath)
		return node
	}

	// chainIDs lists the node ids of one instance's whole ancestor chain,
	// root path first, the instance last — exactly the nodes a section
	// owning this instance renders.
	chainIDs := func(inst *instance) []string {
		ids := make([]string, 0, len(strings.Split(inst.rel, "/"))+1)
		wpath := inst.rootPath
		ids = append(ids, wpath)
		for _, segment := range strings.Split(inst.rel, "/") {
			wpath = path.Join(wpath, segment)
			ids = append(ids, wpath)
		}
		return ids
	}

	// anchor registers the instance's whole ancestor chain and returns the
	// chain's top node — the entry a section lists.
	anchor := func(inst *instance) string {
		var top, parent string
		segments := strings.Split(inst.rel, "/")
		wpath := inst.rootPath
		for i, segment := range segments {
			wpath = path.Join(wpath, segment)
			last := i == len(segments)-1
			kind := NodeFolder
			if last {
				kind = NodeDocument
				if inst.dir {
					kind = NodeDirectory
				}
			}
			node := ensure(wpath, kind)
			if last {
				node.Concept = inst.concept.Kind
				// The row label is the DOCUMENT's identity (its display
				// title, falling back to its name), not the concept's
				// generic label: eight ADRs must read as eight different
				// rows, and two change directories as two different
				// directories. The concept's label stays available through
				// the node's Concept kind.
				node.Label = p.instanceLabel(inst)
				if inst.dir {
					node.Missing = p.missingCompanions(inst)
				}
			}
			switch parent {
			case "":
				top = wpath
			default:
				if !slices.Contains(nodes[parent].Children, wpath) {
					nodes[parent].Children = append(nodes[parent].Children, wpath)
				}
			}
			node.Parent = parent
			parent = wpath
		}
		return top
	}

	for _, inst := range p.instances {
		if viewed[inst.concept.Kind] {
			anchor(inst)
		}
	}

	for _, section := range p.framework.Views.Sections {
		view := View{ID: section.ID, Label: section.Label, Kind: section.Kind}
		if section.Kind == SectionTree {
			seen := make(map[string]bool)
			members := make(map[string]bool)
			for _, kind := range section.Concepts {
				for _, inst := range p.byConcept[kind] {
					for _, id := range chainIDs(inst) {
						members[id] = true
					}
					top := anchor(inst)
					if seen[top] {
						continue
					}
					seen[top] = true
					view.Nodes = append(view.Nodes, top)
				}
			}
			// Sorted membership: the model's collections are deterministic,
			// and a consumer may diff or snapshot it.
			view.Members = make([]string, 0, len(members))
			for id := range members {
				view.Members = append(view.Members, id)
			}
			sort.Strings(view.Members)
		}
		p.tree.Views = append(p.tree.Views, view)
	}

	for _, id := range created {
		node := nodes[id]
		sort.Strings(node.Children)
	}
	for _, id := range created {
		p.tree.Nodes = append(p.tree.Nodes, *nodes[id])
	}
	sort.Slice(p.tree.Nodes, func(i, j int) bool { return p.tree.Nodes[i].Path < p.tree.Nodes[j].Path })
}

// instanceLabel is the display label for one instance node: the document's
// display title for a file (unique per document), the directory's own name
// for a directory instance. The title is read from the markdown BODY —
// frontmatter is metadata, so a block-scalar example heading can never
// become a row label.
func (p *parser) instanceLabel(inst *instance) string {
	if !inst.dir {
		if title := documentTitle(path.Base(inst.rel), bodyOf(inst.content)); title != "" {
			return title
		}
	}
	return path.Base(inst.path)
}

// missingCompanions reports the declared companion files a directory instance
// does not carry, in declaration order.
func (p *parser) missingCompanions(inst *instance) []string {
	var missing []string
	for _, file := range inst.concept.Files {
		if !p.existsFile(path.Join(inst.path, file)) {
			missing = append(missing, file)
		}
	}
	return missing
}

// existsFile is exists restricted to regular files: a declaration that
// requires "index.md" is not satisfied by a DIRECTORY of that name.
func (p *parser) existsFile(wpath string) bool {
	abs, ok := p.resolve(wpath)
	if !ok {
		return false
	}
	info, err := os.Stat(abs)
	return err == nil && info.Mode().IsRegular()
}

// exists reports whether wpath resolves, inside the workspace, to a path of
// ANY kind. It is the predicate for paths a document lists about itself — an
// extracted "## Key Files" entry legitimately names a directory, so requiring
// a regular file there would turn every correctly documented package
// directory into a finding.
func (p *parser) exists(wpath string) bool {
	abs, ok := p.resolve(wpath)
	if !ok {
		return false
	}
	_, err := os.Stat(abs)
	return err == nil
}

// regexpFor returns the compiled pattern, caching by source text. Compilation
// cannot fail here — the loader validated every declared pattern — so an
// unexpected failure only skips the check.
func (p *parser) regexpFor(source string) (*regexp.Regexp, bool) {
	if re, ok := p.regexCache[source]; ok {
		return re, re != nil
	}
	re, err := regexp.Compile(source)
	if err != nil {
		p.regexCache[source] = nil
		p.warnf("pattern %q does not compile, check skipped", source)
		return nil, false
	}
	p.regexCache[source] = re
	return re, true
}

// slugs returns a file's anchor namespace. ok is false when the file could not
// be read (outside the workspace, unreadable, oversized, past the parse
// budget), in which case an anchor is treated as resolving — an
// under-approximation, never a false finding. A readable file with no headings
// at all yields nil slugs with ok=true: the namespace is unknown, so an anchor
// into it is not judged either (see checkCrossRefs).
func (p *parser) slugs(abs string) (map[string]bool, bool) {
	if cached, ok := p.slugCache[abs]; ok {
		return cached.slugs, cached.ok
	}
	// Cross-reference targets are charged to the same per-parse budget as the
	// walk: a document full of anchored links to distinct large files would
	// otherwise re-read half the checkout on every refresh, outside the cap.
	// Past the budget the namespace is simply unknown, and checkAnchor already
	// treats unknown namespaces as resolving.
	if p.budgetExhausted() {
		p.slugCache[abs] = slugResult{}
		return nil, false
	}
	content, readOK := readDocument(abs)
	result := slugResult{ok: readOK}
	if readOK {
		result.slugs = anchorSlugs(bodyOf(content))
		p.budgetBytes += int64(len(content))
	}
	p.slugCache[abs] = result
	return result.slugs, result.ok
}

// -- declared checks --

// runChecks runs exactly the checks the framework declares, in declaration
// order. A framework that is not present has nothing to report: an absent tree
// is a state, not a set of findings.
func (p *parser) runChecks() {
	if !p.tree.Present {
		return
	}
	for i := range p.framework.Checks {
		check := &p.framework.Checks[i]
		switch check.Kind {
		case CheckRequiredFiles:
			p.checkRequiredFiles(check)
		case CheckNamePattern:
			p.checkNamePattern(check)
		case CheckCrossRefs:
			p.checkCrossRefs(check)
		case CheckIndexCompleteness:
			p.checkIndexCompleteness(check)
		case CheckFrontmatterKeys:
			p.checkFrontmatterKeys(check)
		case CheckContentRegex:
			p.checkContentRegex(check)
		case CheckUnique:
			p.checkUnique(check)
		case CheckReferences:
			p.checkReferences(check)
		}
	}
}

// addFinding records one failed check. An empty subject falls back to the
// checked path so every finding names something.
func (p *parser) addFinding(check *Check, file, detail string) {
	p.addFindingKind(check, file, detail, check.Finding)
}

// addFindingKind records one failed check under an explicit finding token —
// the declared one for a resolution failure, the declared `cycle_finding` for
// a cycle in an acyclic relation. The collection is bounded by
// maxParseFindings: the findings past it are counted instead of retained and
// finish appends one summary for them.
func (p *parser) addFindingKind(check *Check, file, detail, kind string) {
	finding := Finding{
		Kind:     kind,
		File:     file,
		Detail:   detail,
		Message:  check.Message,
		Severity: check.Severity,
	}
	if len(p.tree.Findings) >= maxParseFindings {
		// Bounded model: findings past the cap are counted, not retained, and
		// finish collapses them into one summary carrying this finding's
		// severity/kind so the operator still sees what class was truncated.
		p.suppressedFindings++
		p.lastSuppressed = finding
		return
	}
	p.tree.Findings = append(p.tree.Findings, finding)
}

// checkRequiredFiles asserts file existence in one of three declared modes:
// per root (`files` without a concept), per concept instance (`concept` — a
// directory instance's declared companions, or a document's sibling files),
// or per path extracted from documents (`from: <extraction directive>`,
// scoped to the declared concept when it names one). Roots that are not
// present are skipped — a missing root is a root-level state, not a per-file
// finding. A DECLARED path — literal `files` (per root, or per concept
// instance) and a `from: files` companion list — must be a regular FILE: a
// directory at the required path does not satisfy the declaration. An
// EXTRACTED path (`from: <list directive>`) is only asserted to exist — a
// document's own "## Key Files" list legitimately names a package directory,
// and demanding a regular file there would turn a correct tree into permanent
// findings.
func (p *parser) checkRequiredFiles(check *Check) {
	switch {
	case len(check.Files) > 0 && check.Concept != "":
		for _, inst := range p.byConcept[check.Concept] {
			base := inst.path
			if !inst.dir {
				base = path.Dir(inst.path)
			}
			for _, file := range check.Files {
				wpath := path.Join(base, file)
				if p.existsFile(wpath) {
					continue
				}
				p.addFinding(check, wpath, "required file is missing: "+file)
			}
		}
	case len(check.Files) > 0:
		for _, root := range p.tree.Roots {
			if !root.Present {
				continue
			}
			for _, file := range check.Files {
				wpath := path.Join(root.Path, file)
				if p.existsFile(wpath) {
					continue
				}
				p.addFinding(check, wpath, "required file is missing: "+file)
			}
		}
	case check.From == CheckFromFiles:
		for _, inst := range p.byConcept[check.Concept] {
			if !inst.dir {
				continue
			}
			for _, file := range p.missingCompanions(inst) {
				p.addFinding(check, inst.path, "required file is missing: "+file)
			}
		}
	case check.From != "":
		subjects := p.instances
		if check.Concept != "" {
			subjects = p.fileInstances(check.Concept)
		}
		for _, inst := range subjects {
			if inst.dir {
				continue
			}
			for _, entry := range valuesFor(inst.content, check.From) {
				// Extracted entries are the document's OWN claim about the
				// workspace ("## Key Files"), so existence is the whole
				// assertion: real specs list directories there, and the
				// regular-file rule belongs to literal `files:` and to
				// concept companion files, which always name a file.
				if !p.workspacePathCandidate(entry) || p.exists(entry) {
					continue
				}
				p.addFinding(check, inst.path, "required path does not exist: "+entry)
			}
		}
	}
}

// workspacePathCandidate reports whether an extracted entry looks like a
// workspace-root-relative path the existence check can stat: not home,
// absolute or placeholder-shaped, no glob metacharacters, and — when the entry
// has a directory part — rooted at a directory that exists in the workspace.
// The last rule keeps SDK-side entries written as bare module-relative paths
// out of the check.
func (p *parser) workspacePathCandidate(entry string) bool {
	if entry == "" || strings.HasPrefix(entry, "/") || strings.HasPrefix(entry, "~") || strings.HasPrefix(entry, "<") {
		return false
	}
	if strings.ContainsAny(entry, "*?[]") {
		return false
	}
	segment, rest, _ := strings.Cut(entry, "/")
	if rest == "" {
		return true // a file directly at the workspace root
	}
	abs, ok := p.resolve(segment)
	if !ok {
		return false
	}
	info, err := os.Stat(abs)
	return err == nil && info.IsDir()
}

// checkNamePattern asserts each instance's base name matches the pattern, minus
// the exempted names.
func (p *parser) checkNamePattern(check *Check) {
	re, ok := p.regexpFor(check.Pattern)
	if !ok {
		return
	}
	for _, inst := range p.byConcept[check.Concept] {
		name := path.Base(inst.path)
		if slices.Contains(check.Allow, name) || re.MatchString(name) {
			continue
		}
		p.addFinding(check, inst.path, "name does not match "+check.Pattern+": "+name)
	}
}

// lineAnchorRe matches the line-number anchors GitHub-style renderers produce
// ("#L42", "#L10-L20") — positions, not headings, so they are never judged.
var lineAnchorRe = regexp.MustCompile(`^L\d+(?:-L\d+)?$`)

// checkCrossRefs resolves relative links: the target file must exist and the
// anchor must name a heading of it. Targets resolve against the document's
// directory and may legitimately leave the tree (a spec linking ../../README.md
// within the workspace); a target outside the workspace is not inspected at
// all, and a target whose anchor namespace is unknown (unreadable, or no
// headings at all — a plain source file) counts as resolving. Line anchors
// (#L12) and the bare "#" fragment name positions, not headings, and are
// never judged. Percent-escapes in the path are decoded before stat, so
// ../src/my%20file.ts resolves to my file.ts. Resolution itself is bounded by
// maxParseCrossRefs (one stat per reference), because a dense document costs
// work BEFORE any finding is recorded.
func (p *parser) checkCrossRefs(check *Check) {
	for _, inst := range p.crossRefSubjects(check) {
		for _, ref := range parseCrossRefs(bodyOf(inst.content)) {
			if !p.spendCrossRef() {
				return // resolution budget spent: the skip is named once in Warnings
			}
			file, anchor := splitRef(ref)
			if anchor == "" && file == "" {
				continue // a bare "#" fragment names nothing
			}
			if file == "" {
				p.checkAnchor(check, inst, anchor, inst.abs, ref)
				continue
			}
			if decoded, err := url.PathUnescape(file); err == nil {
				file = decoded
			}
			target := filepath.Join(filepath.Dir(inst.abs), filepath.FromSlash(file))
			if ok, err := pathutil.IsWithinPath(p.absRoot, target); err != nil || !ok {
				continue // outside the workspace: not this tree's reference to judge
			}
			if _, err := os.Stat(target); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					p.addFinding(check, inst.path, "link does not resolve: "+ref)
				}
				continue
			}
			if anchor == "" {
				continue
			}
			p.checkAnchor(check, inst, anchor, target, ref)
		}
	}
}

// checkAnchor judges one anchor against a target's namespace. The namespace
// is under-approximate by construction: an anchor is reported missing only
// when the target was readable AND carries headings AND none of them — plain
// or duplicate-suffixed — produces the anchor, and the anchor is not a
// line-position reference.
func (p *parser) checkAnchor(check *Check, inst *instance, anchor, targetAbs, ref string) {
	if lineAnchorRe.MatchString(anchor) {
		return
	}
	slugs, ok := p.slugs(targetAbs)
	if !ok || slugs == nil {
		return // unreadable, or no headings: the namespace is unknown, not broken
	}
	if !slugs[anchor] {
		p.addFinding(check, inst.path, "anchor does not resolve: "+ref)
	}
}

// crossRefSubjects selects the documents a crossrefs check resolves: the named
// concept's instances, or — when the check names none — every concept that
// declares `cross_refs`.
func (p *parser) crossRefSubjects(check *Check) []*instance {
	if check.Concept != "" {
		return p.fileInstances(check.Concept)
	}
	var subjects []*instance
	for _, inst := range p.instances {
		if inst.dir || !slices.Contains(inst.concept.Extract, ExtractCrossRefs) {
			continue
		}
		subjects = append(subjects, inst)
	}
	return subjects
}

// checkIndexCompleteness asserts the index document names every instance of
// the covered concepts: a file instance counts as named by an exact path
// mention or by a bare file-name mention; a DIRECTORY instance counts as named
// by an exact path mention or a bare directory-name mention, in either the
// .md-filtered or the raw token vocabulary (an index may list `bundles/a`
// without a suffix). This is what keeps a `covers` entry naming a directory
// concept from silently checking nothing.
func (p *parser) checkIndexCompleteness(check *Check) {
	for _, index := range p.fileInstances(check.Concept) {
		exact, names, raw := indexMentions(bodyOf(index.content))
		dir := path.Dir(index.path)
		for _, kind := range check.Covers {
			for _, inst := range p.byConcept[kind] {
				rel, inside := relativeTo(dir, inst.path)
				if !inside {
					continue // another subtree: this index does not cover it
				}
				base := path.Base(rel)
				covered := exact[rel] || names[base] || raw[rel] || raw[base]
				if inst.dir {
					covered = covered || raw[rel+"/"] || exact[rel+"/"]
				}
				if covered {
					continue
				}
				p.addFinding(check, inst.path, "not listed in "+path.Base(index.path))
			}
		}
	}
}

// relativeTo returns target relative to dir and whether it lies inside it.
func relativeTo(dir, target string) (string, bool) {
	rel, err := filepath.Rel(filepath.FromSlash(dir), filepath.FromSlash(target))
	if err != nil {
		return "", false
	}
	slash := filepath.ToSlash(rel)
	if slash == ".." || strings.HasPrefix(slash, "../") {
		return "", false
	}
	return slash, true
}

// checkFrontmatterKeys asserts a document's frontmatter carries every declared
// key.
func (p *parser) checkFrontmatterKeys(check *Check) {
	for _, inst := range p.fileInstances(check.Concept) {
		frontmatter := frontmatterOf(inst.content)
		var missing []string
		for _, key := range check.Keys {
			if _, ok := frontmatter[key]; !ok {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			p.addFinding(check, inst.path, "frontmatter is missing keys: "+strings.Join(missing, ", "))
		}
	}
}

// checkContentRegex asserts a document's selected text matches the pattern,
// and — with `capture` and `equals` — that the captured value agrees with the
// document's own name. With `optional`, a non-match is not a finding and an
// empty capture is not compared: the check asserts agreement only when the
// pattern had something to say (an ADR heading that spells no number cannot
// mismatch one).
func (p *parser) checkContentRegex(check *Check) {
	re, ok := p.regexpFor("(?m)" + check.Regex)
	if !ok {
		return
	}
	capture := -1
	if check.Capture != "" {
		capture = re.SubexpIndex(check.Capture)
	}
	for _, inst := range p.fileInstances(check.Concept) {
		match := re.FindStringSubmatch(selectedText(inst.content, check.In))
		if match == nil {
			if !check.Optional {
				p.addFinding(check, inst.path, "pattern does not match: "+check.Regex)
			}
			continue
		}
		if check.Equals != CheckEqualsFilenameNumber || capture < 0 || capture >= len(match) {
			continue
		}
		if check.Optional && match[capture] == "" {
			continue // nothing captured: no comparison was promised
		}
		number, _ := splitName(path.Base(inst.path))
		if number == "" {
			// The name carries no numeric token, so `filename_number` has
			// nothing to compare against: a misnamed file is the name_pattern
			// check's subject, and judging it here would double-report it.
			continue
		}
		if !sameNumber(match[capture], number) {
			p.addFinding(check, inst.path, "captured "+check.Capture+" "+match[capture]+" does not match the file name number "+number)
		}
	}
}

// selectedText returns the text a `content_regex` check reads: the first
// line, the first ATX heading, the frontmatter block, the markdown body with
// fenced examples removed (`prose`), or the whole body (the default). The
// frontmatter mode reads the raw document; every other mode reads the body —
// frontmatter is metadata, not prose.
func selectedText(content, in string) string {
	body := bodyOf(content)
	switch in {
	case CheckInFirstLine:
		line, _, _ := strings.Cut(body, "\n")
		return strings.TrimSuffix(line, "\r")
	case CheckInFirstHeading:
		return parseTitle(body)
	case CheckInFrontmatter:
		if m := frontmatterRe.FindStringSubmatch(content); m != nil {
			return m[1]
		}
		return ""
	case CheckInProse:
		return plainText(body)
	}
	return body
}

// sameNumber compares two number-ish tokens numerically when both parse, and
// verbatim otherwise, so an unpadded spelling ("42" for 042) agrees.
func sameNumber(a, b string) bool {
	na, aerr := strconv.Atoi(strings.TrimSpace(a))
	nb, berr := strconv.Atoi(strings.TrimSpace(b))
	if aerr == nil && berr == nil {
		return na == nb
	}
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// checkUnique asserts an extracted field is unique across a concept's
// instances, reporting one finding per duplicate group.
func (p *parser) checkUnique(check *Check) {
	type group struct {
		value     string
		instances []*instance
	}
	index := make(map[string]*group)
	var order []*group
	for _, inst := range p.fileInstances(check.Concept) {
		value := fieldValue(path.Base(inst.path), inst.content, check.Field)
		if value == "" {
			continue
		}
		key := normalizeNumber(value)
		if existing, ok := index[key]; ok {
			existing.instances = append(existing.instances, inst)
			continue
		}
		entry := &group{value: value, instances: []*instance{inst}}
		index[key] = entry
		order = append(order, entry)
	}
	for _, entry := range order {
		if len(entry.instances) < 2 {
			continue
		}
		files := make([]string, len(entry.instances))
		for i, inst := range entry.instances {
			files[i] = inst.path
		}
		p.addFinding(check, entry.instances[0].path,
			"duplicate "+check.Field+" "+normalizeNumber(entry.value)+": "+strings.Join(files, ", "))
	}
}

// checkReferences asserts an extracted reference field resolves to an instance
// of the declared concept, and — with `acyclic` — that the relation forms no
// cycle. Resolution and cycle detection are independent judgments of the same
// declared check: `acyclic` ADDS the cycle report, it never silences the
// missing-target one (a lone `references {acyclic: true}` still checks both).
// A numeric token in the calendar-year range that resolves to nothing is
// treated as a year, not a broken reference — record numbers of that range
// resolve and are reported like any other.
func (p *parser) checkReferences(check *Check) {
	targets := p.identityIndex(check.Of)
	if check.Acyclic {
		p.checkReferenceCycles(check, targets)
	}
	for _, inst := range p.fileInstances(check.Concept) {
		value := fieldValue(path.Base(inst.path), inst.content, check.Field)
		if value == "" {
			continue
		}
		if _, ok := resolveIdentity(targets, value); ok {
			continue
		}
		if number, ok := numericToken(normalizeRef(value)); ok && unresolvedYearToken(number) {
			continue // a year, not a record number
		}
		p.addFinding(check, inst.path, "unresolved "+check.Field+": "+value)
	}
}

// unresolvedYearToken reports whether a numeric token names a calendar year
// (1900–2099). Such a token in a supersession line ("superseded in 2024")
// names no record this package could resolve, and reporting it would turn
// prose into a false finding; a real record numbered in that range resolves
// before this predicate is consulted.
func unresolvedYearToken(number string) bool {
	n, err := strconv.Atoi(number)
	if err != nil {
		return false
	}
	return n >= 1900 && n <= 2099
}

// checkReferenceCycles reports loops in the reference relation, once per
// cycle, on its lowest-path member. Cycle findings carry the check's declared
// `cycle_finding` token when it declares one, and the check's `finding` token
// otherwise.
func (p *parser) checkReferenceCycles(check *Check, targets map[string]*instance) {
	source := p.fileInstances(check.Concept)
	successor := make(map[string]string, len(source))
	for _, inst := range source {
		value := fieldValue(path.Base(inst.path), inst.content, check.Field)
		if value == "" {
			continue
		}
		target, ok := resolveIdentity(targets, value)
		if !ok {
			continue // a missing target ends the chain; resolution reported it
		}
		successor[inst.path] = target.path
	}

	cycleFinding := check.Finding
	if check.CycleFinding != "" {
		cycleFinding = check.CycleFinding
	}
	reported := make(map[string]bool, len(source))
	for _, start := range source {
		if reported[start.path] {
			continue
		}
		onPath := make(map[string]bool, len(source))
		var order []string
		current, cycled := start.path, false
		for step := 0; step <= len(source); step++ {
			if onPath[current] {
				cycled = true
				break
			}
			onPath[current] = true
			order = append(order, current)
			next, ok := successor[current]
			if !ok {
				break
			}
			current = next
		}
		if !cycled {
			continue
		}
		// The cycle is the on-path suffix starting at the repeated node; a
		// prefix leading into it is not part of the loop.
		first := 0
		for order[first] != current {
			first++
		}
		members := order[first:]
		smallest := members[0]
		for _, member := range members {
			if member < smallest {
				smallest = member
			}
		}
		if reported[smallest] {
			continue
		}
		for _, member := range members {
			reported[member] = true
		}
		p.addFindingKind(check, smallest, "cycle in "+check.Field+": "+strings.Join(members, " -> ")+" -> "+smallest, cycleFinding)
	}
}

// identityIndex resolves a concept's instances by every identity a reference
// may use: the path, the root-relative path, the file name, the name without
// its extension, the slug and the number — each path-shaped key also in its
// extensionless spelling, since a reference may cite either ("records/001-a"
// for records/001-a.md). The first instance in path order wins a shared
// identity, so duplicate carriers resolve deterministically.
func (p *parser) identityIndex(kind string) map[string]*instance {
	index := make(map[string]*instance)
	set := func(key string, inst *instance) {
		if _, ok := index[key]; !ok {
			index[key] = inst
		}
	}
	for _, inst := range p.byConcept[kind] {
		name := path.Base(inst.path)
		base := strings.TrimSuffix(name, path.Ext(name))
		number, slug := splitName(name)
		for _, key := range []string{inst.path, inst.rel, name, base, slug} {
			if key == "" {
				continue
			}
			set("s:"+key, inst)
			if ext := path.Ext(key); ext != "" {
				set("s:"+strings.TrimSuffix(key, ext), inst)
			}
		}
		if number != "" {
			set("n:"+normalizeNumber(number), inst)
		}
	}
	return index
}

// resolveIdentity resolves one reference token against an identity index.
func resolveIdentity(index map[string]*instance, ref string) (*instance, bool) {
	key := normalizeRef(ref)
	if inst, ok := index["s:"+key]; ok {
		return inst, true
	}
	if number, ok := numericToken(key); ok {
		if inst, ok := index["n:"+number]; ok {
			return inst, true
		}
	}
	return nil, false
}

// normalizeRef reduces a reference token to the identity vocabulary: brackets,
// quotes and markup stripped, a leading ADR-/anchor marker and a .md suffix
// removed.
func normalizeRef(ref string) string {
	token := strings.TrimSpace(ref)
	token = strings.Trim(token, "[]()<>`\"'")
	token = strings.TrimPrefix(token, "ADR-")
	token = strings.TrimPrefix(token, "#")
	token = strings.TrimSuffix(token, ".md")
	return token
}

// numericToken reports whether a token is all digits, returning its unpadded
// form.
func numericToken(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	for _, r := range token {
		if !unicode.IsDigit(r) {
			return "", false
		}
	}
	return normalizeNumber(token), true
}

// normalizeNumber renders a numeric token without zero padding ("042" → "42")
// so padded and unpadded spellings agree; a non-numeric token is returned
// verbatim.
func normalizeNumber(token string) string {
	if n, err := strconv.Atoi(token); err == nil {
		return strconv.Itoa(n)
	}
	return token
}

// fileInstances returns the file instances of one concept kind, in path order.
func (p *parser) fileInstances(kind string) []*instance {
	var out []*instance
	for _, inst := range p.byConcept[kind] {
		if !inst.dir {
			out = append(out, inst)
		}
	}
	return out
}

// finish orders the model deterministically and aggregates the counts.
func (p *parser) finish() {
	if p.suppressedWarnings > 0 {
		p.tree.Warnings = append(p.tree.Warnings, fmt.Sprintf(
			"sdd: %d further diagnostics suppressed (the first %d are shown above)", p.suppressedWarnings, maxParseWarnings))
	}
	sort.Slice(p.tree.Findings, func(i, j int) bool {
		a, b := p.tree.Findings[i], p.tree.Findings[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Detail < b.Detail
	})
	p.tree.Findings = dedupeFindings(p.tree.Findings)
	if p.suppressedFindings > 0 {
		// One counted summary closes the list: the operator sees HOW MUCH was
		// truncated and where it started, and the model stays bounded. It
		// carries the last suppressed finding's token/severity so the badge
		// still names the class that overflowed.
		summary := Finding{
			Kind:     p.lastSuppressed.Kind,
			File:     p.lastSuppressed.File,
			Detail:   fmt.Sprintf("%d further findings suppressed (the first %d are shown)", p.suppressedFindings, maxParseFindings),
			Severity: p.lastSuppressed.Severity,
		}
		p.tree.Findings = append(p.tree.Findings, summary)
	}

	p.tree.Counts = Counts{
		Documents:      len(p.tree.Documents),
		Findings:       len(p.tree.Findings),
		ByConcept:      make(map[string]int, len(p.byConcept)),
		FindingsByKind: make(map[string]int, len(p.framework.Checks)),
	}
	for kind, instances := range p.byConcept {
		p.tree.Counts.ByConcept[kind] = len(instances)
	}
	for _, finding := range p.tree.Findings {
		p.tree.Counts.FindingsByKind[finding.Kind]++
	}
}

// dedupeFindings collapses identical findings — one broken reference repeated
// across one file reports once.
func dedupeFindings(findings []Finding) []Finding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]Finding, 0, len(findings))
	for i, finding := range findings {
		if i > 0 && finding == findings[i-1] {
			continue
		}
		out = append(out, finding)
	}
	return out
}
