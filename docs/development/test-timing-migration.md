# Timing migration ledger

## Scope and current status

The user-approved scope is **bounded_increment: finish the first stage, not the full P2 migration**. P0 portability/security diagnostic corrections and the named P1 proofs below are implemented. The stress/default split is explicit and CI-required. Remaining P2 is still unreviewed/unmigrated debt, not approved OS exceptions.

`internal/testtiming/timing-debt.json` is an exact expression/count ratchet, not a determinism certificate. Inventory shrank **160/261 → 155/254 → 126/201 → 124 expressions / 199 occurrences**. The last reduction removes only ProxyClient's 200ms dwell and terminal's 500ms negative window; no new expressions or increased counts. The earlier reduction also classified previously reviewed lexical virtual sleeps, not only newly repaired real waits.

## Completed proofs

| Tests / family | Proof retained |
| --- | --- |
| P0 git diagnostics and silent corpus | Expected line numbers derive from real fixture bytes; exact severity/message/path/attrs/count retained. Hermetic corpus replay accounts for the registry pipeline; golden and must-stay-denied assertions preserved. |
| Deferred wake cancellation, service-call timeout/latency | Self-contained synctest cancellation/timeout/latency; transport classification, attempts, metrics and diagnostics retained. |
| EventBatcher empty flush | Stop joins emitter before zero-event assertion, no quiet window. |
| Auth token cancellation / sign-out refresh | Real HTTP entry/cancel/caller-done/handler-done; release before endpoint Close; signed-out/stale-credential assertions retained. Other auth admission races remain P2. |
| Session runtime timestamps | Scoped fixed Date for stale snapshot ordering, original streaming/label/paused checks. |
| Vector debounce / overlap / cancel | Exact virtual debounce−1ns/deadline and stop-before-deadline; instance-local timer factory keeps real filesystem/storage integration. Pass A entry/release/done, overlap re-arm and pass B completion; both new files, exactly two incremental embeds, peak concurrency exactly one. Shutdown/join precedes fixture cleanup. |
| Vector memory-scavenge | Immutable instance-local Service dependency instead of shared mutable hook; late scavenge isolation of two live instances. Full/incremental/no-op gates, eviction/contentless sidecars retained. Owner Shutdown before storage close. |
| Embedded spawner publication | Count/configured-child publish under one mutex; configure-entry/release/completion plus positive watchdogs and joined cleanup. |
| Conductor limits | In-process synctest, three exact two-entrant waves, individual release barriers, active/peak and full worker drain; peak must equal cap. |
| Auto-fetch timer/reconfiguration | Exact virtual interval/reset/disable/re-arm, immutable config snapshots, cancellation/done join; No Project zero git-event assertion. Real git funnel stays outside bubble. |
| Coherence/project/session timestamps | Set and stat fixture mtime with Chtimes; exact ring cap. Real SQLite round-trip uses fixed old timestamp and parsed strictly later update, no sleep. |
| Session service gate/title eligibility | In-memory caller samples full budget at entry after virtual gate; tracked-worker join. Real named-session no-call assertion after Shutdown. |
| Auth UI / OAuth / CodeMirror | Await actual mount/event/mutation RPCs in async act; subscription cleanup/effect drain. Scoped scheduler/Date, exact cooldown boundaries, pending/settled completion, exact intentional diagnostic assertion. |
| Embedded idle/transport/stop P1 | Isolated virtual timer/budget/grace boundaries; real HTTP load/probe/body entry/release/completion. First readiness probe establishes publication; cancel/Load join and failed-stop handle retention precede cleanup. Other download/budget consumers remain P2. |
| Unix terminal no-exit-on-Stop/StopAll | Real PTY output callback entry is held while explicit teardown completes. Release → per-session readDone (including callbacks) → no onExit/no ended-marker assertions. Cleanup releases first, joins reader, reaps shell, then removes fixture dir. Positive watchdogs only fail hangs. No public Stop callback-join change; Windows ConPTY is not claimed migrated. |

## Default / stress boundary

Only two reviewed probabilistic tests carry the `TestStress*` prefix:

| Stress test | Work / assertions | Default coverage preserved |
| --- | --- | --- |
| `TestStressWriteManifestConcurrentWriters` | Real 8 writers × 25 writes, WaitGroup join, one coherent surviving record and no leftover temporary files. Interleavings remain probabilistic. | `TestManifestPromotionFaults`: first success/transient success/exhaustion/permanent failure, exact backoff/attempts, old record preservation and temp cleanup. Atomic replacement and Windows classifier/held-handle tests remain default. |
| `TestStressReconfigureMCPProxyClient` | Simultaneous bounded reconfigure reader and locked client writer loops; exactly 1000 completed rounds each, join before gateway cleanup. No 200ms scheduler dwell. `-race` observes real scheduling, not guaranteed overlap. | Real HTTP reconfigure/start lock-release and config mapping regressions remain default. The old churn started after the initial proxy snapshot at HTTP entry; the new loops repeatedly exercise reads as well as writes. Remaining MCP admission/publish migrations remain debt. |

**SQLite classification:** `backend/database_test.go` checks pool size and pragmas on distinct checked-out connections, WAL committed snapshots while a write transaction is held, and the single-connection negative control. These are controlled OS integration checks, not probabilistic hammer stress; they stay default. `BenchmarkReadsDuringOpenWrite` is an explicit throughput benchmark, not test acceptance. Other parallel-publication/SQLite consumers have not been blanket categorized or declared deterministic.

Commands and failure semantics:

- `make test` = `make test-go` (`go test -count=1 -v -skip '^TestStress' ./...`) + frontend. Raw `go test ./...` still includes stress; no build tags or runtime skips were introduced.
- `make test-stress` / Windows `go run ./internal/teststress` runs `go test -race -count=20 -shuffle=on -json -timeout=8m -run '^TestStress' ./core ./core/embeddedllm`. Live JSON output and exact per-test run/pass summaries are required. Nonzero subprocess exit, any fail/skip event, missing or excess counts fail; 20 is a fixed repetition count, not retry-to-green.
- All CI legs (Linux amd64/arm64, macOS, Windows) have a separate required stress step, with `!cancelled()` so a default failure does not suppress stress evidence; no continue-on-error. Category AST guard checks exact names/paths and Make/CI drivers; runner self-tests reject missing/incomplete/extra/skip/fail coverage.

## Remaining P2 (not approved exceptions)

- **Admission/publish/join:** builder MCP/config reconfigure, conductor trajectory/todo, vector queued/reset/open/publish, frontend auth/config/MCP/startup, persistent blackboard, session compaction/resume/sendmessage events, desktop startup/event helpers. Replace scheduler opportunities with acknowledgements and completion channels; release before join.
- **Cancellation/budget:** updater checker/downloader, embedded range downloads, builder budget/embedded gate, llmbudget transport, e2s/shared delayed callers. Retain real transfer/cancellation coverage; virtualize only isolated in-process arithmetic/timers, not whole network/filesystem fixtures.
- **TS flush:** GitFileEntry; AttachmentChips/ModelCombobox; FileTreeContextMenu/ProjectSelector; paper/research hooks and panels; LLM/model-profile/security/theme/UI-scale/vector/provider settings; combobox/chat/autonomy/sound families. Await the specific operation, not arbitrary Promise.resolve repetition.
- **OS positive-event integration:** fsnotify/workspace/project/research/skills/paper watchers, FIFO/nonregular gitconfig, writing-open probe retry, clipboard/remaining PTY/ConPTY/Linux notifications/window activation, SQLite consumers. Keep real kernel/transport coverage; positive watchdogs diagnose hangs, negative sampling needs explicit lifecycle/barrier or narrowly named OS sampling contract.
- **Other contention/publication:** category audit remains bounded to the named tests above. Repeated successful runs are not branch coverage or a blanket approval of other concurrent tests.

## Guard rules

`go test -count=1 -v ./internal/testtiming` scans Go test AST (including time aliases/dot imports) and TS/TSX direct delay-promises. New Sleep/empty timeout-select/direct delay-promises and increased legacy duplicate counts fail. Only Sleep lexically inside imported testing/synctest.Test function-literal bodies is classified virtual (aliases/dot imports covered); helpers, unrelated same-named APIs and empty negative timeouts remain debt. Source review must still exclude real OS I/O from bubbles.

Positive timeout branches with failure statements are hang watchdogs; nonblocking absence assertions after joined lifecycle need no timed window. Self-tests pin both patterns. The detector is not exhaustive: helper-hidden waits, nonempty negative timeout branches, elapsed-time comparisons and indirect JS timer constructions still need review.

After a verified removal only: `go test -count=1 -v ./internal/testtiming -update-timing-baseline`, inspect the JSON diff, then run without the flag. Never regenerate in CI or as a failure fallback.

## Verification evidence and limits

Previous-stage evidence (before this bounded increment): full macOS and Linux arm64 Go race/shuffle and frontend suites passed; build/lint/test/vulncheck passed after diagnosed fixes. Windows runtime, Linux amd64 runtime and live desktop/service checks were not locally executed; SDK separately had unresolved intentional diagnostics and is not claimed green. Detailed prior logs remain in session temp, not duplicated here.

Step 8 verification (macOS arm64):

| Check | Result |
| --- | --- |
| Focused six-package `-race -count=20 -shuffle=on -v` (terminal/default manifest/MCP/SQLite/guard/runner) | PASS, 960 PASS records, zero skips or warning/error/race diagnostics. |
| `LC_ALL=C make test-go` (full default Go) | PASS, 7268 PASS records, 11 existing platform/helper/live/invalid-path skips; no stress RUN records, zero warning/error/race diagnostics. |
| Direct runner, then `LC_ALL=C make test-stress` | Both PASS; each of the two stress tests ran/passed exactly 20 times per invocation, zero skips/diagnostics. |
| Guard + runner self-tests `-race -count=5 -shuffle=on -v` | PASS after lint-only regex correction; inventory/CI/Make and missing/incomplete/extra/skip/fail cases checked. |
| `make lint`, `go vet ./...`, `gofmt -d .`, `git diff --check` | Clean final checks; one earlier gocritic regexpSimplify finding fixed and checks repeated. |
| Baseline comparison to pre-increment snapshot | 126/201 → 124/199; only the two specified removals, no additions/increases. |

Complete logs and exit evidence are in session temp (`step8-{focused-final,default,stress-final,guard-final,lint-final,vet,gofmt}.log`); failed lint output is retained separately. Step 8 did not rerun full frontend tests/build/vulncheck, Linux/Windows runtime or remote CI. Configured matrix coverage is not a claim that remote jobs have already executed. Full P2 is intentionally outstanding.

### Step 9 final bounded-increment acceptance

Final diff and CI drivers reviewed. Two verification blockers were repaired without increasing watchdogs or weakening assertions:

- Unix terminal Stop/StopAll now uses a test-local executable `/bin/sh` fixture that explicitly writes readiness, rather than waiting for a user's login-shell prompt/startup plugins. Real PTY, held callback, reader join, shell reap and no-exit/no-marker checks remain intact; public Stop still does not join callbacks.
- `TestTokenManager_FailedRefreshIsSharedByWaiters` previously launched four goroutines without proving they joined the same refresh. A held real HTTP leader and test-local context `Done` acknowledgements now establish follower admission before release. All four callers must return `ErrReauthRequired`, and exactly one refresh must occur. Production auth code is unchanged. Other auth admission debt remains P2.

| Final check | Result |
| --- | --- |
| macOS arm64 sequential `make build` → `make lint` → `make test` → `make vulncheck`, repeated after both fixes | All exit 0; default Go 7268 PASS records / 11 existing skips; frontend 319 files / 4650 tests; no reachable Go vulnerabilities. |
| macOS full uncached Go `-race -count=1 -shuffle=on -v ./...` | Exit 0; 7270 PASS records / 11 existing skips; includes both stress tests once. |
| Dedicated stress runner on macOS and Linux arm64 | Exit 0 on both; each required test exactly 20 runs / 20 passes; zero skips or diagnostic errors/warnings. |
| Real PTY natural-exit and Stop/StopAll `-race -count=50 -shuffle=on -v` on both OSes | 200 PASS records per OS; zero skips/diagnostics. |
| Failed-refresh admission `-race -count=100 -shuffle=on -v` on both OSes | 100 PASS records per OS; zero skips/diagnostics; pre-fix Linux reproduction retained. |
| Linux arm64 Docker, Go 1.27.1, non-root UID 501, `GOWORK=off`, native `/tmp` fixtures | Full default exit 0 (7289 PASS / 24 existing skips); full uncached race/shuffle exit 0 (7291 PASS / 24 skips); `go vet ./...` clean. |
| Linux Node 24.21.0 frontend dependency install, build, lint and full tests | All exit 0; 319 files / 4650 tests; lint/test diagnostic-clean. |
| Windows amd64 and arm64 compile-only (`CGO_ENABLED=0`) | Seven packages compiled: core embeddedllm/terminal/tools/workspace, internal testtiming/teststress, backend providerauth. Not a full CGO/Wails build or runtime test. |
| Guard/runner `-race -count=5 -shuffle=on`, gofmt, vet, diff-check | Clean; baseline remains 124 expressions / 199 occurrences. Against step-8 entry snapshot: only the two reviewed removals, no additions/increases. |

Complete final test logs were inspected: zero emitted WARN/ERROR, React act/unhandled errors or race reports. `plan_test.go`'s negative-case `t.Logf` and hardware/platform/helper/live/optional-tool skip explanations are not operational warnings/errors; skips are reported, not hidden. Build/install output is **not warning-free**: npm allowScripts notices (esbuild/fsevents on macOS, esbuild on Linux), macOS linker deployment target 13.0/11.0, Linux Vite classic prepaint-script notice and chunks over 500 kB remain visible.

Logs and exit files are in session temp: `step9-{build,lint,test,vulncheck,race,stress}-accepted.*`, `step9-linux-{default,race,stress,terminal,vet}-accepted.*`, `step9-{terminal-fixed,auth-fixed,linux-auth-fixed,guard}.*`, `step9-linux-frontend-*-final.*`, and `step9-windows-*-compile.*`. Earlier failures are retained separately, including the inherited-shell watchdog, auth admission reproduction, incomplete frontend asset copy and host-bind TMPDIR filesystem/ownership mismatch. The latter was fixed by native Linux fixtures, not a Git trust bypass or assertion change.

Both repository HEADs and SDK worktree are unchanged; manifests/bindings/icon have no diff. Git fsck exits 0; `--no-reflogs` dangling-object notices are retained and no objects were removed. No commit/push. Windows runtime is unavailable; ConPTY/held-handle execution, Linux amd64 runtime, Linux Wails packaging and live desktop/service acceptance were not performed in this stage. Remote CI green is not claimed. The user-approved first bounded increment is locally verified; full P2 remains the debt enumerated above, not an approved OS exception list.
