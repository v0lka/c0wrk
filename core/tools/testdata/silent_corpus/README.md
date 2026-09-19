# silent_corpus — the audited silent-mode golden corpus

194 replayable fixtures frozen from the independent audit
([`silent-mode-audit-report.md`](../../../silent-mode-audit-report.md), snapshot
of `~/.c0wrk/database.db` on 2026-09-18): every silent-mode `tool_confirm`
decision (roles `silent_decision`/`autonomy_decision`), linked to its actual
call through the audit's methodology — the NEXT `tool_result` after the event
→ its `tool_call_id` → the `tool_call` row → full `command` +
`working_directory` from `args` (194/194 linked).

## Files

- `corpus.ndjson` — one fixture per line: replay inputs (`tool`, `command`,
  `workdir`, `workspace`, `path`), the audit classification (`audit_class`:
  TRUE_ALLOW 143 / FALSE_ALLOW 0 / TRUE_DENY 8 / FALSE_DENY 43), the
  historical gate context (`gate_verdict`, `gate_mode`, `gate_reason`,
  `criterion`), the fix-track tags from
  [`silent-mode-deny-accuracy-recommendations.md`](../../../silent-mode-deny-accuracy-recommendations.md)
  (A=13 evidence validity, B=26 verification marker, C=4 expansion bindings,
  D=2 judge determinism; tags only on FALSE_DENY), and
  `expected_final_outcome` — the post-tracks target (TD → deny, everything
  else → allow).
- `baseline_snapshot.json` — the golden cross-tab of the deterministic replay
  (`TestSilentCorpus_Replay`). Update it consciously with
  `SILENT_CORPUS_REGEN_SNAPSHOT=1 go test ./core/tools -run TestSilentCorpus_Replay`
  as each track lands; the snapshot diff is the review artifact for "which
  FDs did this PR retire".

## Tests

- `core/tools/silent_corpus_test.go` — loader + integrity (distribution,
  pinned TD event ids, track arithmetic).
- `core/tools/silent_corpus_replay_unix_test.go` — the replay: real flowsh
  analysis + real registry gates + real silent terminal, deterministic stub
  judge (ALLOW ⟺ marker-B ∨ no hard criteria), inert tool Execute overrides
  so corpus commands are never run.

## Regeneration

The corpus is a frozen audit artifact: do NOT regenerate it casually — a
regeneration reclassifies nothing but must be re-audited. The extraction
script (testonly, deliberately NOT committed to the repository) read the DB
read-only and joined the report table with the tool_call linkage described
above; if the corpus ever must be rebuilt, re-derive it from the audit report
table + the same linkage and re-verify the 143/0/8/43 distribution and the
pinned TD ids before committing.
