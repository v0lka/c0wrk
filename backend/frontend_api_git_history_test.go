package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/v0lka/c0wrk/core/workspace"
)

// ---------------------------------------------------------------------------
// Unified history+graph RPC: GetGitHistory / parseGitHistory
// ---------------------------------------------------------------------------
//
// Deterministic parser tests (no git) use table-driven subtests and
// cmp.Diff for struct/slice comparisons, mirroring TestParseGitGraph.
// Integration tests exercise the real git binary through the FrontendAPI
// RPC using the shared withGitRepo / commitFile helpers defined in this
// package, mirroring TestGetGitGraph_Success.

// ---------------------------------------------------------------------------
// parseGitHistory — deterministic, no git
// ---------------------------------------------------------------------------

func TestParseGitHistory(t *testing.T) {
	// %H%x1f%P%x1f%an%x1f%ae%x1f%ad%x1f%s%x1f%d%x1e
	// (record sep = \x1e, field sep = \x1f)
	tests := []struct {
		name  string
		input string
		want  []GitHistoryCommit
	}{
		{
			name:  "empty",
			input: "",
			want:  []GitHistoryCommit{},
		},
		{
			name:  "whitespace only",
			input: "  \n ",
			want:  []GitHistoryCommit{},
		},
		{
			name:  "single commit with parents author date and refs",
			input: "def456\x1faaa bbb\x1fAlice\x1falice@x\x1f2024-01-01\x1ffeat: x\x1f (HEAD -> main, tag: v1.0)",
			want: []GitHistoryCommit{
				{SHA: "def456", Parents: []string{"aaa", "bbb"}, Author: "Alice", Email: "alice@x", Date: "2024-01-01", Message: "feat: x", Refs: []string{"HEAD -> main", "tag: v1.0"}},
			},
		},
		{
			name:  "multiple commits with parents refs author date",
			input: "s1\x1fp1\x1fA\x1fa@x\x1fd1\x1fm1\x1f (HEAD -> main)\x1es2\x1f\x1fB\x1fb@x\x1fd2\x1fm2\x1f",
			want: []GitHistoryCommit{
				{SHA: "s1", Parents: []string{"p1"}, Author: "A", Email: "a@x", Date: "d1", Message: "m1", Refs: []string{"HEAD -> main"}},
				{SHA: "s2", Parents: []string{}, Author: "B", Email: "b@x", Date: "d2", Message: "m2", Refs: []string{}},
			},
		},
		{
			name:  "merge commit with multiple parents",
			input: "merge1\x1fparentA parentB\x1fMerger\x1fm@x\x1f2024-02-02\x1fmerge: combine\x1f (HEAD -> main)",
			want: []GitHistoryCommit{
				{SHA: "merge1", Parents: []string{"parentA", "parentB"}, Author: "Merger", Email: "m@x", Date: "2024-02-02", Message: "merge: combine", Refs: []string{"HEAD -> main"}},
			},
		},
		{
			name:  "commit with no decorations parses refs as empty",
			input: "abc123\x1f\x1fAlice\x1falice@x\x1f2024-01-01\x1fadd file\x1f",
			want: []GitHistoryCommit{
				{SHA: "abc123", Parents: []string{}, Author: "Alice", Email: "alice@x", Date: "2024-01-01", Message: "add file", Refs: []string{}},
			},
		},
		{
			name:  "malformed record with too few fields is skipped",
			input: "s1\x1fp1\x1fA\x1fa@x\x1fd1\x1fm1\x1f (HEAD -> main)\x1eshort",
			want: []GitHistoryCommit{
				{SHA: "s1", Parents: []string{"p1"}, Author: "A", Email: "a@x", Date: "d1", Message: "m1", Refs: []string{"HEAD -> main"}},
			},
		},
		{
			name:  "trailing record separator ignored",
			input: "s1\x1f\x1fA\x1fa@x\x1fd1\x1fm1\x1f\x1e",
			want: []GitHistoryCommit{
				{SHA: "s1", Parents: []string{}, Author: "A", Email: "a@x", Date: "d1", Message: "m1", Refs: []string{}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGitHistory(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parseGitHistory(%q) mismatch (-want +got):\n%s", tc.input, diff)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetGitHistory — integration (real git)
// ---------------------------------------------------------------------------

func TestGetGitHistory_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	if _, err := f.GetGitHistory(0, 0); err == nil {
		t.Fatal("GetGitHistory: expected error when no active project")
	}
}

// TestNormalizeGitHistoryLimit pins the limit defaulting/capping rules:
// limit<=0 → 300, limit>1000 → 1000, otherwise passthrough.
func TestNormalizeGitHistoryLimit(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero falls back to default", 0, 300},
		{"negative falls back to default", -10, 300},
		{"one is allowed", 1, 1},
		{"default passthrough", 300, 300},
		{"max passthrough", 1000, 1000},
		{"above max is capped", 1001, 1000},
		{"far above max is capped", 5000, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeGitHistoryLimit(tc.in); got != tc.want {
				t.Errorf("normalizeGitHistoryLimit(%d): got %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestGetGitHistory_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// withGitRepo commits committed.txt (1 commit). Add a second.
		commitFile(t, dir, "b.txt", "b\n")

		page, err := f.GetGitHistory(0, 0)
		if err != nil {
			t.Fatalf("GetGitHistory: %v", err)
		}
		history := page.Commits
		if len(history) != 2 {
			t.Fatalf("len: got %d, want 2", len(history))
		}
		// Two commits on one page (limit defaults to 300) → no more pages.
		if page.HasMore {
			t.Error("page.HasMore: got true, want false (2 commits < default limit)")
		}
		if page.NextSkip != 2 {
			t.Errorf("page.NextSkip: got %d, want 2", page.NextSkip)
		}
		// Newest first.
		if history[0].Message != "add b.txt" {
			t.Errorf("history[0].Message: got %q, want %q", history[0].Message, "add b.txt")
		}
		// All union fields populated on the newest commit.
		if history[0].SHA == "" {
			t.Error("history[0].SHA is empty")
		}
		if history[0].Author == "" || history[0].Email == "" || history[0].Date == "" {
			t.Errorf("history[0]: expected populated author/email/date, got %+v", history[0])
		}
		// Second commit is the root: newest has it as parent, root has none.
		if len(history[0].Parents) != 1 {
			t.Errorf("history[0].Parents: got %d, want 1", len(history[0].Parents))
		} else if history[0].Parents[0] != history[1].SHA {
			t.Errorf("history[0].Parents[0]: got %q, want %q", history[0].Parents[0], history[1].SHA)
		}
		if len(history[1].Parents) != 0 {
			t.Errorf("history[1].Parents: got %v, want empty (root)", history[1].Parents)
		}
		// HEAD -> branch decoration sits on the newest commit; root has none.
		if len(history[0].Refs) == 0 {
			t.Errorf("history[0].Refs: got %v, want at least one (HEAD -> branch)", history[0].Refs)
		}
		if len(history[1].Refs) != 0 {
			t.Errorf("history[1].Refs: got %v, want empty (root has no decoration)", history[1].Refs)
		}
		// Stats: each commit adds one file; --root makes the root commit's
		// file count as an addition too.
		if history[0].Stats.Added != 1 || history[0].Stats.Modified != 0 || history[0].Stats.Deleted != 0 {
			t.Errorf("history[0].Stats: got %+v, want {1 0 0} (adds b.txt)", history[0].Stats)
		}
		if history[1].Stats.Added != 1 || history[1].Stats.Modified != 0 || history[1].Stats.Deleted != 0 {
			t.Errorf("history[1].Stats: got %+v, want {1 0 0} (root commit, --root)", history[1].Stats)
		}
	})
}

// TestGetGitHistory_Pagination verifies limit/skip paging against a real
// repository with more commits than the page size: the first page is
// exactly `limit` commits in git-log (newest-first) order with
// HasMore=true and NextSkip=limit; the second page holds the remainder
// with HasMore=false. Pages are disjoint and together reconstruct the full
// history in order.
func TestGetGitHistory_Pagination(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// withGitRepo commits committed.txt; add two more for 3 total.
		commitFile(t, dir, "b.txt", "b\n")
		commitFile(t, dir, "c.txt", "c\n")

		const limit = 2

		page1, err := f.GetGitHistory(limit, 0)
		if err != nil {
			t.Fatalf("GetGitHistory page1: %v", err)
		}
		if len(page1.Commits) != limit {
			t.Fatalf("page1 len: got %d, want %d", len(page1.Commits), limit)
		}
		if !page1.HasMore {
			t.Error("page1.HasMore: got false, want true (saturated page)")
		}
		if page1.NextSkip != limit {
			t.Errorf("page1.NextSkip: got %d, want %d", page1.NextSkip, limit)
		}

		page2, err := f.GetGitHistory(limit, page1.NextSkip)
		if err != nil {
			t.Fatalf("GetGitHistory page2: %v", err)
		}
		if len(page2.Commits) != 1 {
			t.Fatalf("page2 len: got %d, want 1", len(page2.Commits))
		}
		if page2.HasMore {
			t.Error("page2.HasMore: got true, want false (short page)")
		}
		if page2.NextSkip != 3 {
			t.Errorf("page2.NextSkip: got %d, want 3", page2.NextSkip)
		}

		// The concatenation of the pages must equal the full history.
		paged := append(append([]GitHistoryCommit{}, page1.Commits...), page2.Commits...)
		full, err := f.GetGitHistory(0, 0)
		if err != nil {
			t.Fatalf("GetGitHistory full: %v", err)
		}
		if len(full.Commits) != 3 {
			t.Fatalf("full len: got %d, want 3", len(full.Commits))
		}
		if diff := cmp.Diff(full.Commits, paged); diff != "" {
			t.Errorf("paged history mismatch (-full +paged):\n%s", diff)
		}
	})
}

// ---------------------------------------------------------------------------
// parseGitHistory — Stats bucket coverage (root/merge/rename/broken entries)
// ---------------------------------------------------------------------------

func TestParseGitHistoryStats(t *testing.T) {
	// Wire format as produced by GetGitHistory: each \x1e-delimited chunk
	// is self-contained — the format line first, then the --name-status
	// lines git appends after a blank separator.
	tests := []struct {
		name      string
		input     string
		wantStats []workspace.CommitStats
	}{
		{
			name:      "no name-status lines: all zeros",
			input:     "s1\x1fp1\x1fA\x1fa@x\x1fd1\x1fm1\x1f",
			wantStats: []workspace.CommitStats{{}},
		},
		{
			name:      "root commit with --root additions",
			input:     "r1\x1f\x1fA\x1fa@x\x1fd1\x1froot\x1f\n\nA\tfirst.txt\nA\tdir/second.txt\n",
			wantStats: []workspace.CommitStats{{Added: 2}},
		},
		{
			name:      "added modified deleted counted per letter",
			input:     "s1\x1fp1\x1fA\x1fa@x\x1fd1\x1fm1\x1f\n\nM\ta.txt\nA\tb.txt\nD\tc.txt\nM\td.txt\n",
			wantStats: []workspace.CommitStats{{Added: 1, Modified: 2, Deleted: 1}},
		},
		{
			name:      "rename with similarity score buckets as modified",
			input:     "s1\x1fp1\x1fA\x1fa@x\x1fd1\x1frename\x1f\n\nR100\told.txt\tnew.txt\n",
			wantStats: []workspace.CommitStats{{Modified: 1}},
		},
		{
			name:      "copy and typechange bucket as modified",
			input:     "s1\x1fp1\x1fA\x1fa@x\x1fd1\x1fmix\x1f\n\nC90\torig.txt\tcopy.txt\nT\tsymlink\n",
			wantStats: []workspace.CommitStats{{Modified: 2}},
		},
		{
			name:      "merge commit reports zeros even with status lines",
			input:     "m1\x1fpa pb\x1fM\x1fm@x\x1fd1\x1fmerge\x1f\n\nA\tf.txt\n",
			wantStats: []workspace.CommitStats{{}},
		},
		{
			name:      "broken entries without tab are ignored",
			input:     "s1\x1fp1\x1fA\x1fa@x\x1fd1\x1fm1\x1f\n\ngarbage-line\n\tleading-tab.txt\n\nM\tok.txt\n",
			wantStats: []workspace.CommitStats{{Modified: 1}},
		},
		{
			name:      "statuses attributed to their own commit",
			input:     "s1\x1f\x1fA\x1fa@x\x1fd1\x1fm1\x1f\n\nM\ta.txt\n\x1es2\x1fp1\x1fA\x1fa@x\x1fd2\x1fm2\x1f\n\nD\tb.txt\n",
			wantStats: []workspace.CommitStats{{Modified: 1}, {Deleted: 1}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			commits := parseGitHistory(tc.input)
			if len(commits) != len(tc.wantStats) {
				t.Fatalf("len(commits): got %d, want %d", len(commits), len(tc.wantStats))
			}
			for i, want := range tc.wantStats {
				if got := commits[i].Stats; got != want {
					t.Errorf("commits[%d].Stats: got %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

// TestGetGitHistory_StatsBuckets exercises the Stats buckets end-to-end
// against a real repository: root addition (--root), a rename (R →
// modified) and a non-fast-forward merge (zeros).
func TestGetGitHistory_StatsBuckets(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Rename: identical content → R100 → modified bucket.
		runGit(t, dir, "mv", "committed.txt", "renamed.txt")
		runGit(t, dir, "commit", "-m", "rename committed.txt")

		// Branch commit + non-fast-forward merge → real merge commit.
		// Capture the base branch BEFORE switching to feature.
		base := gitDefaultBranch(t, dir)
		runGit(t, dir, "checkout", "-qb", "feature")
		commitFile(t, dir, "feature.txt", "f\n")
		runGit(t, dir, "checkout", "-q", base)
		runGit(t, dir, "merge", "--no-ff", "-m", "merge feature", "feature")

		page, err := f.GetGitHistory(0, 0)
		if err != nil {
			t.Fatalf("GetGitHistory: %v", err)
		}
		if len(page.Commits) != 4 {
			t.Fatalf("len: got %d, want 4", len(page.Commits))
		}

		// Key the bucket expectations by message: the rename and feature
		// commits share a commit second, so their relative order in the
		// log is not deterministic.
		statsByMsg := make(map[string]workspace.CommitStats, len(page.Commits))
		var mergeCommits int
		for _, c := range page.Commits {
			statsByMsg[c.Message] = c.Stats
			if len(c.Parents) >= 2 {
				mergeCommits++
				if c.Stats != (workspace.CommitStats{}) {
					t.Errorf("merge commit %q.Stats: got %+v, want zeros", c.Message, c.Stats)
				}
			}
		}
		if mergeCommits != 1 {
			t.Fatalf("merge commits: got %d, want 1 (the --no-ff merge)", mergeCommits)
		}
		want := map[string]workspace.CommitStats{
			"merge feature":        {},
			"add feature.txt":      {Added: 1},
			"rename committed.txt": {Modified: 1}, // R100 committed.txt → renamed.txt
			"add committed.txt":    {Added: 1},    // root commit via --root
		}
		for msg, wc := range want {
			got, ok := statsByMsg[msg]
			if !ok {
				t.Errorf("commit %q missing from history", msg)
				continue
			}
			if got != wc {
				t.Errorf("commit %q.Stats: got %+v, want %+v", msg, got, wc)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// GetCommitMessage
// ---------------------------------------------------------------------------

func TestClampCommitMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "under the cap passes through",
			in:   strings.Repeat("a", 100),
			want: strings.Repeat("a", 100),
		},
		{
			name: "exactly the cap passes through",
			in:   strings.Repeat("a", commitMessageMaxBytes),
			want: strings.Repeat("a", commitMessageMaxBytes),
		},
		{
			name: "over the cap is cut with an ellipsis",
			in:   strings.Repeat("a", commitMessageMaxBytes+10),
			want: strings.Repeat("a", commitMessageMaxBytes) + "…",
		},
		{
			name: "cut lands on a rune boundary",
			// A 3-byte rune straddles the cap: the cut backs up so no torn
			// byte is returned.
			in:   strings.Repeat("a", commitMessageMaxBytes-1) + "日本",
			want: strings.Repeat("a", commitMessageMaxBytes-1) + "…",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampCommitMessage(tc.in); got != tc.want {
				t.Errorf("clampCommitMessage: got %d bytes, want %d bytes (and equal content)", len(got), len(tc.want))
			}
		})
	}
}

func TestGetCommitMessage_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Multi-line commit message: subject + blank line + body.
		path := filepath.Join(dir, "msg.txt")
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write msg.txt: %v", err)
		}
		runGit(t, dir, "add", "msg.txt")
		runGit(t, dir, "commit", "-m", "subject line\n\nbody line one\nbody line two")

		page, err := f.GetGitHistory(1, 0)
		if err != nil {
			t.Fatalf("GetGitHistory: %v", err)
		}
		sha := page.Commits[0].SHA

		msg, err := f.GetCommitMessage(sha)
		if err != nil {
			t.Fatalf("GetCommitMessage: %v", err)
		}
		want := "subject line\n\nbody line one\nbody line two"
		if msg != want {
			t.Errorf("GetCommitMessage: got %q, want %q", msg, want)
		}
	})
}

func TestGetCommitMessage_CapsOutput(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "big.txt")
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write big.txt: %v", err)
		}
		runGit(t, dir, "add", "big.txt")
		runGit(t, dir, "commit", "-m", strings.Repeat("a", commitMessageMaxBytes*2))

		page, err := f.GetGitHistory(1, 0)
		if err != nil {
			t.Fatalf("GetGitHistory: %v", err)
		}
		msg, err := f.GetCommitMessage(page.Commits[0].SHA)
		if err != nil {
			t.Fatalf("GetCommitMessage: %v", err)
		}
		want := strings.Repeat("a", commitMessageMaxBytes) + "…"
		if msg != want {
			t.Errorf("GetCommitMessage: got %d bytes, want %d (capped with ellipsis)", len(msg), len(want))
		}
	})
}

func TestGetCommitMessage_EmptySHA(t *testing.T) {
	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	if _, err := f.GetCommitMessage(""); err == nil {
		t.Error("GetCommitMessage(\"\"): expected error")
	}
	if _, err := f.GetCommitMessage("   "); err == nil {
		t.Error("GetCommitMessage(\"   \"): expected error")
	}
}

func TestGetCommitMessage_InvalidSha(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if _, err := f.GetCommitMessage("not-a-sha"); err == nil {
			t.Error("GetCommitMessage(not-a-sha): expected error")
		}
		if _, err := f.GetCommitMessage("-n"); err == nil {
			t.Error("GetCommitMessage(-n): expected error")
		}
	})
}

func TestGetCommitMessage_UnknownSha(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Valid SHA shape, absent from the repository: git must fail.
		if _, err := f.GetCommitMessage("deadbeef"); err == nil {
			t.Error("GetCommitMessage(deadbeef): expected error for unknown revision")
		}
	})
}

func TestGetCommitMessage_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	if _, err := f.GetCommitMessage("abcdef1234"); err == nil {
		t.Fatal("GetCommitMessage: expected error when no active project")
	}
}

// ---------------------------------------------------------------------------
// SearchGitHistory — pickaxe search RPC (real git) / parseGitHistoryShas
// ---------------------------------------------------------------------------

// searchFixture builds a small marker history on top of withGitRepo's base
// commit: "add marker.txt" adds an "alpha" line, an unrelated commit never
// touches it, "drop alpha" removes the line again. Newest first:
// drop alpha → unrelated → add marker.txt → base.
func searchFixture(t *testing.T, dir string) {
	t.Helper()
	commitFile(t, dir, "marker.txt", "alpha one\n")
	commitFile(t, dir, "other.txt", "nothing here\n")
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("beta two\n"), 0o644); err != nil {
		t.Fatalf("write marker.txt: %v", err)
	}
	runGit(t, dir, "add", "marker.txt")
	runGit(t, dir, "commit", "-m", "drop alpha")
}

// commitMessages extracts a page's subjects in order for comparisons.
func commitMessages(page *GitHistoryPage) []string {
	got := make([]string, 0, len(page.Commits))
	for _, c := range page.Commits {
		got = append(got, c.Message)
	}
	return got
}

func TestSearchGitHistory_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	if _, err := f.SearchGitHistory("alpha", false, 10, 0); err == nil {
		t.Fatal("SearchGitHistory: expected error when no active project")
	}
}

func TestSearchGitHistory_EmptyPattern(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		for _, pattern := range []string{"", "   "} {
			if _, err := f.SearchGitHistory(pattern, false, 10, 0); err == nil {
				t.Errorf("SearchGitHistory(%q): expected error before git runs", pattern)
			}
		}
	})
}

func TestSearchGitHistory_PickaxeString(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		searchFixture(t, dir)

		page, err := f.SearchGitHistory("alpha", false, 10, 0)
		if err != nil {
			t.Fatalf("SearchGitHistory: %v", err)
		}
		if page.HasMore {
			t.Error("HasMore: got true, want false for an unsaturated page")
		}
		want := []string{"drop alpha", "add marker.txt"}
		if diff := cmp.Diff(want, commitMessages(page)); diff != "" {
			t.Errorf("matching commits mismatch (-want +got):\n%s", diff)
		}
		// Log fields come through parseGitHistory like any history page.
		first := page.Commits[0]
		if first.SHA == "" || first.Author == "" || first.Date == "" || first.Parents == nil {
			t.Errorf("log fields not parsed: %+v", first)
		}
		if first.Stats.Modified != 1 {
			t.Errorf("drop alpha stats: got %+v, want Modified=1", first.Stats)
		}
		if next := page.Commits[1]; next.Stats.Added != 1 {
			t.Errorf("add marker.txt stats: got %+v, want Added=1", next.Stats)
		}
	})
}

func TestSearchGitHistory_PickaxeRegex(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		searchFixture(t, dir)

		// Regex mode: "al.*a" matches the changed lines via -G.
		page, err := f.SearchGitHistory("al.*a", true, 10, 0)
		if err != nil {
			t.Fatalf("SearchGitHistory regex: %v", err)
		}
		want := []string{"drop alpha", "add marker.txt"}
		if diff := cmp.Diff(want, commitMessages(page)); diff != "" {
			t.Errorf("regex matches mismatch (-want +got):\n%s", diff)
		}

		// The same pattern in text mode searches the LITERAL string
		// "al.*a" — no commit ever contains it, so nothing matches. This
		// pins the isRegex flag flip (a -G/-S mixup would fail one half).
		literal, err := f.SearchGitHistory("al.*a", false, 10, 0)
		if err != nil {
			t.Fatalf("SearchGitHistory literal: %v", err)
		}
		if len(literal.Commits) != 0 {
			t.Errorf("literal mode: got %d commits, want 0", len(literal.Commits))
		}
	})
}

func TestSearchGitHistory_RootCommitMatch(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// The scan pickaxes the rev-list window with --no-walk, so every
		// listed commit must still be diffed against its own parent (the
		// root against the empty tree) — a pattern introduced by the very
		// first commit is found, not silently dropped with the traversal.
		page, err := f.SearchGitHistory("v1", false, 10, 0)
		if err != nil {
			t.Fatalf("SearchGitHistory: %v", err)
		}
		want := []string{"add committed.txt"}
		if diff := cmp.Diff(want, commitMessages(page)); diff != "" {
			t.Errorf("root-commit match mismatch (-want +got):\n%s", diff)
		}
		if len(page.Commits) == 1 && page.Commits[0].Stats.Added != 1 {
			t.Errorf("root commit stats: got %+v, want Added=1", page.Commits[0].Stats)
		}
	})
}

func TestSearchGitHistory_CaseInsensitive(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// The frontend's metadata half matches case-insensitively, so the
		// pickaxe half must too (-i): a patch that adds "Alpha" answers a
		// search for "alpha" in BOTH text and regex mode.
		commitFile(t, dir, "case.txt", "Alpha one\n")
		for _, regexMode := range []bool{false, true} {
			page, err := f.SearchGitHistory("alpha", regexMode, 10, 0)
			if err != nil {
				t.Fatalf("SearchGitHistory(regex=%v): %v", regexMode, err)
			}
			want := []string{"add case.txt"}
			if diff := cmp.Diff(want, commitMessages(page)); diff != "" {
				t.Errorf("case-insensitive match (regex=%v) mismatch (-want +got):\n%s", regexMode, diff)
			}
		}
	})
}

func TestSearchGitHistory_Pagination(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Four commits each add occurrences of "needle"; newest first:
		// needle4 → needle3 → needle2 → needle1 → base.
		for _, name := range []string{"needle1", "needle2", "needle3", "needle4"} {
			commitFile(t, dir, name+".txt", "needle\n")
		}

		full, err := f.SearchGitHistory("needle", false, 1000, 0)
		if err != nil {
			t.Fatalf("SearchGitHistory full: %v", err)
		}
		if len(full.Commits) != 4 || full.HasMore {
			t.Fatalf("full page: got %d commits hasMore=%v, want 4/false", len(full.Commits), full.HasMore)
		}

		page1, err := f.SearchGitHistory("needle", false, 2, 0)
		if err != nil {
			t.Fatalf("SearchGitHistory page1: %v", err)
		}
		if len(page1.Commits) != 2 || !page1.HasMore || page1.NextSkip != 2 {
			t.Fatalf("page1: got %d commits skip=%d hasMore=%v, want 2/2/true",
				len(page1.Commits), page1.NextSkip, page1.HasMore)
		}
		page2, err := f.SearchGitHistory("needle", false, 2, page1.NextSkip)
		if err != nil {
			t.Fatalf("SearchGitHistory page2: %v", err)
		}
		if len(page2.Commits) != 2 || page2.HasMore || page2.NextSkip != 4 {
			t.Fatalf("page2: got %d commits skip=%d hasMore=%v, want 2/4/false",
				len(page2.Commits), page2.NextSkip, page2.HasMore)
		}

		// Pages are disjoint and reconstruct the single-shot order.
		combined := append(append([]string{}, commitMessages(page1)...), commitMessages(page2)...)
		if diff := cmp.Diff(commitMessages(full), combined); diff != "" {
			t.Errorf("paged result mismatch (-full +paged):\n%s", diff)
		}

		// A skip past the end is a clean empty page.
		tail, err := f.SearchGitHistory("needle", false, 2, 4)
		if err != nil {
			t.Fatalf("SearchGitHistory tail: %v", err)
		}
		if len(tail.Commits) != 0 || tail.HasMore {
			t.Errorf("tail: got %d commits hasMore=%v, want empty/false", len(tail.Commits), tail.HasMore)
		}
	})
}

func TestSearchGitHistory_InvalidRegex(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// git rejects "[" as a regex; the error must surface, not a page.
		if _, err := f.SearchGitHistory("[", true, 10, 0); err == nil {
			t.Error("SearchGitHistory(\"[\", regex): expected git error")
		}
	})
}

func TestParseGitHistoryShas(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", []string{}},
		{"whitespace only", " \n\t\n", []string{}},
		{"single sha", "abc123\n", []string{"abc123"}},
		{"order preserved", "aaa\nbbb\nccc", []string{"aaa", "bbb", "ccc"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, parseGitHistoryShas(tc.input)); diff != "" {
				t.Errorf("parseGitHistoryShas(%q) mismatch (-want +got):\n%s", tc.input, diff)
			}
		})
	}
}
