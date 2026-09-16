// Package papers — content parsing layer.
//
// This file holds everything that turns paper artifact TEXT into the model
// types: the YAML front-matter reader (with a tolerant line-scanner fallback),
// the Markdown table reader, and the pure functions that extract claims,
// red flags, uncertainty, and the appraisal verdict. The two thin filesystem
// orchestrators at the bottom (ParsePaperDir / ParseLibraryDir) are the only
// routines here that touch disk, and they only READ — the writer layer owns
// all mutation.
//
// Parsing is deliberately best-effort and total: every artifact is optional,
// every table is optional, and a malformed or truncated document yields the
// fields that could be recovered rather than an error. This mirrors the
// research parser: a partially written paper must still render usefully.
package papers

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	yaml "gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Front matter
// ---------------------------------------------------------------------------

// frontMatter splits a Markdown document into its YAML front matter — the block
// between the leading "---" fences — and the body. ok is false when the
// document does not open with a front-matter fence, in which case fm is empty
// and body is the whole input. A leading UTF-8 BOM is tolerated.
//
// An unterminated fence is recovered best-effort: the remainder is treated as
// front matter (a truncated card must not lose its fields) and the body is
// empty.
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
		if k := fenceKind(lines[i]); k == fenceClose {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return strings.Join(lines[1:], "\n"), "", true
}

// fenceKind classifies a line as a front-matter fence opener ("---"), closer
// ("---" or "..."), or not a fence at all.
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

// paperFrontMatter is the decoded shape of paper.md's front matter. Every field
// is optional; unknown keys are ignored, and the tolerant scalar/list types
// accept both the canonical block form and hand-written shorthands.
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

// parseFrontMatter decodes a front-matter block. It prefers a real YAML decode
// (which handles nested identifiers/anchors and quoting); when that fails — a
// hand-edited or truncated block — it falls back to a line scanner that
// recovers the scalar fields it can.
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

// parseFrontMatterFallback recovers scalar front-matter fields from a block
// that is not valid YAML, by scanning "key: value" lines. List-valued keys are
// split on commas/semicolons. It never errors: unparseable lines are skipped.
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
				if n, err := strconv.Atoi(leadingDigits(value)); err == nil {
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

// splitLineKeyValue splits a "key: value" line into its parts. The key must be
// a bare token (no whitespace); a line without a colon, or with whitespace in
// the key, is not a key/value line. Surrounding quotes on the value are
// stripped so both `title: "X"` and `title: X` parse identically.
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

// ---------------------------------------------------------------------------
// Tolerant YAML scalar/list types
// ---------------------------------------------------------------------------

// stringList is a []string that also accepts a single scalar (comma/semicolon
// separated) or a YAML flow list, so hand-written front matter does not have to
// use the canonical block-list form.
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

// flexibleInt is an int that also accepts a quoted scalar or a date-like
// scalar ("2017", "2017-06", "c. 2017"), recovering the leading number.
type flexibleInt int

// UnmarshalYAML implements yaml.Unmarshaler.
func (n *flexibleInt) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return nil
	}
	v := strings.TrimSpace(node.Value)
	if v == "" {
		*n = 0
		return nil
	}
	if iv, err := strconv.Atoi(v); err == nil {
		*n = flexibleInt(iv)
		return nil
	}
	if d := leadingDigits(v); d != "" {
		if iv, err := strconv.Atoi(d); err == nil {
			*n = flexibleInt(iv)
		}
	}
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

// parseIdentifierToken parses a bare identifier scalar. A "scheme:value" token
// with an identifier-like scheme splits into its parts; a URL keeps its scheme
// as "url"; anything else is stored as a bare value.
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

// isSchemeToken reports whether s is a plausible identifier-scheme tag (short,
// alphanumeric with hyphens/underscores, no dots so it does not swallow a
// domain-like value).
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

// ---------------------------------------------------------------------------
// paper.md parsing
// ---------------------------------------------------------------------------

// ParsePaperMD parses a paper.md document — its YAML front matter plus the
// optional Markdown body — into a PaperRecord. It is best-effort: a document
// with no front matter, or with malformed front matter, still parses, carrying
// whatever fields could be recovered. When the front matter declares no title,
// the body's first ATX heading is used as a fallback.
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

// normalizeAnchors trims anchor fields and drops fully-empty anchors. It
// returns nil rather than an empty slice when nothing survives, so the zero
// value stays canonical.
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

// ---------------------------------------------------------------------------
// Markdown tables
// ---------------------------------------------------------------------------

// mdTable is a parsed Markdown table: the heading it sits under (nearest ATX
// heading above it, if any), its header cells, and its data rows.
type mdTable struct {
	Heading string
	Header  []string
	Rows    [][]string
}

// parseTables extracts every Markdown table from content, tagging each with the
// nearest preceding heading. A contiguous run of |-prefixed lines forms a table
// only when it contains a separator row after a header row; a |-prefixed run
// without one is skipped.
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

// pickTable selects the table for an artifact: the first table whose header
// contains every header token, else the first table sitting under a heading
// that contains any heading token. ok is false when neither matches.
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

// columnIndex returns the index of the first header cell matching any name
// (case-insensitive substring), or -1 when none matches.
func columnIndex(header []string, names ...string) int {
	for i, h := range header {
		hn := strings.ToLower(strings.Trim(h, "*_` "))
		for _, n := range names {
			if strings.Contains(hn, n) {
				return i
			}
		}
	}
	return -1
}

// colOr returns columnIndex(header, names...) when it resolves, else the
// positional fallback index (when in range), else -1 — so a table with
// non-standard headers still yields data by column position.
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

// ---------------------------------------------------------------------------
// Table cell escaping (shared contract with the writer)
// ---------------------------------------------------------------------------

// splitCells splits a single Markdown table row (with leading/trailing pipes)
// into its trimmed, unescaped cell values.
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

// joinCells re-joins trimmed cells into a pipe-delimited Markdown row with the
// canonical surrounding spaces. Each cell is escaped so a value containing a
// pipe or newline cannot corrupt the row; splitCells reverses the escape.
func joinCells(cells []string) string {
	esc := make([]string, len(cells))
	for i, c := range cells {
		esc[i] = escapeCell(strings.TrimSpace(c))
	}
	return "| " + strings.Join(esc, " | ") + " |"
}

// escapeCell shields a logical value so it can live inside a single Markdown
// table row: a raw newline becomes the "<br>" fold marker and a literal pipe
// becomes "\|". unescapeCell reverses both on parse. (A value that literally
// contains the marker text is a documented, accepted limitation.)
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

// ---------------------------------------------------------------------------
// note.md parsing
// ---------------------------------------------------------------------------

// ParseNote parses a note.md document into its three artifact lists — claims
// (claim→evidence), red flags, and uncertainty. It is best-effort: a missing or
// unrecognized table yields a nil list rather than an error.
func ParseNote(content string) (claims []Claim, redFlags []RedFlag, uncertainties []Uncertainty) {
	tables := parseTables(content)
	return parseClaims(tables), parseRedFlags(tables), parseUncertainties(tables)
}

// parseClaims extracts the claim→evidence table.
func parseClaims(tables []mdTable) []Claim {
	t, ok := pickTable(tables, []string{"claim"}, []string{"claim", "assertion", "finding"})
	if !ok {
		return nil
	}
	ic := colOr(t.Header, 0, "claim", "assertion", "finding", "statement")
	ie := colOr(t.Header, 1, "evidence", "support", "supporting")
	il := columnIndex(t.Header, "location", "source", "anchor", "ref", "where")
	is := columnIndex(t.Header, "stance", "relation", "agrees")
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

// ---------------------------------------------------------------------------
// appraisal.md parsing
// ---------------------------------------------------------------------------

// keyValue is one recovered key/value pair from an appraisal sheet.
type keyValue struct{ key, value string }

// ParseAppraisal parses an appraisal.md document into its verdict and
// confidence. It accepts either a Field|Value table or free-form
// "Verdict: ..." / "**Confidence:** ..." lines. Missing or unrecognized
// documents yield empty values.
func ParseAppraisal(content string) (Verdict, Confidence) {
	var verdict, conf string
	for _, kv := range scanKeyValues(content) {
		switch normalizeKey(kv.key) {
		case "verdict", "decision", "recommendation", "judgement", "judgment":
			if verdict == "" {
				verdict = kv.value
			}
		case "confidence", "confidence level", "certainty":
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

// ---------------------------------------------------------------------------
// Whole-record assembly
// ---------------------------------------------------------------------------

// ParsePaper combines a paper's three artifact documents into one record. It is
// the pure core of ParsePaperDir, exposed so callers (and tests) holding
// artifact content can parse without touching disk. The paper.md card supplies
// the identity fields; note.md supplies claims/red flags/uncertainty; and
// appraisal.md supplies the verdict/confidence — which, being the dedicated
// appraisal sheet, wins over a conflicting value in the front matter.
func ParsePaper(paperMD, noteMD, appraisalMD string) PaperRecord {
	rec := ParsePaperMD(paperMD)
	rec.Claims, rec.RedFlags, rec.Uncertainties = ParseNote(noteMD)
	if v, c := ParseAppraisal(appraisalMD); v != "" || c != "" {
		if v != "" {
			rec.Verdict = v
		}
		if c != "" {
			rec.Confidence = c
		}
	}
	return rec
}

// ---------------------------------------------------------------------------
// Filesystem orchestrators (read-only)
// ---------------------------------------------------------------------------

// readFile reads a file's contents, returning ("", false) when the file is
// missing or unreadable — a missing optional artifact is a normal partial
// state, not a failure. Parsing is rendering-only; mutation lives in the
// writer, which uses its own strict reads.
func readFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// ParsePaperDir parses a single paper directory. It is fully best-effort: each
// of the three artifacts is optional, so a directory carrying only paper.md (or
// even nothing) parses cleanly into a record holding whatever was present. Only
// a missing or unreadable directory itself is an error.
func ParsePaperDir(dir string) (*PaperRecord, error) {
	rec, _, err := parsePaperDir(dir)
	return rec, err
}

// parsePaperDir is ParsePaperDir plus a presence flag: had reports whether the
// directory carried at least one of the three artifacts. ParseLibraryDir uses
// it to skip stray non-paper directories.
func parsePaperDir(dir string) (*PaperRecord, bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return nil, false, errors.New("paper path is not a directory: " + dir)
	}

	paperMD, hasPaper := readFile(filepath.Join(dir, PaperFileName))
	noteMD, hasNote := readFile(filepath.Join(dir, NoteFileName))
	appraisalMD, hasAppraisal := readFile(filepath.Join(dir, AppraisalFileName))

	rec := ParsePaper(paperMD, noteMD, appraisalMD)
	rec.Dir = dir
	// Derive a fallback identity from the directory name: its base is the slug
	// the writer used, and may also be a P-NNN id.
	base := filepath.Base(dir)
	if rec.Slug == "" {
		rec.Slug = base
	}
	if rec.ID == "" {
		rec.ID = NormalizePaperID(base)
	}
	return &rec, hasPaper || hasNote || hasAppraisal, nil
}

// ParseLibraryDir parses a paper-library root directory: every non-hidden paper
// subdirectory, each parsed by ParsePaperDir. Directories carrying none of the
// three artifacts are skipped (they are not papers), and an unparsable paper is
// skipped rather than failing the whole library. Only an unreadable root path
// is an error. The returned library is sorted deterministically by ID then
// slug.
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
