# ADR-116: Compressed Session Artifacts — Config Codec + Per-File Magic Sniffing, zstd Default

## Status

Accepted

## Context

While an agent works, the backend writes a small set of strictly append-only,
per-session artifacts:

- the session log `logs/session_<id>.log` (every log level, via `slog` through
  `Manager.createSessionLogger`),
- the DEBUG-gated LLM dumps `dumps/session_<id>_llm_dump.jsonl` and
  `dumps/steps/step_<stepID>.jsonl` (`openDumpArtifacts`, the sp4rk
  `StepDumpTracker`).

Every dump record resends the LLM conversation so far — a long task's request
records each carry the full growing context — so the artifacts accumulate
enormous cross-record redundancy and reach hundreds of MiB per session
(observed: a single 332 MiB step dump, message count growing 3 → 20 over the
session). Nothing in the app reads these files back: they are a post-mortem
debugging surface consumed with external tools. And the write path is already
the shape compression wants: `safeio` append-only opens
(`O_CREATE|O_WRONLY|O_APPEND`, link-safe), one `json.Encoder.Encode` = one
`Write` syscall per JSONL record, no in-place rewrites, no seeks.

Measurements on live session artifacts (a real long-session dump set):

| codec | ratio | note |
| ----- | ----- | ---- |
| gzip (default level) | x3.0–x3.5 | the 32 KiB LZ77 window cannot dedup across records |
| xz -1 | x22–x160 | two orders below zstd, and see Alternatives |
| zstd (klauspost/compress v1.20.3) | x112–x461 | 332 MiB → 755 KiB, 242 ms encode with Flush per record |

The deciding constraint is the append contract: a compressed artifact must
remain a valid stream under (a) append re-open across session restarts, (b) a
writer that crashes before `Close`, and (c) live tailing — a reader must see
every acknowledged record the moment `Write` returns, and files must stay
readable by third-party means (the `zstd` CLI, `mc`'s on-the-fly archive
viewers). gzip multi-member and zstd multi-frame streams satisfy all three
when each record is flushed before its `Write` returns; existing files must
never be rewritten by a config flip.

## Decision

The behavioral contract (layout, sniff rule, plumbing) is documented in the
domain spec: [session-lifecycle.md § Compressed Session
Artifacts](../domains/session-lifecycle.md#compressed-session-artifacts).

**1. `appendcodec` (sp4rk) — the append-safe codec layer.** A new
`github.com/v0lka/sp4rk/appendcodec` package provides `CodecNone` /
`CodecGzip` / `CodecZstd` writers with the core property that a write is
durable the moment `Write` returns (a trailing `Flush` per record), so every
completed record is already a decodable prefix on disk and an appended-to file
stays a valid continuation of the existing stream. `DetectMagic` sniffs an
existing file's codec from its leading bytes without following a symlink;
`OpenAppend` reads the existing magic from the SAME descriptor it appends
through (no Stat→Open TOCTOU window) and refuses a codec mismatch rather than
producing an undecodable file. A single writer is safe for concurrent use (a
mutex serializes `Write`); several writers on one path are refused by
convention — one path, one writer. The sp4rk `StepDumpTracker` accepts the
codec via `WithCodec`/`WithCodecWindowLog`.

**2. Config — `session_artifacts.compression`** (`backend/config/session_artifacts.go`,
documented in `config.example.yaml`):

- `algorithm: none | gzip | zstd` — closed enum, default `zstd` (the measured
  best). `xz` is deliberately NOT in the enum.
- `zstd_window_log: 10..27`, default `25` (a 32 MiB window — spans the
  observed dedup distances). 10 is the zstd format floor (1 KiB); 27 is an
  operator cap 4× below the library max (`MaxWindowSize = 1<<29`) so a typo
  cannot allocate half a GiB per stream. Memory scales linearly with the
  window — decoder ~1×, encoder ~2× (history buffer) plus hash tables.
- Load path FAIL-SOFT: unset values are materialized to the defaults by
  `ApplyDefaults` (the persisted shape is always explicit), and each invalid
  knob is warned + reset in place (`normalizeSessionArtifacts`) without
  discarding the sibling knob. The window is validated even under non-zstd
  algorithms so the persisted shape stays canonical.
- Save path FAIL-CLOSED: `validateSessionArtifactsForSave` refuses to persist
  an invalid algorithm or an out-of-range window (both paths share one
  `validate()` rule plus the exported `ValidateSessionZstdWindowLog`, so they
  cannot drift).

**3. Scope is fixed by the artifact writers, not operator-configurable:**
`logs/session_<id>.log`, `dumps/session_<id>_llm_dump.jsonl`,
`dumps/steps/step_<stepID>.jsonl`. The section has no glob/enable knobs.

**4. The codec lives in the file name, and existing files keep their own
codec.** Extensions: `none` → `""`, `gzip` → `.gz`, `zstd` → `.zst`
(`config.SessionArtifactExtension`). Openers probe the candidate paths in the
fixed order `[plain, .gz, .zst]`
(`config.SessionArtifactAlgorithmsInProbeOrder` — a determinism contract, not
a preference), sniff the first existing file's magic, and continue it with its
own codec; an empty existing file adopts its extension's codec; only when no
candidate exists is the file created under the configured algorithm's
extension. A config flip therefore never rewrites history — a session's
artifacts are created once and continued forever in their birth codec ("для
существующих сессий алгоритм не меняем"). A sniff/open refusal (planted
symlink, non-regular file) disables the artifact at the caller with the
existing warnings — it never falls through to creating the configured path,
which would silently bypass the link.

**5. Per-step dumps follow the configured codec** (`WithCodec`): step files
are new files at execution time, so the sniff-continue rule is reserved for
artifacts that already exist at session open; a re-executed step whose earlier
file carries a different codec starts a new file under the current extension,
and `appendcodec`'s same-descriptor magic check still refuses any mismatch at
the exact path.

**6. Live-follow.** `Manager.SetSessionCompression` (wired from the loaded
config in `application.go`) applies the knobs to sessions created afterwards;
since existing files keep their codec, a mid-session flip only affects
artifacts that do not exist yet.

**7. Migration is a manual operator step — no tool ships with the feature.**
The app never rewrites existing artifacts, and old plain files are fully
supported residents: there is no migration deadline and no bundled or
in-app migration path. An operator who wants compression on old artifacts
converts them by hand with the standard CLIs (`zstd file` / `gzip -k file`
produce exactly the on-disk shape the app writes, extension included; the
sniff rule continues the converted file regardless of a stale extension).

## Consequences

- Dump storage drops by the measured two orders of magnitude (x112–x461);
  logs gain gzip-class or better compression for free.
- Every acknowledged record is a decodable prefix on disk: live tailing works
  against compressed artifacts, and a crash mid-write loses nothing already
  acknowledged (a stream truncated before `Close` decodes fully — verified
  sha-identical against the system `zstd -cd`). The append continuation has
  one documented sharp edge: when the app reopens such a truncated file, the
  shape becomes `[broken frame][new frames]`, and a straight third-party
  decode stops at the broken frame — the frames completed before the crash
  still stream out, the frame in flight fails its checksum (`zstd -dc`
  exits 1, `Restored data doesn't match checksum`) and everything appended
  after the restart is skipped (gzip: head emitted, then `invalid
  compressed data`). The appended tail is recovered by hand: split the file
  at the frame magic that begins the appended section (`28 b5 2f fd` per
  zstd frame, `1f 8b` per gzip member) and decode the parts separately —
  the post-magic part decodes cleanly, the head emits its pre-crash content
  (verified against the system CLIs).
- Old sessions are untouched: pre-codec plain files stay plain, gzip stays
  gzip; every reader understands all three forms, so no migration deadline
  exists.
- The per-stream encoder RAM is bounded by the window cap (~2× window plus
  hash tables; ~64 MiB at the default 25; ≤ ~256 MiB at 27) — the deliberate
  ceiling below the library maximum. Reading a `.zst` artifact costs the
  decoder ~1× window.
- One extra `Flush` per record costs a little write amplification (measured:
  242 ms to encode 332 MiB with per-record flushes — noise against the session
  it belongs to).
- `github.com/klauspost/compress` becomes a direct dependency (pinned;
  verified `Flush` on the streaming writer and `WithWindowSize` up to
  `1<<29`).
- Artifact file names now vary by codec (`.jsonl`, `.jsonl.gz`, `.jsonl.zst`);
  path helpers append the extension centrally (`SessionLogPathFor` /
  `SessionDumpPathFor`) and openers probe — callers never guess.
- Hand-edited configs with a typo in the section fail soft at load (one
  warning naming key and value each, reset to the default) and cannot persist
  (save gate), so a broken value can never silently wedge session creation.

## Alternatives Considered

- **gzip as the default** — rejected on measurements: x3.0–x3.5 only. The
  32 KiB LZ77 window cannot reach the cross-record dedup that makes these
  artifacts so redundant (each record resends the whole context so far). It
  stays available as an `algorithm` value for environments that want the
  stdlib-only path.
- **xz** — rejected and deliberately excluded from the enum:
  `github.com/ulikunitz/xz` v0.5.17's `xz.Writer` has no streaming `Flush`
  (only `Write`/`Close`), so a live tail would lag a whole block behind the
  writer and crash semantics worsen — the append contract (§ Decision 1)
  cannot be honoured. Its measured ratios (x22–x160) are also far below
  zstd's. The workaround — concatenating fresh xz streams per rotation — was
  verified decodable but rejected by the operator as too much wrapping for a
  log file. An `xz` value in the config is a load warning + reset and a save
  refusal, with the reason named in the error text.
- **A own container format** (framing records inside a custom file) —
  rejected: artifacts must remain readable by third-party tools (`zstd -cd`,
  `mc`'s on-the-fly gz/xz/zst viewers). A custom container makes every dump
  inspection a c0wrk-only operation and duplicates exactly what the
  multi-member/multi-frame append formats already provide.
- **Status quo (no compression)** — rejected: a 332 MiB single step dump
  makes the artifacts operationally painful to keep and slow to inspect; the
  append-only write shape and the absence of in-app readers make compression
  nearly free to adopt.
- **Automatic recompression of old artifacts on startup** — rejected:
  rewriting user data as a side effect of a launch (and of a codec flip)
  has no review point; a migration, where an operator wants one, is a
  deliberate manual action instead.
