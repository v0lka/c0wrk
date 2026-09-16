#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""literature.py - optional helper: find predecessors, citing works, and contradiction candidates.

This is a convenience helper. The study-paper workflow runs without it.

WHAT IT DOES
    Given a seed paper (a DOI, an arXiv id, an OpenAlex id, a title, or a URL),
    it collects and prints:
      - predecessors : works the seed references
      - citing       : works that cite the seed
      - contradictions : a heuristic shortlist pulled from those two sets whose
        titles (and abstracts, with --with-abstracts) carry refutation markers
        such as "contradict", "fails to replicate", "no evidence", "counterexample",
        "reanalysis", "challenge", "rebuttal", "correction", or "retraction".

SOURCES (keyless first; queried politely)
    1. OpenAlex    https://api.openalex.org          (keyless; primary)
    2. Crossref    https://api.crossref.org          (keyless; reference lists)
    3. arXiv       https://export.arxiv.org/api/query (keyless)
    4. Semantic Scholar (OPTIONAL) - used only with --semantic-scholar or S2_API_KEY.
       Its keyless tier is heavily rate-limited; this helper backs off on HTTP 429.

DEPENDENCIES
    - Python 3.8+ standard library only. No third-party packages are used.
    - Network access is required. Without it the script stops with a clear
      message and exit code 2.
    - Optional: S2_API_KEY in the environment for higher Semantic Scholar limits.

RATE LIMITS (HTTP 429)
    Every request goes through one helper that honours the Retry-After header and
    otherwise backs off exponentially (capped), retrying a bounded number of
    times. If the limit still will not clear, it reports a clear error naming the
    source instead of hanging or printing a raw traceback.

EXIT CODES
    0  seed resolved and results produced
    1  the seed could not be resolved
    2  network unavailable
    3  unexpected problem

USAGE
    python3 literature.py 10.1038/nature12373
    python3 literature.py arXiv:1706.03762 --direction citing --limit 50
    python3 literature.py "Attention Is All You Need" --format text
    python3 literature.py 10.1038/nature12373 --semantic-scholar --with-abstracts
"""

import argparse
import json
import os
import re
import socket
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET

USER_AGENT = "study-paper-literature/1.0"
DEFAULT_TIMEOUT = 25
MAX_RETRIES = 3

ATOM_NS = {"a": "http://www.w3.org/2005/Atom", "arxiv": "http://arxiv.org/schemas/atom"}
DOI_RE = re.compile(r"10\.\d{4,9}/[^\s\"'<>]+", re.IGNORECASE)
OPENALEX_ID_RE = re.compile(r"(?:openalex\.org/)?(W\d{4,})", re.IGNORECASE)
ARXIV_OLD_RE = re.compile(r"^[a-z\-]+(?:\.[A-Z]{2})?/\d{7}(v\d+)?$")

STATS = {"rate_limits": 0}

CONTRADICTION_MARKERS = (
    ("contradict", "explicit contradiction"),
    ("counterexample", "counterexample"),
    ("counter-example", "counterexample"),
    ("fails to replicate", "replication failure"),
    ("failed to replicate", "replication failure"),
    ("failure to replicate", "replication failure"),
    ("failed replication", "replication failure"),
    ("could not replicate", "replication failure"),
    ("cannot replicate", "replication failure"),
    ("replication crisis", "replication failure"),
    ("not reproducible", "reproducibility problem"),
    ("cannot reproduce", "reproducibility problem"),
    ("could not reproduce", "reproducibility problem"),
    ("no evidence", "evidence challenged"),
    ("we find no", "null result"),
    ("challenge", "direct challenge"),
    ("rebut", "rebuttal"),
    ("reanalysis", "re-analysis"),
    ("re-analysis", "re-analysis"),
    ("disagree", "disagreement"),
    ("correction", "correction"),
    ("erratum", "correction"),
    ("retract", "retraction"),
    ("overestimat", "overestimation claim"),
    ("flawed", "methodological criticism"),
    ("critique", "critique"),
    ("comment on", "comment"),
    ("reconsider", "reconsideration"),
)


class LitError(Exception):
    """Base class for literature lookup problems."""


class NetworkError(LitError):
    """The host could not be reached."""


class ApiError(LitError):
    """The host answered with an error status."""


class RateLimitError(LitError):
    """A source kept returning HTTP 429 after the retries were exhausted."""


# --------------------------------------------------------------------------- #
# HTTP with rate-limit handling
# --------------------------------------------------------------------------- #

def _retry_delay(error, attempt):
    header = None
    try:
        header = error.headers.get("Retry-After")
    except Exception:
        header = None
    if header:
        try:
            return max(0.0, min(float(header), 60.0))
        except ValueError:
            pass  # HTTP-date form; fall back to exponential backoff
    return min(float(2 ** attempt), 30.0)


def http_get_json(url, timeout, headers=None, retries=MAX_RETRIES):
    """GET a URL and parse JSON, retrying politely on HTTP 429 / 503."""
    merged = {"User-Agent": USER_AGENT, "Accept": "application/json"}
    if headers:
        merged.update(headers)
    attempt = 0
    while True:
        request = urllib.request.Request(url, headers=merged)
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                return json.loads(response.read().decode("utf-8", "replace"))
        except urllib.error.HTTPError as exc:
            if exc.code in (429, 503) and attempt < retries:
                wait = _retry_delay(exc, attempt)
                STATS["rate_limits"] += 1
                sys.stderr.write(
                    "rate limited (HTTP %s) by %s; waiting %.1fs, retry %d/%d\n"
                    % (exc.code, urllib.parse.urlsplit(url).netloc, wait, attempt + 1, retries)
                )
                time.sleep(wait)
                attempt += 1
                continue
            if exc.code == 429:
                STATS["rate_limits"] += 1
                raise RateLimitError(
                    "HTTP 429 from %s after %d retries; try again later or use a "
                    "different source" % (urllib.parse.urlsplit(url).netloc, retries)
                )
            if exc.code == 403 and "semanticscholar" in url:
                raise ApiError(
                    "Semantic Scholar returned HTTP 403; set S2_API_KEY for a higher quota"
                )
            if exc.code == 404:
                raise ApiError("HTTP 404 (not found) for %s" % url)
            raise ApiError("HTTP %s for %s" % (exc.code, url))
        except socket.timeout:
            raise NetworkError("timed out after %ss contacting %s" % (timeout, url))
        except urllib.error.URLError as exc:
            raise NetworkError("cannot reach %s (%s)" % (url, exc.reason))
        except OSError as exc:
            raise NetworkError("cannot reach %s (%s)" % (url, exc))
        except ValueError as exc:
            raise ApiError("non-JSON response from %s (%s)" % (url, exc))


# --------------------------------------------------------------------------- #
# small helpers
# --------------------------------------------------------------------------- #

def collapse(text):
    return " ".join(text.split()) if text else None


def _strip_doi(doi):
    if not doi:
        return None
    return re.sub(r"^https?://(?:dx\.)?doi\.org/", "", doi, flags=re.IGNORECASE)


def _openalex_abstract(work):
    inverted = work.get("abstract_inverted_index")
    if not inverted:
        return None
    positioned = []
    for word, positions in inverted.items():
        for position in positions:
            positioned.append((position, word))
    positioned.sort()
    return collapse(" ".join(word for _, word in positioned))


def _work_summary(work, with_abstracts=False):
    summary = {
        "title": collapse(work.get("title") or work.get("display_name")),
        "year": work.get("publication_year"),
        "doi": _strip_doi(work.get("doi")),
        "openalex": work.get("id"),
        "cited_by": work.get("cited_by_count"),
        "authors": [
            (authorship.get("author") or {}).get("display_name")
            for authorship in (work.get("authorships") or [])
        ][:6],
    }
    if with_abstracts:
        summary["abstract"] = _openalex_abstract(work)
    return summary


def classify_seed(raw):
    """Return (kind, identifier) with kind in openalex|arxiv|doi|title."""
    value = raw.strip()
    if not value:
        raise LitError("empty seed")

    match = OPENALEX_ID_RE.search(value)
    if match:
        return "openalex", match.group(1)

    lowered = value.lower()
    match = re.search(r"arxiv[:\s/]*(\d{4}\.\d{4,5})(v\d+)?", lowered)
    if match:
        return "arxiv", match.group(1) + (match.group(2) or "")
    if re.match(r"^\d{4}\.\d{4,5}(v\d+)?$", lowered):
        return "arxiv", value
    if ARXIV_OLD_RE.match(value):
        return "arxiv", value

    match = DOI_RE.search(value)
    if match:
        return "doi", match.group(0).rstrip(").,;")

    return "title", value


# --------------------------------------------------------------------------- #
# source calls
# --------------------------------------------------------------------------- #

def _with_mailto(url, email):
    if not email:
        return url
    joiner = "&" if "?" in url else "?"
    return url + joiner + urllib.parse.urlencode({"mailto": email})


def openalex_work_by_identifier(identifier, timeout, email):
    url = _with_mailto("https://api.openalex.org/works/" + identifier, email)
    data = http_get_json(url, timeout)
    if not isinstance(data, dict) or "id" not in data:
        raise ApiError("OpenAlex returned no work for %s" % identifier)
    return data


def openalex_work_by_doi(doi, timeout, email):
    return openalex_work_by_identifier("https://doi.org/" + doi, timeout, email)


def openalex_title_search(title, timeout, email):
    params = {"filter": "title.search:" + title, "per-page": "1"}
    if email:
        params["mailto"] = email
    url = "https://api.openalex.org/works?" + urllib.parse.urlencode(params)
    results = (http_get_json(url, timeout) or {}).get("results") or []
    if not results:
        raise ApiError("OpenAlex title search found nothing for %r" % title)
    return results[0]


def arxiv_lookup(arxiv_id, timeout):
    params = {"id_list": arxiv_id, "max_results": "1"}
    url = "https://export.arxiv.org/api/query?" + urllib.parse.urlencode(params)
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read()
    except urllib.error.HTTPError as exc:
        raise ApiError("arXiv returned HTTP %s" % exc.code)
    except (urllib.error.URLError, OSError) as exc:
        raise NetworkError("cannot reach export.arxiv.org (%s)" % exc)
    try:
        root = ET.fromstring(raw)
    except ET.ParseError as exc:
        raise ApiError("arXiv returned unparseable XML (%s)" % exc)
    entry = root.find("a:entry", ATOM_NS)
    if entry is None:
        raise ApiError("arXiv has no record for %s" % arxiv_id)
    title_node = entry.find("a:title", ATOM_NS)
    doi_node = entry.find("arxiv:doi", ATOM_NS)
    return {
        "title": collapse(title_node.text) if title_node is not None else None,
        "doi": _strip_doi(doi_node.text) if doi_node is not None and doi_node.text else None,
    }


def openalex_predecessors(work, limit, timeout, email, with_abstracts):
    references = work.get("referenced_works") or []
    if not references:
        raise ApiError("the seed lists no referenced works in OpenAlex")
    ids = [reference.rsplit("/", 1)[-1] for reference in references][:limit]
    params = {"filter": "ids.openalex:" + "|".join(ids), "per-page": str(len(ids))}
    if email:
        params["mailto"] = email
    url = "https://api.openalex.org/works?" + urllib.parse.urlencode(params)
    results = (http_get_json(url, timeout) or {}).get("results") or []
    items = [_work_summary(work_item, with_abstracts) for work_item in results]
    order = {identifier: position for position, identifier in enumerate(ids)}
    items.sort(key=lambda item: order.get((item.get("openalex") or "").rsplit("/", 1)[-1], 10 ** 9))
    return items


def crossref_references(doi, limit, timeout):
    url = "https://api.crossref.org/works/" + urllib.parse.quote(doi, safe="/")
    message = (http_get_json(url, timeout) or {}).get("message") or {}
    references = message.get("reference") or []
    if not references:
        raise ApiError("Crossref lists no references for %s" % doi)
    items = []
    for reference in references[:limit]:
        items.append({
            "title": collapse(reference.get("article-title") or reference.get("unstructured")),
            "year": reference.get("year"),
            "doi": _strip_doi(reference.get("DOI")),
            "openalex": None,
            "cited_by": None,
            "authors": [reference.get("author")] if reference.get("author") else [],
            "origin": "crossref-reference",
        })
    return items


def openalex_citing(openalex_id, limit, timeout, email, with_abstracts):
    work_id = openalex_id.rsplit("/", 1)[-1]
    params = {
        "filter": "cites:" + work_id,
        "per-page": str(min(limit, 200)),
        "sort": "publication_date:desc",
    }
    if email:
        params["mailto"] = email
    url = "https://api.openalex.org/works?" + urllib.parse.urlencode(params)
    results = (http_get_json(url, timeout) or {}).get("results") or []
    return [_work_summary(work_item, with_abstracts) for work_item in results]


def _s2_summary(paper, with_abstracts):
    external = paper.get("externalIds") or {}
    summary = {
        "title": collapse(paper.get("title")),
        "year": paper.get("year"),
        "doi": external.get("DOI"),
        "openalex": None,
        "cited_by": paper.get("citationCount"),
        "authors": [author.get("name") for author in (paper.get("authors") or [])][:6],
        "origin": "semantic-scholar",
    }
    if with_abstracts:
        summary["abstract"] = collapse(paper.get("abstract"))
    return summary


def semantic_scholar(seed_id, direction, limit, timeout, api_key, with_abstracts):
    fields = "title,year,authors,externalIds,citationCount"
    if with_abstracts:
        fields += ",abstract"
    url = "https://api.semanticscholar.org/graph/v1/paper/%s/%s?%s" % (
        urllib.parse.quote(seed_id, safe=":"),
        direction,
        urllib.parse.urlencode({"fields": fields, "limit": min(limit, 500)}),
    )
    headers = {"x-api-key": api_key} if api_key else None
    data = http_get_json(url, timeout, headers=headers)
    items = []
    for row in data.get("data", []):
        paper = row.get("citedPaper") or row.get("citingPaper") or {}
        items.append(_s2_summary(paper, with_abstracts))
    return items


# --------------------------------------------------------------------------- #
# contradiction heuristic
# --------------------------------------------------------------------------- #

def find_contradictions(items, with_abstracts):
    found = []
    seen = set()
    for item in items:
        haystack = " ".join(
            filter(None, [item.get("title") or "", item.get("abstract") or ""])
        ).lower()
        if not haystack:
            continue
        reasons = sorted({label for marker, label in CONTRADICTION_MARKERS if marker in haystack})
        if not reasons:
            continue
        key = (item.get("doi") or item.get("title") or "").lower()
        if key in seen:
            continue
        seen.add(key)
        entry = dict(item)
        entry["reasons"] = reasons
        found.append(entry)
    return found


# --------------------------------------------------------------------------- #
# orchestration
# --------------------------------------------------------------------------- #

def collect(args):
    state = {"notes": [], "sources": [], "network_failures": 0}

    def call(label, function):
        try:
            value = function()
        except NetworkError as exc:
            state["network_failures"] += 1
            state["notes"].append("%s: %s" % (label, exc))
            return None
        except LitError as exc:
            state["notes"].append("%s: %s" % (label, exc))
            return None
        if value is not None:
            state["sources"].append(label)
        return value

    kind, identifier = classify_seed(args.seed)
    seed_work = None
    match = None
    arxiv_id = None
    doi = None

    if kind == "openalex":
        seed_work = call("OpenAlex", lambda: openalex_work_by_identifier(identifier, args.timeout, args.email))
        match = "identifier"
    elif kind == "doi":
        doi = identifier
        seed_work = call("OpenAlex", lambda: openalex_work_by_doi(identifier, args.timeout, args.email))
        match = "identifier"
    elif kind == "arxiv":
        arxiv_id = identifier
        arxiv_meta = call("arXiv", lambda: arxiv_lookup(identifier, args.timeout))
        if arxiv_meta and arxiv_meta.get("doi"):
            doi = arxiv_meta["doi"]
            seed_work = call("OpenAlex", lambda: openalex_work_by_doi(doi, args.timeout, args.email))
            match = "identifier"
        elif arxiv_meta and arxiv_meta.get("title"):
            seed_work = call("OpenAlex", lambda: openalex_title_search(arxiv_meta["title"], args.timeout, args.email))
            match = "title-search"
    else:
        seed_work = call("OpenAlex", lambda: openalex_title_search(identifier, args.timeout, args.email))
        match = "title-search"

    if seed_work is None:
        if state["network_failures"]:
            raise NetworkError(
                "network unavailable; the seed could not be resolved. Notes: %s"
                % ("; ".join(state["notes"]) or "none")
            )
        raise LitError(
            "could not resolve the seed %r. Notes: %s"
            % (args.seed, "; ".join(state["notes"]) or "none")
        )

    seed = _work_summary(seed_work, args.with_abstracts)
    if seed.get("doi"):
        doi = doi or seed["doi"]
    if match == "title-search":
        state["notes"].append("OpenAlex: seed matched by title search, not by identifier - verify it is the right paper")

    predecessors = []
    citing = []
    directions = args.direction
    want_pred = directions in ("predecessors", "both", "contradictions")
    want_citing = directions in ("citing", "both", "contradictions")

    if want_pred:
        if seed_work.get("referenced_works"):
            predecessors = call(
                "OpenAlex predecessors",
                lambda: openalex_predecessors(seed_work, args.limit, args.timeout, args.email, args.with_abstracts),
            ) or []
        if doi:
            extra = call(
                "Crossref references",
                lambda: crossref_references(doi, args.limit, args.timeout),
            ) or []
            present = {(item.get("doi") or (item.get("title") or "").lower()) for item in predecessors}
            for item in extra:
                key = item.get("doi") or (item.get("title") or "").lower()
                if key and key not in present:
                    predecessors.append(item)
                    present.add(key)

    if want_citing:
        citing = call(
            "OpenAlex citing",
            lambda: openalex_citing(seed_work["id"], args.limit, args.timeout, args.email, args.with_abstracts),
        ) or []

    api_key = os.environ.get("S2_API_KEY")
    use_s2 = args.semantic_scholar or bool(api_key)
    if use_s2:
        s2_seed = None
        if doi:
            s2_seed = "DOI:" + doi
        elif arxiv_id:
            s2_seed = "ARXIV:" + arxiv_id
        if s2_seed is None:
            state["notes"].append("Semantic Scholar: no DOI or arXiv id for the seed; skipped")
        else:
            if want_pred:
                extra = call(
                    "Semantic Scholar references",
                    lambda: semantic_scholar(s2_seed, "references", args.limit, args.timeout, api_key, args.with_abstracts),
                ) or []
                predecessors.extend(item for item in extra if (item.get("title") or item.get("doi")))
            if want_citing:
                extra = call(
                    "Semantic Scholar citations",
                    lambda: semantic_scholar(s2_seed, "citations", args.limit, args.timeout, api_key, args.with_abstracts),
                ) or []
                citing.extend(item for item in extra if (item.get("title") or item.get("doi")))

    predecessors = predecessors[:args.limit]
    citing = citing[:args.limit]

    pool = []
    if want_pred:
        pool.extend(predecessors)
    if want_citing:
        pool.extend(citing)
    contradictions = find_contradictions(pool, args.with_abstracts) if directions != "predecessors" else []

    return {
        "generated_at": utc_now_iso(),
        "seed": seed,
        "seed_input": args.seed,
        "seed_match": match,
        "direction": directions,
        "predecessors": predecessors if want_pred else [],
        "citing": citing if want_citing else [],
        "contradictions": contradictions,
        "sources_used": state["sources"],
        "rate_limit_events": STATS["rate_limits"],
        "notes": state["notes"],
    }


# --------------------------------------------------------------------------- #
# rendering + entry point
# --------------------------------------------------------------------------- #

def utc_now_iso():
    """Current UTC instant as an ISO-8601 string, e.g. "2026-09-16T12:34:56Z".

    Stamped into the payload as `generated_at` so the UI can show how stale a
    recorded neighbourhood is (the file is only rewritten on an explicit run).
    """
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def write_atomic(path, text):
    """Write `text` to `path` atomically.

    The payload is staged in a sibling temp file (same directory, so the final
    rename is a same-filesystem, atomic os.replace), flushed and fsynced, then
    renamed over the destination. A reader therefore never observes a
    half-written literature.json, and a failed write leaves any previous file
    intact. The temp file is removed on failure.
    """
    directory = os.path.dirname(os.path.abspath(path)) or "."
    handle_fd, tmp_path = tempfile.mkstemp(prefix=".literature-", suffix=".tmp", dir=directory)
    try:
        with os.fdopen(handle_fd, "w", encoding="utf-8") as handle:
            handle.write(text)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp_path, path)
    except BaseException:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass
        raise


def render_text(record):
    lines = []
    seed = record["seed"]
    lines.append("Seed: %s (%s) [%s]" % (seed.get("title"), seed.get("year"), record["seed_match"]))
    if seed.get("doi"):
        lines.append("  DOI: %s" % seed["doi"])
    if seed.get("openalex"):
        lines.append("  OpenAlex: %s" % seed["openalex"])

    def section(name, items):
        lines.append("")
        lines.append("%s (%d):" % (name, len(items)))
        if not items:
            lines.append("  (none)")
        for item in items:
            suffix = ""
            if item.get("doi"):
                suffix = " doi:%s" % item["doi"]
            reasons = item.get("reasons")
            if reasons:
                suffix += " -- reasons: %s" % ", ".join(reasons)
            lines.append("  - %s %s%s" % (item.get("year") or "----", item.get("title") or "(untitled)", suffix))

    section("Predecessors", record["predecessors"])
    section("Citing", record["citing"])
    section("Contradiction candidates", record["contradictions"])

    if record["notes"]:
        lines.append("")
        lines.append("Notes:")
        for note in record["notes"]:
            lines.append("  - %s" % note)
    if record["rate_limit_events"]:
        lines.append("")
        lines.append("Rate-limit retries: %d" % record["rate_limit_events"])
    return "\n".join(lines) + "\n"


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Find predecessors, citing works, and contradiction candidates for a paper (optional helper)."
    )
    parser.add_argument("seed", help="DOI, arXiv id, OpenAlex id, title, or URL")
    parser.add_argument(
        "--direction", default="both",
        choices=("predecessors", "citing", "both", "contradictions"),
        help="which sets to fetch (default both; 'contradictions' also fetches both)",
    )
    parser.add_argument("--limit", type=int, default=25, help="max items per set (default 25)")
    parser.add_argument(
        "--email",
        default=os.environ.get("OPENALEX_MAILTO") or os.environ.get("UNPAYWALL_EMAIL"),
        help="contact e-mail for OpenAlex's polite pool (env OPENALEX_MAILTO)",
    )
    parser.add_argument("--semantic-scholar", action="store_true",
                        help="also query Semantic Scholar (keyless is rate-limited; set S2_API_KEY)")
    parser.add_argument("--with-abstracts", action="store_true",
                        help="fetch abstracts too (improves the contradiction heuristic)")
    parser.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT,
                        help="per-request timeout in seconds (default %d)" % DEFAULT_TIMEOUT)
    parser.add_argument("--format", default="json", choices=("json", "text"),
                        help="output format (default json)")
    parser.add_argument("--out", help="write the result to this path instead of stdout")
    parser.add_argument("--compact", action="store_true", help="emit compact JSON")
    args = parser.parse_args(argv)

    try:
        record = collect(args)
    except NetworkError as exc:
        sys.stderr.write("network unavailable: %s\n" % exc)
        sys.stderr.write(
            "This helper must reach api.openalex.org / api.crossref.org / "
            "export.arxiv.org. Retry when the network is back, or gather the "
            "literature context without the helper.\n"
        )
        return 2
    except RateLimitError as exc:
        sys.stderr.write("rate limited: %s\n" % exc)
        return 3
    except LitError as exc:
        sys.stderr.write("could not resolve the seed: %s\n" % exc)
        return 1

    if args.format == "text":
        output = render_text(record)
    else:
        output = json.dumps(record, indent=None if args.compact else 2, ensure_ascii=False) + "\n"

    if args.out:
        try:
            write_atomic(args.out, output)
        except OSError as exc:
            sys.stderr.write("could not write %s: %s\n" % (args.out, exc))
            return 3
        sys.stderr.write("wrote %s\n" % args.out)
    else:
        sys.stdout.write(output)

    for note in record["notes"]:
        sys.stderr.write("note: %s\n" % note)
    return 0


if __name__ == "__main__":
    sys.exit(main())
