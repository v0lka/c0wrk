package core

import "strings"

// planCanonicalLabels maps the case/whitespace-normalized spellings accepted
// for plan-field labels to their canonical display names. The name set mirrors
// the frontend parser (frontend/src/lib/planParser.ts normalizeFieldName:
// what|where|how|acceptance_criteria); "ac" is an additional alias.
var planCanonicalLabels = map[string]string{
	"what":                "What",
	"where":               "Where",
	"how":                 "How",
	"acceptance criteria": "Acceptance Criteria",
	"ac":                  "Acceptance Criteria",
}

// normalizePlanLabel reports the canonical display name ("What", "Where",
// "How", "Acceptance Criteria") when s is a known plan-field label in any
// casing/whitespace spelling, and "" otherwise.
func normalizePlanLabel(s string) string {
	return planCanonicalLabels[strings.Join(strings.Fields(strings.ToLower(s)), " ")]
}

// planLineKind tells the description scanner how a line must be treated.
const (
	planLineOutside     planLineKind = iota // prose: eligible for rewriting
	planLineFenceOpen                       // opens a code fence: verbatim
	planLineFenceInside                     // fence body: verbatim
	planLineFenceClose                      // closes a code fence: verbatim
)

// planLineKind is the line classification returned by planFenceState.next.
type planLineKind int

// planFenceState tracks the single open code fence of a description scan
// (fences in descriptions never nest, so one open/close pair is the only
// state needed).
type planFenceState struct {
	in     bool
	marker byte
	length int
}

// next classifies line against the current fence state and advances it.
func (f *planFenceState) next(line string) planLineKind {
	if f.in {
		if fenceCloses(line, f.marker, f.length) {
			f.in = false
			return planLineFenceClose
		}
		return planLineFenceInside
	}
	if c, n, ok := fenceOpens(line); ok {
		f.in, f.marker, f.length = true, c, n
		return planLineFenceOpen
	}
	return planLineOutside
}

// fenceOpens reports whether line starts a code fence (up to three spaces of
// indent, then a run of >= 3 backticks or tildes; a backtick info string must
// not contain further backticks) and returns its marker and run length.
func fenceOpens(line string) (marker byte, length int, ok bool) {
	s := planFenceIndented(line)
	if len(s) < 3 || (s[0] != '`' && s[0] != '~') {
		return 0, 0, false
	}
	n := fenceRun(s, s[0])
	if n < 3 || (s[0] == '`' && strings.ContainsRune(s[n:], '`')) {
		return 0, 0, false
	}
	return s[0], n, true
}

// fenceCloses reports whether line closes a fence opened with the given
// marker: an equally long pure marker run (no info string).
func fenceCloses(line string, marker byte, minLength int) bool {
	s := planFenceIndented(line)
	if len(s) < minLength {
		return false
	}
	return fenceRun(s, marker) == len(s)
}

// planFenceIndented strips up to three leading spaces, the CommonMark fence
// indent allowance.
func planFenceIndented(line string) string {
	n := 0
	for n < len(line) && n < 3 && line[n] == ' ' {
		n++
	}
	return line[n:]
}

// fenceRun counts the leading run of c in s.
func fenceRun(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

// normalizePlanDescription rewrites a plan-step description into the
// canonical shape the reviewer surface and the frontend plan parser expect.
// Outside code fences: `#`-headers become bold text, known field labels
// (What/Where/How/AC in any spelling, separated by `:` or ` - `) become
// `**What**:`-style markers, task-list checkboxes (`- [ ] `, `1. [x] `) are
// stripped from list items — AC are declared as a plain unordered list —
// and runs of 3+ blank lines collapse to one.
// Fence bodies stay verbatim and an unclosed trailing fence is closed.
// Idempotent: canonical input passes through unchanged.
func normalizePlanDescription(desc string) string {
	if desc == "" {
		return desc
	}
	lines := strings.Split(desc, "\n")
	// A trailing "\n" is a line terminator, not an extra blank line: drop the
	// phantom element and re-append the terminator at the end.
	trailing := false
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
		trailing = true
	}
	out := make([]string, 0, len(lines)+1)
	var fences planFenceState
	blanks := 0
	flushBlanks := func() {
		if blanks >= 3 {
			blanks = 1
		}
		for i := 0; i < blanks; i++ {
			out = append(out, "")
		}
		blanks = 0
	}
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		switch fences.next(line) {
		case planLineFenceOpen:
			flushBlanks()
			out = append(out, line)
		case planLineFenceInside, planLineFenceClose:
			// Fence content and its closing line stay verbatim; the blanks
			// immediately before a close line were fence body, not prose.
			out = append(out, line)
		default:
			if strings.TrimSpace(line) == "" {
				blanks++
				continue
			}
			flushBlanks()
			rewritten := normalizePlanProseLine(line)
			out = append(out, rewritten)
			// A canonical label line can carry the fence opener after the
			// marker (`**How**: ```md` — the shape serializePlanMarkdown
			// writes for multi-line field values); arm the fence state from
			// it, mirroring planParser.ts extractFields, so the body that
			// follows stays verbatim until the standalone close instead of
			// being reinterpreted as prose (a `#` line would demote to a
			// label, a bare ``` would open a new fence).
			if marker, length, opens := planInlineFenceContent(rewritten); opens {
				fences.in, fences.marker, fences.length = true, marker, length
			}
		}
	}
	if fences.in {
		out = append(out, strings.Repeat(string(fences.marker), fences.length))
		return strings.Join(out, "\n") + "\n"
	}
	flushBlanks()
	joined := strings.Join(out, "\n")
	if trailing {
		return joined + "\n"
	}
	return joined
}

// normalizePlanProseLine applies the outside-fence rewrites to one line,
// preserving any leading indent.
func normalizePlanProseLine(line string) string {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	indent, body := line[:i], line[i:]
	if out, changed := stripPlanTaskMarkers(body); changed {
		body = out
	}
	if out, ok := planBoldLabelLine(body); ok {
		return indent + out
	}
	if _, text, ok := planHeaderSplit(body); ok {
		if out, ok := planLabelLine(text, true); ok {
			return indent + out
		}
		return indent + "**" + text + "**"
	}
	if out, ok := planLabelLine(body, false); ok {
		return indent + out
	}
	return indent + body
}

// stripPlanTaskMarkers removes Markdown task-list checkboxes from a list-item
// body and reports whether anything changed. A checkbox is a `[ ]`, `[x]` or
// `[X]` token immediately after a list marker (`-`, `*`, `+`, or an ordered
// `N.`/`N)`) followed by one space. Checkboxed ordered items demote to plain
// `- ` bullets: the checkbox marks the line as task-list content, and plan AC
// are declared as a plain unordered list, so the normalizer enforces both.
// Repeats while a checkbox remains (a double-marker line converges in one
// call), keeping normalizePlanDescription idempotent.
func stripPlanTaskMarkers(body string) (string, bool) {
	changed := false
	for {
		out, ok := stripPlanTaskMarker(body)
		if !ok {
			return body, changed
		}
		body, changed = out, true
	}
}

// stripPlanTaskMarker strips one leading checkbox from body, rewriting a
// checkboxed ordered marker to `-`. Non-list lines and list items without a
// checkbox pass through unchanged (ok=false).
func stripPlanTaskMarker(body string) (string, bool) {
	if body == "" {
		return body, false
	}
	i := 0
	ordered := false
	switch body[0] {
	case '-', '*', '+':
		i = 1
	default:
		for i < len(body) && body[i] >= '0' && body[i] <= '9' {
			i++
		}
		if i == 0 || i >= len(body) || (body[i] != '.' && body[i] != ')') {
			return body, false
		}
		ordered = true
		i++
	}
	// Exactly one space must separate the marker from the `[`: `-  [ ] x`
	// (two spaces) and `-[ ] x` (none) are not checkbox shapes. No space is
	// required after the `]`: `- [ ]foo` still reads as a checkbox, so it is
	// stripped anyway.
	if i >= len(body) || body[i] != ' ' {
		return body, false
	}
	i++
	if i+2 >= len(body) || body[i] != '[' || body[i+2] != ']' {
		return body, false
	}
	switch body[i+1] {
	case ' ', 'x', 'X':
	default:
		return body, false
	}
	boxStart := i
	bullet := body[:boxStart-1] // the list marker without its trailing space
	if ordered {
		bullet = "-" // checkboxed ordered items demote to plain bullets
	}
	rest := strings.TrimSpace(body[boxStart+3:])
	if rest == "" {
		return bullet, true
	}
	return bullet + " " + rest, true
}

// planHeaderSplit recognizes an ATX header (`#` run of 1-6, one space/tab,
// non-empty text) and returns the header text.
func planHeaderSplit(s string) (depth int, text string, ok bool) {
	for depth < len(s) && depth < 6 && s[depth] == '#' {
		depth++
	}
	if depth == 0 || depth >= len(s) || (s[depth] != ' ' && s[depth] != '\t') {
		return 0, "", false
	}
	text = strings.TrimSpace(s[depth:])
	if text == "" {
		return 0, "", false
	}
	return depth, text, true
}

// planBoldLabelParts parses a `**Label**…` line into the raw label text,
// its canonical display name and the inline content following the marker
// token; ok is false when the line is not a recognized bold label line.
func planBoldLabelParts(s string) (rawLabel, canon, content string, ok bool) {
	if !strings.HasPrefix(s, "**") {
		return "", "", "", false
	}
	end := strings.Index(s[2:], "**")
	if end < 0 {
		return "", "", "", false
	}
	rawLabel = strings.TrimRight(s[2:2+end], ":")
	canon = normalizePlanLabel(rawLabel)
	if canon == "" {
		return "", "", "", false
	}
	return rawLabel, canon, planBoldLabelContent(s, end), true
}

// planInlineFenceContent reports whether a canonical `**Label**: ```lang`
// marker line carries a code-fence opener in its inline content, returning
// the opener's marker and run length. serializePlanMarkdown writes
// multi-line field values exactly this way, and planParser.ts arms its
// fence state from the same inline content, so the Go normalizer treats the
// following lines as fence body — not prose — until the standalone close.
// planBoldLabelContent already consumed one separator; a dash spelling that
// survives it (`**How**: - ```sh` — the collapse shape serializePlanMarkdown
// writes when the parsed value itself starts with `- ```sh`) must still arm,
// or the body is re-parsed as prose and the standalone ``` opens a phantom
// fence that swallows the rest of the description.
func planInlineFenceContent(line string) (marker byte, length int, ok bool) {
	_, _, content, isLabel := planBoldLabelParts(strings.TrimLeft(line, " \t"))
	if !isLabel || content == "" {
		return 0, 0, false
	}
	content = strings.TrimLeft(content, ":- \t")
	return fenceOpens(content)
}

// planBoldLabelLine rewrites a `**Known label**…` line to the canonical
// `**Label**:` marker. A line whose label already carries the canonical
// display name is returned untouched, which anchors idempotency — unless the
// inline content carries a task-list checkbox, which the AC contract bans
// even on canonical lines.
func planBoldLabelLine(s string) (string, bool) {
	label, canon, content, ok := planBoldLabelParts(s)
	if !ok {
		return "", false
	}
	if label == canon {
		if content != "" {
			if stripped := stripLabelContentCheckboxes(content); stripped != content {
				if stripped == "" {
					return "**" + canon + "**:", true
				}
				return "**" + canon + "**: " + stripped, true
			}
		}
		return s, true
	}
	content = stripLabelContentCheckboxes(content)
	if content == "" {
		return "**" + canon + "**:", true
	}
	return "**" + canon + "**: " + content, true
}

// planBoldLabelContent extracts the inline content following the `**Label**`
// token: leading `:` or `-` separators are consumed (a checkboxed item's
// `- ` marker is eaten here, leaving a bare box for the checkbox strip).
func planBoldLabelContent(s string, end int) string {
	content := strings.TrimSpace(s[2+end+2:])
	if strings.HasPrefix(content, ":") || strings.HasPrefix(content, "-") {
		content = strings.TrimSpace(content[1:])
	}
	return content
}

// planLabelLine rewrites a line that starts with a known field label —
// `What: x`, `AC - x`, or (for headers) a bare `Acceptance Criteria` — into
// the canonical marker plus the content carried after the separator, with
// any leading task-list checkbox stripped from that content.
func planLabelLine(s string, allowBare bool) (string, bool) {
	canon, rest, ok := planLabelMatch(s, allowBare)
	if !ok {
		return "", false
	}
	rest = stripLabelContentCheckboxes(rest)
	if rest == "" {
		return "**" + canon + "**:", true
	}
	return "**" + canon + "**: " + rest, true
}

// stripLabelContentCheckboxes strips task-list checkboxes from the inline
// content carried after a field-label marker: label lines can collect the
// first AC item inline (`**Acceptance Criteria**: - [ ] foo`), and the
// checkbox rules of stripPlanTaskMarkers apply to that content too. A
// checkbox whose list marker was already consumed by label parsing (the
// `AC - [ ] foo` separator shape arrives as a bare box) loses the box via
// stripBarePlanCheckbox. A separator run left in front of the box (`:
// - [ ] foo` — planBoldLabelContent consumes exactly one leading `:` or
// `-`, so a doubled spelling like `**Acceptance Criteria**:: - [ ] foo`
// arrives here with the residue) is dropped by stripSeparatorsToCheckbox,
// mirroring the `[*: ]`-class strip the TS mirror applies to the same
// label content before stripInlineCheckboxes. All strips repeat to a fixed
// point — one strip can expose another checkbox (`[ ] [x] foo`, `:: -
// [ ] [x] foo`), and a leftover would break the fixed-point guarantee of
// normalizePlanDescription — mirroring the loop in stripPlanTaskMarkers.
func stripLabelContentCheckboxes(content string) string {
	for {
		out, changed := stripPlanTaskMarkers(content)
		if changed {
			content = out
			continue
		}
		out, changed = stripBarePlanCheckbox(content)
		if changed {
			content = out
			continue
		}
		out, changed = stripSeparatorsToCheckbox(content)
		if !changed {
			return content
		}
		content = out
	}
}

// stripSeparatorsToCheckbox drops a leading run of label-separator and
// emphasis characters (`:`, `*`, spaces, tabs) from label content when — and
// only when — the remainder starts with a strippable checkbox, applying that
// strip. The gate keeps prose that legitimately starts with `:` or `*`
// (`**How**: :: intro`, `**How**: **bold** intro`) untouched: the run drops
// only as a side effect of removing a checkbox, which is where the TS
// mirror's `[*: ]`-class strip and stripInlineCheckboxes meet on
// checkbox-bearing label content. The tab extends the TS class by one
// character; TS trims the same tab at the edges of the same rest
// (planParser.ts: `[*: ]` strip, then `.trim()`), so the shapes both
// scanners parse behave identically, and shapes only the Go label path
// reaches (`What: :\t[ ] x`) still lose the box.
func stripSeparatorsToCheckbox(content string) (string, bool) {
	j := 0
	for j < len(content) {
		switch content[j] {
		case ':', '*', ' ', '\t':
			j++
			continue
		}
		break
	}
	if j == 0 || j >= len(content) {
		return content, false
	}
	tail := content[j:]
	if out, changed := stripPlanTaskMarkers(tail); changed {
		return out, true
	}
	if out, changed := stripBarePlanCheckbox(tail); changed {
		return out, true
	}
	return content, false
}

// stripBarePlanCheckbox strips one leading `[ ]`/`[x]`/`[X]` token from
// content and reports whether anything changed. It backs the label-content
// path, where the list marker may have been eaten as the label separator
// before the content reaches a checkbox strip.
func stripBarePlanCheckbox(content string) (string, bool) {
	if len(content) < 3 || content[0] != '[' || content[2] != ']' {
		return content, false
	}
	switch content[1] {
	case ' ', 'x', 'X':
	default:
		return content, false
	}
	rest := strings.TrimSpace(content[3:])
	if rest == "" {
		return "", true
	}
	return rest, true
}

// planLabelMatch parses s as [known label][separator][rest]: the separator is
// the first `:` (candidate = prefix before it) or the first `-` preceded by
// whitespace; allowBare also accepts the label alone with no separator.
func planLabelMatch(s string, allowBare bool) (canon, rest string, ok bool) {
	if idx := strings.IndexByte(s, ':'); idx >= 0 {
		if c := normalizePlanLabel(s[:idx]); c != "" {
			return c, strings.TrimSpace(s[idx+1:]), true
		}
	}
	for i := 1; i < len(s); i++ {
		if s[i] != '-' || (s[i-1] != ' ' && s[i-1] != '\t') {
			continue
		}
		if c := normalizePlanLabel(s[:i]); c != "" {
			return c, strings.TrimSpace(s[i+1:]), true
		}
	}
	if allowBare {
		if c := normalizePlanLabel(s); c != "" {
			return c, "", true
		}
	}
	return "", "", false
}
