package backend

import (
	"testing"

	"github.com/google/go-cmp/cmp"
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
	if _, err := f.GetGitHistory(); err == nil {
		t.Fatal("GetGitHistory: expected error when no active project")
	}
}

func TestGetGitHistory_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// withGitRepo commits committed.txt (1 commit). Add a second.
		commitFile(t, dir, "b.txt", "b\n")

		history, err := f.GetGitHistory()
		if err != nil {
			t.Fatalf("GetGitHistory: %v", err)
		}
		if len(history) != 2 {
			t.Fatalf("len: got %d, want 2", len(history))
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
	})
}
