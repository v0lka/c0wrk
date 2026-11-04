package vectorindex

// Ignore-policy plumbing for the indexing walks (vector_index.respect_gitignore
// / respect_aiignore / exclude_patterns / index_submodules): the honoured
// ignore-file set, the layered exclude patterns, and the submodule toggle must
// reach every walk identically — IndexFull's walkProjectFiles, the duplicated
// walk in ValidateCollection (which turns "no longer walked" into "deleted",
// evicting previously-indexed files), and the watcher's isIndexablePath
// ancestor check. Synthetic fixtures pin the mechanics; gittest fixtures with a
// genuine `git submodule add` checkout pin the real on-disk shape (.git as a
// regular gitdir-pointer file).

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
	"github.com/v0lka/sp4rk/ignore"
)

// writeFileT creates path (with parents) and writes content, failing the test
// on the first error.
func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeSubmoduleWorkspace plants a workspace whose sub/ directory carries a
// regular .git file — the on-disk shape of a checked-out submodule (and of a
// linked worktree). Nested-repo detection (isNestedRepoDir) is stat-only, so
// the pointer body is documentary.
func writeSubmoduleWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "main.go"), "package main\n")
	writeFileT(t, filepath.Join(root, "sub", "code.go"), "package sub\n")
	writeFileT(t, filepath.Join(root, "sub", ".git"), "gitdir: ../.git/modules/sub\n")
	writeFileT(t, filepath.Join(root, "sub", "deep", "nested.go"), "package deep\n")
	return root
}

// writeSelectionWorkspace plants a workspace with one .gitignore-covered and
// one .aiignore-covered file, for ignore-file selection tests.
func writeSelectionWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, ".gitignore"), "gitignored.log\n")
	writeFileT(t, filepath.Join(root, ".aiignore"), "aiignored.log\n")
	writeFileT(t, filepath.Join(root, "gitignored.log"), "git content\n")
	writeFileT(t, filepath.Join(root, "aiignored.log"), "ai content\n")
	writeFileT(t, filepath.Join(root, "main.go"), "package main\n")
	return root
}

// indexedRelFiles snapshots the collection's file set as workspace-relative
// slash paths.
func indexedRelFiles(t *testing.T, svc *Service, root string) map[string]bool {
	t.Helper()
	files, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	out := make(map[string]bool, len(files))
	for key := range files {
		// Sidecar keys are root-relative occurrence coordinates
		// (ADR-104 L2) when the indexing pass bound a workspace root;
		// absolute only for legacy/unbound services.
		if !filepath.IsAbs(key) {
			out[filepath.ToSlash(key)] = true
			continue
		}
		rel, relErr := filepath.Rel(root, key)
		if relErr != nil {
			t.Fatalf("Rel(%q, %q): %v", root, key, relErr)
		}
		out[filepath.ToSlash(rel)] = true
	}
	return out
}

// newPolicyIndexer builds an Indexer over svc with the fake chunk/hash
// functions; mutate patches the policy fields under test.
func newPolicyIndexer(svc *Service, mutate func(*IndexerConfig)) *Indexer {
	cfg := IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewIndexer(cfg)
}

func walkRelSet(t *testing.T, files []string, root string) map[string]bool {
	t.Helper()
	out := make(map[string]bool, len(files))
	for _, p := range files {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatalf("Rel(%q, %q): %v", root, p, err)
		}
		out[filepath.ToSlash(rel)] = true
	}
	return out
}

// --- walk-level submodule policy -------------------------------------------

func TestWalkProjectFiles_SubmoduleSkippedByDefault(t *testing.T) {
	root := writeSubmoduleWorkspace(t)

	got, err := walkProjectFiles(root, noopIgnoreChecker{}, 1<<20, false)
	if err != nil {
		t.Fatalf("walkProjectFiles: %v", err)
	}
	rel := walkRelSet(t, got, root)
	if !rel["main.go"] {
		t.Errorf("main.go missing from walk: %v", rel)
	}
	for forbidden := range map[string]bool{
		"sub/code.go":        true,
		"sub/deep/nested.go": true,
	} {
		if rel[forbidden] {
			t.Errorf("walk included submodule file %q", forbidden)
		}
	}
}

func TestWalkProjectFiles_SubmoduleIndexedWhenIncluded(t *testing.T) {
	root := writeSubmoduleWorkspace(t)

	got, err := walkProjectFiles(root, noopIgnoreChecker{}, 1<<20, true)
	if err != nil {
		t.Fatalf("walkProjectFiles: %v", err)
	}
	rel := walkRelSet(t, got, root)
	for _, want := range []string{"main.go", "sub/code.go", "sub/deep/nested.go"} {
		if !rel[want] {
			t.Errorf("expected %q in walk: %v", want, rel)
		}
	}
	// The pointer .git file itself stays excluded by the hidden-name guard.
	if rel["sub/.git"] {
		t.Error("walk included the submodule .git pointer file")
	}
}

// TestWalkProjectFiles_PointerGitAtWalkRootStillIndexed pins the root
// exemption: a linked-worktree workspace (pointer .git at its own root) is a
// legitimate index root and must not prune itself as a "submodule".
func TestWalkProjectFiles_PointerGitAtWalkRootStillIndexed(t *testing.T) {
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, ".git"), "gitdir: /elsewhere/private\n")
	writeFileT(t, filepath.Join(root, "main.go"), "package main\n")

	got, err := walkProjectFiles(root, noopIgnoreChecker{}, 1<<20, false)
	if err != nil {
		t.Fatalf("walkProjectFiles: %v", err)
	}
	rel := walkRelSet(t, got, root)
	if !rel["main.go"] {
		t.Errorf("main.go missing from walk of a pointer-.git root: %v", rel)
	}
}

// --- watcher-facing indexability -------------------------------------------

func TestIsIndexablePath_SubmodulePolicy(t *testing.T) {
	root := writeSubmoduleWorkspace(t)
	cases := []struct {
		rel     string
		wantOff bool // expected with submodules excluded (the default)
		wantOn  bool // expected with submodules included
	}{
		{"main.go", true, true},
		{"sub/code.go", false, true},
		{"sub/deep/nested.go", false, true},
		{"sub", false, true}, // the submodule directory event itself
	}
	for _, tc := range cases {
		abs := filepath.Join(root, filepath.FromSlash(tc.rel))
		if got := isIndexablePath(abs, root, noopIgnoreChecker{}, false); got != tc.wantOff {
			t.Errorf("isIndexablePath(%q, submodules off) = %v, want %v", tc.rel, got, tc.wantOff)
		}
		if got := isIndexablePath(abs, root, noopIgnoreChecker{}, true); got != tc.wantOn {
			t.Errorf("isIndexablePath(%q, submodules on) = %v, want %v", tc.rel, got, tc.wantOn)
		}
	}
}

// --- ignore-file selection and the noop shortcut ---------------------------

func TestBuildIgnoreChecker_FileSelection(t *testing.T) {
	root := writeSelectionWorkspace(t)
	gitFile := filepath.Join(root, "gitignored.log")
	aiFile := filepath.Join(root, "aiignored.log")

	// Zero options = the historical default: both files honoured.
	both, err := buildIgnoreChecker(root, ignore.Options{})
	if err != nil {
		t.Fatalf("buildIgnoreChecker(zero): %v", err)
	}
	if !both.Ignored(gitFile, false) || !both.Ignored(aiFile, false) {
		t.Error("zero Options must honour both .gitignore and .aiignore")
	}

	// Only .gitignore honoured: .aiignore rules lose their effect.
	gitOnly, err := buildIgnoreChecker(root, ignore.Options{IgnoreFiles: []string{".gitignore"}})
	if err != nil {
		t.Fatalf("buildIgnoreChecker(.gitignore only): %v", err)
	}
	if !gitOnly.Ignored(gitFile, false) {
		t.Error(".gitignore rule not honoured under IgnoreFiles=[.gitignore]")
	}
	if gitOnly.Ignored(aiFile, false) {
		t.Error(".aiignore rule honoured under IgnoreFiles=[.gitignore]")
	}

	// Only .aiignore honoured.
	aiOnly, err := buildIgnoreChecker(root, ignore.Options{IgnoreFiles: []string{".aiignore"}})
	if err != nil {
		t.Fatalf("buildIgnoreChecker(.aiignore only): %v", err)
	}
	if aiOnly.Ignored(gitFile, false) {
		t.Error(".gitignore rule honoured under IgnoreFiles=[.aiignore]")
	}
	if !aiOnly.Ignored(aiFile, false) {
		t.Error(".aiignore rule not honoured under IgnoreFiles=[.aiignore]")
	}

	// Neither honoured and no extra patterns: the noop checker is returned
	// without building a resolver at all.
	none, err := buildIgnoreChecker(root, ignore.Options{IgnoreFiles: []string{}})
	if err != nil {
		t.Fatalf("buildIgnoreChecker(no files): %v", err)
	}
	if _, ok := none.(noopIgnoreChecker); !ok {
		t.Errorf("buildIgnoreChecker(no files, no patterns) = %T, want noopIgnoreChecker", none)
	}
	if none.Ignored(gitFile, false) || none.Ignored(aiFile, false) {
		t.Error("noop checker must report nothing ignored")
	}

	// An unknown ignore-file entry fails the build and falls open to noop.
	bad, err := buildIgnoreChecker(root, ignore.Options{IgnoreFiles: []string{".cvsignore"}})
	if err == nil {
		t.Error("buildIgnoreChecker with unknown IgnoreFiles entry must fail")
	}
	if _, ok := bad.(noopIgnoreChecker); !ok {
		t.Errorf("failed build returned %T, want fail-open noopIgnoreChecker", bad)
	}
}

// --- end-to-end over the real Indexer/Service pipeline ---------------------

// TestIndexFull_RespectGitignoreOffIndexesIgnoredFiles pins vector_index.
// respect_gitignore=false: with no ignore file honoured the gitignored file is
// indexed; the default (nil IgnoreFiles) keeps skipping it.
func TestIndexFull_RespectGitignoreOffIndexesIgnoredFiles(t *testing.T) {
	ws := t.TempDir()
	writeFileT(t, filepath.Join(ws, ".gitignore"), "ignored.go\n")
	writeFileT(t, filepath.Join(ws, "ignored.go"), "package ignored\n")
	writeFileT(t, filepath.Join(ws, "main.go"), "package main\n")

	t.Run("respect on (default)", func(t *testing.T) {
		svc := setupTestService(t)
		if err := newPolicyIndexer(svc, nil).IndexFull(context.Background(), ws); err != nil {
			t.Fatalf("IndexFull: %v", err)
		}
		files := indexedRelFiles(t, svc, ws)
		if !files["main.go"] {
			t.Errorf("main.go missing: %v", files)
		}
		if files["ignored.go"] {
			t.Errorf("gitignored file indexed under the default policy: %v", files)
		}
	})

	t.Run("respect_gitignore=false", func(t *testing.T) {
		svc := setupTestService(t)
		idx := newPolicyIndexer(svc, func(c *IndexerConfig) { c.IgnoreFiles = []string{} })
		if err := idx.IndexFull(context.Background(), ws); err != nil {
			t.Fatalf("IndexFull: %v", err)
		}
		files := indexedRelFiles(t, svc, ws)
		if !files["main.go"] || !files["ignored.go"] {
			t.Errorf("respect off must index the gitignored file: %v", files)
		}
	})
}

// TestIndexIncremental_ExcludePatternsEvictIndexedFiles pins vector_index.
// exclude_patterns: files a pattern covers never enter a walk, and a file
// indexed before the pattern existed is evicted by the next incremental pass
// (ValidateCollection reports it deleted), with a clean no-op follow-up.
func TestIndexIncremental_ExcludePatternsEvictIndexedFiles(t *testing.T) {
	ws := t.TempDir()
	writeFileT(t, filepath.Join(ws, "keep.go"), "package keep\n")
	writeFileT(t, filepath.Join(ws, "drop.log"), "log line\n")

	svc := setupTestService(t)

	if err := newPolicyIndexer(svc, nil).IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("initial IndexFull: %v", err)
	}
	files := indexedRelFiles(t, svc, ws)
	if !files["keep.go"] || !files["drop.log"] {
		t.Fatalf("initial pass must index both files: %v", files)
	}

	idx := newPolicyIndexer(svc, func(c *IndexerConfig) { c.ExcludePatterns = []string{"*.log"} })
	if err := idx.IndexIncremental(context.Background(), ws); err != nil {
		t.Fatalf("IndexIncremental with exclude pattern: %v", err)
	}
	files = indexedRelFiles(t, svc, ws)
	if !files["keep.go"] {
		t.Errorf("keep.go evicted by unrelated exclude pattern: %v", files)
	}
	if files["drop.log"] {
		t.Errorf("drop.log must be evicted from the index by the exclude pattern: %v", files)
	}

	// Follow-up pass is a no-op: the excluded file is neither walked nor
	// stored, so the incremental pass must not churn on it forever.
	if err := idx.IndexIncremental(context.Background(), ws); err != nil {
		t.Fatalf("follow-up IndexIncremental: %v", err)
	}
	files = indexedRelFiles(t, svc, ws)
	if len(files) != 1 || !files["keep.go"] {
		t.Errorf("follow-up pass changed the file set: %v", files)
	}
}

// TestSubmodulePolicy_IndexFullAndIncremental drives the full Indexer pipeline
// across both toggle states: off prunes the submodule, on indexes it, and
// flipping back off evicts its files through the incremental pass (the
// collection.go walk applies the same pruning).
func TestSubmodulePolicy_IndexFullAndIncremental(t *testing.T) {
	ws := writeSubmoduleWorkspace(t)
	svc := setupTestService(t)

	if err := newPolicyIndexer(svc, nil).IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull (submodules off): %v", err)
	}
	files := indexedRelFiles(t, svc, ws)
	if !files["main.go"] {
		t.Errorf("main.go missing: %v", files)
	}
	if files["sub/code.go"] {
		t.Errorf("submodule file indexed with index_submodules=false: %v", files)
	}

	idxOn := newPolicyIndexer(svc, func(c *IndexerConfig) { c.IncludeSubmodules = true })
	if err := idxOn.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull (submodules on): %v", err)
	}
	files = indexedRelFiles(t, svc, ws)
	if !files["sub/code.go"] || !files["sub/deep/nested.go"] {
		t.Errorf("submodule files missing with index_submodules=true: %v", files)
	}
	if files["sub/.git"] {
		t.Errorf("submodule .git pointer file indexed: %v", files)
	}

	if err := newPolicyIndexer(svc, nil).IndexIncremental(context.Background(), ws); err != nil {
		t.Fatalf("IndexIncremental (submodules off again): %v", err)
	}
	files = indexedRelFiles(t, svc, ws)
	if files["sub/code.go"] || files["sub/deep/nested.go"] {
		t.Errorf("submodule files must be evicted after the toggle flips off: %v", files)
	}
	if !files["main.go"] {
		t.Errorf("main.go evicted: %v", files)
	}
}

// --- real-git fixtures ------------------------------------------------------

// realSubmoduleFixture creates a superproject with a genuine `git submodule
// add` checkout at vendor/dep. protocol.file.allow=always is required since
// git 2.38.1 for local-path submodule URLs.
func realSubmoduleFixture(t *testing.T) string {
	t.Helper()
	gittest.RequireGit(t)
	tmp := gittest.TempDir(t)
	sub := gittest.InitRepo(t, filepath.Join(tmp, "dep"), "package dep\n")
	super := gittest.InitRepo(t, filepath.Join(tmp, "super"), "hello\n")
	super.Git(t, "-c", "protocol.file.allow=always", "submodule", "add", sub.Root, filepath.Join("vendor", "dep"))
	super.Git(t, "commit", "-m", "add submodule")

	// Fixture sanity: the submodule checkout's .git is a regular gitdir
	// pointer file, the shape isNestedRepoDir detects.
	pointer := filepath.Join(super.Root, "vendor", "dep", ".git")
	fi, err := os.Stat(pointer)
	if err != nil {
		t.Fatalf("fixture sanity: stat %s: %v", pointer, err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("fixture sanity: %s must be a regular pointer file, got mode %v", pointer, fi.Mode())
	}
	if !isNestedRepoDir(filepath.Join(super.Root, "vendor", "dep")) {
		t.Fatal("fixture sanity: isNestedRepoDir must detect the real submodule checkout")
	}
	return super.Root
}

func TestWalkProjectFiles_RealGitSubmodule(t *testing.T) {
	root := realSubmoduleFixture(t)
	// walkRelSet keys are ToSlash-normalized, so the expected key is spelled
	// with literal slashes (filepath.Join would yield a backslash key on Windows).
	subFile := "vendor/dep/file.txt"

	got, err := walkProjectFiles(root, testIgnoreChecker(t, root), DefaultMaxIndexableFileSize, false)
	if err != nil {
		t.Fatalf("walkProjectFiles (off): %v", err)
	}
	rel := walkRelSet(t, got, root)
	if !rel["file.txt"] {
		t.Errorf("superproject file missing: %v", rel)
	}
	for p := range rel {
		if strings.HasPrefix(p, "vendor/dep/") {
			t.Errorf("submodule path %q in walk with index_submodules=false", p)
		}
	}

	got, err = walkProjectFiles(root, testIgnoreChecker(t, root), DefaultMaxIndexableFileSize, true)
	if err != nil {
		t.Fatalf("walkProjectFiles (on): %v", err)
	}
	rel = walkRelSet(t, got, root)
	if !rel[subFile] {
		t.Errorf("submodule file missing with index_submodules=true: %v", rel)
	}
}

func TestSubmodulePolicy_RealGitSubmoduleIndex(t *testing.T) {
	root := realSubmoduleFixture(t)
	svc := setupTestService(t)

	if err := newPolicyIndexer(svc, nil).IndexFull(context.Background(), root); err != nil {
		t.Fatalf("IndexFull (off): %v", err)
	}
	files := indexedRelFiles(t, svc, root)
	if files["vendor/dep/file.txt"] {
		t.Errorf("real submodule file indexed with index_submodules=false: %v", files)
	}
	if !files["file.txt"] {
		t.Errorf("superproject file missing: %v", files)
	}

	idx := newPolicyIndexer(svc, func(c *IndexerConfig) { c.IncludeSubmodules = true })
	if err := idx.IndexFull(context.Background(), root); err != nil {
		t.Fatalf("IndexFull (on): %v", err)
	}
	files = indexedRelFiles(t, svc, root)
	if !files["vendor/dep/file.txt"] {
		t.Errorf("real submodule file missing with index_submodules=true: %v", files)
	}
}

// --- ManagerConfig plumbing -------------------------------------------------

// TestNewManager_ForwardsIgnorePolicyToIndexer pins the ManagerConfig →
// initProject → NewIndexer forwarding: the fields the desktop wiring will set
// from vector_index.* reach the published Indexer and take effect.
func TestNewManager_ForwardsIgnorePolicyToIndexer(t *testing.T) {
	ws := writeSubmoduleWorkspace(t)

	mgr, err := NewManager(ManagerConfig{
		EmbeddingFunc:     fakeEmbeddingFunc(),
		IgnoreFiles:       []string{".aiignore"},
		ExcludePatterns:   []string{"/dist"},
		IncludeSubmodules: true,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(mgr.Shutdown)

	if err := mgr.SwitchProject("policy-fwd", ws, filepath.Join(t.TempDir(), "vi"), ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	if err := mgr.Service().WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	mgr.mu.RLock()
	idx := mgr.indexer
	mgr.mu.RUnlock()
	if idx == nil {
		t.Fatal("indexer not published after readiness")
	}
	if !idx.includeSubmodules {
		t.Error("ManagerConfig.IncludeSubmodules not forwarded to the Indexer")
	}
	if !slices.Equal(idx.ignoreOptions.IgnoreFiles, []string{".aiignore"}) {
		t.Errorf("IgnoreFiles forwarded as %v, want [.aiignore]", idx.ignoreOptions.IgnoreFiles)
	}
	if !slices.Equal(idx.ignoreOptions.ExtraPatterns, []string{"/dist"}) {
		t.Errorf("ExcludePatterns forwarded as %v, want [/dist]", idx.ignoreOptions.ExtraPatterns)
	}

	// Functional pin: the submodule's file is indexed because the toggle was
	// forwarded.
	files := indexedRelFiles(t, mgr.Service(), ws)
	if !files["sub/code.go"] {
		t.Errorf("submodule file missing despite IncludeSubmodules=true: %v", files)
	}
}

// TestNewManager_DefaultPolicySkipsSubmodules pins the zero-value Manager
// defaults: both ignore files honoured, no extra patterns, submodules pruned.
func TestNewManager_DefaultPolicySkipsSubmodules(t *testing.T) {
	ws := writeSubmoduleWorkspace(t)

	mgr, err := NewManager(ManagerConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(mgr.Shutdown)

	if err := mgr.SwitchProject("policy-default", ws, filepath.Join(t.TempDir(), "vi"), ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	if err := mgr.Service().WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	files := indexedRelFiles(t, mgr.Service(), ws)
	if !files["main.go"] {
		t.Errorf("main.go missing under default policy: %v", files)
	}
	if files["sub/code.go"] {
		t.Errorf("submodule file indexed under default policy: %v", files)
	}

	mgr.mu.RLock()
	idx := mgr.indexer
	mgr.mu.RUnlock()
	if idx == nil {
		t.Fatal("indexer not published after readiness")
	}
	if idx.includeSubmodules {
		t.Error("default IncludeSubmodules must be false")
	}
	if idx.ignoreOptions.IgnoreFiles != nil || idx.ignoreOptions.ExtraPatterns != nil {
		t.Errorf("default ignore policy must be zero-value, got %+v", idx.ignoreOptions)
	}
}
