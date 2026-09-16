#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""fetch_paper.py - optional helper: resolve a paper reference to metadata and an open-access PDF link.

This is a convenience helper. The study-paper workflow runs without it.

WHAT IT DOES
    Takes a DOI, an arXiv id (or arXiv URL), or a publisher/landing-page URL and
    prints one JSON record containing:
      - canonical identifiers (DOI, arXiv id, OpenAlex id)
      - bibliographic metadata (title, authors, year, venue, abstract)
      - the best open-access PDF link found, plus every candidate link

SOURCES (all keyless; queried politely with a contact address when known)
    - arXiv Atom API   https://export.arxiv.org/api/query
    - Crossref         https://api.crossref.org/works/<doi>
    - OpenAlex         https://api.openalex.org/works/<id>
    - Unpaywall        https://api.unpaywall.org/v2/<doi>   (needs a real e-mail)

DEPENDENCIES
    - Python 3.8+ standard library only. No third-party packages are used.
    - Network access is required. Without it the script stops with a clear
      message and exit code 2.
    - Unpaywall is optional and refuses placeholder addresses, so it needs a real
      contact e-mail. Supply one with --email or the UNPAYWALL_EMAIL /
      OPENALEX_MAILTO environment variable. With no e-mail the Unpaywall pass is
      skipped and everything else still works.

EXIT CODES
    0  resolved successfully
    1  input unrecognised, or it could not be resolved
    2  network unavailable
    3  unexpected problem

USAGE
    python3 fetch_paper.py 10.1038/nature12373
    python3 fetch_paper.py arXiv:1706.03762 --email me@example.org
    python3 fetch_paper.py https://arxiv.org/abs/1706.03762 --out record.json
    python3 fetch_paper.py https://www.nature.com/articles/nature12373
"""

import argparse
import html
import json
import os
import re
import socket
import sys
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET

USER_AGENT = "study-paper-fetch/1.0"
DEFAULT_TIMEOUT = 20

ATOM_NS = {"a": "http://www.w3.org/2005/Atom", "arxiv": "http://arxiv.org/schemas/atom"}
DOI_RE = re.compile(r"10\.\d{4,9}/[^\s\"'<>]+", re.IGNORECASE)
ARXIV_OLD_RE = re.compile(r"^[a-z\-]+(?:\.[A-Z]{2})?/\d{7}(v\d+)?$")
META_TAG_RE = re.compile(r"<meta\b[^>]*>", re.IGNORECASE)
LINK_TAG_RE = re.compile(r"<link\b[^>]*>", re.IGNORECASE)
ATTR_RE = re.compile(r"([a-zA-Z:_-]+)\s*=\s*(\"[^\"]*\"|'[^']*')")


class ResolveError(Exception):
    """The input could not be turned into a known identifier or resolved."""


class NetError(Exception):
    """A request failed. kind is 'network' (unreachable) or 'http' (bad status)."""

    def __init__(self, message, kind="network"):
        super().__init__(message)
        self.kind = kind


# --------------------------------------------------------------------------- #
# low-level HTTP
# --------------------------------------------------------------------------- #

def http_get(url, timeout=DEFAULT_TIMEOUT, accept="application/json"):
    """Return the response body, or raise NetError with a classified kind."""
    request = urllib.request.Request(
        url,
        headers={"User-Agent": USER_AGENT, "Accept": accept},
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.read()
    except urllib.error.HTTPError as exc:
        raise NetError("HTTP %s for %s" % (exc.code, url), kind="http")
    except socket.timeout:
        raise NetError("timed out after %ss contacting %s" % (timeout, url), kind="network")
    except urllib.error.URLError as exc:
        raise NetError("cannot reach %s (%s)" % (url, exc.reason), kind="network")
    except OSError as exc:
        raise NetError("cannot reach %s (%s)" % (url, exc), kind="network")


def get_json(url, timeout):
    raw = http_get(url, timeout=timeout, accept="application/json")
    try:
        return json.loads(raw.decode("utf-8", "replace"))
    except ValueError as exc:
        raise NetError("server returned non-JSON for %s (%s)" % (url, exc), kind="http")


# --------------------------------------------------------------------------- #
# small text helpers
# --------------------------------------------------------------------------- #

def collapse(text):
    return " ".join(text.split()) if text else None


def strip_markup(text):
    if not text:
        return None
    return collapse(re.sub(r"<[^>]+>", " ", html.unescape(text)))


def _first_year(text):
    if not text:
        return None
    match = re.search(r"(?:19|20)\d{2}", text)
    return int(match.group(0)) if match else None


def _strip_doi(doi):
    if not doi:
        return None
    return re.sub(r"^https?://(?:dx\.)?doi\.org/", "", doi, flags=re.IGNORECASE)


def _attrs(tag):
    result = {}
    for name, raw_value in ATTR_RE.findall(tag):
        result[name.lower()] = raw_value[1:-1]
    return result


# --------------------------------------------------------------------------- #
# input classification
# --------------------------------------------------------------------------- #

def classify(raw):
    """Return (kind, identifier) where kind is 'doi', 'arxiv', or 'url'."""
    value = raw.strip()
    if not value:
        raise ResolveError("empty input")
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

    if lowered.startswith("http://") or lowered.startswith("https://"):
        return "url", value

    raise ResolveError(
        "unrecognised input: %r (expected a DOI, an arXiv id, or a URL)" % raw
    )


# --------------------------------------------------------------------------- #
# source lookups -- each returns a partial record dict
# --------------------------------------------------------------------------- #

def _atom_text(element, path):
    found = element.find(path, ATOM_NS)
    return found.text if (found is not None and found.text) else None


def lookup_arxiv(arxiv_id, timeout):
    query = urllib.parse.urlencode({"id_list": arxiv_id, "max_results": "1"})
    raw = http_get(
        "https://export.arxiv.org/api/query?" + query,
        timeout=timeout,
        accept="application/atom+xml",
    )
    try:
        root = ET.fromstring(raw)
    except ET.ParseError as exc:
        raise NetError("arXiv returned unparseable XML (%s)" % exc, kind="http")

    entry = root.find("a:entry", ATOM_NS)
    if entry is None:
        raise ResolveError("arXiv has no record for %s" % arxiv_id)

    record = {"arxiv": arxiv_id}
    record["title"] = collapse(_atom_text(entry, "a:title"))
    record["abstract"] = collapse(_atom_text(entry, "a:summary"))
    record["published"] = collapse(_atom_text(entry, "a:published"))
    record["authors"] = [
        collapse(_atom_text(author, "a:name"))
        for author in entry.findall("a:author", ATOM_NS)
    ]
    record["authors"] = [name for name in record["authors"] if name]

    pdf = None
    landing = None
    for link in entry.findall("a:link", ATOM_NS):
        if link.get("type") == "application/pdf" or link.get("title") == "pdf":
            pdf = link.get("href")
        elif link.get("rel") == "alternate":
            landing = link.get("href")
    record["pdf"] = pdf or ("https://arxiv.org/pdf/%s" % arxiv_id)
    record["url"] = landing or ("https://arxiv.org/abs/%s" % arxiv_id)

    doi = _atom_text(entry, "arxiv:doi")
    if doi:
        record["doi"] = doi.strip()
    return record


def _crossref_year(message):
    for key in ("published-print", "published-online", "issued", "created"):
        parts = (message.get(key) or {}).get("date-parts")
        if parts and parts[0] and parts[0][0]:
            return parts[0][0]
    return None


def _crossref_author(author):
    given = (author.get("given") or "").strip()
    family = (author.get("family") or "").strip()
    name = (given + " " + family).strip()
    return name or (author.get("name") or "").strip() or None


def lookup_crossref(doi, timeout):
    url = "https://api.crossref.org/works/" + urllib.parse.quote(doi, safe="/")
    message = get_json(url, timeout).get("message")
    if not message:
        raise ResolveError("Crossref has no record for %s" % doi)

    record = {"doi": message.get("DOI") or doi}
    titles = message.get("title") or []
    record["title"] = collapse(titles[0]) if titles else None
    record["authors"] = [_crossref_author(a) for a in message.get("author", [])]
    record["authors"] = [name for name in record["authors"] if name]
    record["year"] = _crossref_year(message)
    containers = message.get("container-title") or []
    record["venue"] = collapse(containers[0]) if containers else None
    record["abstract"] = strip_markup(message.get("abstract"))
    record["url"] = message.get("URL")
    record["reference_count"] = message.get("reference-count")
    record["cited_by_count"] = message.get("is-referenced-by-count")
    return record


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


def _openalex_record(work, match="identifier"):
    record = {"openalex": work.get("id"), "match": match}
    record["doi"] = _strip_doi(work.get("doi"))
    record["title"] = collapse(work.get("title")) or collapse(work.get("display_name"))
    record["authors"] = [
        (authorship.get("author") or {}).get("display_name")
        for authorship in work.get("authorships", [])
    ]
    record["authors"] = [name for name in record["authors"] if name]
    record["year"] = work.get("publication_year")
    source = (work.get("primary_location") or {}).get("source") or {}
    record["venue"] = source.get("display_name")
    record["abstract"] = _openalex_abstract(work)
    record["reference_count"] = len(work.get("referenced_works") or [])
    record["cited_by_count"] = work.get("cited_by_count")
    open_access = work.get("open_access") or {}
    best = work.get("best_oa_location") or {}
    record["pdf"] = best.get("pdf_url") or open_access.get("oa_url")
    record["oa_status"] = open_access.get("oa_status")
    return record


def lookup_openalex(identifier, timeout, mailto=None):
    url = "https://api.openalex.org/works/" + identifier
    if mailto:
        url += "?" + urllib.parse.urlencode({"mailto": mailto})
    work = get_json(url, timeout)
    if not isinstance(work, dict) or "id" not in work:
        raise ResolveError("OpenAlex returned no work for %s" % identifier)
    return _openalex_record(work, "identifier")


def search_openalex(title, timeout, mailto=None):
    params = {"search": title, "per-page": "1"}
    if mailto:
        params["mailto"] = mailto
    url = "https://api.openalex.org/works?" + urllib.parse.urlencode(params)
    results = (get_json(url, timeout) or {}).get("results") or []
    if not results:
        raise ResolveError("OpenAlex title search found nothing for %r" % title)
    return _openalex_record(results[0], "title-search")


def lookup_unpaywall(doi, email, timeout):
    if not email:
        raise ResolveError(
            "no contact e-mail supplied, skipping Unpaywall (it rejects placeholder addresses)"
        )
    url = (
        "https://api.unpaywall.org/v2/"
        + urllib.parse.quote(doi, safe="/")
        + "?"
        + urllib.parse.urlencode({"email": email})
    )
    payload = get_json(url, timeout)
    if payload.get("error"):
        raise ResolveError("Unpaywall error: %s" % payload.get("message"))

    best = payload.get("best_oa_location") or {}
    record = {
        "doi": payload.get("doi"),
        "title": collapse(payload.get("title")),
        "year": payload.get("year"),
        "venue": payload.get("journal_name"),
        "oa_status": payload.get("oa_status"),
        "is_oa": payload.get("is_oa"),
        "pdf": best.get("url_for_pdf") or best.get("url"),
    }
    locations = []
    for location in payload.get("oa_locations", []):
        link = location.get("url_for_pdf") or location.get("url")
        if link:
            locations.append(link)
    record["oa_locations"] = locations
    return record


def _parse_meta(html):
    values = {}
    for tag in META_TAG_RE.findall(html):
        attrs = _attrs(tag)
        key = (attrs.get("name") or attrs.get("property") or attrs.get("http-equiv") or "").strip().lower()
        content = attrs.get("content")
        if key and content:
            values.setdefault(key, html.unescape(content).strip())
    return values


def _find_pdf_link(html):
    for tag in LINK_TAG_RE.findall(html):
        attrs = _attrs(tag)
        if (attrs.get("type") or "").lower() == "application/pdf" and attrs.get("href"):
            return attrs["href"]
    return None


def lookup_url(url, timeout):
    raw = http_get(
        url,
        timeout=timeout,
        accept="text/html,application/xhtml+xml,application/pdf;q=0.9,*/*;q=0.5",
    )
    if raw[:5] == b"%PDF-":
        return {"kind": "pdf", "url": url, "pdf": url}

    html = raw[:400000].decode("utf-8", "replace")
    meta = _parse_meta(html)
    record = {"kind": "url", "url": url}
    record["title"] = meta.get("citation_title") or meta.get("og:title") or meta.get("dc.title")
    record["authors"] = [
        value for key, value in meta.items() if key in ("citation_author", "dc.creator")
    ]
    record["year"] = _first_year(
        meta.get("citation_publication_date") or meta.get("citation_date") or meta.get("dc.date")
    )
    record["venue"] = meta.get("citation_journal_title") or meta.get("og:site_name")
    record["doi"] = _strip_doi(meta.get("citation_doi"))
    record["pdf"] = meta.get("citation_pdf_url") or _find_pdf_link(html)
    record["abstract"] = strip_markup(
        meta.get("citation_abstract") or meta.get("og:description") or meta.get("description")
    )
    return record


# --------------------------------------------------------------------------- #
# merge
# --------------------------------------------------------------------------- #

def _pick(*values):
    for value in values:
        if value not in (None, "", [], {}):
            return value
    return None


def _year_from_arxiv(arxiv):
    return _first_year((arxiv or {}).get("published"))


def resolve(raw, email, timeout):
    """Resolve the input and return the merged JSON record."""
    state = {"notes": [], "sources": [], "network_failures": 0, "network_error": None}

    def attempt(label, function):
        try:
            value = function()
        except NetError as exc:
            state["notes"].append("%s: %s" % (label, exc))
            if exc.kind == "network":
                state["network_failures"] += 1
                state["network_error"] = str(exc)
            return None
        except (ResolveError, ValueError, KeyError, TypeError) as exc:
            state["notes"].append("%s: %s" % (label, exc))
            return None
        if value is not None:
            state["sources"].append(label)
        return value

    kind, identifier = classify(raw)
    arxiv = crossref = openalex = unpaywall = page = None

    if kind == "arxiv":
        arxiv = attempt("arXiv", lambda: lookup_arxiv(identifier, timeout))
        doi = (arxiv or {}).get("doi")
        if doi:
            crossref = attempt("Crossref", lambda: lookup_crossref(doi, timeout))
            openalex = attempt("OpenAlex", lambda: lookup_openalex("https://doi.org/" + doi, timeout, email))
        if not openalex and (arxiv or {}).get("title"):
            openalex = attempt("OpenAlex", lambda: search_openalex(arxiv["title"], timeout, email))
    elif kind == "doi":
        crossref = attempt("Crossref", lambda: lookup_crossref(identifier, timeout))
        openalex = attempt("OpenAlex", lambda: lookup_openalex("https://doi.org/" + identifier, timeout, email))
    else:
        page = attempt("landing page", lambda: lookup_url(identifier, timeout))
        if page and page.get("kind") == "url":
            doi = page.get("doi")
            if doi:
                crossref = attempt("Crossref", lambda: lookup_crossref(doi, timeout))
                openalex = attempt("OpenAlex", lambda: lookup_openalex("https://doi.org/" + doi, timeout, email))
            elif page.get("title"):
                openalex = attempt("OpenAlex", lambda: search_openalex(page["title"], timeout, email))

    # An OpenAlex record resolved by identifier is trustworthy; one resolved by a
    # title search is a guess and must not silently override better metadata.
    oa_confident = openalex if (openalex or {}).get("match") == "identifier" else None
    oa_search = openalex if (openalex or {}).get("match") == "title-search" else None
    if oa_search:
        state["notes"].append(
            "OpenAlex: record matched by title search, not by identifier (%s) - "
            "verify it is the same paper" % (oa_search.get("openalex") or "unknown id")
        )

    def value(record, key):
        return (record or {}).get(key)

    doi = _pick(
        value(crossref, "doi"),
        value(oa_confident, "doi"),
        value(arxiv, "doi"),
        identifier if kind == "doi" else None,
    )
    if doi and email:
        unpaywall = attempt("Unpaywall", lambda: lookup_unpaywall(doi, email, timeout))
    elif doi:
        state["notes"].append(
            "Unpaywall: skipped (no --email / UNPAYWALL_EMAIL / OPENALEX_MAILTO supplied)"
        )

    if kind == "arxiv":
        title_order = (value(arxiv, "title"), value(crossref, "title"), value(oa_confident, "title"), value(oa_search, "title"))
        author_order = (value(arxiv, "authors"), value(crossref, "authors"), value(oa_confident, "authors"), value(oa_search, "authors"))
        year_order = (_year_from_arxiv(arxiv), value(crossref, "year"), value(oa_confident, "year"), value(oa_search, "year"))
    else:
        title_order = (value(crossref, "title"), value(oa_confident, "title"), value(page, "title"), value(oa_search, "title"), value(arxiv, "title"))
        author_order = (value(crossref, "authors"), value(oa_confident, "authors"), value(page, "authors"), value(oa_search, "authors"), value(arxiv, "authors"))
        year_order = (value(crossref, "year"), value(oa_confident, "year"), value(page, "year"), value(oa_search, "year"), _year_from_arxiv(arxiv))

    metadata = {
        "title": _pick(*title_order),
        "authors": _pick(*author_order),
        "year": _pick(*year_order),
        "venue": _pick(value(crossref, "venue"), value(oa_confident, "venue"), value(page, "venue"), value(oa_search, "venue")),
        "abstract": _pick(value(arxiv, "abstract"), value(crossref, "abstract"), value(oa_confident, "abstract"), value(oa_search, "abstract"), value(page, "abstract")),
        "landing_url": _pick(value(page, "url"), value(arxiv, "url"), value(crossref, "url")),
    }

    candidates = []
    for link in [
        (page or {}).get("pdf") if page else None,
        (arxiv or {}).get("pdf"),
        (openalex or {}).get("pdf"),
        (unpaywall or {}).get("pdf"),
    ] + ((unpaywall or {}).get("oa_locations") or []):
        if link and link not in candidates:
            candidates.append(link)

    identifiers = {
        "doi": doi,
        "arxiv": _pick((arxiv or {}).get("arxiv"), identifier if kind == "arxiv" else None),
        "openalex": (openalex or {}).get("openalex"),
        "openalex_match": (openalex or {}).get("match"),
        "crossref_api": ("https://api.crossref.org/works/" + doi) if doi else None,
        "unpaywall_api": ("https://api.unpaywall.org/v2/" + doi) if (doi and email) else None,
    }

    open_access = {
        "best_pdf": candidates[0] if candidates else None,
        "candidates": candidates,
        "oa_status": _pick((openalex or {}).get("oa_status"), (unpaywall or {}).get("oa_status")),
        "is_oa": (unpaywall or {}).get("is_oa"),
    }

    counts = {
        "referenced_works": _pick(value(oa_confident, "reference_count"), value(openalex, "reference_count"), value(crossref, "reference_count")),
        "cited_by": _pick(value(oa_confident, "cited_by_count"), value(openalex, "cited_by_count"), value(crossref, "cited_by_count")),
    }

    if not state["sources"]:
        if state["network_failures"]:
            raise NetError(
                "network unavailable; no source could be reached. Last error: %s"
                % state["network_error"],
                kind="network",
            )
        raise ResolveError(
            "could not resolve %r from any source. Notes: %s"
            % (raw, "; ".join(state["notes"]) or "none")
        )

    return {
        "input": raw,
        "input_kind": kind,
        "identifiers": identifiers,
        "metadata": metadata,
        "open_access": open_access,
        "counts": counts,
        "sources_used": state["sources"],
        "notes": state["notes"],
    }


# --------------------------------------------------------------------------- #
# entry point
# --------------------------------------------------------------------------- #

def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Resolve a paper reference to metadata and an open-access PDF link (optional helper)."
    )
    parser.add_argument("reference", help="DOI, arXiv id, arXiv URL, or landing-page URL")
    parser.add_argument(
        "--email",
        default=os.environ.get("UNPAYWALL_EMAIL") or os.environ.get("OPENALEX_MAILTO"),
        help="contact e-mail for the polite pools and for Unpaywall "
             "(env UNPAYWALL_EMAIL / OPENALEX_MAILTO)",
    )
    parser.add_argument(
        "--timeout", type=int, default=DEFAULT_TIMEOUT,
        help="per-request timeout in seconds (default %d)" % DEFAULT_TIMEOUT,
    )
    parser.add_argument("--out", help="write the JSON record to this path instead of stdout")
    parser.add_argument("--compact", action="store_true", help="emit compact JSON instead of indented JSON")
    args = parser.parse_args(argv)

    try:
        record = resolve(args.reference, args.email, args.timeout)
    except NetError as exc:
        if exc.kind == "network":
            sys.stderr.write("network unavailable: %s\n" % exc)
            sys.stderr.write(
                "This helper must reach api.openalex.org / api.crossref.org / "
                "export.arxiv.org. Resolve the paper without the helper, or retry "
                "when the network is back.\n"
            )
            return 2
        sys.stderr.write("API error: %s\n" % exc)
        return 1
    except ResolveError as exc:
        sys.stderr.write("could not resolve: %s\n" % exc)
        return 1

    text = json.dumps(record, indent=None if args.compact else 2, ensure_ascii=False)
    if args.out:
        try:
            with open(args.out, "w", encoding="utf-8") as handle:
                handle.write(text + "\n")
        except OSError as exc:
            sys.stderr.write("could not write %s: %s\n" % (args.out, exc))
            return 3
        sys.stderr.write("wrote %s\n" % args.out)
    else:
        sys.stdout.write(text + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
