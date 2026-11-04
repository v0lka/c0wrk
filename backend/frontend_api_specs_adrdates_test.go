package backend

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/project"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// initSpecsFixtureRepo initializes ws as a git repository with the same
// defensive fixture pins gittest.InitRepo uses: a globally enabled commit
// signing must not break fixture commits.
func initSpecsFixtureRepo(t *testing.T, dir string) {
	t.Helper()
	gitInit(t, dir)
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "config", "tag.gpgsign", "false")
}

// commitPathAtDate stages path (repo-relative) in dir and commits it with
// both author and committer dates pinned to date (the "2025-06-01 12:00:00
// +0000" form), so the fixture's %as add date is deterministic.
func commitPathAtDate(t *testing.T, dir, path, date string) {
	t.Helper()
	stage := exec.CommandContext(context.Background(), "git", "add", path)
	stage.Dir = dir
	if out, err := stage.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v\n%s", path, err, out)
	}
	commit := exec.CommandContext(context.Background(), "git", "commit", "-m", "add "+path)
	commit.Dir = dir
	commit.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit %s: %v\n%s", path, err, out)
	}
}

// writeSpecsFile writes one file under ws (slash-relative path), creating
// parent directories.
func writeSpecsFile(t *testing.T, ws, rel, content string) {
	t.Helper()
	abs := filepath.Join(ws, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// adrDateBySlug collects the DTO ADR dates keyed by slug.
func adrDateBySlug(dto *SpecsStatusDTO) map[string]string {
	dates := make(map[string]string, len(dto.ADRs))
	for _, adr := range dto.ADRs {
		dates[adr.Slug] = adr.Date
	}
	return dates
}

// adrWireKeys marshals one DTO ADR and returns its wire keys — used to pin
// the omitempty behavior of the date field.
func adrWireKeys(t *testing.T, adr ADRMetaDTO) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(adr)
	if err != nil {
		t.Fatalf("marshal ADR: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal ADR wire: %v", err)
	}
	keys := make(map[string]bool, len(wire))
	for k := range wire {
		keys[k] = true
	}
	return keys
}

// ---------------------------------------------------------------------------
// pure parser
// ---------------------------------------------------------------------------

// TestParseADRDatesLog pins the git-log parse: date-first commit blocks,
// newest-first with first date winning per path (the add date, even when a
// path was deleted and re-added), repo-root nesting and git's C-style path
// quoting normalized away, stray path lines skipped, CRLF tolerated.
func TestParseADRDatesLog(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]string
	}{
		{
			name: "two commits newest-first, first date wins per path",
			in: "2025-07-02\n" +
				"specs/decisions/001-a.md\n" +
				"\n" +
				"2025-06-01\n" +
				"specs/decisions/001-a.md\n" +
				"specs/decisions/002-b.md\n",
			want: map[string]string{
				"decisions/001-a.md": "2025-07-02",
				"decisions/002-b.md": "2025-06-01",
			},
		},
		{
			name: "repo root IS the specs tree (no specs/ prefix)",
			in:   "2025-06-01\ndecisions/003-c.md\n",
			want: map[string]string{"decisions/003-c.md": "2025-06-01"},
		},
		{
			name: "workspace nested under the repository root",
			in:   "2025-06-01\napps/desktop/specs/decisions/004-d.md\n",
			want: map[string]string{"decisions/004-d.md": "2025-06-01"},
		},
		{
			name: "quoted non-ascii path is unquoted",
			// git quotes paths when core.quotePath is on: "specs/decisions/005-ololo.md"
			// with ololo in Cyrillic becomes C-style escaped UTF-8.
			in:   "2025-06-01\n\"specs/decisions/005-\320\276\320\273\320\276\320\273\320\276.md\"\n",
			want: map[string]string{"decisions/005-ололо.md": "2025-06-01"},
		},
		{
			name: "path line before any date header is skipped",
			in:   "stray/path.md\n2025-06-01\ndecisions/006-e.md\n",
			want: map[string]string{"decisions/006-e.md": "2025-06-01"},
		},
		{
			name: "CRLF line endings",
			in:   "2025-06-01\r\nspecs/decisions/007-f.md\r\n",
			want: map[string]string{"decisions/007-f.md": "2025-06-01"},
		},
		{
			name: "empty output",
			in:   "",
			want: map[string]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseADRDatesLog(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseADRDatesLog = %v, want %v", got, tc.want)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("dates[%q] = %q, want %q (full map %v)", k, got[k], want, got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// fail-soft helper
// ---------------------------------------------------------------------------

// TestSpecsADRDates_FailSoft pins the always-fail-soft contract of the git
// probe itself: a nonexistent workspace root yields an empty map (the spawn
// cannot even chdir) without panicking or propagating an error.
func TestSpecsADRDates_FailSoft(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()

	got := specsADRDates(ctx, nil, t.TempDir())
	if len(got) != 0 {
		t.Errorf("specsADRDates = %v, want an empty map for a non-git workspace", got)
	}
	if got == nil {
		t.Error("specsADRDates returned nil, want a non-nil (empty) map")
	}
	if got := specsADRDates(ctx, nil, ""); len(got) != 0 || got == nil {
		t.Errorf("specsADRDates(\"\") = %v, want an empty non-nil map", got)
	}
}

// ---------------------------------------------------------------------------
// RPC-level enrichment
// ---------------------------------------------------------------------------

const specsADRFixtureDate = "2025-06-01 12:00:00 +0000"

// TestGetSpecsStatus_ADRDatesCommittedAndUncommitted pins the enrichment:
// a committed ADR carries its deterministic add date, an ADR born after the
// commit stays undated, and the wire form is additive — "date" present only
// where known. Both existing 001 decisions come from the single pinned
// commit; decisions/002-uncommitted.md is written afterwards and never
// staged.
func TestGetSpecsStatus_ADRDatesCommittedAndUncommitted(t *testing.T) {
	api, workspaces := specsTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	ws := workspaces["proj-1"]
	writeSpecsTree(t, ws)
	initSpecsFixtureRepo(t, ws)
	commitPathAtDate(t, ws, "specs", specsADRFixtureDate)
	writeSpecsFile(t, ws, "specs/decisions/002-uncommitted.md",
		"# ADR-002: Uncommitted\n\n## Status\n\nAccepted\n")

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if !status.Present {
		t.Fatal("Present = false, want true")
	}
	if len(status.ADRs) != 3 {
		t.Fatalf("ADRs = %+v, want all three decisions/ files", status.ADRs)
	}

	want := map[string]string{
		"dup":         "2025-06-01",
		"first":       "2025-06-01",
		"uncommitted": "",
	}
	got := adrDateBySlug(status)
	for slug, wantDate := range want {
		if got[slug] != wantDate {
			t.Errorf("ADR %q date = %q, want %q", slug, got[slug], wantDate)
		}
	}

	// Wire form: the committed ADR carries a string "date"; the uncommitted
	// one omits the key entirely (omitempty).
	for _, adr := range status.ADRs {
		keys := adrWireKeys(t, adr)
		if adr.Slug == "uncommitted" {
			if keys["date"] {
				t.Error("uncommitted ADR serializes a \"date\" key, want it omitted")
			}
			continue
		}
		if !keys["date"] {
			t.Errorf("committed ADR %q missing \"date\" on the wire", adr.Slug)
			continue
		}
		raw, err := json.Marshal(adr)
		if err != nil {
			t.Fatalf("marshal ADR %q: %v", adr.Slug, err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("unmarshal ADR %q wire: %v", adr.Slug, err)
		}
		if got := string(wire["date"]); got != `"2025-06-01"` {
			t.Errorf("ADR %q date = %s, want \"2025-06-01\"", adr.Slug, got)
		}
	}

	assertSpecsWireShape(t, status)
}

// TestGetSpecsStatus_ADRDatesNonGitWorkspace pins the non-git branch of the
// fail-soft rule: a real specs/ tree in a plain directory (no repository)
// parses fine, returns no dates, and never surfaces an error.
func TestGetSpecsStatus_ADRDatesNonGitWorkspace(t *testing.T) {
	api, workspaces := specsTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	writeSpecsTree(t, workspaces["proj-1"])

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus (non-git workspace): %v", err)
	}
	if !status.Present {
		t.Fatal("Present = false, want true for an existing tree in a non-git workspace")
	}
	if len(status.ADRs) != 2 {
		t.Fatalf("ADRs = %+v, want both decisions/ files", status.ADRs)
	}
	for slug, date := range adrDateBySlug(status) {
		if date != "" {
			t.Errorf("ADR %q date = %q, want empty in a non-git workspace", slug, date)
		}
	}
	assertSpecsWireShape(t, status)
}

// TestGetSpecsStatus_ADRDatesUnbornHEAD pins the unborn-HEAD branch of the
// fail-soft rule: an initialized repository with zero commits fails the
// git log probe (exit 128), which must degrade to absent dates — not an
// RPC error.
func TestGetSpecsStatus_ADRDatesUnbornHEAD(t *testing.T) {
	api, workspaces := specsTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	ws := workspaces["proj-1"]
	writeSpecsTree(t, ws)
	initSpecsFixtureRepo(t, ws) // git init, no commits — HEAD is unborn

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus (unborn HEAD): %v", err)
	}
	if !status.Present {
		t.Fatal("Present = false, want true for an existing tree in a fresh repository")
	}
	for slug, date := range adrDateBySlug(status) {
		if date != "" {
			t.Errorf("ADR %q date = %q, want empty with an unborn HEAD", slug, date)
		}
	}
	assertSpecsWireShape(t, status)
}
