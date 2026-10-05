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
// (Font-FAMILY stacks are deliberately out of scope here: they are plain
// CSS/TS literals on this branch, not tokenized.)

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
