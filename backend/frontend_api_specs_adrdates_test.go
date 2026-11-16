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

// adrDateBySlug collects the decision-document dates keyed by slug.
func adrDateBySlug(dto *SpecsStatusDTO) map[string]string {
	dates := make(map[string]string, len(dto.Documents))
	for _, doc := range dto.Documents {
		if doc.Concept == "decision" {
			dates[doc.Slug] = doc.Date
		}
	}
	return dates
}

// documentWireKeys marshals one document DTO and returns its wire keys — used
// to pin the omitempty behavior of the date field.
func documentWireKeys(t *testing.T, doc SDDDocumentDTO) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal document wire: %v", err)
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
				"specs/decisions/001-a.md": "2025-07-02",
				"specs/decisions/002-b.md": "2025-06-01",
			},
		},
		{
			name: "operator root spells without the specs/ prefix",
			in:   "2025-06-01\ndecisions/003-c.md\n",
			want: map[string]string{"decisions/003-c.md": "2025-06-01"},
		},
		{
			// No folding happens here: git prints repo-root-relative paths
			// (the log args carry no --relative flag), so parse keys them
			// verbatim and remapADRDates rekeys onto the workspace spelling.
			name: "workspace nested under the repository root",
			in:   "2025-06-01\nspecs/decisions/004-d.md\n",
			want: map[string]string{"specs/decisions/004-d.md": "2025-06-01"},
		},
		{
			name: "quoted non-ascii path is unquoted",
			// git quotes paths when core.quotePath is on: "specs/decisions/005-ololo.md"
			// with ololo in Cyrillic becomes C-style escaped UTF-8.
			in:   "2025-06-01\n\"specs/decisions/005-\320\276\320\273\320\276\320\273\320\276.md\"\n",
			want: map[string]string{"specs/decisions/005-ололо.md": "2025-06-01"},
		},
		{
			name: "path line before any date header is skipped",
			in:   "stray/path.md\n2025-06-01\ndecisions/006-e.md\n",
			want: map[string]string{"decisions/006-e.md": "2025-06-01"},
		},
		{
			name: "CRLF line endings",
			in:   "2025-06-01\r\nspecs/decisions/007-f.md\r\n",
			want: map[string]string{"specs/decisions/007-f.md": "2025-06-01"},
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

// TestRemapADRDates pins the repo-prefix folding: git prints repo-root-
// relative paths while the pathspecs are workspace-relative, so every printed
// path is <repo prefix>/<pathspec>/<tail> and remapADRDates rekeys it onto
// the workspace spelling. The longest pathspec is tried first, so a broader
// decision directory's marker cannot steal a nested one's paths, and a path
// matching no pathspec is dropped.
func TestRemapADRDates(t *testing.T) {
	cases := []struct {
		name      string
		pathspecs []string
		raw       map[string]string
		want      map[string]string
	}{
		{
			name:      "repo prefix folds away to the workspace-relative spelling",
			pathspecs: []string{"specs/decisions"},
			raw:       map[string]string{"monorepo/app/specs/decisions/004-d.md": "2025-06-01"},
			want:      map[string]string{"specs/decisions/004-d.md": "2025-06-01"},
		},
		{
			name:      "longest pathspec wins when one decision directory nests the other's marker",
			pathspecs: []string{"specs/decisions", "decisions"},
			raw: map[string]string{
				"repo/specs/decisions/001-a.md":  "2025-06-01",
				"repo/custom/decisions/002-b.md": "2025-07-02",
			},
			want: map[string]string{
				"specs/decisions/001-a.md": "2025-06-01",
				"decisions/002-b.md":       "2025-07-02",
			},
		},
		{
			name:      "a path matching no pathspec is dropped",
			pathspecs: []string{"specs/decisions"},
			raw:       map[string]string{"repo/other/003-c.md": "2025-06-01"},
			want:      map[string]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := remapADRDates(tc.raw, tc.pathspecs)
			if len(got) != len(tc.want) {
				t.Fatalf("remapADRDates = %v, want %v", got, tc.want)
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

	got := specsADRDates(ctx, nil, t.TempDir(), []string{"specs/decisions"})
	if len(got) != 0 {
		t.Errorf("specsADRDates = %v, want an empty map for a non-git workspace", got)
	}
	if got == nil {
		t.Error("specsADRDates returned nil, want a non-nil (empty) map")
	}
	if got := specsADRDates(ctx, nil, "", []string{"specs/decisions"}); len(got) != 0 || got == nil {
		t.Errorf("specsADRDates(\"\") = %v, want an empty non-nil map", got)
	}
}

// ---------------------------------------------------------------------------
// RPC-level enrichment
// ---------------------------------------------------------------------------

const specsADRFixtureDate = "2025-06-01 12:00:00 +0000"

// sddDatesFrontend builds the SDD harness with the global vibespec-* skill
// installed, so a specs/ tree in the workspace is parsed through the VibeSpec
// framework.
func sddDatesFrontend(t *testing.T) (api *FrontendAPI, ws string) {
	t.Helper()
	api, workspaces, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	seedGlobalSkill(t, agentDir, "vibespec-check")
	return api, workspaces["proj-1"]
}

// TestGetSpecsStatus_ADRDatesCommittedAndUncommitted pins the enrichment: a
// committed decision document carries its deterministic add date, a document
// born after the commit stays undated, and the wire form is additive — "date"
// present only where known.
func TestGetSpecsStatus_ADRDatesCommittedAndUncommitted(t *testing.T) {
	api, ws := sddDatesFrontend(t)
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
	if len(status.Documents) == 0 {
		t.Fatal("documents = empty, want the parsed specs tree")
	}

	want := map[string]string{
		"dup":         "2025-06-01",
		"first":       "2025-06-01",
		"uncommitted": "",
	}
	got := adrDateBySlug(status)
	if len(got) != 3 {
		t.Fatalf("decision documents = %v, want all three decisions/ files", got)
	}
	for slug, wantDate := range want {
		if got[slug] != wantDate {
			t.Errorf("decision %q date = %q, want %q", slug, got[slug], wantDate)
		}
	}

	// Wire form: committed documents carry a string "date"; the uncommitted
	// one omits the key entirely (omitempty).
	for _, doc := range status.Documents {
		if doc.Concept != "decision" {
			continue
		}
		keys := documentWireKeys(t, doc)
		if doc.Slug == "uncommitted" {
			if keys["date"] {
				t.Error("uncommitted document serializes a \"date\" key, want it omitted")
			}
			continue
		}
		if !keys["date"] {
			t.Errorf("committed document %q missing \"date\" on the wire", doc.Slug)
			continue
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal document %q: %v", doc.Slug, err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("unmarshal document %q wire: %v", doc.Slug, err)
		}
		if date := string(wire["date"]); date != `"2025-06-01"` {
			t.Errorf("document %q date = %s, want \"2025-06-01\"", doc.Slug, date)
		}
	}

	assertSDDWireShape(t, status)
}

// TestGetSpecsStatus_ADRDatesNonGitWorkspace pins the non-git branch of the
// fail-soft rule: a real specs/ tree in a plain directory (no repository)
// parses fine, returns no dates, and never surfaces an error.
func TestGetSpecsStatus_ADRDatesNonGitWorkspace(t *testing.T) {
	api, ws := sddDatesFrontend(t)
	writeSpecsTree(t, ws)

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus (non-git workspace): %v", err)
	}
	if !status.Present {
		t.Fatal("Present = false, want true for an existing tree in a non-git workspace")
	}
	if len(adrDateBySlug(status)) != 2 {
		t.Fatalf("decision documents = %v, want both decisions/ files", adrDateBySlug(status))
	}
	for slug, date := range adrDateBySlug(status) {
		if date != "" {
			t.Errorf("decision %q date = %q, want empty in a non-git workspace", slug, date)
		}
	}
	assertSDDWireShape(t, status)
}

// TestGetSpecsStatus_ADRDatesUnbornHEAD pins the unborn-HEAD branch of the
// fail-soft rule: an initialized repository with zero commits fails the git
// log probe (exit 128), which must degrade to absent dates — not an RPC error.
func TestGetSpecsStatus_ADRDatesUnbornHEAD(t *testing.T) {
	api, ws := sddDatesFrontend(t)
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
			t.Errorf("decision %q date = %q, want empty with an unborn HEAD", slug, date)
		}
	}
	assertSDDWireShape(t, status)
}

// TestEnrichSpecsDocumentDates_TwoRootsSameNameDoNotCollide pins the keying
// contract behind the generalized enrichment: the lookup key is the FULL
// workspace-relative path, so two decision documents with the same name in
// different roots (the built-in specs/decisions/ tree and an operator's
// custom decisions/ root) are two distinct lookup slots — each carries its
// own add date, and an uncommitted document in the custom root stays undated
// instead of inheriting the other root's history.
func TestEnrichSpecsDocumentDates_TwoRootsSameNameDoNotCollide(t *testing.T) {
	ws := t.TempDir()
	initSpecsFixtureRepo(t, ws)
	writeSpecsFile(t, ws, "specs/decisions/001-first.md",
		"# ADR-001: First\n\n## Status\n\nAccepted\n")
	commitPathAtDate(t, ws, "specs/decisions/001-first.md", "2025-06-01 12:00:00 +0000")
	writeSpecsFile(t, ws, "decisions/001-first.md",
		"# ADR-001: Operator root\n\n## Status\n\nAccepted\n")
	commitPathAtDate(t, ws, "decisions/001-first.md", "2025-07-15 12:00:00 +0000")
	writeSpecsFile(t, ws, "decisions/002-later.md",
		"# ADR-002: Uncommitted in the custom root\n\n## Status\n\nAccepted\n")

	api := &FrontendAPI{}
	api.seedPublished.Store(true)
	docs := []SDDDocumentDTO{
		{Path: "specs/decisions/001-first.md", Concept: "decision"},
		{Path: "decisions/001-first.md", Concept: "decision"},
		{Path: "decisions/002-later.md", Concept: "decision"},
	}
	api.enrichSpecsDocumentDates(context.Background(), docs, ws)

	want := map[string]string{
		"specs/decisions/001-first.md": "2025-06-01",
		"decisions/001-first.md":       "2025-07-15",
		"decisions/002-later.md":       "",
	}
	for i := range docs {
		if docs[i].Date != want[docs[i].Path] {
			t.Errorf("document %q date = %q, want %q", docs[i].Path, docs[i].Date, want[docs[i].Path])
		}
	}
}
