// Builder for the agent task text dispatched by the git-config risk toast's
// "Fix" action. Kept out of the component file so the component module
// exports only components (react-refresh) and the builder is unit-testable
// in isolation.

import type { GitConfigRiskData } from '@/types/events'

/** Code points that could restructure the prompt or visually smuggle text
 *  past the reader: C0/C1 controls, DEL, zero-width and bidi overrides. */
// eslint-disable-next-line no-control-regex -- stripping control characters is the very purpose of this regex
const STRUCTURAL_OR_INVISIBLE = /[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2060\ufeff]/g

/** Risk payload fields derive from repository-controlled content — the
 *  scanner's key embeds git-config subsection names, which the author of a
 *  hostile .git/config chooses freely (and a future backend change could
 *  start echoing values too). Treat every payload field as untrusted data
 *  when splicing it into the prompt: one line, no control or invisible
 *  characters, and no backticks that could break out of the code spans the
 *  template wraps around keys and commands. */
function asInlineData(value: string): string {
  return value.replace(STRUCTURAL_OR_INVISIBLE, ' ').replace(/\s+/g, ' ').replace(/`/g, "'").trim()
}

/** Builds a self-contained description of the exact findings behind an
 *  "untrusted git configuration" warning, so the agent can inspect and clean
 *  up the repository's git configuration. */
export function buildGitConfigFixPrompt(risk: GitConfigRiskData): string {
  const path = asInlineData(risk.path)
  const findings = risk.findings
    .map((f) => `- \`${asInlineData(f.key)}\` — ${asInlineData(f.description)}`)
    .join('\n')
  return [
    `Fix the unsafe git configuration in the repository at ${path}.`,
    '',
    'c0wrk flagged its git configuration as untrusted for these reasons:',
    findings,
    '',
    'Inspect these settings, explain the risk each one poses, and remove the ones that are not genuinely needed. Edit the repository config directly (`.git/config` plus any files its include directives pull in) — writing under `.git/` asks the user for confirmation, which is expected here. `git config --unset` may be blocked by the execute-tool policy; if the command is denied, fall back to editing the config file. If a setting is legitimate (for example a real LFS filter or an intentional hooks directory), verify it points at a trusted binary on this machine and say so instead of removing it. Work only inside this repository and do not change any other configuration.',
  ].join('\n')
}
