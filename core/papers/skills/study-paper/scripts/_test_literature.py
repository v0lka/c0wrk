#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Unit tests for the study-paper literature helper (literature.py).

Focus: the WRITE LOGIC must be atomic — a reader must never observe a
partially-written literature.json — and the payload must carry a parseable
`generated_at` timestamp so the UI can flag a stale neighbourhood.

The file name is underscore-prefixed on purpose: `//go:embed skills` (see
core/papers/skillpack.go) excludes dotfiles and underscore-prefixed files, so
this test is NOT shipped inside the paper-study skill pack. Run it directly:

    python3 core/papers/skills/study-paper/scripts/_test_literature.py

Standard library only (matches the helper's own constraint).
"""

import importlib.util
import os
import pathlib
import re
import sys
import tempfile
import unittest

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


if __name__ == "__main__":
    unittest.main(verbosity=2)
