#!/usr/bin/env node
// Production-dependency audit gate with a reviewed, dated allowlist.
//
// `npm audit` has no ignore flag, and the CI step (`npm run audit:prod`,
// wired into .github/workflows/ci.yml) must stay green on advisories that
// are real but carry no upstream fix, without muting the gate for anything
// else. This mirrors the Go-side contract of internal/vulncheck + its
// allowlist.json: the wrapper re-runs the audit in JSON mode and applies
// frontend/audit-allowlist.json — every entry carries a reason and a
// review_by date, an entry whose advisory now HAS a fix fails the build
// (bump the dependency, delete the entry), an entry the report no longer
// mentions is stale and fails the build, an entry past its review date
// fails the build, and any finding not covered by an entry fails the build.
// Nothing unreviewed is ever waived: structural problems in the allowlist
// or in the audit report itself are failures, not skips.

import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const SEVERITY_RANK = { info: 0, low: 1, moderate: 2, high: 3, critical: 4 }
const AUDIT_LEVEL = 'low' // mirrors the removed `npm audit --audit-level=low` step

function fail(messages) {
  for (const m of messages) console.error(`✗ ${m}`)
  console.error('frontend production-dependency audit: FAILED')
  process.exit(1)
}

function parseArgs(argv) {
  const args = { auditJson: null, allowlist: null }
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--audit-json') args.auditJson = argv[++i]
    else if (argv[i] === '--allowlist') args.allowlist = argv[++i]
    else fail([`unknown argument: ${argv[i]} (supported: --audit-json <file>, --allowlist <file>)`])
  }
  return args
}

function loadJson(file, label) {
  let raw
  try {
    raw = readFileSync(file, 'utf8')
  } catch (err) {
    fail([`cannot read ${label} at ${file}: ${err.message}`])
  }
  try {
    return JSON.parse(raw)
  } catch (err) {
    fail([`${label} at ${file} is not valid JSON: ${err.message}`])
  }
}

function loadAllowlist(file) {
  const data = loadJson(file, 'audit allowlist')
  if (!data || typeof data !== 'object' || !Array.isArray(data.entries)) {
    fail([`audit allowlist at ${file} must be an object with an "entries" array`])
  }
  const today = new Date().toISOString().slice(0, 10)
  const problems = []
  for (const [i, e] of data.entries.entries()) {
    if (!e || typeof e !== 'object') { problems.push(`entries[${i}]: not an object`); continue }
    if (typeof e.id !== 'string' || !e.id.trim()) problems.push(`entries[${i}].id: missing advisory id`)
    if (typeof e.module !== 'string' || !e.module.trim()) problems.push(`entries[${i}].module: missing module name`)
    if (typeof e.reason !== 'string' || e.reason.trim().length < 20) {
      problems.push(`entries[${i}].reason: must describe why the advisory is waived (>= 20 chars)`)
    }
    if (typeof e.review_by !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(e.review_by)) {
      problems.push(`entries[${i}].review_by: must be an ISO date (YYYY-MM-DD)`)
    } else if (e.review_by < today) {
      problems.push(`entries[${i}] (${e.id}): review overdue since ${e.review_by} - re-review the advisory, then update review_by or remove the entry`)
    }
  }
  if (problems.length > 0) fail([`invalid audit allowlist:`, ...problems])
  return data.entries
}

function runNpmAudit(cwd) {
  const npm = process.platform === 'win32' ? 'npm.cmd' : 'npm'
  // shell:true is required on Windows: since Node's 2024 hardening
  // (CVE-2024-27980) spawning a .cmd/.bat shim without it fails with EINVAL,
  // and npm is a shim there. The arguments are fixed literals, so shell
  // joining cannot inject anything.
  const res = spawnSync(npm, ['audit', '--omit=dev', '--json'], {
    cwd,
    encoding: 'utf8',
    shell: process.platform === 'win32',
  })
  if (res.error) fail([`cannot run npm audit: ${res.error.message}`])
  if (!res.stdout || !res.stdout.trim()) {
    fail([`npm audit produced no JSON output (exit ${res.status})`, res.stderr?.trim()].filter(Boolean))
  }
  try {
    return JSON.parse(res.stdout)
  } catch (err) {
    fail([`npm audit output is not valid JSON: ${err.message}`, res.stderr?.trim()].filter(Boolean))
  }
}

// npm audit's JSON carries the advisory id only inside via[].url
// (via[].source is a numeric database id), so resolve the well-known
// GHSA id out of the URL and fall back to the raw source value.
function advisoryId(via) {
  const ghsa = typeof via.url === 'string' ? /GHSA-[0-9a-z-]+/i.exec(via.url) : null
  return ghsa ? ghsa[0] : via.source != null ? String(via.source) : null
}

function collectFindings(report, level) {
  const findings = []
  for (const [name, v] of Object.entries(report?.vulnerabilities ?? {})) {
    // An unmapped severity (an npm release introducing a level this
    // wrapper predates, or a malformed report) is a structural surprise in
    // the report itself: treat it as the WORST rank so the finding fails
    // the gate instead of silently skipping it.
    const rank = SEVERITY_RANK[v.severity] ?? SEVERITY_RANK.critical
    if (rank < SEVERITY_RANK[level]) continue
    const advisories = (v.via ?? [])
      .filter((x) => typeof x === 'object' && x !== null)
      .map((x) => ({ id: advisoryId(x), title: x.title ?? '', url: x.url ?? '' }))
    findings.push({
      name,
      severity: v.severity,
      range: v.range ?? '',
      fixAvailable: v.fixAvailable ?? false,
      advisories: advisories.length > 0 ? advisories : [{ id: null, title: '', url: '' }],
    })
  }
  return findings
}

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const frontendRoot = path.resolve(scriptDir, '..')
const args = parseArgs(process.argv.slice(2))
const allowlistPath = args.allowlist ?? path.join(frontendRoot, 'audit-allowlist.json')
const allowlist = loadAllowlist(allowlistPath)

const report = args.auditJson
  ? loadJson(args.auditJson, 'audit report')
  : runNpmAudit(frontendRoot)
const findings = collectFindings(report, AUDIT_LEVEL)

const failures = []
const waived = []
const matchedEntries = new Set()

for (const finding of findings) {
  const entry = allowlist.find((e) =>
    finding.advisories.some((a) => a.id === e.id || (a.url && a.url.includes(e.id))),
  )
  if (!entry) {
    const details = finding.advisories
      .map((a) => `${a.id ?? 'unknown-advisory'} ${a.title}`.trim())
      .join('; ')
    failures.push(
      `${finding.name}@${finding.range} severity=${finding.severity}: ${details || 'no advisory detail'} - fixAvailable=${JSON.stringify(finding.fixAvailable)}`,
    )
    continue
  }
  matchedEntries.add(entry.id)
  if (finding.fixAvailable !== false) {
    failures.push(
      `${finding.name} (${entry.id}): an upstream fix IS available now (fixAvailable=${JSON.stringify(finding.fixAvailable)}) - bump the dependency and DELETE the allowlist entry instead of waiving further`,
    )
    continue
  }
  waived.push(`${finding.name} (${entry.id}, severity=${finding.severity}, waived until ${entry.review_by})`)
}

for (const e of allowlist) {
  if (!matchedEntries.has(e.id)) {
    failures.push(
      `stale allowlist entry ${e.id} (${e.module}): the audit report no longer lists it - remove the entry`,
    )
  }
}

if (failures.length > 0) fail(failures)

console.log('frontend production-dependency audit: OK')
if (waived.length === 0) console.log('no production vulnerabilities reported')
for (const w of waived) console.log(`waived (reviewed): ${w}`)
console.log(`allowlist: ${allowlistPath} (${allowlist.length} entr${allowlist.length === 1 ? 'y' : 'ies'})`)
