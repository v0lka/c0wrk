#!/usr/bin/env python3
"""agent-quality-scan — reproducible model-fault / agent-quality scan over c0wrk session artifacts.

Companion runbook: docs/development/agent-quality.md

The scan reproduces the weekly model-fault report from two on-disk sources:

1. LLM dumps  ~/.c0wrk/projects/<project>/<session>/dumps/**
   Files: session_<sid>_llm_dump.jsonl[.zst|.gz] plus per-delegation steps/*.jsonl[.zst|.gz].
   Each line is a JSON record:
     {"ts": "...", "direction": "request"|"response", "data": {...}, "error"?: "..."}
   Requests carry data.messages; assistant messages hold model tool_calls, tool-role
   messages hold tool results. Tool-result content is classified against the fault
   taxonomy below. History replay repeats every tool result in every later request,
   so results are deduplicated per (project, session, tool_call_id, category).
   Successful results of untrusted-source tools (bash_exec, read_file, web_search, ...)
   are wrapped in <untrusted-content> BEFORE entering history; genuine tool-authored
   error results are not wrapped. Wrapped content is therefore excluded outright —
   this removes code/log-text false positives entirely (the wrap discriminator).

2. slog logs  ~/.c0wrk/logs/session-*.log (JSON lines, MSK timestamps)
   - bad-JSON scan: records with msg "tool call arguments received" carry provider,
     tool, tool_call_id, arg_len, valid_json, repaired, dispatch_outcome. Records with
     valid_json=false are model-emitted tool-call arguments the transport could not
     decode — the log-based bad-JSON signal.
   - nudge near-miss: records whose msg contains "format nudge" (oneshot format-retry).
   - provider attribution: tool-call volume, bad-JSON and nudges per provider.

Report reproduction (this week's report, cut off at its generation moment):

    python3 scripts/agent-quality-scan.py --since 2026-11-13T00:00:00Z --until 2026-11-20T08:41:00Z

Latest state (window open-ended at "now"):

    python3 scripts/agent-quality-scan.py

Exit code is 0 for any successful scan; 2 on usage/environment errors.
"""

from __future__ import annotations

import argparse
import gzip
import json
import os
import re
import subprocess
import sys
from collections import Counter, defaultdict
from datetime import datetime, timezone

DEFAULT_ROOT = os.path.expanduser("~/.c0wrk/projects")
DEFAULT_LOGS = os.path.expanduser("~/.c0wrk/logs")
DEFAULT_SINCE = "2026-11-13T00:00:00Z"

# ---------------------------------------------------------------------------
# Timestamps
# ---------------------------------------------------------------------------

_ISO_RE = re.compile(
    r"^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})?$"
)


def parse_ts(ts: str) -> datetime | None:
    """Parse c0wrk timestamps (dumps: UTC Z + nanoseconds; logs: local +03:00)."""
    m = _ISO_RE.match(ts or "")
    if not m:
        return None
    base, frac, off = m.groups()
    micro = ((frac or "") + "000000")[:6]
    try:
        t = datetime.fromisoformat(f"{base}.{micro}{off or '+00:00'}")
    except ValueError:
        return None
    return t.astimezone(timezone.utc)


# ---------------------------------------------------------------------------
# Fault taxonomy
# ---------------------------------------------------------------------------

# Each category matches a verbatim content marker emitted by the engine
# (sp4rk agent/executor_run.go renders, tools/* error results). Order matters:
# first match wins. MODEL_* = the model produced a call the engine refused or the
# user rejected; TOOL_* = the tool ran but failed on its own terms. Predicates are
# kept semantically identical to the original one-off investigation scan so weekly
# runs stay comparable.
CATEGORIES: list[tuple[str, object]] = [
    ("MODEL_PARSE", lambda c: 'failed to parse input' in c),
    # sp4rk *tools.InputValidationError render; the "input for \"" marker alone is
    # NOT sufficient — the discriminator suffix is required (an MCP echo, CLI
    # stderr or quoted web content may contain the tool-name fragment without it).
    ("MODEL_VALIDATION", lambda c: 'input for "' in c
        and (' is invalid: ' in c or ' is invalid at ' in c)),
    ("MODEL_NOT_FOUND", lambda c: c.lstrip().startswith('tool not found: ')),
    ("MODEL_POLICY", lambda c: 'blocked by security policy' in c),
    ("MODEL_USER_DENY", lambda c: 'Tool execution denied by user.' in c),
    ("MODEL_HITL_ERR", lambda c: 'HITL handler error' in c),
    # per-sub-call failure render inside an executed batch
    ("MODEL_BATCH_SUB", lambda c: re.match(r'error executing "', c.lstrip()) is not None),
    ("TOOL_VALIDATION", lambda c: c.lstrip().startswith('validation error: ')),
    ("TOOL_COHERENCE", lambda c: 'File conflict: ' in c and 'modified since your last read' in c),
    ("TOOL_EDIT_UNIQ", lambda c: 'old_string is not unique' in c or 'must appear exactly once' in c),
    ("TOOL_EDIT_CONFLICT", lambda c: 'file changed since read' in c.lower()
        or 'matched more than once' in c or ('old_string' in c and 'was not found' in c)),
    ("TOOL_READ_FAIL", lambda c: 'failed to read file: ' in c),
    ("TOOL_WRITE_FAIL", lambda c: 'failed to write file: ' in c),
    ("TOOL_NO_FILE", lambda c: re.search(
        r'No such file|does not exist|not a directory|is a directory', c) is not None),
    ("TOOL_REGEX", lambda c: 'invalid pattern' in c or 'error parsing regex' in c
        or 'invalid regex' in c),
]

# Near-miss buckets: content that merely *looks* like an error. These are manual
# triage aids and deliberately over-capture (e.g. a successful store_fact result
# whose keywords mention "transport" lands in INFRA_SUSPECT).
INFRA_RE = re.compile(
    r"timeout|deadline|context cancel|connection refused|ECONNREFUSED|rate limit"
    r"|overloaded|\b5\d\d\b|EOF|broken pipe|temporarily unavailable|transport",
    re.I,
)
OTHER_ERROR_RE = re.compile(r"\s*(Error|ERROR|error)[:\s]")


def classify(content: str) -> str | None:
    """Classify one tool-result content string; None = not a counted fault."""
    stripped = content.lstrip()
    # Wrap discriminator: <untrusted-content> marks SUCCESSFUL output of
    # untrusted-source tools stored in history. Never a model fault.
    if stripped.startswith("<untrusted-content"):
        return None
    head = stripped[:400]
    for cat, pred in CATEGORIES:
        try:
            if pred(head):
                return cat
        except Exception:
            pass
    if OTHER_ERROR_RE.match(head):
        return "OTHER_SUSPECT"
    if INFRA_RE.search(head[:300]):
        return "INFRA_SUSPECT"
    return None


# ---------------------------------------------------------------------------
# Dump decoding
# ---------------------------------------------------------------------------

_GZIP_MAGIC = b"\x1f\x8b"
_ZSTD_MAGIC = b"\x28\xb5\x2f\xfd"


def decode_dump(path: str) -> str | None:
    """Decode a dump file (plain / gzip / zstd). Returns None when undecodable."""
    try:
        with open(path, "rb") as f:
            magic = f.read(4)
    except OSError as exc:
        print(f"READ_FAIL {path}: {exc}", file=sys.stderr)
        return None
    try:
        if magic[:2] == _GZIP_MAGIC:
            with gzip.open(path, "rb") as f:
                return f.read().decode("utf-8", "replace")
        if magic == _ZSTD_MAGIC:
            # rc=1 is expected for a truncated (live / unclean-shutdown) tail
            # file; the decoded prefix is still valid.
            r = subprocess.run(["zstd", "-dc", path], capture_output=True)
            if not r.stdout:
                return None
            return r.stdout.decode("utf-8", "replace")
        with open(path, "rb") as f:
            return f.read().decode("utf-8", "replace")
    except OSError as exc:
        print(f"DECODE_FAIL {path}: {exc}", file=sys.stderr)
        return None


def scan_dumps(root: str, since: datetime, until: datetime, entries_out: list) -> dict:
    """Walk dumps/**, classify tool results, dedup, aggregate. Returns stats."""
    since_epoch = since.timestamp()
    seen: set[tuple[str, str, str, str]] = set()
    stats: Counter = Counter()
    by_tool: dict[str, Counter] = defaultdict(Counter)
    by_model: dict[str, Counter] = defaultdict(Counter)
    sessions: set[tuple[str, str]] = set()
    llm_errors: Counter = Counter()
    other_samples: dict[str, list[str]] = defaultdict(list)
    nfiles = 0
    zstd_warned = False

    for dirpath, _, names in os.walk(root):
        rel_dir = os.path.relpath(dirpath, root)
        parts = rel_dir.split(os.sep)
        # Layout: <project>/<session>/dumps[/steps]/<file>; the last component
        # of the *directory* path tells us which container we are in.
        if len(parts) < 3 or parts[-1] != "dumps" and parts[-1] != "steps":
            continue
        proj, sid = parts[0], parts[1]
        for name in sorted(names):
            is_main = "llm_dump" in name
            is_step = parts[-1] == "steps"
            if not (is_main or is_step):
                continue
            if not name.endswith((".jsonl", ".jsonl.zst", ".jsonl.gz")):
                continue
            path = os.path.join(dirpath, name)
            try:
                if os.path.getmtime(path) < since_epoch - 48 * 3600:
                    continue  # cannot contain in-window records; skips old bodies
            except OSError:
                continue
            if name.endswith(".zst") and not zstd_warned:
                try:
                    subprocess.run(["zstd", "--version"], capture_output=True, check=True)
                except (OSError, subprocess.CalledProcessError):
                    print("warning: zstd binary not found; .zst dumps skipped", file=sys.stderr)
                    zstd_warned = True
            text = decode_dump(path)
            if text is None:
                continue
            nfiles += 1
            calls: dict[str, tuple[str, str]] = {}
            last_model = ""
            for line in text.splitlines():
                if not line.strip():
                    continue
                try:
                    rec = json.loads(line)
                except Exception:
                    continue
                ts = parse_ts(rec.get("ts", ""))
                if ts is None or not (since <= ts < until):
                    continue
                direction = rec.get("direction")
                data = rec.get("data") or {}
                if direction == "response":
                    model = data.get("model") or ""
                    if model:
                        last_model = model
                    if rec.get("error"):
                        llm_errors[str(rec["error"])[:120]] += 1
                    continue
                if direction != "request":
                    continue
                for msg in data.get("messages") or []:
                    role = msg.get("role")
                    if role == "assistant":
                        for tc in msg.get("tool_calls") or []:
                            args = tc.get("input")
                            calls[tc.get("id")] = (
                                tc.get("name") or "?",
                                json.dumps(args, ensure_ascii=False)[:600] if args is not None else "",
                            )
                    elif role == "tool":
                        cid = msg.get("tool_call_id") or ""
                        content = msg.get("content") or ""
                        if not isinstance(content, str):
                            content = json.dumps(content, ensure_ascii=False)
                        cat = classify(content)
                        if cat is None:
                            continue
                        key = (proj, sid, cid, cat)
                        if key in seen:
                            continue  # history replay: same result seen again
                        seen.add(key)
                        name_t, args_t = calls.get(cid, ("?", ""))
                        entry = {
                            "project": proj,
                            "session": sid,
                            "ts": rec.get("ts"),
                            "purpose": data.get("call_purpose") or "",
                            "category": cat,
                            "tool": name_t or "?",
                            "model": last_model,
                            "args": args_t,
                            "content": content[:400],
                        }
                        entries_out.append(entry)
                        stats[cat] += 1
                        by_tool[cat][entry["tool"]] += 1
                        if last_model:
                            by_model[cat][last_model] += 1
                        sessions.add((proj, sid))
                        if cat == "OTHER_SUSPECT" and len(other_samples[entry["tool"]]) < 3:
                            other_samples[entry["tool"]].append(content[:200])

    return {
        "files": nfiles,
        "stats": stats,
        "by_tool": by_tool,
        "by_model": by_model,
        "sessions": sessions,
        "llm_errors": llm_errors,
        "other_samples": other_samples,
    }


# ---------------------------------------------------------------------------
# Log scan (bad JSON, nudges, provider attribution)
# ---------------------------------------------------------------------------


def scan_logs(logs_dir: str, since: datetime, until: datetime) -> dict:
    providers: Counter = Counter()  # (provider, in-window) -> calls
    bad_json: Counter = Counter()  # (provider, in-window) -> invalid-JSON args
    repaired: Counter = Counter()  # (provider, in-window) -> repaired args
    nudges: Counter = Counter()  # provider ('?' when absent) -> format nudges
    outcomes: Counter = Counter()  # dispatch_outcome (in-window)
    nfiles = 0
    if not os.path.isdir(logs_dir):
        return {
            "providers": providers, "bad_json": bad_json, "repaired": repaired,
            "nudges": nudges, "outcomes": outcomes, "files": 0,
        }
    for name in sorted(os.listdir(logs_dir)):
        if not name.startswith("session-") or not name.endswith(".log"):
            continue
        path = os.path.join(logs_dir, name)
        try:
            if os.path.getmtime(path) < since.timestamp() - 48 * 3600:
                continue
        except OSError:
            continue
        nfiles += 1
        try:
            with open(path, "r", encoding="utf-8", errors="replace") as f:
                for line in f:
                    line = line.strip()
                    if not line.startswith("{"):
                        continue
                    try:
                        rec = json.loads(line)
                    except Exception:
                        continue
                    ts = parse_ts(rec.get("time", ""))
                    if ts is None or not (since <= ts < until):
                        continue
                    msg = rec.get("msg", "")
                    prov = rec.get("provider", "?")
                    if msg == "tool call arguments received":
                        inwin = True  # ts already window-filtered
                        valid = rec.get("valid_json")
                        if valid is None:
                            providers[("legacy-format", True)] += 1
                            continue
                        providers[(prov, inwin)] += 1
                        if not valid:
                            bad_json[(prov, inwin)] += 1
                        if rec.get("repaired"):
                            repaired[(prov, inwin)] += 1
                        oc = rec.get("dispatch_outcome")
                        if oc:
                            outcomes[oc] += 1
                    elif "format nudge" in msg:
                        nudges[prov] += 1
        except OSError as exc:
            print(f"LOG_READ_FAIL {path}: {exc}", file=sys.stderr)
    return {
        "providers": providers, "bad_json": bad_json, "repaired": repaired,
        "nudges": nudges, "outcomes": outcomes, "files": nfiles,
    }


# ---------------------------------------------------------------------------
# Reporting
# ---------------------------------------------------------------------------


def fmt_provider_table(logstats: dict) -> str:
    lines = ["provider            calls   bad_json   repaired"]
    provs = sorted({p for p, _ in logstats["providers"]})
    for p in provs:
        calls = logstats["providers"].get((p, True), 0)
        bad = logstats["bad_json"].get((p, True), 0)
        rep = logstats["repaired"].get((p, True), 0)
        lines.append(f"{p:<20}{calls:>6}  {bad:>9}  {rep:>9}")
    return "\n".join(lines)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--root", default=DEFAULT_ROOT, help="projects root (default ~/.c0wrk/projects)")
    ap.add_argument("--logs-dir", default=DEFAULT_LOGS, help="slog logs dir (default ~/.c0wrk/logs)")
    ap.add_argument("--since", default=DEFAULT_SINCE, help="window start, ISO-8601 UTC (default report week)")
    ap.add_argument("--until", default=None, help="window end, ISO-8601 UTC (default: now)")
    ap.add_argument("--json", dest="json_out", default=None, help="also write full JSON report to this path")
    args = ap.parse_args()

    since = parse_ts(args.since)
    if since is None:
        print(f"error: bad --since: {args.since!r}", file=sys.stderr)
        return 2
    until = parse_ts(args.until) if args.until else datetime.now(timezone.utc)
    if until is None:
        print(f"error: bad --until: {args.until!r}", file=sys.stderr)
        return 2
    if since >= until:
        print("error: --since must be before --until", file=sys.stderr)
        return 2

    entries: list = []
    dumpstats = scan_dumps(args.root, since, until, entries)
    logstats = scan_logs(args.logs_dir, since, until)

    stats = dumpstats["stats"]
    total = sum(stats.values())

    print(f"window: {since.isoformat()} .. {until.isoformat()}")
    print(f"dumps files decoded: {dumpstats['files']}")
    print(f"sessions with counted faults: {len(dumpstats['sessions'])}")
    print("--- category histogram (deduped by project/session/tool_call_id/category) ---")
    for cat in sorted(stats, key=lambda c: -stats[c]):
        tools = ", ".join(f"{t}={n}" for t, n in dumpstats["by_tool"][cat].most_common(6))
        print(f"{cat:<20}{stats[cat]:>5}   [{tools}]")
    print(f"{'TOTAL':<20}{total:>5}")
    print("--- near-miss nudge scan (dumps OTHER_SUSPECT samples by tool) ---")
    if not dumpstats["other_samples"]:
        print("  (none)")
    for t, samples in dumpstats["other_samples"].items():
        print(f"  {t}:")
        for s in samples:
            print(f"    {s!r}")
    print("--- near-miss INFRA_SUSPECT (deliberately over-captures; verify manually) ---")
    for t, n in dumpstats["by_tool"].get("INFRA_SUSPECT", Counter()).most_common():
        print(f"  {t}: {n}")
    print("--- log scan: bad-JSON / provider attribution (in-window) ---")
    print(fmt_provider_table(logstats))
    print(f"format-nudge retries (in-window): {sum(logstats['nudges'].values())}"
          + (f"  {dict(logstats['nudges'])}" if logstats["nudges"] else ""))
    print(f"dispatch outcomes (in-window): {dict(logstats['outcomes']) or '{}'}")
    print("--- LLM-level response errors (dumps, in-window; infra/provider failures) ---")
    if not dumpstats["llm_errors"]:
        print("  (none)")
    for e, n in dumpstats["llm_errors"].most_common(15):
        print(f"  {n:>4}  {e!r}")

    if args.json_out:
        payload = {
            "window": {"since": since.isoformat(), "until": until.isoformat()},
            "dumps_files": dumpstats["files"],
            "sessions": sorted(f"{p}/{s}" for p, s in dumpstats["sessions"]),
            "category_histogram": dict(stats),
            "by_tool": {c: dict(v) for c, v in dumpstats["by_tool"].items()},
            "by_model": {c: dict(v) for c, v in dumpstats["by_model"].items()},
            "llm_errors": dict(dumpstats["llm_errors"]),
            "log_scan": {
                "files": logstats["files"],
                "providers": {f"{p}|{'in' if i else 'pre'}": n for (p, i), n in logstats["providers"].items()},
                "bad_json": {f"{p}|{'in' if i else 'pre'}": n for (p, i), n in logstats["bad_json"].items()},
                "repaired": {f"{p}|{'in' if i else 'pre'}": n for (p, i), n in logstats["repaired"].items()},
                "format_nudges": dict(logstats["nudges"]),
                "dispatch_outcomes": dict(logstats["outcomes"]),
            },
            "entries": entries,
        }
        with open(args.json_out, "w", encoding="utf-8") as f:
            json.dump(payload, f, ensure_ascii=False, indent=1)
        print(f"JSON report written: {args.json_out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
