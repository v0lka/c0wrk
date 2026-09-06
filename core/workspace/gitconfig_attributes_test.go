package workspace

// Scanner tests for the post-v0.7.3 review fixes [1] and [2]:
//
//   - [1]a/[1]c: .git/info/attributes and core.attributesFile are attribute
//     routing sources attr.tree does not cover; they are scanned and every
//     routed driver name is neutralized by -c overrides (which beat file
//     config wherever the driver is defined — including included files). A
//     routing source that exists but cannot be scanned fails the whole scan
//     closed: the attributes mechanism has no config kill-switch.
//   - [1]d: the attr.tree empty-tree constant is selected from the
//     repository's object format (SHA-1 vs SHA-256); on SHA-256 repositories
//     the SHA-1 hash is a verified silent no-op.
//   - [2]a: a linked worktree's config is the COMMON config plus the
//     config.worktree overlay (when extensions.worktreeConfig is enabled),
//     resolved through the worktree gitdir's commondir file, with git's
//     layering (worktree value wins) semantics.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// worktreeFixture builds a main repository plus a linked worktree and
// returns (main repo, worktree root).
func worktreeFixture(t *testing.T) (repo *gittest.Repo, worktree string) {
	t.Helper()
	repo = gittest.InitRepo(t, filepath.Join(t.TempDir(), "main"), "hello\n")
	worktree = filepath.Join(t.TempDir(), "wt")
	repo.Git(t, "worktree", "add", worktree, "-b", "wtbranch")
	return repo, worktree
}

// wantOverride asserts the neutralizing set equals want (sorted argv form).
func wantOverride(t *testing.T, info *GitConfigInfo, want ...string) {
	t.Helper()
	got := overrideArgvs(info)
	if !slices.Equal(got, want) {
		t.Errorf("overrides = %v, want %v", got, want)
	}
}

func TestScanGitConfig_WorktreeScansCommonConfig(t *testing.T) {
	repo, wt := worktreeFixture(t)
	// The canary filter lives in the COMMON config — invisible to the old
	// scan, which looked for the nonexistent worktrees/<n>/config.
	repo.AppendConfig(t, "[filter \"canary\"]\n\tprocess = /tmp/evil.sh\n\tclean = /tmp/evil.sh\n")

	info, err := ScanGitConfig(wt)
	if err != nil {
		t.Fatalf("ScanGitConfig(worktree): %v", err)
	}
	mainGitDir := filepath.Join(evalDir(t, repo.Root), ".git")
	if info.CommonDir != mainGitDir {
		t.Errorf("CommonDir = %q, want %q", info.CommonDir, mainGitDir)
	}
	if info.GitDir == mainGitDir || !strings.HasPrefix(filepath.Base(info.GitDir), "wt") {
		t.Errorf("GitDir = %q, want the per-worktree gitdir under %s/worktrees/", info.GitDir, mainGitDir)
	}
	f := finding(t, info, "filter.canary.process")
	if f.Value != "/tmp/evil.sh" {
		t.Errorf("filter.canary.process = %q", f.Value)
	}
	// Narrow mode ([56]): the visible filter is neutralized by its per-name
	// pins (which cover every routing source), so no blanket attr.tree kill
	// is derived.
	wantOverride(t, info,
		"filter.canary.clean=cat",
		"filter.canary.process=",
		"filter.canary.smudge=cat")
	if info.Clean() {
		t.Error("worktree over a hostile common config must not be Clean")
	}
}

func TestScanGitConfig_WorktreeConfigOverlayMerge(t *testing.T) {
	repo, wt := worktreeFixture(t)
	repo.AppendConfig(t,
		"[core]\n\tattributesFile = /tmp/from-common\n[extensions]\n\tworktreeConfig = true\n")
	wtGitDir := filepath.Join(evalDir(t, repo.Root), ".git", "worktrees", "wt")
	if err := os.MkdirAll(wtGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wtConfig := "[core]\n\tattributesFile = /tmp/from-worktree\n\tfsmonitor = /tmp/evil-wt-fsmonitor.sh\n"
	if err := os.WriteFile(filepath.Join(wtGitDir, "config.worktree"), []byte(wtConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := ScanGitConfig(wt)
	if err != nil {
		t.Fatalf("ScanGitConfig(worktree): %v", err)
	}
	if info.WorktreeConfigPath == "" {
		t.Fatal("config.worktree was not scanned despite extensions.worktreeConfig=true")
	}
	// Overlay keys are findings; the duplicated attributesFile key resolves
	// to the worktree (last) value, mirroring git's layering.
	finding(t, info, "core.attributesfile")
	if info.attributesFilePath != "/tmp/from-worktree" {
		t.Errorf("merged core.attributesFile = %q, want the worktree value /tmp/from-worktree", info.attributesFilePath)
	}
	finding(t, info, "core.fsmonitor")
	// One finding per unique key despite both files defining attributesFile.
	n := 0
	for i := range info.Findings {
		if info.Findings[i].FullKey == "core.attributesfile" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("core.attributesFile findings = %d, want 1 (merge last-wins)", n)
	}
}

func TestScanGitConfig_WorktreeConfigIgnoredWithoutExtension(t *testing.T) {
	repo, wt := worktreeFixture(t)
	wtGitDir := filepath.Join(evalDir(t, repo.Root), ".git", "worktrees", "wt")
	if err := os.WriteFile(filepath.Join(wtGitDir, "config.worktree"), []byte("[core]\n\tfsmonitor = /tmp/evil.sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := ScanGitConfig(wt)
	if err != nil {
		t.Fatalf("ScanGitConfig(worktree): %v", err)
	}
	if info.WorktreeConfigPath != "" {
		t.Error("config.worktree scanned although extensions.worktreeConfig is disabled")
	}
	for i := range info.Findings {
		if info.Findings[i].FullKey == "core.fsmonitor" {
			t.Error("git ignores config.worktree without the extension; the scan must not report it")
		}
	}
}

func TestScanGitConfig_WorktreeCommonInfoAttributes(t *testing.T) {
	repo, wt := worktreeFixture(t)
	// Verified against git 2.50.1: the COMMON .git/info/attributes routes
	// files in every linked worktree.
	infoPath := filepath.Join(repo.GitDir(), "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, []byte("*.txt filter=hidden\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := ScanGitConfig(wt)
	if err != nil {
		t.Fatalf("ScanGitConfig(worktree): %v", err)
	}
	routed := finding(t, info, "info/attributes:filter=hidden")
	if routed.Kind != GitConfigFindingAttrRouting || routed.Subsection != "hidden" {
		t.Errorf("routing finding = %+v", routed)
	}
	// Routing-only findings pin the routed names without forcing attr.tree:
	// the name overrides cover every routing source (including in-tree),
	// and not engaging attr.tree preserves benign attributes (review [56]).
	wantOverride(t, info,
		"filter.hidden.clean=cat",
		"filter.hidden.process=",
		"filter.hidden.smudge=cat")
}

func TestScanGitConfig_BrokenWorktreeFailsClosed(t *testing.T) {
	// A gitdir with the worktree marker but no commondir: the common config
	// it hides cannot be modeled → fail closed.
	root := t.TempDir()
	fakeGitDir := filepath.Join(root, "fakegit")
	if err := os.MkdirAll(fakeGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeGitDir, "gitdir"), []byte(root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+fakeGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanGitConfig(root); err == nil {
		t.Fatal("expected fail-closed error for a worktree gitdir without commondir")
	}
}

func TestScanGitConfig_CommondirNotAFileFailsClosed(t *testing.T) {
	root := writeTempRepo(t, "gitdir: .fakegit\n", false, "")
	fakeGitDir := filepath.Join(root, ".fakegit")
	if err := os.MkdirAll(filepath.Join(fakeGitDir, "commondir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanGitConfig(root); err == nil {
		t.Fatal("expected fail-closed error for a non-regular commondir")
	}
}

func TestScanGitConfig_CommondirTargetMissingFailsClosed(t *testing.T) {
	root := writeTempRepo(t, "gitdir: .fakegit\n", false, "")
	fakeGitDir := filepath.Join(root, ".fakegit")
	if err := os.MkdirAll(fakeGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeGitDir, "commondir"), []byte("../../nowhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeGitDir, "gitdir"), []byte(root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanGitConfig(root); err == nil {
		t.Fatal("expected fail-closed error for a commondir pointing nowhere")
	}
}

// --- [1]a: attribute routing sources ---

func TestScanGitConfig_InfoAttributesRoutingNeutered(t *testing.T) {
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")
	// Include-hidden filter: extra.conf is where git finds the driver, so
	// the scan never sees the definition — only the routing.
	repo.AppendConfig(t, fmt.Sprintf("[include]\n\tpath = %s\n", filepath.Join(repo.Root, "extra.conf")))
	if err := os.WriteFile(filepath.Join(repo.Root, "extra.conf"),
		[]byte("[filter \"x\"]\n\tclean = /tmp/evil.sh\n\tsmudge = /tmp/evil.sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	infoPath := filepath.Join(repo.GitDir(), "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, []byte("*.txt filter=x\n*.bin diff=tc merge=drv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := ScanGitConfig(repo.Root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	if len(info.Includes) != 1 {
		t.Fatalf("includes = %+v, want the one include", info.Includes)
	}
	routed := finding(t, info, "info/attributes:filter=x")
	if routed.Subsection != "x" || routed.Value != "*.txt" || routed.Line != 1 {
		t.Errorf("filter routing finding = %+v", routed)
	}
	finding(t, info, "info/attributes:merge=drv")
	finding(t, info, "info/attributes:diff=tc")
	// Closure of every routing source: attr.tree (in-tree), empty
	// core.attributesFile (that source), and name pins for the routed
	// drivers (info/attributes itself has no kill-switch). The diff pin
	// covers BOTH command-bearing keys of the driver — textconv and the
	// command the git diff porcelain executes by default (post-v0.7.3
	// review [55]).
	wantOverride(t, info,
		"attr.tree="+EmptyTreeSHA1,
		"core.askPass=",
		"core.attributesFile=",
		"core.sshCommand=ssh",
		"credential.helper=",
		"diff.external=",
		"diff.tc.command=",
		"diff.tc.textconv=cat",
		"filter.x.clean=cat",
		"filter.x.process=",
		"filter.x.smudge=cat",
		"merge.drv.driver=false %O %A %B")
}

func TestScanGitConfig_AttributesFileRouting(t *testing.T) {
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")
	attrsDir := t.TempDir()
	attrsPath := filepath.Join(attrsDir, "attrs")
	if err := os.WriteFile(attrsPath, []byte("*.txt filter=q\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.AppendConfig(t, "[core]\n\tattributesFile = "+attrsPath+"\n")

	info, err := ScanGitConfig(repo.Root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	f := finding(t, info, "core.attributesfile")
	if f.Kind != GitConfigFindingAttributesFile || f.Value != attrsPath {
		t.Errorf("attributesFile finding = %+v", f)
	}
	routed := finding(t, info, "core.attributesFile:filter=q")
	if routed.Subsection != "q" {
		t.Errorf("routing finding = %+v", routed)
	}
	// The routing source is disabled (empty override) and the routed name is
	// pinned; attr.tree stays disengaged (no in-tree vector exists without
	// an include or a visible armed key, either of which would trigger it).
	wantOverride(t, info,
		"core.attributesFile=",
		"filter.q.clean=cat",
		"filter.q.process=",
		"filter.q.smudge=cat")
}

func TestScanGitConfig_AttributesFileRelativeResolvesAgainstRepo(t *testing.T) {
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")
	if err := os.MkdirAll(filepath.Join(repo.Root, "attrs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Root, "attrs", "evil.txt"), []byte("*.txt filter=rel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.AppendConfig(t, "[core]\n\tattributesFile = attrs/evil.txt\n")
	info, err := ScanGitConfig(repo.Root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	finding(t, info, "core.attributesFile:filter=rel")
}

func TestScanGitConfig_AttributesFileMissingIsInertButReported(t *testing.T) {
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")
	repo.AppendConfig(t, "[core]\n\tattributesFile = /nonexistent/attrs\n")
	info, err := ScanGitConfig(repo.Root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	finding(t, info, "core.attributesfile")
	if len(info.Findings) != 1 {
		t.Errorf("missing routing file should add no routing findings: %+v", info.Findings)
	}
	wantOverride(t, info, "core.attributesFile=")
}

func TestScanGitConfig_InfoAttributesUnscannableFailsClosed(t *testing.T) {
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")
	// A directory where info/attributes should be: not a regular file, so
	// the scan must refuse rather than run git with invisible routing.
	infoPath := filepath.Join(repo.GitDir(), "info", "attributes")
	if err := os.MkdirAll(infoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanGitConfig(repo.Root); err == nil || !strings.Contains(err.Error(), "fail closed") {
		t.Fatalf("expected fail-closed error for unscannable info/attributes, got %v", err)
	}
}

func TestScanGitConfig_AttributesFileUnscannableFailsClosed(t *testing.T) {
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")
	dir := t.TempDir() // a directory: exists, but not a regular file
	repo.AppendConfig(t, "[core]\n\tattributesFile = "+dir+"\n")
	if _, err := ScanGitConfig(repo.Root); err == nil || !strings.Contains(err.Error(), "fail closed") {
		t.Fatalf("expected fail-closed error for unscannable core.attributesFile target, got %v", err)
	}
}

func TestScanGitConfig_BenignInfoAttributesStaysClean(t *testing.T) {
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")
	infoPath := filepath.Join(repo.GitDir(), "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Benign attributes (eol/text normalization, explicit disarm) produce no
	// findings and no attr.tree collateral (review [56]).
	if err := os.WriteFile(infoPath, []byte("* text=auto\n*.bin -diff\n# comment\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := ScanGitConfig(repo.Root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	if !info.Clean() {
		t.Errorf("benign info/attributes should stay Clean: %+v", info.Findings)
	}
	if ov := info.NeutralizingOverrides(); ov != nil {
		t.Errorf("benign info/attributes produced overrides: %v", ov)
	}
}

// --- [1]d: object format ---

func TestParseGitConfig_ExtensionsCapture(t *testing.T) {
	src := "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectformat = sha256\n\tworktreeConfig = true\n"
	info := parseTestConfig(t, src)
	if info.repositoryFormatVersion != 1 || info.objectFormat != "sha256" || !info.worktreeConfigEnabled {
		t.Errorf("captured model = %+v", info)
	}
	if err := info.validateRepoFormat(); err != nil {
		t.Errorf("sha256 repo must validate: %v", err)
	}
	if got := info.emptyTreeHash(); got != EmptyTreeSHA256 {
		t.Errorf("emptyTreeHash = %q, want %q", got, EmptyTreeSHA256)
	}
}

func TestParseGitConfig_BadRepositoryFormat(t *testing.T) {
	info := parseTestConfig(t, "[core]\n\trepositoryformatversion = abc\n")
	if len(info.Errors) == 0 {
		t.Error("non-integer repositoryformatversion must be a parse error")
	}
	info = parseTestConfig(t, "[core]\n\trepositoryformatversion = 2\n")
	if err := info.validateRepoFormat(); err == nil {
		t.Error("repositoryformatversion 2 must fail closed")
	}
	info = parseTestConfig(t, "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectformat = sha512\n")
	if err := info.validateRepoFormat(); err == nil {
		t.Error("unknown objectformat must fail closed")
	}
}

func TestScanGitConfig_UnsupportedRepoFormatFailsClosed(t *testing.T) {
	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n")
	repo.AppendConfig(t, "[core]\n\trepositoryformatversion = 2\n")
	if _, err := ScanGitConfig(repo.Root); err == nil {
		t.Fatal("expected fail-closed error for repositoryformatversion 2")
	}
}

func TestScanGitConfig_SHA256RepoUsesSHA256EmptyTree(t *testing.T) {
	gittest.RequireGit(t)
	root := filepath.Join(t.TempDir(), "sha256repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// git < 2.29 has no --object-format; skip rather than fail there.
	setup := gittest.Repo{Root: root}
	if err := runGitBestEffort(root, "init", "-b", "main", "--object-format=sha256"); err != nil {
		t.Skipf("git lacks sha256 support: %v", err)
	}
	setup.Git(t, "config", "user.email", "t@t.invalid")
	setup.Git(t, "config", "user.name", "t")
	setup.Write(t, "file.txt", "hello\n")
	setup.Git(t, "add", ".")
	setup.Git(t, "commit", "-m", "init")
	// The include keeps the attr.tree kill engaged (narrow mode, review
	// [56]: visible drivers alone no longer derive it), preserving this
	// test's discriminator — the SHA-1 constant is inert on sha256
	// repositories, so the kill must carry the SHA-256 empty tree.
	setup.AppendConfig(t, "[filter \"x\"]\n\tclean = /tmp/evil.sh\n[include]\n\tpath = extra.conf\n")
	setup.Write(t, ".gitattributes", "*.txt filter=x\n")

	info, err := ScanGitConfig(root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	if info.ObjectFormat != "sha256" {
		t.Errorf("ObjectFormat = %q, want sha256", info.ObjectFormat)
	}
	// The SHA-1 constant is a verified silent no-op on sha256 repositories;
	// the override set must carry the SHA-256 empty tree.
	wantOverride(t, info,
		"attr.tree="+EmptyTreeSHA256,
		"core.askPass=",
		"core.attributesFile=",
		"core.sshCommand=ssh",
		"credential.helper=",
		"diff.external=",
		"filter.x.clean=cat",
		"filter.x.process=",
		"filter.x.smudge=cat")
	if f := finding(t, info, "filter.x.clean"); len(f.Overrides) == 0 {
		t.Error("filter finding must carry overrides")
	}
}

// runGitBestEffort runs a git command and reports failure without failing
// the test (fixture-probing helper).
func runGitBestEffort(dir string, args ...string) error {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

// --- attributes parser (pure) ---

func TestParseAttributesRouting(t *testing.T) {
	src := "# leading comment\n" +
		"*.txt filter=x\n" +
		"\n" +
		"\"quoted pattern\" merge=m\n" +
		"*.bin -filter=x !merge diff=tc\n" +
		"lfs filter=lfs -text\n" + // macro definition line
		"*.dat text filter=a merge=b diff=c # inline comment\n" +
		"*.raw\n"
	findings := parseAttributesRouting(src, "info/attributes", "/x/.git/info/attributes")
	keys := make([]string, 0, len(findings))
	for _, f := range findings {
		keys = append(keys, f.FullKey)
	}
	want := []string{
		"info/attributes:filter=x",   // line 2
		"info/attributes:merge=m",    // quoted pattern
		"info/attributes:diff=tc",    // routing token among disarming forms
		"info/attributes:filter=lfs", // macro definition captured
		"info/attributes:filter=a",   // multi-attr line
		"info/attributes:merge=b",
		"info/attributes:diff=c",
	}
	if !slices.Equal(keys, want) {
		t.Errorf("routing keys = %v, want %v", keys, want)
	}
	if f := findings[0]; f.Line != 2 || f.Value != "*.txt" {
		t.Errorf("finding[0] = %+v, want line 2 pattern *.txt", f)
	}
	if f := findings[1]; f.Value != "quoted" {
		t.Errorf("quoted pattern = %q", f.Value)
	}
}
