package seedpack

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// unseedPackFS builds a two-entry pack (one: SKILL.md + ref.md, two:
// SKILL.md) as the embed-FS stand-in for UnseedTarget/UnseedEntries.
func unseedPackFS() fstest.MapFS {
	return fstest.MapFS{
		"root/one/SKILL.md": &fstest.MapFile{Data: []byte("A")},
		"root/one/ref.md":   &fstest.MapFile{Data: []byte("B")},
		"root/two/SKILL.md": &fstest.MapFile{Data: []byte("C")},
	}
}

// writeUnseedTree materializes files (rel path → content) under dir and
// optionally the version marker.
func writeUnseedTree(t *testing.T, dir string, marker bool, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if marker {
		if err := os.WriteFile(filepath.Join(dir, VersionFile), []byte("1"), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
	}
}

// TestUnseedTarget_Classification pins the unseed decision table: only
// byte-identical copies (any marker state) and pack-marked truncated subsets
// are removed; user-owned (marker-less, diverging) and locally modified
// (marker-present, diverging) copies — including a pack copy the user added
// a file to — are preserved, and a missing target reports Missing.
func TestUnseedTarget_Classification(t *testing.T) {
	packFiles := map[string]string{"SKILL.md": "A", "ref.md": "B"}
	cases := []struct {
		name   string
		files  map[string]string
		marker bool
		want   UnseedAction
	}{
		{"identical with marker", packFiles, true, UnseedRemoved},
		{"identical without marker", packFiles, false, UnseedRemoved},
		{
			"truncated with marker",
			map[string]string{"SKILL.md": "A"},
			true,
			UnseedRemoved,
		},
		{
			"truncated without marker",
			map[string]string{"SKILL.md": "A"},
			false,
			UnseedPreservedUser,
		},
		{
			"diverged with marker",
			map[string]string{"SKILL.md": "X", "ref.md": "B"},
			true,
			UnseedPreservedModified,
		},
		{
			"diverged without marker",
			map[string]string{"SKILL.md": "X", "ref.md": "B"},
			false,
			UnseedPreservedUser,
		},
		{
			"pack copy plus user file",
			map[string]string{"SKILL.md": "A", "ref.md": "B", "extra.md": "E"},
			true,
			UnseedPreservedModified,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			target := filepath.Join(dest, "one")
			writeUnseedTree(t, target, tc.marker, tc.files)

			got, err := UnseedTarget(unseedPackFS(), "root", "one", target)
			if err != nil {
				t.Fatalf("UnseedTarget: %v", err)
			}
			if got != tc.want {
				t.Fatalf("UnseedTarget = %v, want %v", got, tc.want)
			}
			if tc.want == UnseedRemoved {
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Errorf("removed target still exists (stat err=%v)", err)
				}
			} else if _, err := os.Stat(target); err != nil {
				t.Errorf("preserved target disappeared: %v", err)
			}
		})
	}

	t.Run("missing target", func(t *testing.T) {
		dest := t.TempDir()
		got, err := UnseedTarget(unseedPackFS(), "root", "one", filepath.Join(dest, "one"))
		if err != nil {
			t.Fatalf("UnseedTarget: %v", err)
		}
		if got != UnseedMissing {
			t.Fatalf("UnseedTarget = %v, want %v", got, UnseedMissing)
		}
	})

	t.Run("target is a file", func(t *testing.T) {
		dest := t.TempDir()
		target := filepath.Join(dest, "one")
		if err := os.WriteFile(target, []byte("A"), 0o644); err != nil {
			t.Fatalf("write file target: %v", err)
		}
		if _, err := UnseedTarget(unseedPackFS(), "root", "one", target); err == nil {
			t.Fatal("UnseedTarget on a plain-file target must fail")
		}
	})
}

// TestUnseedEntries_AggregatesAndWarns pins the loop wrapper: buckets per
// action, actually removes the removed, and emits one visible warning naming
// every locally modified copy the run kept on disk (the preserve_warn
// contract). The logger is a test-local buffer — the warning is behavior
// under test here, intercepted and asserted at the source.
func TestUnseedEntries_AggregatesAndWarns(t *testing.T) {
	dest := t.TempDir()
	// one: byte-identical (removed); two: marker-present, diverged (kept +
	// warned). A non-pack entry ("mine") is never in the names list, so it
	// must survive untouched.
	writeUnseedTree(t, filepath.Join(dest, "one"), true, map[string]string{"SKILL.md": "A", "ref.md": "B"})
	writeUnseedTree(t, filepath.Join(dest, "two"), true, map[string]string{"SKILL.md": "X"})
	writeUnseedTree(t, filepath.Join(dest, "mine"), false, map[string]string{"SKILL.md": "M"})

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	res, err := UnseedEntries(unseedPackFS(), "root", dest, []string{"one", "two"}, "test entry", logger)
	if err != nil {
		t.Fatalf("UnseedEntries: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != "one" {
		t.Errorf("Removed = %v, want [one]", res.Removed)
	}
	if len(res.PreservedModified) != 1 || res.PreservedModified[0] != "two" {
		t.Errorf("PreservedModified = %v, want [two]", res.PreservedModified)
	}
	if _, err := os.Stat(filepath.Join(dest, "one")); !os.IsNotExist(err) {
		t.Errorf("removed entry still exists (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "two", "SKILL.md")); err != nil {
		t.Errorf("preserved entry disappeared: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "mine", "SKILL.md")); err != nil {
		t.Errorf("non-pack entry was touched: %v", err)
	}
	warn := logBuf.String()
	if !bytes.Contains([]byte(warn), []byte("kept on disk")) || !bytes.Contains([]byte(warn), []byte("two")) {
		t.Errorf("unseed log must warn naming the preserved modified entry, got:\n%s", warn)
	}
}

// TestUnseedEntries_EmptyDestErrors pins the guard: an empty destination is
// a programming error, mirrored from the Seed* entry points.
func TestUnseedEntries_EmptyDestErrors(t *testing.T) {
	if _, err := UnseedEntries(unseedPackFS(), "root", "", []string{"one"}, "test entry", nil); err == nil {
		t.Fatal("UnseedEntries with an empty destDir must fail")
	}
}

// TestUnseedEntries_PerEntryFailSoft pins the fail-soft loop: an entry whose
// own removal errors (a plain file squatting the pack name) lands in the
// Failed bucket and is named in the joined error, while the remaining
// entries still unseed — one hostile entry never blocks the rest of the
// pack, and the failed entry stays on disk for the next reconcile.
func TestUnseedEntries_PerEntryFailSoft(t *testing.T) {
	dest := t.TempDir()
	writeUnseedTree(t, filepath.Join(dest, "one"), true, map[string]string{"SKILL.md": "A", "ref.md": "B"})
	if err := os.WriteFile(filepath.Join(dest, "two"), []byte("C"), 0o644); err != nil {
		t.Fatalf("write squatter file: %v", err)
	}

	res, err := UnseedEntries(unseedPackFS(), "root", dest, []string{"one", "two"}, "test entry", nil)
	if err == nil {
		t.Fatal("a per-entry failure must surface in the returned error")
	}
	if !strings.Contains(err.Error(), `"two"`) {
		t.Errorf("the joined error must name the failed entry, got: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != "one" {
		t.Errorf("Removed = %v, want [one] — the squatter must not abort the pack", res.Removed)
	}
	if len(res.Failed) != 1 || res.Failed[0] != "two" {
		t.Errorf("Failed = %v, want [two]", res.Failed)
	}
	if _, err := os.Stat(filepath.Join(dest, "one")); !os.IsNotExist(err) {
		t.Errorf("healthy entry was not removed (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "two")); err != nil {
		t.Errorf("the failed entry must stay on disk for the next reconcile: %v", err)
	}
}

// TestUnseedEntries_ForeignNameWithTargetErrors pins the programming-error
// guard: a name absent from the embed tree fails ITS OWN entry when a
// target directory exists (InspectTarget must load the pack set to classify
// it) and surfaces in the joined error; a foreign name with NO target on
// disk is benign — the classification never consults the pack, so it
// reports Missing.
func TestUnseedEntries_ForeignNameWithTargetErrors(t *testing.T) {
	dest := t.TempDir()
	absent := filepath.Join(dest, "nope")
	if got, err := UnseedEntries(unseedPackFS(), "root", dest, []string{"nope"}, "test entry", nil); err != nil || len(got.Removed) != 0 {
		t.Fatalf("foreign name without a target must be a benign Missing, got result %+v err %v", got, err)
	}
	if err := os.MkdirAll(absent, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if _, err := UnseedEntries(unseedPackFS(), "root", dest, []string{"nope"}, "test entry", nil); err == nil {
		t.Fatal("UnseedEntries with a foreign name over an existing target must fail")
	}
}
