package workspace

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseTestConfig runs the pure parser over data with a no-op logger.
func parseTestConfig(t *testing.T, data string) *GitConfigInfo {
	t.Helper()
	return parseGitConfigData(data, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
}

// parseTestConfigWithLog parses data while capturing log output.
func parseTestConfigWithLog(t *testing.T, data string) (*GitConfigInfo, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	info := parseGitConfigData(data, slog.New(slog.NewTextHandler(buf, nil)))
	return info, buf
}

// finding returns the first finding with the given full key.
func finding(t *testing.T, info *GitConfigInfo, fullKey string) *GitConfigFinding {
	t.Helper()
	for i := range info.Findings {
		if info.Findings[i].FullKey == fullKey {
			return &info.Findings[i]
		}
	}
	t.Fatalf("no finding with full key %q in %+v", fullKey, info.Findings)
	return nil
}

func overrideArgvs(info *GitConfigInfo) []string {
	ov := info.NeutralizingOverrides()
	out := make([]string, 0, len(ov))
	for _, o := range ov {
		out = append(out, o.Argv())
	}
	return out
}

const armedConfig = `[core]
	fsmonitor = /tmp/evil-fsmonitor.sh
	hooksPath = /tmp/evil-hooks
	editor = /tmp/evil-editor.sh
[filter "lfs"]
	process = /tmp/evil-filter.sh
	clean = /tmp/evil-clean.sh
[merge "evil"]
	driver = /tmp/evil-merge.sh %O %A %B
[diff "evil"]
	textconv = /tmp/evil-textconv.sh
`

func TestParseGitConfig_ArmedConfig(t *testing.T) {
	info := parseTestConfig(t, armedConfig)
	if len(info.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %+v", info.Errors)
	}
	wantKinds := map[string]string{
		"core.fsmonitor":     GitConfigFindingFSMonitor,
		"core.hookspath":     GitConfigFindingHooksPath,
		"core.editor":        GitConfigFindingEditor,
		"filter.lfs.process": GitConfigFindingFilter,
		"filter.lfs.clean":   GitConfigFindingFilter,
		"merge.evil.driver":  GitConfigFindingMergeDriver,
		"diff.evil.textconv": GitConfigFindingTextConv,
	}
	if len(info.Findings) != len(wantKinds) {
		t.Fatalf("got %d findings, want %d: %+v", len(info.Findings), len(wantKinds), info.Findings)
	}
	for fullKey, kind := range wantKinds {
		f := finding(t, info, fullKey)
		if f.Kind != kind {
			t.Errorf("%s: kind = %q, want %q", fullKey, f.Kind, kind)
		}
	}
	if f := finding(t, info, "core.fsmonitor"); !f.BaselineCovered {
		t.Error("core.fsmonitor should be baseline-covered")
	}
	if f := finding(t, info, "core.hookspath"); !f.BaselineCovered {
		t.Error("core.hooksPath should be baseline-covered")
	}
	if f := finding(t, info, "core.editor"); !f.BaselineCovered {
		t.Error("core.editor should be baseline-covered")
	}
	if f := finding(t, info, "filter.lfs.process"); f.BaselineCovered {
		t.Error("filter.lfs.process should not be baseline-covered")
	}
	// Spot-check values and line numbers.
	if f := finding(t, info, "merge.evil.driver"); f.Value != "/tmp/evil-merge.sh %O %A %B" || f.Line != 9 {
		t.Errorf("merge.evil.driver = %+v, want value %q at line 9", f, "/tmp/evil-merge.sh %O %A %B")
	}
	if f := finding(t, info, "diff.evil.textconv"); f.Value != "/tmp/evil-textconv.sh" || f.Line != 11 {
		t.Errorf("diff.evil.textconv = %+v, want value at line 11", f)
	}
	// Filter findings carry the verified neutralization triple.
	f := finding(t, info, "filter.lfs.clean")
	wantTriple := []GitConfigOverride{
		{Key: "filter.lfs.process", Value: ""},
		{Key: "filter.lfs.clean", Value: "cat"},
		{Key: "filter.lfs.smudge", Value: "cat"},
	}
	if len(f.Overrides) != len(wantTriple) {
		t.Fatalf("filter overrides = %+v, want %+v", f.Overrides, wantTriple)
	}
	for i, o := range f.Overrides {
		if o != wantTriple[i] {
			t.Errorf("filter override[%d] = %+v, want %+v", i, o, wantTriple[i])
		}
	}
	if f := finding(t, info, "merge.evil.driver"); len(f.Overrides) != 1 ||
		f.Overrides[0].Argv() != "merge.evil.driver=false %O %A %B" {
		t.Errorf("merge driver overrides = %+v", f.Overrides)
	}
	if f := finding(t, info, "diff.evil.textconv"); len(f.Overrides) != 1 ||
		f.Overrides[0].Argv() != "diff.evil.textconv=cat" {
		t.Errorf("textconv overrides = %+v", f.Overrides)
	}
	// Baseline-covered keys need no per-repo overrides.
	if f := finding(t, info, "core.editor"); len(f.Overrides) != 0 {
		t.Errorf("core.editor overrides = %+v, want none", f.Overrides)
	}
}

func TestParseGitConfig_ValueForms(t *testing.T) {
	// All forms verified against git 2.50.1 behavior.
	cases := []struct {
		name     string
		valueSrc string
		want     string
	}{
		{"plain", `x`, `x`},
		{"quoted space", `"hello world"  `, `hello world`},
		{"partial quote", `pre"mid dle"post`, `premid dlepost`},
		{"escaped quote", `"a\"b"`, `a"b`},
		{"escape n t", `"l\ni\tb"`, "l\ni\tb"},
		{"hash inside quotes", `"a # not comment"`, `a # not comment`},
		{"semicolon comment", `val ; comment`, `val`},
		{"hash comment", `val # comment`, `val`},
		{"interior ws preserved", `a   b`, `a   b`},
		{"escaped backslash", `a\\b`, `a\b`},
		{"tabs inside quotes", `"x\ty"`, "x\ty"},
		// Continuation: backslash-newline vanishes, surrounding whitespace
		// is preserved (verified against git).
		{"continuation", "line1 \\\n\t  line2", "line1 \t  line2"},
		{"continuation immediate", "\\\n\tfinal", "final"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "[filter \"v\"]\n\tprocess = " + tc.valueSrc + "\n"
			info := parseTestConfig(t, src)
			if len(info.Errors) != 0 {
				t.Fatalf("unexpected errors: %+v", info.Errors)
			}
			f := finding(t, info, "filter.v.process")
			if f.Value != tc.want {
				t.Errorf("value = %q, want %q", f.Value, tc.want)
			}
			if f.Boolean {
				t.Error("value form should not be boolean")
			}
		})
	}
}

func TestParseGitConfig_BareKeyIsEmptyBoolean(t *testing.T) {
	// Verified: `git config --get` on a bare key prints an empty string.
	info := parseTestConfig(t, "[filter \"b\"]\n\tprocess\n")
	f := finding(t, info, "filter.b.process")
	if !f.Boolean || f.Value != "" {
		t.Errorf("bare key: Boolean=%v Value=%q, want true/\"\"", f.Boolean, f.Value)
	}
	// `k=` is an explicit empty string, not a boolean.
	info = parseTestConfig(t, "[filter \"b\"]\n\tprocess =\n")
	f = finding(t, info, "filter.b.process")
	if f.Boolean {
		t.Error("explicit empty value should not be boolean")
	}
}

func TestParseGitConfig_HeaderForms(t *testing.T) {
	// Standard quoted subsection.
	info := parseTestConfig(t, "[filter \"lfs\"]\n\tprocess = x\n")
	if f := finding(t, info, "filter.lfs.process"); f.Subsection != "lfs" || f.Section != "filter" {
		t.Errorf("standard header: %+v", f)
	}
	// Section and key names are case-insensitive; quoted subsections are
	// case-sensitive and preserved verbatim (verified against git).
	info = parseTestConfig(t, "[FILTER \"LFS\"]\n\tPROCESS = x\n")
	if f := finding(t, info, "filter.LFS.process"); f.Subsection != "LFS" || f.Key != "process" {
		t.Errorf("case handling: %+v", f)
	}
	// Old dotted syntax: subsection is lowercased (verified against git).
	info = parseTestConfig(t, "[FILTER.LFS]\n\tprocess = x\n")
	if f := finding(t, info, "filter.lfs.process"); f.Subsection != "lfs" {
		t.Errorf("dotted header lowercasing: %+v", f)
	}
	info = parseTestConfig(t, "[filter.lfs]\n\tprocess = x\n")
	finding(t, info, "filter.lfs.process")
	// Quoted subsections differing only by case are distinct filters.
	info = parseTestConfig(t, "[filter \"LFS\"]\n\tprocess = x\n[filter \"lfs\"]\n\tprocess = y\n")
	finding(t, info, "filter.LFS.process")
	finding(t, info, "filter.lfs.process")
	if len(info.Findings) != 2 {
		t.Fatalf("case-distinct subsections: got %d findings, want 2", len(info.Findings))
	}
	// Dots are allowed inside quoted subsections.
	info = parseTestConfig(t, "[filter \"a.b\"]\n\tprocess = x\n")
	finding(t, info, "filter.a.b.process")
	// Subsection-less filter keys are meaningless to git: no finding.
	info = parseTestConfig(t, "[filter]\n\tprocess = x\n")
	if len(info.Findings) != 0 {
		t.Errorf("subsection-less filter produced findings: %+v", info.Findings)
	}
}

func TestParseGitConfig_EntryOutsideSection(t *testing.T) {
	src := "fsmonitor = /tmp/evil\n[core]\n\tpager = less\n"
	info := parseTestConfig(t, src)
	if len(info.Errors) == 0 {
		t.Fatal("expected an error for an entry outside of any section (git refuses such files)")
	}
	if len(info.Findings) != 1 || info.Findings[0].FullKey != "core.pager" {
		t.Errorf("findings = %+v, want exactly the scoped core.pager entry", info.Findings)
	}
	if info.Clean() {
		t.Error("Clean() = true, want false: intake must warn on this config")
	}
}

func TestParseGitConfig_Malformed(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		wantErrs  int
		wantAfter string // full key of a finding that must still be found after the error
	}{
		{"empty section name", "[\n[filter \"lfs\"]\n\tprocess = x\n", 1, "filter.lfs.process"},
		{"unterminated header", "[filter \"lfs\"\n[filter \"ok\"]\n\tprocess = x\n", 1, "filter.ok.process"},
		{"garbage in header", "[filter \"x\" y]\n[filter \"ok\"]\n\tprocess = x\n", 1, "filter.ok.process"},
		{"bad key start", "1bad = x\n[filter \"ok\"]\n\tprocess = x\n", 1, "filter.ok.process"},
		{"junk after boolean key", "[filter \"lfs\"]\n\tprocess junk\n[filter \"ok\"]\n\tprocess = x\n", 1, "filter.ok.process"},
		{"unterminated escape at EOF", "[filter \"lfs\"]\n\tprocess = \"abc\\", 1, ""},
		{"unterminated subsection at EOF", "[filter \"lfs", 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := parseTestConfig(t, tc.src)
			if len(info.Errors) != tc.wantErrs {
				t.Fatalf("errors = %d (%+v), want %d", len(info.Errors), info.Errors, tc.wantErrs)
			}
			if info.Errors[0].Line < 1 {
				t.Errorf("error line = %d, want >= 1", info.Errors[0].Line)
			}
			if tc.wantAfter != "" {
				finding(t, info, tc.wantAfter) // must not panic, must be found
			}
			if info.Clean() {
				t.Error("malformed config must not be Clean")
			}
		})
	}
}

func TestParseGitConfig_UnterminatedQuoteSwallowsRestButFailsClosed(t *testing.T) {
	// git refuses a file with a raw unterminated quote wholesale, so
	// swallowing the remainder is safe: nothing in it would ever run.
	src := "[filter \"lfs\"]\n\tprocess = \"ev\n[filter \"ok\"]\n\tprocess = x\n"
	info := parseTestConfig(t, src)
	if len(info.Errors) == 0 {
		t.Fatal("expected an unterminated-quote error")
	}
	if info.Clean() {
		t.Error("unterminated quote must not be Clean")
	}
}

func TestParseGitConfig_UnknownEscapeReportedAndLenient(t *testing.T) {
	// git refuses the whole file on unknown escapes; the parser records the
	// error and keeps the characters literally so the key is still surfaced
	// (over-reporting is the safe direction) and later lines still parse.
	src := "[filter \"lfs\"]\n\tprocess = a\\qb\n[filter \"ok\"]\n\tprocess = x\n"
	info := parseTestConfig(t, src)
	if len(info.Errors) != 1 {
		t.Fatalf("errors = %+v, want 1", info.Errors)
	}
	if f := finding(t, info, "filter.lfs.process"); f.Value != `a\qb` {
		t.Errorf("value = %q, want %q", f.Value, `a\qb`)
	}
	finding(t, info, "filter.ok.process")
}

func TestParseGitConfig_Duplicates(t *testing.T) {
	// Verified: git's scalar lookup takes the last occurrence; the scanner
	// reports every occurrence and the override set is keyed, so the
	// neutralization covers all of them.
	src := "[filter \"dup\"]\n\tprocess = one\n\tprocess = two\n"
	info := parseTestConfig(t, src)
	if len(info.Findings) != 2 {
		t.Fatalf("got %d findings, want 2 (every occurrence reported)", len(info.Findings))
	}
	f0, f1 := &info.Findings[0], &info.Findings[1]
	if f0.Value != "one" || f1.Value != "two" {
		t.Errorf("values = %q, %q; want one, two", f0.Value, f1.Value)
	}
	if f0.Line != 2 || f1.Line != 3 {
		t.Errorf("lines = %d, %d; want 2, 3", f0.Line, f1.Line)
	}
	got := overrideArgvs(info)
	want := []string{
		"attr.tree=" + EmptyTreeSHA1,
		"filter.dup.clean=cat",
		"filter.dup.process=",
		"filter.dup.smudge=cat",
	}
	if len(got) != len(want) {
		t.Fatalf("overrides = %v, want %v (deduplicated)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("override[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseGitConfig_IncludesIgnoredAndLogged(t *testing.T) {
	src := "[include]\n\tpath = ~/.gitconfig-extra\n" +
		"[includeIf \"gitdir:~/work/\"]\n\tpath = /abs/evil.config\n"
	info, logBuf := parseTestConfigWithLog(t, src)
	if len(info.Includes) != 2 {
		t.Fatalf("includes = %+v, want 2", info.Includes)
	}
	if info.Includes[0].Conditional || info.Includes[0].Path != "~/.gitconfig-extra" || info.Includes[0].Line != 2 {
		t.Errorf("include[0] = %+v", info.Includes[0])
	}
	if !info.Includes[1].Conditional || info.Includes[1].Condition != "gitdir:~/work/" ||
		info.Includes[1].Path != "/abs/evil.config" || info.Includes[1].Line != 4 {
		t.Errorf("include[1] = %+v", info.Includes[1])
	}
	if len(info.Findings) != 0 {
		t.Errorf("include entries leaked into findings: %+v", info.Findings)
	}
	logged := logBuf.String()
	if strings.Count(logged, "include directive ignored") != 2 {
		t.Errorf("expected 2 ignored-include log lines, got: %s", logged)
	}
	if !strings.Contains(logged, "path=/abs/evil.config") {
		t.Errorf("log should name the include path: %s", logged)
	}
	// Includes mean the config is an incomplete view: compensate with the
	// attribute-routing kill.
	got := overrideArgvs(info)
	if len(got) != 1 || got[0] != "attr.tree="+EmptyTreeSHA1 {
		t.Errorf("overrides with includes = %v, want [attr.tree=empty tree]", got)
	}
	if info.Clean() {
		t.Error("config with includes must not be Clean")
	}
}

func TestParseGitConfig_CoreKeys(t *testing.T) {
	src := "[core]\n\tfsmonitor\n\tfsmonitorHook = /tmp/evil-hook.sh\n\tpager = /tmp/evil-pager.sh\n" +
		"\tsshCommand = /tmp/evil-ssh.sh\n\taskPass = /tmp/evil-askpass.sh\n"
	info := parseTestConfig(t, src)
	f := finding(t, info, "core.fsmonitor")
	if !f.Boolean || !f.BaselineCovered {
		t.Errorf("bare fsmonitor: Boolean=%v BaselineCovered=%v", f.Boolean, f.BaselineCovered)
	}
	if f2 := finding(t, info, "core.fsmonitorhook"); f2.Kind != GitConfigFindingFSMonitor || !f2.BaselineCovered {
		t.Errorf("fsmonitorHook: %+v", f2)
	}
	for _, key := range []string{"core.pager", "core.sshcommand", "core.askpass"} {
		f := finding(t, info, key)
		if f.BaselineCovered {
			t.Errorf("%s must not claim baseline coverage", key)
		}
		if len(f.Overrides) != 0 {
			t.Errorf("%s must not produce overrides: %+v", key, f.Overrides)
		}
		if f.Description == "" {
			t.Errorf("%s must carry a human-readable description", key)
		}
	}
	// Baseline-covered-only config needs no per-repo overrides.
	if ov := info.NeutralizingOverrides(); ov != nil {
		t.Errorf("baseline-covered config produced overrides: %v", ov)
	}
}

func TestParseGitConfig_DiffVariants(t *testing.T) {
	src := "[diff \"tc\"]\n\ttextconv = /tmp/evil-tc.sh\n\tcommand = /tmp/evil-ext.sh\n" +
		"[diff]\n\texternal = /tmp/evil-default.sh\n[diff \"tc\"]\n\twordRegex = x\n"
	info := parseTestConfig(t, src)
	tc := finding(t, info, "diff.tc.textconv")
	if len(tc.Overrides) != 1 || tc.Overrides[0].Argv() != "diff.tc.textconv=cat" {
		t.Errorf("textconv overrides = %+v", tc.Overrides)
	}
	// External diff drivers only run with --ext-diff: finding, no override.
	for _, key := range []string{"diff.tc.command", "diff.external"} {
		f := finding(t, info, key)
		if len(f.Overrides) != 0 {
			t.Errorf("%s produced overrides: %+v", key, f.Overrides)
		}
	}
	if len(info.Findings) != 3 {
		t.Errorf("findings = %+v, want 3 (wordRegex ignored)", info.Findings)
	}
	got := overrideArgvs(info)
	want := []string{"attr.tree=" + EmptyTreeSHA1, "diff.tc.textconv=cat"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("overrides = %v, want %v", got, want)
	}
}

func TestParseGitConfig_IgnoredKeys(t *testing.T) {
	// Benign keys of dangerous sections and unknown sections produce nothing.
	src := "[filter \"lfs\"]\n\trequired = true\n[merge \"x\"]\n\tname = display only\n" +
		"[attr]\n\tsomethingelse = y\n[remote \"origin\"]\n\turl = https://x\n[core]\n\tquotepath = off\n"
	info := parseTestConfig(t, src)
	if len(info.Findings) != 0 {
		t.Errorf("benign keys produced findings: %+v", info.Findings)
	}
	if !info.Clean() {
		t.Error("benign config should be Clean")
	}
}

func TestNeutralizingOverrides_Matrix(t *testing.T) {
	et := "attr.tree=" + EmptyTreeSHA1
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"empty", "", nil},
		{"benign", "[core]\n\tquotepath = off\n", nil},
		{"fsmonitor only (baseline-covered)", "[core]\n\tfsmonitor = /tmp/evil.sh\n", nil},
		{"filter", "[filter \"x\"]\n\tprocess = /tmp/evil.sh\n", []string{
			et, "filter.x.clean=cat", "filter.x.process=", "filter.x.smudge=cat"}},
		{"filter clean only", "[filter \"x\"]\n\tclean = /tmp/evil.sh\n", []string{
			et, "filter.x.clean=cat", "filter.x.process=", "filter.x.smudge=cat"}},
		{"merge driver", "[merge \"drv\"]\n\tdriver = /tmp/evil.sh %O %A %B\n", []string{
			et, "merge.drv.driver=false %O %A %B"}},
		{"textconv", "[diff \"tc\"]\n\ttextconv = /tmp/evil.sh\n", []string{
			et, "diff.tc.textconv=cat"}},
		{"external diff", "[diff]\n\texternal = /tmp/evil.sh\n", []string{et}},
		{"attacker attr.tree is beaten", "[attr]\n\ttree = deadbeef\n", []string{et}},
		{"includes only", "[include]\n\tpath = x\n", []string{et}},
		{"two filters", "[filter \"a\"]\n\tprocess = e\n[filter \"b\"]\n\tclean = e\n", []string{
			et,
			"filter.a.clean=cat", "filter.a.process=", "filter.a.smudge=cat",
			"filter.b.clean=cat", "filter.b.process=", "filter.b.smudge=cat",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := parseTestConfig(t, tc.src)
			got := overrideArgvs(info)
			if len(got) != len(tc.want) {
				t.Fatalf("overrides = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("override[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
			// The flat argv rendering must be exactly "-c", "k=v" pairs.
			argv := info.NeutralizingArgv()
			if tc.want == nil {
				if argv != nil {
					t.Errorf("NeutralizingArgv = %v, want nil", argv)
				}
				return
			}
			if len(argv) != len(tc.want)*2 {
				t.Fatalf("argv = %v, want %d elements", argv, len(tc.want)*2)
			}
			for i, arg := range tc.want {
				if argv[i*2] != "-c" || argv[i*2+1] != arg {
					t.Errorf("argv[%d,%d] = %q,%q; want \"-c\",%q", i*2, i*2+1, argv[i*2], argv[i*2+1], arg)
				}
			}
		})
	}
}

func TestGitConfigInfo_Clean(t *testing.T) {
	var nilInfo *GitConfigInfo
	if !nilInfo.Clean() {
		t.Error("nil info should be Clean")
	}
	if !(&GitConfigInfo{}).Clean() {
		t.Error("empty info should be Clean")
	}
	withFinding := parseTestConfig(t, "[core]\n\tfsmonitor = x\n")
	if withFinding.Clean() {
		t.Error("finding must break Clean")
	}
	withError := parseTestConfig(t, "[\n")
	if withError.Clean() {
		t.Error("parse error must break Clean")
	}
	withInclude := parseTestConfig(t, "[include]\n\tpath = x\n")
	if withInclude.Clean() {
		t.Error("include must break Clean")
	}
}

func TestGitConfigOverride_Argv(t *testing.T) {
	if got := (GitConfigOverride{Key: "a.b", Value: "v"}).Argv(); got != "a.b=v" {
		t.Errorf("Argv = %q", got)
	}
	if got := (GitConfigOverride{Key: "filter.x.process", Value: ""}).Argv(); got != "filter.x.process=" {
		t.Errorf("Argv (empty value) = %q", got)
	}
	// Spaces survive in a single argv element (no shell involved).
	if got := (GitConfigOverride{Key: "merge.d.driver", Value: neutralMergeDriverValue}).Argv(); got != "merge.d.driver=false %O %A %B" {
		t.Errorf("Argv (merge driver) = %q", got)
	}
}

// writeTempRepo creates a temp repo root with the given .git layout and
// returns its path.
func writeTempRepo(t *testing.T, dotGit string, isDir bool, config string) string {
	t.Helper()
	root := t.TempDir()
	gitPath := filepath.Join(root, ".git")
	if isDir {
		if err := os.MkdirAll(gitPath, 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(gitPath, []byte(dotGit), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if config != "" {
		path := gitPath
		if !isDir {
			path = filepath.Join(root, strings.TrimSpace(strings.TrimPrefix(dotGit, "gitdir:")))
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// evalDir resolves symlinks in a fixture path: discovery runs on the
// physical chain (like git's chdir-based discovery), so paths reported by
// ScanGitConfig are anchored at the evaluated root (e.g. /private/var/...
// instead of /var/... on macOS).
func evalDir(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestScanGitConfig_Resolution(t *testing.T) {
	armed := armedConfig

	t.Run("git dir", func(t *testing.T) {
		root := writeTempRepo(t, "", true, armed)
		info, err := ScanGitConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		if info.GitDir != filepath.Join(evalDir(t, root), ".git") {
			t.Errorf("GitDir = %q", info.GitDir)
		}
		if len(info.Findings) != 7 {
			t.Errorf("findings = %d, want 7", len(info.Findings))
		}
	})

	t.Run("gitdir pointer relative", func(t *testing.T) {
		root := writeTempRepo(t, "gitdir: .realgit\n", false, armed)
		info, err := ScanGitConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		if info.GitDir != filepath.Join(evalDir(t, root), ".realgit") {
			t.Errorf("GitDir = %q, want %q", info.GitDir, filepath.Join(evalDir(t, root), ".realgit"))
		}
		if len(info.Findings) != 7 {
			t.Errorf("findings = %d, want 7", len(info.Findings))
		}
	})

	t.Run("gitdir pointer absolute", func(t *testing.T) {
		abs := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.MkdirAll(abs, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(abs, "config"), []byte(armed), 0o644); err != nil {
			t.Fatal(err)
		}
		root := writeTempRepo(t, "gitdir: "+abs+"\n", false, "")
		info, err := ScanGitConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		if info.GitDir != abs {
			t.Errorf("GitDir = %q, want %q", info.GitDir, abs)
		}
		if len(info.Findings) != 7 {
			t.Errorf("findings = %d, want 7", len(info.Findings))
		}
	})

	t.Run("no .git", func(t *testing.T) {
		root := t.TempDir()
		info, err := ScanGitConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Clean() || info.GitDir != "" || info.ConfigPath != "" {
			t.Errorf("missing .git should yield empty info, got %+v", info)
		}
		if info.NeutralizingOverrides() != nil {
			t.Error("missing .git should need no overrides")
		}
	})

	t.Run("discovery walks up to the parent repository", func(t *testing.T) {
		root := writeTempRepo(t, "", true, armed)
		sub := filepath.Join(root, "nested", "deep")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		info, err := ScanGitConfig(sub)
		if err != nil {
			t.Fatal(err)
		}
		if info.GitDir != filepath.Join(evalDir(t, root), ".git") {
			t.Errorf("GitDir = %q, want the parent repository's .git", info.GitDir)
		}
		if len(info.Findings) != 7 {
			t.Errorf("findings = %d, want 7 (same as a scan of the repo root)", len(info.Findings))
		}
	})

	t.Run("fail closed on unscannable parent config", func(t *testing.T) {
		root := writeTempRepo(t, "", true, "[core]\n\tpager = less\n")
		if err := os.Remove(filepath.Join(root, ".git", "config")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, ".git", "config"), 0o755); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(root, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanGitConfig(sub); err == nil {
			t.Fatal("expected error: the discovered parent config is unscannable")
		}
	})

	t.Run("malformed gitdir pointer", func(t *testing.T) {
		root := writeTempRepo(t, "not a pointer\n", false, "")
		if _, err := ScanGitConfig(root); err == nil {
			t.Fatal("expected error for malformed .git pointer")
		}
	})

	t.Run("empty gitdir pointer", func(t *testing.T) {
		root := writeTempRepo(t, "gitdir:   \n", false, "")
		if _, err := ScanGitConfig(root); err == nil {
			t.Fatal("expected error for empty gitdir")
		}
	})

	t.Run("missing config inside git dir", func(t *testing.T) {
		root := writeTempRepo(t, "", true, "")
		info, err := ScanGitConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Clean() {
			t.Errorf("missing config should be Clean, got %+v", info)
		}
		if info.ConfigPath != filepath.Join(evalDir(t, root), ".git", "config") {
			t.Errorf("ConfigPath = %q", info.ConfigPath)
		}
	})

	t.Run("oversized config refused", func(t *testing.T) {
		root := writeTempRepo(t, "", true, strings.Repeat("# pad\n", maxGitConfigBytes/6+1))
		_, err := ScanGitConfig(root)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("expected oversize error, got %v", err)
		}
	})
}

func TestScanGitConfigFile_MissingFile(t *testing.T) {
	info, err := ScanGitConfigFile(filepath.Join(t.TempDir(), "nope", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Clean() {
		t.Errorf("missing file should be Clean, got %+v", info)
	}
}

func TestParseGitConfig_CRLFAndBOM(t *testing.T) {
	src := "\ufeff[filter \"lfs\"]\r\n\tprocess = evil\r\n"
	info := parseTestConfig(t, src)
	if len(info.Errors) != 0 {
		t.Fatalf("CRLF/BOM errors: %+v", info.Errors)
	}
	f := finding(t, info, "filter.lfs.process")
	if f.Value != "evil" || f.Line != 2 {
		t.Errorf("CRLF value = %q at line %d", f.Value, f.Line)
	}
}

func TestParseGitConfig_NoProcessExecution(t *testing.T) {
	// Structural guard for the acceptance criterion: the parser source must
	// not reference process-spawning APIs. It reads the sibling source file
	// as text and fails if any spawn entry point appears.
	src, err := os.ReadFile("gitconfig.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{
		`"os/exec"`, "exec.Command", "CommandContext", "cmd.Run", "cmd.Start", "CombinedOutput",
	} {
		if bytes.Contains(src, []byte(banned)) {
			t.Errorf("gitconfig.go references %q — the parser must never execute processes", banned)
		}
	}
}
