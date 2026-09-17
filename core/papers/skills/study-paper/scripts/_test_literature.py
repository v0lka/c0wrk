#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Unit tests for the study-paper literature helper (literature.py).

Focus: the WRITE LOGIC must be atomic — a reader must never observe a
partially-written literature.json — the payload must carry a parseable
`generated_at` timestamp so the UI can flag a stale neighbourhood, and the
seed-resolution order and HTTP retry policy must hold: an arXiv seed resolves
through OpenAlex via the deterministic DataCite DOI first (export.arxiv.org
intermittently throttles with HTTP 406), the arXiv API stays as the fallback,
and 406 from export.arxiv.org gets the same polite backoff as 429/503.

The file name is underscore-prefixed on purpose: `//go:embed skills` (see
core/papers/skillpack.go) excludes dotfiles and underscore-prefixed files, so
this test is NOT shipped inside the paper-study skill pack. Run it directly:

    python3 core/papers/skills/study-paper/scripts/_test_literature.py

Standard library only (matches the helper's own constraint).
"""

import argparse
import importlib.util
import json
import os
import pathlib
import re
import sys
import tempfile
import unittest
import urllib.error
import urllib.parse
from unittest import mock

_SCRIPT = pathlib.Path(__file__).with_name("literature.py")
_spec = importlib.util.spec_from_file_location("literature_under_test", _SCRIPT)
assert _spec is not None and _spec.loader is not None
literature = importlib.util.module_from_spec(_spec)
# Register before exec so the module can be pickled/reloaded if needed.
sys.modules[_spec.name] = literature
_spec.loader.exec_module(literature)

ISO_UTC_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")


class WriteAtomicTests(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        self.target = os.path.join(self.dir.name, "literature.json")

    def _siblings(self):
        return sorted(os.listdir(self.dir.name))

    def test_writes_the_exact_content(self):
        literature.write_atomic(self.target, '{"seed":{"title":"S"}}\n')
        with open(self.target, encoding="utf-8") as handle:
            self.assertEqual(handle.read(), '{"seed":{"title":"S"}}\n')

    def test_replaces_an_existing_file(self):
        literature.write_atomic(self.target, "OLD")
        literature.write_atomic(self.target, "NEW")
        with open(self.target, encoding="utf-8") as handle:
            self.assertEqual(handle.read(), "NEW")

    def test_leaves_no_temp_siblings_behind(self):
        literature.write_atomic(self.target, "payload")
        # Only the destination survives — no staged .literature-*.tmp file.
        self.assertEqual(self._siblings(), ["literature.json"])

    def test_failure_leaves_the_previous_file_intact(self):
        literature.write_atomic(self.target, "GOOD")
        # A non-string body raises inside handle.write; the atomic writer must
        # clean up its temp file and NOT clobber the committed destination.
        with self.assertRaises(TypeError):
            literature.write_atomic(self.target, 12345)
        with open(self.target, encoding="utf-8") as handle:
            self.assertEqual(handle.read(), "GOOD")
        self.assertEqual(self._siblings(), ["literature.json"])

    def test_failure_on_a_fresh_target_writes_nothing(self):
        # target's parent does not exist -> mkstemp fails, nothing is created.
        missing = os.path.join(self.dir.name, "nope", "literature.json")
        with self.assertRaises(OSError):
            literature.write_atomic(missing, "payload")
        self.assertFalse(os.path.exists(missing))

    def test_accepts_a_unicode_payload(self):
        literature.write_atomic(self.target, "café — 漢字\n")
        with open(self.target, encoding="utf-8") as handle:
            self.assertEqual(handle.read(), "café — 漢字\n")


class TimestampTests(unittest.TestCase):
    def test_utc_now_iso_is_iso8601_z(self):
        self.assertRegex(literature.utc_now_iso(), ISO_UTC_RE)


# --------------------------------------------------------------------------- #
# offline HTTP transport for the seed-resolution and retry-policy tests
# --------------------------------------------------------------------------- #

class _FakeBody:
    """Minimal context-manager body matching how http_get reads responses."""

    def __init__(self, body):
        self._body = body if isinstance(body, bytes) else body.encode("utf-8")

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False

    def read(self):
        return self._body


class RecordingUrlopen:
    """Offline stand-in for urllib.request.urlopen.

    Serves a scripted list of steps (a body to return, or an exception to
    raise, one per call) and records every requested URL so tests can assert
    which source was contacted, in which order, and with which identifier.
    """

    def __init__(self, steps):
        self.steps = list(steps)
        self.requests = []

    def __call__(self, request, timeout=None):
        self.requests.append(request.full_url)
        if not self.steps:
            raise AssertionError("unexpected HTTP request: %s" % request.full_url)
        step = self.steps.pop(0)
        if isinstance(step, Exception):
            raise step
        return _FakeBody(step)


def _http_error(url, code, message):
    return urllib.error.HTTPError(url, code, message, None, None)


def _openalex_work_dict():
    return {
        "id": "https://openalex.org/W3030163527",
        "title": "Language Models are Few-Shot Learners",
        "publication_year": 2020,
        "doi": None,
        "cited_by_count": 1,
        "authorships": [],
        "referenced_works": [],
    }


def _openalex_work():
    return json.dumps(_openalex_work_dict())


def _openalex_title_results():
    return json.dumps({"results": [_openalex_work_dict()]})


ARXIV_ATOM_WITH_DOI = (
    '<?xml version="1.0" encoding="UTF-8"?>'
    '<feed xmlns="http://www.w3.org/2005/Atom">'
    '<entry><title>Demo Preprint</title>'
    '<arxiv:doi xmlns:arxiv="http://arxiv.org/schemas/atom">10.1000/demo</arxiv:doi>'
    '</entry></feed>'
)

ARXIV_ATOM_TITLE_ONLY = (
    '<?xml version="1.0" encoding="UTF-8"?>'
    '<feed xmlns="http://www.w3.org/2005/Atom">'
    '<entry><title>Demo Preprint</title></entry>'
    '</feed>'
)


def _collect_args(seed):
    """The argparse namespace collect() needs; predecessors-only keeps the
    scripted HTTP traffic down to the seed resolution itself (no referenced
    works, no DOI -> no predecessor/citation/S2 calls)."""
    return argparse.Namespace(
        seed=seed, timeout=5, email=None, direction="predecessors",
        limit=5, with_abstracts=False, semantic_scholar=False,
    )


class ArxivDataciteDoiTests(unittest.TestCase):
    def test_strips_the_version_suffix(self):
        self.assertEqual(
            literature.arxiv_datacite_doi("1706.03762v7"),
            "10.48550/arXiv.1706.03762",
        )

    def test_keeps_an_unversioned_id_unchanged(self):
        self.assertEqual(
            literature.arxiv_datacite_doi("2005.14165"),
            "10.48550/arXiv.2005.14165",
        )

    def test_handles_old_style_ids(self):
        self.assertEqual(
            literature.arxiv_datacite_doi("hep-th/9901001v3"),
            "10.48550/arXiv.hep-th/9901001",
        )
        self.assertEqual(
            literature.arxiv_datacite_doi("cs.AI/0112017"),
            "10.48550/arXiv.cs.AI/0112017",
        )


class CollectArxivSeedTests(unittest.TestCase):
    """arXiv seed resolution: OpenAlex DataCite DOI first, arXiv API as the
    fallback, title search as the last resort."""

    def _collect(self, seed, steps):
        transport = RecordingUrlopen(steps)
        env = {k: v for k, v in os.environ.items() if k != "S2_API_KEY"}
        with mock.patch.object(literature.urllib.request, "urlopen", transport), \
                mock.patch.dict(os.environ, env, clear=True):
            record = literature.collect(_collect_args(seed))
        return record, transport

    def test_resolves_via_openalex_datacite_doi_without_touching_arxiv(self):
        record, transport = self._collect("arXiv:2005.14165", [_openalex_work()])
        self.assertEqual(record["seed_match"], "identifier")
        self.assertEqual(record["sources_used"], ["OpenAlex"])
        self.assertEqual(len(transport.requests), 1)
        self.assertTrue(
            transport.requests[0].startswith(
                "https://api.openalex.org/works/https://doi.org/10.48550/arXiv.2005.14165"
            ),
            transport.requests[0],
        )
        self.assertFalse(any("export.arxiv.org" in url for url in transport.requests))

    def test_datacite_lookup_strips_the_version_suffix(self):
        record, transport = self._collect("arXiv:2401.00001v2", [_openalex_work()])
        self.assertEqual(record["seed_match"], "identifier")
        self.assertIn("10.48550/arXiv.2401.00001", transport.requests[0])
        self.assertNotIn("2401.00001v2", transport.requests[0])

    def test_falls_back_to_the_arxiv_api_when_openalex_lacks_the_datacite_doi(self):
        record, transport = self._collect(
            "arXiv:2401.00001",
            [
                _http_error("https://api.openalex.org/works", 404, "Not Found"),
                ARXIV_ATOM_WITH_DOI,
                _openalex_work(),
                json.dumps({"message": {}}),  # Crossref: no references listed
            ],
        )
        self.assertEqual(record["seed_match"], "identifier")
        self.assertEqual(record["sources_used"], ["arXiv", "OpenAlex"])
        self.assertIn("api.openalex.org", transport.requests[0])
        self.assertIn("10.48550/arXiv.2401.00001", transport.requests[0])
        self.assertIn("export.arxiv.org", transport.requests[1])
        self.assertIn("10.1000/demo", transport.requests[2])
        self.assertIn("api.crossref.org", transport.requests[3])

    def test_falls_back_to_title_search_when_arxiv_has_no_doi(self):
        record, transport = self._collect(
            "arXiv:2401.00001",
            [
                _http_error("https://api.openalex.org/works", 404, "Not Found"),
                ARXIV_ATOM_TITLE_ONLY,
                _openalex_title_results(),
            ],
        )
        self.assertEqual(record["seed_match"], "title-search")
        self.assertEqual(record["sources_used"], ["arXiv", "OpenAlex"])
        title_search_url = urllib.parse.unquote_plus(transport.requests[2])
        self.assertIn("title.search", title_search_url)
        self.assertIn("Demo Preprint", title_search_url)


class HttpGetRetryPolicyTests(unittest.TestCase):
    ARXIV_URL = "https://export.arxiv.org/api/query?id_list=2401.00001"
    OPENALEX_URL = "https://api.openalex.org/works/W1"

    def _get(self, url, steps, retries=2):
        self.transport = RecordingUrlopen(steps)
        self.sleeps = []
        with mock.patch.object(literature.urllib.request, "urlopen", self.transport), \
                mock.patch.object(literature.time, "sleep", self.sleeps.append):
            return literature.http_get(url, 5, retries=retries)

    def test_retries_406_from_export_arxiv_with_the_standard_backoff(self):
        before = literature.STATS["rate_limits"]
        body = self._get(
            self.ARXIV_URL,
            [_http_error(self.ARXIV_URL, 406, "Not Acceptable"), b"<payload/>"],
        )
        self.assertEqual(body, b"<payload/>")
        self.assertEqual(len(self.transport.requests), 2)
        self.assertEqual(self.sleeps, [1.0])  # exponential backoff, first attempt
        self.assertEqual(literature.STATS["rate_limits"], before + 1)

    def test_does_not_retry_406_from_another_host(self):
        with self.assertRaises(literature.ApiError):
            self._get(self.OPENALEX_URL, [_http_error(self.OPENALEX_URL, 406, "Not Acceptable")])
        self.assertEqual(len(self.transport.requests), 1)
        self.assertEqual(self.sleeps, [])

    def test_406_from_export_arxiv_exhausting_retries_raises_api_error(self):
        errors = [_http_error(self.ARXIV_URL, 406, "Not Acceptable") for _ in range(3)]
        with self.assertRaises(literature.ApiError) as ctx:
            self._get(self.ARXIV_URL, errors)
        self.assertIn("406", str(ctx.exception))
        self.assertEqual(len(self.transport.requests), 3)
        self.assertEqual(self.sleeps, [1.0, 2.0])


if __name__ == "__main__":
    unittest.main(verbosity=2)
