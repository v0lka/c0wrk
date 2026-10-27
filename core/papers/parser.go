// Package papers — content parsing layer: front matter, Markdown tables,
// and the claim/appraisal extractors. Best-effort and total (specs/domains/
// papers.md); only ParsePaperDir/ParseLibraryDir touch disk, read-only.
package papers

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	yaml "gopkg.in/yaml.v3"

	"github.com/v0lka/sp4rk/safeio"
)

// --- Front matter ---

// frontMatter splits a document into its YAML front matter ("---"/"..."
// delimited) and body; ok is false without an opening fence. An
// unterminated fence recovers best-effort: remainder = front matter.
func frontMatter(content string) (fm, body string, ok bool) {
	s := strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return "", content, false
	}
	if fenceKind(lines[0]) != fenceOpen {
		return "", content, false
	}
	for i := 1; i < len(lines); i++ {
		// Any fence line closes the block — "---" or "..." — so a
		// `---`-delimited card terminates and its body survives.
		if k := fenceKind(lines[i]); k != fenceNone {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return strings.Join(lines[1:], "\n"), "", true
}

// fenceKind classifies a line as a front-matter fence ("---" or "..."; either
// delimits the block at the opener and at the closer) or not a fence at all.
func fenceKind(line string) int {
	switch strings.TrimSpace(strings.TrimRight(line, "\r")) {
	case "---":
		return fenceOpen
	case "...":
		return fenceClose
	default:
		return fenceNone
	}
}

const (
	fenceNone = iota
	fenceOpen
	fenceClose
)

// paperFrontMatter is the decoded shape of paper.md's front matter: fields
// optional, unknown keys ignored, tolerant scalar/list types.
type paperFrontMatter struct {
	ID          string         `yaml:"id"`
	Slug        string         `yaml:"slug"`
	Title       string         `yaml:"title"`
	Authors     stringList     `yaml:"authors"`
	Year        flexibleInt    `yaml:"year"`
	Venue       string         `yaml:"venue"`
	Identifiers identifierList `yaml:"identifiers"`
	Mode        string         `yaml:"mode"`
	Reading     string         `yaml:"reading"`
	Verdict     string         `yaml:"verdict"`
	Confidence  string         `yaml:"confidence"`
	ResearchIDs stringList     `yaml:"research_ids"`
	Anchors     anchorList     `yaml:"anchors"`
}

// parseFrontMatter prefers a real YAML decode; on failure it falls back
// to the tolerant line scanner.
func parseFrontMatter(fm string) paperFrontMatter {
	fm = strings.TrimSpace(fm)
	if fm == "" {
		return paperFrontMatter{}
	}
	var meta paperFrontMatter
	if err := yaml.Unmarshal([]byte(fm), &meta); err == nil {
		return meta
	}
	return parseFrontMatterFallback(fm)
}

// parseFrontMatterFallback recovers "key: value" scalar fields from a block
// that is not valid YAML; unparseable lines are skipped.
func parseFrontMatterFallback(fm string) paperFrontMatter {
	var meta paperFrontMatter
	for _, raw := range strings.Split(fm, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		key, value, ok := splitLineKeyValue(line)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "id":
			if meta.ID == "" {
				meta.ID = value
			}
		case "slug":
			if meta.Slug == "" {
				meta.Slug = value
			}
		case "title":
			if meta.Title == "" {
				meta.Title = value
			}
		case "venue":
			if meta.Venue == "" {
				meta.Venue = value
			}
		case "mode":
			if meta.Mode == "" {
				meta.Mode = value
			}
		case "reading":
			if meta.Reading == "" {
				meta.Reading = value
			}
		case "verdict":
			if meta.Verdict == "" {
				meta.Verdict = value
			}
		case "confidence":
			if meta.Confidence == "" {
				meta.Confidence = value
			}
		case "year":
			if meta.Year == 0 {
				if n, ok := recoverYear(value); ok {
					meta.Year = flexibleInt(n)
				}
			}
		case "authors":
			if len(meta.Authors) == 0 {
				meta.Authors = stringList(splitListScalar(value))
			}
		case "research_ids", "researchids", "research-ids":
			if len(meta.ResearchIDs) == 0 {
				meta.ResearchIDs = stringList(splitListScalar(value))
			}
		}
	}
	return meta
}

// splitLineKeyValue splits a "key: value" line: the key must be a bare
// token; value quotes are stripped so `title: "X"` == `title: X`.
func splitLineKeyValue(line string) (key, value string, ok bool) {
	i := strings.Index(line, ":")
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:i])
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	value = strings.TrimSpace(line[i+1:])
	value = strings.Trim(value, `"'`)
	return key, value, true
}

// splitListScalar splits a scalar list value ("a, b; c", or a YAML flow list
// "[a, b]") into its trimmed, non-empty items.
func splitListScalar(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil
	}
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if t := strings.TrimSpace(strings.Trim(f, `"'`)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// leadingDigits returns the leading run of ASCII digits in s ("" when s does
// not start with a digit). Used to recover a year from "2017", "2017-06", etc.
func leadingDigits(s string) string {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// fullYearRe matches the first four-digit run anywhere in a scalar, so a
// qualifier-prefixed year ("c. 2017", "circa 2017") is recovered.
var fullYearRe = regexp.MustCompile(`\d{4}`)

// recoverYear extracts a year: the leading digit run ("2017-06" → 2017),
// else the first four-digit run anywhere ("c. 2017" → 2017).
func recoverYear(s string) (int, bool) {
	if d := leadingDigits(s); d != "" {
		if n, err := strconv.Atoi(d); err == nil {
			return n, true
		}
	}
	if d := fullYearRe.FindString(s); d != "" {
		if n, err := strconv.Atoi(d); err == nil {
			return n, true
		}
	}
	return 0, false
}

// --- Tolerant YAML scalar/list types ---

// stringList accepts a single scalar (comma/semicolon separated) or a flow
// list, so hand-written front matter needs no canonical block form.
type stringList []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (l *stringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*l = stringList(splitListScalar(node.Value))
		return nil
	case yaml.SequenceNode:
		var out []string
		if err := node.Decode(&out); err != nil {
			return err
		}
		trimmed := make([]string, 0, len(out))
		for _, s := range out {
			if t := strings.TrimSpace(s); t != "" {
				trimmed = append(trimmed, t)
			}
		}
		*l = stringList(trimmed)
		return nil
	default:
		// A mapping/alias carries no list meaning here; ignore it best-effort.
		return nil
	}
}

// flexibleInt accepts a quoted or date-like scalar, recovering the year
// when one is present.
type flexibleInt int

// UnmarshalYAML implements yaml.Unmarshaler.
func (n *flexibleInt) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return nil
	}
	if year, ok := recoverYear(node.Value); ok {
		*n = flexibleInt(year)
		return nil
	}
	*n = 0
	return nil
}

// identifierList is a []Identifier that also accepts a scalar ("doi:10..."), a
// bare scalar, or a {scheme: value} mapping.
type identifierList []Identifier

// UnmarshalYAML implements yaml.Unmarshaler.
func (l *identifierList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if id, ok := parseIdentifierToken(node.Value); ok {
			*l = identifierList{id}
		}
		return nil
	case yaml.MappingNode:
		var m map[string]string
		if err := node.Decode(&m); err != nil {
			// Surface the error: parseFrontMatter then falls back to the
			// tolerant line scanner, which is the intended best-effort path.
			return err
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v := strings.TrimSpace(m[k]); v != "" {
				*l = append(*l, Identifier{Scheme: strings.ToLower(strings.TrimSpace(k)), Value: v})
			}
		}
		return nil
	case yaml.SequenceNode:
		for _, item := range node.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				if id, ok := parseIdentifierToken(item.Value); ok {
					*l = append(*l, id)
				}
			case yaml.MappingNode:
				var id Identifier
				if err := item.Decode(&id); err == nil && (id.Scheme != "" || id.Value != "") {
					id.Scheme = strings.ToLower(strings.TrimSpace(id.Scheme))
					id.Value = strings.TrimSpace(id.Value)
					*l = append(*l, id)
				}
			}
		}
		return nil
	default:
		return nil
	}
}

// parseIdentifierToken parses a bare identifier scalar: "scheme:value" with
// an identifier-like scheme splits; a URL keeps scheme "url"; else bare value.
func parseIdentifierToken(s string) (Identifier, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Identifier{}, false
	}
	if strings.Contains(s, "://") {
		return Identifier{Scheme: "url", Value: s}, true
	}
	if i := strings.Index(s, ":"); i > 0 {
		scheme := strings.TrimSpace(s[:i])
		value := strings.TrimSpace(s[i+1:])
		if isSchemeToken(scheme) && value != "" {
			return Identifier{Scheme: strings.ToLower(scheme), Value: value}, true
		}
	}
	return Identifier{Value: s}, true
}

// isSchemeToken: short alphanumeric tag with hyphens/underscores, no dots
// (must not swallow a domain-like value).
func isSchemeToken(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// anchorList is a []Anchor that also accepts bare scalar anchors (each stored
// as an Anchor.Ref).
type anchorList []Anchor

// UnmarshalYAML implements yaml.Unmarshaler.
func (l *anchorList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if s := strings.TrimSpace(node.Value); s != "" {
			*l = anchorList{{Ref: s}}
		}
		return nil
	case yaml.SequenceNode:
		for _, item := range node.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				if s := strings.TrimSpace(item.Value); s != "" {
					*l = append(*l, Anchor{Ref: s})
				}
			case yaml.MappingNode:
				var a Anchor
				if err := item.Decode(&a); err == nil && (a.Label != "" || a.Ref != "" || a.Note != "") {
					*l = append(*l, a)
				}
			}
		}
		return nil
	default:
		return nil
	}
}

// --- paper.md parsing ---

// ParsePaperMD parses paper.md best-effort (specs/domains/papers.md); a
// missing title falls back to the body's first ATX heading.
func ParsePaperMD(content string) PaperRecord {
	fm, body, _ := frontMatter(content)
	rec := recordFromFrontMatter(parseFrontMatter(fm))
	if rec.Title == "" {
		rec.Title = firstHeading(body)
	}
	return rec
}

// recordFromFrontMatter normalizes a decoded front matter into a record.
func recordFromFrontMatter(meta paperFrontMatter) PaperRecord {
	rec := PaperRecord{
		Slug:       strings.TrimSpace(meta.Slug),
		Title:      strings.TrimSpace(meta.Title),
		Year:       int(meta.Year),
		Venue:      strings.TrimSpace(meta.Venue),
		Mode:       NormalizeMode(meta.Mode),
		Reading:    NormalizeReading(meta.Reading),
		Verdict:    NormalizeVerdict(meta.Verdict),
		Confidence: NormalizeConfidence(meta.Confidence),
	}
	// Keep the raw id when it is not a canonical P-NNN, so a hand-authored
	// identifier is not silently dropped.
	if id := strings.TrimSpace(meta.ID); id != "" {
		if canon := NormalizePaperID(id); canon != "" {
			rec.ID = canon
		} else {
			rec.ID = id
		}
	}
	for _, a := range meta.Authors {
		if t := strings.TrimSpace(a); t != "" {
			rec.Authors = append(rec.Authors, t)
		}
	}
	for _, id := range meta.Identifiers {
		id.Scheme = strings.ToLower(strings.TrimSpace(id.Scheme))
		id.Value = strings.TrimSpace(id.Value)
		if id.Scheme == "" && id.Value == "" {
			continue
		}
		rec.Identifiers = append(rec.Identifiers, id)
	}
	for _, r := range meta.ResearchIDs {
		if n := NormalizeResearchID(r); n != "" {
			rec.ResearchIDs = appendUnique(rec.ResearchIDs, n)
		}
	}
	rec.Anchors = normalizeAnchors(meta.Anchors)
	return rec
}

// normalizeAnchors trims fields, drops fully-empty anchors, and returns nil
// (not an empty slice) so the zero value stays canonical.
func normalizeAnchors(in []Anchor) []Anchor {
	if len(in) == 0 {
		return nil
	}
	out := make([]Anchor, 0, len(in))
	for _, a := range in {
		a.Label = strings.TrimSpace(a.Label)
		a.Ref = strings.TrimSpace(a.Ref)
		a.Note = strings.TrimSpace(a.Note)
		if a.Label == "" && a.Ref == "" && a.Note == "" {
			continue
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// firstHeading returns the text of the first ATX heading in body ("" when the
// body carries none).
func firstHeading(body string) string {
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if !strings.HasPrefix(line, "#") {
			continue
		}
		if text, ok := headingText(line); ok {
			return text
		}
	}
	return ""
}

// appendUnique appends v to list when it is not already present.
func appendUnique(list []string, v string) []string {
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}

// --- Markdown tables ---

// mdTable is a parsed Markdown table: its nearest preceding ATX heading,
// header cells, and data rows.
type mdTable struct {
	Heading string
	Header  []string
	Rows    [][]string
}

// parseTables extracts every Markdown table, tagged with its nearest
// preceding heading; a |-run without a header+separator shape is skipped.
func parseTables(content string) []mdTable {
	lines := strings.Split(content, "\n")
	var tables []mdTable
	heading := ""
	for i := 0; i < len(lines); {
		line := cleanLine(lines[i])
		if h, ok := headingText(line); ok {
			heading = h
			i++
			continue
		}
		if !isTableRow(line) {
			i++
			continue
		}
		start := i
		for i < len(lines) && isTableRow(cleanLine(lines[i])) {
			i++
		}
		if t, ok := buildTable(lines[start:i]); ok {
			t.Heading = heading
			tables = append(tables, t)
		}
	}
	return tables
}

// buildTable turns a contiguous block of |-prefixed lines into a table, or
// returns ok=false when the block carries no header-before-separator shape.
func buildTable(block []string) (mdTable, bool) {
	cells := make([][]string, 0, len(block))
	sepIdx := -1
	for _, line := range block {
		c := splitCells(line)
		cells = append(cells, c)
		if sepIdx < 0 && isSeparatorRow(c) {
			sepIdx = len(cells) - 1
		}
	}
	if sepIdx < 1 {
		// A table needs a header row immediately before its separator.
		return mdTable{}, false
	}
	t := mdTable{Header: cells[sepIdx-1]}
	for _, c := range cells[sepIdx+1:] {
		if len(c) == 0 || isSeparatorRow(c) {
			continue
		}
		t.Rows = append(t.Rows, c)
	}
	return t, true
}

// cleanLine trims trailing CR and surrounding whitespace from a source line.
func cleanLine(line string) string {
	return strings.TrimSpace(strings.TrimRight(line, "\r"))
}

// isTableRow reports whether a line is a Markdown table row (starts with "|").
func isTableRow(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "|")
}

// headingText returns the cleaned text of an ATX heading line, and whether the
// line is a heading at all.
func headingText(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#") {
		return "", false
	}
	text := strings.TrimSpace(strings.TrimLeft(line, "#"))
	text = strings.Trim(text, "*_` ")
	if text == "" {
		return "", false
	}
	return text, true
}

// isSeparatorRow reports whether a table row is a delimiter row like
// "|---|---|" (optionally with colons for alignment).
func isSeparatorRow(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if c == "" {
			return false
		}
		for _, r := range c {
			if r != '-' && r != ':' && r != ' ' {
				return false
			}
		}
	}
	return true
}

// pickTable selects an artifact's table: all header tokens matched, else a
// heading token matched; ok=false when neither.
func pickTable(tables []mdTable, headerTokens, headingTokens []string) (mdTable, bool) {
	for _, t := range tables {
		if headerHasAll(t.Header, headerTokens) {
			return t, true
		}
	}
	for _, t := range tables {
		if headingHasAny(t.Heading, headingTokens) {
			return t, true
		}
	}
	return mdTable{}, false
}

// headerHasAll reports whether every token appears (case-insensitive substring)
// in at least one header cell.
func headerHasAll(header, tokens []string) bool {
	if len(header) == 0 {
		return false
	}
	for _, tk := range tokens {
		found := false
		for _, h := range header {
			if strings.Contains(strings.ToLower(h), tk) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// headingHasAny reports whether the heading contains any of the tokens
// (case-insensitive substring).
func headingHasAny(heading string, tokens []string) bool {
	h := strings.ToLower(heading)
	if h == "" {
		return false
	}
	for _, tk := range tokens {
		if strings.Contains(h, tk) {
			return true
		}
	}
	return false
}

// columnIndex returns the first header cell matching any name (-1 when none),
// mirroring the frontend twin's regexes in frontend/src/lib/flashcards.ts.
func columnIndex(header []string, names ...string) int {
	for i, h := range header {
		hn := strings.ToLower(strings.Trim(h, "*_` "))
		for _, n := range names {
			if columnNameMatches(hn, n) {
				return i
			}
		}
	}
	return -1
}

// looseColumnTokens match as plain substrings, mirroring the frontend
// twin's UNANCHORED regexes (flashcards.ts) so `Questions`/`Grades` etc.
// resolve on both sides; other tokens stay boundary-anchored.
var looseColumnTokens = map[string]struct{}{
	"front":    {},
	"question": {},
	"prompt":   {},
	"back":     {},
	"answer":   {},
	"grade":    {},
	"rating":   {},
	"result":   {},
	"date":     {},
	"when":     {},
}

// columnNameMatches: loose tokens as substrings, others on both word
// boundaries ("Reference" ≠ "ref", "Tags" ≠ "tag"), "id" anchored to the
// cell start. Go↔frontend parity with flashcards.ts is a two-sided
// contract.
// See specs/domains/papers.md (Invariants).
func columnNameMatches(hn, name string) bool {
	n := strings.ToLower(strings.Trim(name, "*_` "))
	if n == "" {
		return false
	}
	if n == "id" {
		if !strings.HasPrefix(hn, "id") {
			return false
		}
		rest := hn[len("id"):]
		return rest == "" || !isWordByte(rest[0])
	}
	if _, loose := looseColumnTokens[n]; loose {
		return strings.Contains(hn, n)
	}
	for from := 0; from < len(hn); {
		j := strings.Index(hn[from:], n)
		if j < 0 {
			return false
		}
		start := from + j
		beforeOK := start == 0 || !isWordByte(hn[start-1])
		end := start + len(n)
		afterOK := end >= len(hn) || !isWordByte(hn[end])
		if beforeOK && afterOK {
			return true
		}
		from = start + 1
	}
	return false
}

// isWordByte reports whether b is a word character (ASCII letters, digits,
// underscore — the frontend `\b` class).
func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// colOr resolves columnIndex, else the positional fallback (when in range),
// so non-standard headers still yield data by column position.
func colOr(header []string, fallback int, names ...string) int {
	if i := columnIndex(header, names...); i >= 0 {
		return i
	}
	if fallback >= 0 && fallback < len(header) {
		return fallback
	}
	return -1
}

// cellAt returns the trimmed cell at idx, or "" when idx is out of range.
func cellAt(row []string, idx int) string {
	if idx < 0 || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

// --- Table cell escaping (shared contract with the writer) ---

// splitCells splits a table row into its trimmed, unescaped cell values.
func splitCells(row string) []string {
	row = strings.TrimSpace(strings.TrimRight(row, "\r"))
	row = strings.TrimPrefix(row, "|")
	if strings.HasSuffix(row, "|") && !strings.HasSuffix(row, `\|`) {
		row = row[:len(row)-1]
	}
	parts := splitUnescapedPipes(row)
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(unescapeCell(p)))
	}
	return cells
}

// splitUnescapedPipes splits s on "|" characters not preceded by a backslash.
func splitUnescapedPipes(s string) []string {
	parts := make([]string, 0, strings.Count(s, "|")+1)
	start := 0
	prev := rune(0)
	for i, r := range s {
		if r == '|' && prev != '\\' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
		prev = r
	}
	return append(parts, s[start:])
}

// joinCells re-joins trimmed cells into a canonical pipe-delimited row,
// escaping pipes/newlines; splitCells reverses the escape.
func joinCells(cells []string) string {
	esc := make([]string, len(cells))
	for i, c := range cells {
		esc[i] = escapeCell(strings.TrimSpace(c))
	}
	return "| " + strings.Join(esc, " | ") + " |"
}

// escapeCell makes a value safe in one table row (newline → "<br>", pipe
// → "\|"); a value containing the marker text is an accepted limitation.
func escapeCell(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	v = strings.ReplaceAll(v, "|", `\|`)
	v = strings.ReplaceAll(v, "\n", "<br>")
	return v
}

// unescapeCell reverses escapeCell.
func unescapeCell(v string) string {
	v = strings.ReplaceAll(v, `\|`, "|")
	v = strings.ReplaceAll(v, "<br>", "\n")
	return v
}

// --- note.md parsing ---

// ParseNote parses note.md into claims, red flags, and uncertainties —
// best-effort: a missing/unrecognized table yields nil.
func ParseNote(content string) (claims []Claim, redFlags []RedFlag, uncertainties []Uncertainty) {
	tables := parseTables(content)
	return parseClaims(tables), parseRedFlags(tables), parseUncertainties(tables)
}

// parseClaims extracts the claim→evidence table; columns resolve
// exclusively, so headers sharing the word "claim" cannot double-select.
func parseClaims(tables []mdTable) []Claim {
	t, ok := pickTable(tables, []string{"claim"}, []string{"claim", "assertion", "finding"})
	if !ok {
		return nil
	}
	ic := colOr(t.Header, 0, "claim", "assertion", "finding", "statement")
	ie := columnIndexExcept(t.Header, []int{ic}, "evidence", "support", "supporting")
	il := columnIndexExcept(t.Header, []int{ic, ie}, "location", "source", "anchor", "ref", "where")
	is := columnIndexExcept(t.Header, []int{ic, ie, il}, "stance", "relation", "agrees")
	var out []Claim
	for _, row := range t.Rows {
		c := Claim{
			Claim:    cellAt(row, ic),
			Evidence: cellAt(row, ie),
			Location: cellAt(row, il),
			Stance:   cellAt(row, is),
		}
		if c.Claim == "" && c.Evidence == "" && c.Location == "" && c.Stance == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// parseRedFlags extracts the red-flags table.
func parseRedFlags(tables []mdTable) []RedFlag {
	t, ok := pickTable(tables, []string{"flag"}, []string{"flag", "concern", "warning", "red flag"})
	if !ok {
		return nil
	}
	ifl := colOr(t.Header, 0, "flag", "concern", "warning", "issue", "risk")
	idt := colOr(t.Header, 1, "detail", "why", "note", "explanation", "description")
	isev := columnIndex(t.Header, "severity", "impact", "level")
	var out []RedFlag
	for _, row := range t.Rows {
		f := RedFlag{
			Flag:     cellAt(row, ifl),
			Detail:   cellAt(row, idt),
			Severity: cellAt(row, isev),
		}
		if f.Flag == "" && f.Detail == "" && f.Severity == "" {
			continue
		}
		out = append(out, f)
	}
	return out
}

// parseUncertainties extracts the uncertainty table.
func parseUncertainties(tables []mdTable) []Uncertainty {
	t, ok := pickTable(tables, []string{"uncertain"}, []string{"uncertain", "unknown", "open question", "limitation", "unresolved", "gap"})
	if !ok {
		return nil
	}
	ii := colOr(t.Header, 0, "item", "question", "uncertainty", "issue", "unknown", "limitation")
	idt := colOr(t.Header, 1, "detail", "why", "note", "explanation", "reason")
	var out []Uncertainty
	for _, row := range t.Rows {
		u := Uncertainty{Item: cellAt(row, ii), Detail: cellAt(row, idt)}
		if u.Item == "" && u.Detail == "" {
			continue
		}
		out = append(out, u)
	}
	return out
}

// --- appraisal.md parsing ---

// keyValue is one recovered key/value pair from an appraisal sheet.
type keyValue struct{ key, value string }

// ParseAppraisal parses appraisal.md into verdict and confidence (a
// Field|Value table or free-form lines); unrecognized input → empty.
func ParseAppraisal(content string) (Verdict, Confidence) {
	var verdict, conf string
	for _, kv := range scanKeyValues(content) {
		switch normalizeKey(kv.key) {
		case "verdict", "decision", "recommendation", "judgement", "judgment":
			if verdict == "" {
				verdict = kv.value
			}
		case "confidence", "confidence level", "certainty", "confidence in this verdict":
			if conf == "" {
				conf = kv.value
			}
		}
	}
	return NormalizeVerdict(verdict), NormalizeConfidence(conf)
}

// scanKeyValues recovers ordered key/value pairs from a document, from both
// Markdown tables and free-form "Key: value" lines.
func scanKeyValues(content string) []keyValue {
	var out []keyValue
	for _, t := range parseTables(content) {
		for _, row := range t.Rows {
			if len(row) < 2 {
				continue
			}
			if key := cleanKey(row[0]); key != "" {
				out = append(out, keyValue{key: key, value: strings.TrimSpace(row[1])})
			}
		}
	}
	for _, raw := range strings.Split(content, "\n") {
		key, value, ok := parseLineKeyValue(raw)
		if ok {
			out = append(out, keyValue{key: key, value: value})
		}
	}
	return out
}

// parseLineKeyValue recovers a key/value pair from a single free-form line:
// "Key: value", "- **Key:** value", or a "| Key | value |" row.
func parseLineKeyValue(raw string) (key, value string, ok bool) {
	line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
	if strings.HasPrefix(line, "|") {
		cells := splitCells(line)
		if len(cells) >= 2 {
			return cleanKey(cells[0]), strings.TrimSpace(cells[1]), true
		}
		return "", "", false
	}
	i := strings.Index(line, ":")
	if i <= 0 {
		return "", "", false
	}
	key = cleanKey(line[:i])
	if key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(line[i+1:]), true
}

// cleanKey trims Markdown emphasis and a trailing colon from a candidate key.
func cleanKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "*_` ")
	s = strings.TrimSuffix(s, ":")
	return strings.TrimSpace(s)
}

// normalizeKey lower-cases a cleaned key for matching.
func normalizeKey(s string) string {
	return strings.ToLower(cleanKey(s))
}

// --- Whole-record assembly ---

// ParsePaper combines the three artifacts; the appraisal sheet's verdict/
// confidence win over the front matter, but the verdict override applies
// only to canonical constants — a template placeholder can never overwrite
// the card's verdict (specs/domains/papers.md).
func ParsePaper(paperMD, noteMD, appraisalMD string) PaperRecord {
	rec := ParsePaperMD(paperMD)
	rec.Claims, rec.RedFlags, rec.Uncertainties = ParseNote(noteMD)
	if v, c := ParseAppraisal(appraisalMD); v != "" || c != "" {
		if isCanonicalVerdict(v) {
			rec.Verdict = v
		}
		if c != "" {
			rec.Confidence = c
		}
	}
	return rec
}

// isCanonicalVerdict reports whether v is one of the canonical verdict
// constants (as opposed to a verbatim hand-authored value or a placeholder).
func isCanonicalVerdict(v Verdict) bool {
	switch v {
	case VerdictAccepted, VerdictRejected, VerdictUncertain:
		return true
	default:
		return false
	}
}

// --- Filesystem orchestrators (read-only) ---

// maxArtifactBytes bounds one artifact read (safeio.ReadFile is
// unbounded); oversize is a read failure that best-effort parsing skips.
const maxArtifactBytes = 8 << 20

// readFile: fs.ErrNotExist is a normal partial state (absent ≠ error);
// any other read error — and an oversize file — is a genuine failure.
func readFile(path string) (body string, ok bool, err error) {
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > maxArtifactBytes {
		return "", false, fmt.Errorf("reading paper artifact %q: file exceeds the %d byte read cap", path, maxArtifactBytes)
	}
	data, err := safeio.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("reading paper artifact %q: %w", path, err)
	}
	return string(data), true, nil
}

// ParsePaperDir parses one paper directory, fully best-effort; only a
// missing/unreadable directory or an unreadable artifact is an error.
func ParsePaperDir(dir string) (*PaperRecord, error) {
	rec, _, err := parsePaperDir(dir)
	return rec, err
}

// parsePaperDir adds a presence flag (any of the three artifacts) so
// ParseLibraryDir can skip stray non-paper directories.
func parsePaperDir(dir string) (*PaperRecord, bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return nil, false, errors.New("paper path is not a directory: " + dir)
	}

	paperMD, hasPaper, err := readFile(filepath.Join(dir, PaperFileName))
	if err != nil {
		return nil, false, err
	}
	noteMD, hasNote, err := readFile(filepath.Join(dir, NoteFileName))
	if err != nil {
		return nil, false, err
	}
	appraisalMD, hasAppraisal, err := readFile(filepath.Join(dir, AppraisalFileName))
	if err != nil {
		return nil, false, err
	}

	rec := ParsePaper(paperMD, noteMD, appraisalMD)
	rec.Dir = dir
	// Directory-name fallback identity: the record always carries a
	// deterministic, non-empty id (the frontend keys records on it).
	base := filepath.Base(dir)
	if rec.Slug == "" {
		rec.Slug = base
	}
	if rec.ID == "" {
		if id := NormalizePaperID(base); id != "" {
			rec.ID = id
		} else {
			rec.ID = rec.Slug
		}
	}
	return &rec, hasPaper || hasNote || hasAppraisal, nil
}

// ParseLibraryDir parses artifact-carrying subdirectories, skipping stray
// dirs and unparsable papers; the result is sorted deterministically.
func ParseLibraryDir(root string) (*PaperLibrary, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("paper library path is not a directory: " + root)
	}

	lib := &PaperLibrary{Root: root}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		rec, had, perr := parsePaperDir(filepath.Join(root, e.Name()))
		if perr != nil || !had {
			continue
		}
		lib.Papers = append(lib.Papers, rec)
	}
	sortPapers(lib.Papers)
	return lib, nil
}
