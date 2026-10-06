// @vitest-environment node
//
// Type-scale invariant — project-wide guard.
//
// Font sizes are relative: text is sized with the named Tailwind scale
// (`text-xs`, `text-sm`, …), whose rem values resolve against the single
// absolute anchor — `html { font-size: 14px }` in `index.css`. Three
// regressions are guarded here:
//
// 1. Arbitrary font-size utilities (`text-[10px]`, `text-[0.75rem]`) bypass
//    the scale — they are how the UI accumulated ten different sizes before
//    the cleanup. Sizes must go through the named scale; only COLOR
//    arbitrary values (`text-[var(--color-…)]`, `text-[color-mix(…)]`) are
//    legal in the `text-[…]` slot.
//
// 2. Inline TS/TSX font sizes must be relative too: a bare number or a
//    number/px string (`fontSize: 10`, `fontSize="9"`, `fontSize: '13px'`)
//    is an absolute size. Rem strings (`fontSize: '0.875rem'` in CodeMirror
//    themes) stay legal. Two API-bound exceptions are allowlisted — the
//    xterm constructor (`Terminal.tsx`, whose `fontSize` option is a px
//    number by API) and the SVG labels on the research DAG canvas
//    (`ResearchDagCanvas.tsx`, bound to canvas geometry).
//
// 3. CSS carries exactly one absolute font size: the `html` root rule in
//    `index.css`. Every other `font-size` declaration is rem (relative).
//
// 4. Font-family stacks are tokenized: `--font-sans`/`--font-mono`/
//    `--font-icon` in `@theme`, with a TS mirror in `lib/fonts.ts` for the
//    non-CSS consumers (CodeMirror 6 themes and the xterm constructor need a
//    literal stack string, not a `var()` reference). CSS may only reference
//    `var(--font-*)` (plus the `@font-face` declaration itself); TS may only
//    use the `FONT_*_STACK` constants (or a `var(--font-…)` string). The
//    icon font (`--font-icon`, SauceCodePro NF) carries Nerd Font glyphs
//    ONLY — text mono is `--font-mono`, so the terminal rides the plain
//    mono stack with no icon font.

import { describe, it, expect } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join, relative } from "node:path";
// The real TS parser is used to identify comments: unlike a regex or a
// hand-rolled scanner it knows which `//`/`/*` sequences are comments and which
// are string, regex or JSX-text content. Same approach as
// zoomViewportInvariant.test.ts.
import ts from "typescript";

// This file lives directly under <src>/test/, so '..' resolves to <src>/.
const SRC_DIR = fileURLToPath(new URL("..", import.meta.url));

/** Recursively collect `.ts`/`.tsx` sources, excluding test files. */
function collectSources(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...collectSources(full));
    } else if (
      /\.tsx?$/.test(entry.name) &&
      !/\.test\.tsx?$/.test(entry.name)
    ) {
      out.push(full);
    }
  }
  return out;
}

/** Recursively collect `.css` files (index.css + assets/themes/*). */
function collectStylesheets(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...collectStylesheets(full));
    } else if (entry.name.endsWith(".css")) {
      out.push(full);
    }
  }
  return out;
}

/** Offsets of every REAL comment in `source`, as the TypeScript parser sees
 * them (see zoomViewportInvariant.test.ts for the full rationale: strings,
 * regex literals and JSX text must keep being scanned). */
function commentRanges(
  source: string,
  fileName: string,
  kind: ts.ScriptKind,
): Array<readonly [number, number]> {
  const sf = ts.createSourceFile(
    fileName,
    source,
    ts.ScriptTarget.Latest,
    /* setParentNodes */ true,
    kind,
  );
  const ranges: Array<readonly [number, number]> = [];
  const add = (rs: readonly ts.CommentRange[] | undefined): void => {
    if (rs) for (const r of rs) ranges.push([r.pos, r.end]);
  };
  const visit = (node: ts.Node): void => {
    add(ts.getLeadingCommentRanges(source, node.pos));
    for (const child of node.getChildren(sf)) visit(child);
    add(ts.getTrailingCommentRanges(source, node.end));
  };
  visit(sf);
  return ranges;
}

/** Blank every comment span (newlines kept) so code is scanned while comment
 * prose is not. Offsets and line structure are preserved exactly. */
function stripComments(source: string, fileName: string): string {
  const kind = fileName.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const ranges = [...commentRanges(source, fileName, kind)].sort(
    (a, b) => a[0] - b[0],
  );
  let out = "";
  let cursor = 0;
  for (const [start, end] of ranges) {
    if (start < cursor) continue;
    out += source.slice(cursor, start);
    out += source.slice(start, end).replace(/[^\n]/g, " ");
    cursor = end;
  }
  return out + source.slice(cursor);
}

/** An arbitrary font-size utility: `text-[10px]`, `text-[0.75rem]`, …
 * Color arbitrary values (`text-[var(--color-x)]`, `text-[color-mix(…)]`)
 * contain no px/rem length and do not match. */
const PX_FONT_SIZE = /text-\[\d+(?:\.\d+)?(?:px|rem)\]/;

/** An absolute inline font size in TS/TSX: a bare number (`fontSize: 10` —
 * the xterm option's unit is px) or a number/px string (`fontSize="9"`,
 * `fontSize: '13px'`), optionally inside a JSX expression
 * (`fontSize={13}`). Rem strings stay unflagged. */
const TS_ABSOLUTE_FONT_SIZE =
  /fontSize\s*[:=]\s*\{?\s*(?:(['"])[\d.]+(?:px)?\1|[\d.]+(?![\w.]))/;

/** Every absolute (`px`) `font-size:` declaration in CSS. */
const CSS_PX_FONT_SIZE = /font-size\s*:\s*[\d.]+px/g;

/** A hardcoded font-family stack in TS: `fontFamily: 'ui-monospace, …'`.
 * Token constants (`fontFamily: FONT_MONO_STACK`) and `var()` references do
 * not match — the value must be a quoted string literal. */
const TS_RAW_FONT_FAMILY = /fontFamily\s*[:=]\s*(['"`])((?!\1).+)\1/;

/** A `font-family:` declaration value in CSS, captured to the terminator.
 * Global: `matchAll` requires it. */
const CSS_FONT_FAMILY = /font-family\s*:\s*([^;]+);/g;

/** Files whose inline font sizes are bound to an external API or a geometry
 * contract and are therefore legitimately absolute (each is named in the
 * frontend spec). */
const INLINE_FONT_SIZE_EXCEPTIONS = [
  join("components", "terminal", "Terminal.tsx"), // xterm constructor: px number by API
  join("components", "research", "ResearchDagCanvas.tsx"), // SVG labels bound to canvas geometry
];

/** Byte range of the `html { … }` root rule in a stylesheet — the one place
 * an absolute font size may live. Brace-matched from the top-level `html`
 * selector so comments and nested blocks inside the rule don't truncate the
 * range. */
function htmlRootRange(css: string): readonly [number, number] | undefined {
  const m = /(^|\n)html\s*\{/.exec(css);
  if (m === null) return undefined;
  const start = m.index + m[0].length;
  let depth = 1;
  for (let i = start; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}") {
      depth--;
      if (depth === 0) return [start, i];
    }
  }
  return undefined;
}

/** Spans of every `@font-face { … }` block, whose own `font-family:` names
 * the face being declared and is therefore legitimate. */
function fontFaceRanges(css: string): Array<readonly [number, number]> {
  const ranges: Array<readonly [number, number]> = [];
  for (const m of css.matchAll(/@font-face\s*\{[^}]*\}/g)) {
    ranges.push([m.index ?? 0, (m.index ?? 0) + m[0].length]);
  }
  return ranges;
}

/** Is this CSS `font-family` value legal? Only `var(--font-*)` references are;
 * inside `@font-face` the literal face name is the declaration itself. */
function isLegalCssFontFamily(value: string, inFontFace: boolean): boolean {
  if (inFontFace) return true;
  return value.trim().startsWith("var(--font-");
}

describe("type-scale guard (relative typography invariant)", () => {
  const sources = collectSources(SRC_DIR);
  const stylesheets = collectStylesheets(SRC_DIR);

  it("scans a non-trivial number of source and stylesheet files", () => {
    expect(sources.length).toBeGreaterThan(50);
    expect(stylesheets.length).toBeGreaterThan(5);
  });

  it("anchors the relative scale at the html root font size", () => {
    const css = readFileSync(join(SRC_DIR, "index.css"), "utf8");
    expect(htmlRootRange(css)).toBeDefined();
  });

  it("defines the type-scale and font-family tokens in @theme", () => {
    const css = readFileSync(join(SRC_DIR, "index.css"), "utf8");
    expect(css).toMatch(/--font-sans:/);
    expect(css).toMatch(/--font-mono:/);
    expect(css).toMatch(/--font-icon:/);
    // Text-size tokens mirror the Tailwind defaults exactly (rem, resolved
    // against the 14px html root) so the text-* utilities render unchanged;
    // component CSS sizes text through var(--text-*) instead of raw rem.
    expect(css).toMatch(/--text-xs:\s*0\.75rem/);
    expect(css).toMatch(/--text-sm:\s*0\.875rem/);
    expect(css).toMatch(/--text-base:\s*1rem/);
    expect(css).toMatch(/--text-lg:\s*1\.125rem/);
  });

  it("draws the chat autocomplete tooltip with the UI font chain, never mono", () => {
    const css = readFileSync(join(SRC_DIR, "index.css"), "utf8");
    // Skill/file/agent suggestions are UI chrome: they follow --font-sans so
    // the Interface-font pick (or its default stack) reaches the hint list,
    // and a mono pick must not leak in either.
    //
    // The contested tooltip rules (the list's font among them) live in
    // cmChatTheme.ts, NOT here: @codemirror/autocomplete's baseTheme beats
    // any global rule with editor-scoped selectors, so an index.css rule for
    // that surface is dead code — its presence here means someone
    // reintroduced the bug that rendered the hints in CM's literal
    // `monospace` family. The live cascade winner is resolved (and the
    // theme's own styles pinned) by test/cmTooltipCascade.test.ts and
    // lib/cmChatTheme.test.ts.
    expect(css).not.toMatch(/\.cm-tooltip-autocomplete\s+ul\s*\{/);
    // The theme must carry the tooltip list font through the var chain.
    const theme = readFileSync(join(SRC_DIR, "lib", "cmChatTheme.ts"), "utf8");
    expect(theme).toContain("'.cm-tooltip.cm-tooltip-autocomplete > ul'");
    expect(theme).toMatch(/fontFamily:\s*'var\(--font-sans\)'/);
    expect(theme).not.toMatch(/fontFamily:\s*'(?!var\()[^']*'/);
    // A sans surface stays out of the mono smoothing group — it inherits the
    // :root sans smoothing rule instead.
    const monoSmoothing = css.indexOf(
      "-webkit-font-smoothing: var(--font-smoothing-mono",
    );
    expect(monoSmoothing).toBeGreaterThan(-1);
    const group = css.slice(
      css.lastIndexOf("}", monoSmoothing) + 1,
      css.indexOf("}", monoSmoothing) + 1,
    );
    expect(group).toContain(".cm-viewer-container"); // sanity: the right group
    expect(group).not.toContain(".cm-tooltip-autocomplete");
  });

  it("never hardcodes a font-family stack in TS outside lib/fonts.ts", () => {
    const offenders: string[] = [];
    for (const file of sources) {
      if (file.endsWith(join("lib", "fonts.ts"))) continue; // the TS mirror itself
      const lines = stripComments(readFileSync(file, "utf8"), file).split("\n");
      lines.forEach((line, i) => {
        const m = TS_RAW_FONT_FAMILY.exec(line);
        const stack = m?.[2];
        if (stack !== undefined && !stack.trim().startsWith("var(--font-")) {
          offenders.push(`${relative(SRC_DIR, file)}:${i + 1}: ${line.trim()}`);
        }
      });
    }
    expect(offenders).toEqual([]);
  });

  it("never hardcodes a font-family stack in CSS outside @font-face", () => {
    const offenders: string[] = [];
    for (const file of stylesheets) {
      const css = readFileSync(file, "utf8");
      const faceRanges = fontFaceRanges(css);
      const inFace = (pos: number): boolean =>
        faceRanges.some(([s, e]) => pos >= s && pos < e);
      for (const m of css.matchAll(CSS_FONT_FAMILY)) {
        const value = m[1];
        if (value !== undefined && !isLegalCssFontFamily(value, inFace(m.index ?? 0))) {
          const line = css.slice(0, m.index ?? 0).split("\n").length;
          offenders.push(`${relative(SRC_DIR, file)}:${line}: ${m[0].trim()}`);
        }
      }
    }
    expect(offenders).toEqual([]);
  });

  it("never sizes text with an arbitrary px/rem utility", () => {
    const offenders: string[] = [];
    for (const file of sources) {
      const lines = stripComments(readFileSync(file, "utf8"), file).split("\n");
      lines.forEach((line, i) => {
        if (PX_FONT_SIZE.test(line)) {
          offenders.push(`${relative(SRC_DIR, file)}:${i + 1}: ${line.trim()}`);
        }
      });
    }
    expect(offenders).toEqual([]);
  });

  it("never carries an absolute inline font size outside the API-bound exceptions", () => {
    const offenders: string[] = [];
    for (const file of sources) {
      if (INLINE_FONT_SIZE_EXCEPTIONS.some((ex) => file.endsWith(ex))) continue;
      const lines = stripComments(readFileSync(file, "utf8"), file).split("\n");
      lines.forEach((line, i) => {
        if (TS_ABSOLUTE_FONT_SIZE.test(line)) {
          offenders.push(`${relative(SRC_DIR, file)}:${i + 1}: ${line.trim()}`);
        }
      });
    }
    expect(offenders).toEqual([]);
  });

  it("keeps CSS font sizes relative except the html root rule", () => {
    const offenders: string[] = [];
    for (const file of stylesheets) {
      const css = readFileSync(file, "utf8");
      const root = file.endsWith(join("index.css"))
        ? htmlRootRange(css)
        : undefined;
      for (const m of css.matchAll(CSS_PX_FONT_SIZE)) {
        const at = m.index ?? 0;
        const inRoot = root !== undefined && at >= root[0] && at < root[1];
        if (!inRoot) {
          const line = css.slice(0, at).split("\n").length;
          offenders.push(`${relative(SRC_DIR, file)}:${line}: ${m[0].trim()}`);
        }
      }
    }
    expect(offenders).toEqual([]);
  });

  it("the px-size pattern catches font sizes but not color values", () => {
    expect(PX_FONT_SIZE.test('className="text-[10px]"')).toBe(true);
    expect(PX_FONT_SIZE.test('className="sm:text-[11px]"')).toBe(true);
    expect(PX_FONT_SIZE.test('className="text-[0.75rem]"')).toBe(true);
    expect(PX_FONT_SIZE.test('className="text-[var(--color-highlight)]"')).toBe(
      false,
    );
    expect(
      PX_FONT_SIZE.test(
        'className="text-[color-mix(in_srgb,var(--color-foreground)_50%,transparent)]"',
      ),
    ).toBe(false);
    // Other size utilities with px values (padding etc.) are not text sizes.
    expect(PX_FONT_SIZE.test('className="p-[10px] text-xs"')).toBe(false);
  });

  it("the inline pattern catches absolute sizes but not rem strings", () => {
    expect(TS_ABSOLUTE_FONT_SIZE.test("fontSize: 10,")).toBe(true);
    expect(TS_ABSOLUTE_FONT_SIZE.test('fontSize="9"')).toBe(true);
    expect(TS_ABSOLUTE_FONT_SIZE.test("fontSize: '13px',")).toBe(true);
    expect(TS_ABSOLUTE_FONT_SIZE.test("fontSize={13}")).toBe(true);
    // Relative values stay unflagged.
    expect(TS_ABSOLUTE_FONT_SIZE.test("fontSize: '0.875rem',")).toBe(false);
    expect(TS_ABSOLUTE_FONT_SIZE.test("fontSize: FONT_SIZE_VAR")).toBe(false);
    // Only the fontSize property is scanned.
    expect(TS_ABSOLUTE_FONT_SIZE.test("lineHeight: 1.5,")).toBe(false);
  });

  it("the TS stack pattern catches literals but not token constants", () => {
    expect(TS_RAW_FONT_FAMILY.exec("fontFamily: 'Menlo, monospace'")?.[2]).toBe(
      "Menlo, monospace",
    );
    expect(TS_RAW_FONT_FAMILY.exec('fontFamily: "Menlo, monospace"')?.[2]).toBe(
      "Menlo, monospace",
    );
    expect(TS_RAW_FONT_FAMILY.exec("fontFamily: `Menlo, monospace`")?.[2]).toBe(
      "Menlo, monospace",
    );
    // Token constants and var() references are not hardcoded stacks.
    expect(TS_RAW_FONT_FAMILY.test("fontFamily: FONT_MONO_STACK")).toBe(false);
    expect(TS_RAW_FONT_FAMILY.test("fontFamily: buildStack(mono)")).toBe(false);
    // Only the fontFamily property is scanned.
    expect(TS_RAW_FONT_FAMILY.test("fontStyle: 'italic'")).toBe(false);
  });

  it("the CSS stack pattern accepts only var() references outside @font-face", () => {
    expect(isLegalCssFontFamily("var(--font-mono);", false)).toBe(true);
    expect(isLegalCssFontFamily("  var(--font-sans);", false)).toBe(true);
    expect(isLegalCssFontFamily('"SauceCodePro NF", monospace;', false)).toBe(
      false,
    );
    expect(isLegalCssFontFamily("ui-monospace, Menlo, monospace;", false)).toBe(
      false,
    );
    // Inside @font-face the literal name is the declaration itself.
    expect(isLegalCssFontFamily('"SauceCodePro NF";', true)).toBe(true);
  });

  it("the @font-face range finder brackets the declaration blocks", () => {
    const css =
      "p { font-family: Menlo; }\n@font-face {\n  font-family: \"SauceCodePro NF\";\n  src: url(x.ttf);\n}\nq { font-family: var(--font-mono); }";
    const ranges = fontFaceRanges(css);
    expect(ranges).toHaveLength(1);
    const inFace = (pos: number): boolean =>
      ranges.some(([s, e]) => pos >= s && pos < e);
    const faceDecl = css.indexOf('@font-face {\n  font-family:');
    expect(inFace(faceDecl)).toBe(true);
    expect(inFace(css.indexOf("p { font-family:"))).toBe(false);
    expect(inFace(css.indexOf("q { font-family:"))).toBe(false);
  });

  it("the CSS pattern locates the html root rule by brace matching", () => {
    const css =
      "a { color: red }\nhtml {\n  font-size: 14px;\n  .x { color: blue }\n}\np { font-size: 13px }";
    const range = htmlRootRange(css);
    expect(range).toBeDefined();
    const [start, end] = range as readonly [number, number];
    const rule = css.slice(start, end);
    expect(rule).toContain("font-size: 14px");
    expect(rule).toContain(".x");
    expect(rule).not.toContain("13px");
  });
});
