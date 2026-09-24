package embeddedllm

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests drive the REAL install pipeline — production downloader,
// production SHA256 verification, production extraction, production
// provisioning commands — against kilobyte artifacts served by an in-test
// HTTPS server and an in-test asset table. Nothing about the flow is mocked
// out except the bytes' size, the hardware probe, the external commands and
// the config sink.

// testInstallInstant is the fixed clock every install is stamped with.
var testInstallInstant = time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)

// ── fixture ──

// installCase describes the machine and the artifact set one install test runs
// against.
type installCase struct {
	platform string
	arch     string
	backend  Backend
	ramGiB   float64
	// hostOS feeds Installer.HostOS, which decides the macOS provisioning
	// branch. Tests set it explicitly so darwin-only behaviour is covered on
	// every CI platform.
	hostOS string
	// cudart adds the Windows CUDA companion archive to the set.
	cudart bool
	// corrupt makes the server return bytes that do not match the pin for one
	// component, so the verification gate fails for real.
	corrupt Component
	// freeSpace is the injected free-space reading. Zero means "generous".
	freeSpace int64
	// failCommand makes every recorded command whose command line contains the
	// substring fail.
	failCommand string
	// missingCommand makes every command whose base name equals it report
	// exec.ErrNotFound.
	missingCommand string
	// sinkErr / stopErr make the config sink / server stop fail.
	sinkErr error
	stopErr error
}

// darwinMetalCase is the canonical happy path: Apple Silicon, 32 GiB, Metal.
func darwinMetalCase() installCase {
	return installCase{
		platform: PlatformDarwinARM64,
		arch:     "arm64",
		backend:  BackendMetal,
		ramGiB:   32,
		hostOS:   "darwin",
	}
}

// windowsCUDACase exercises the two-archive runtime set (runtime + cudart).
func windowsCUDACase() installCase {
	return installCase{
		platform: PlatformWindowsAMD64,
		arch:     "amd64",
		backend:  BackendCUDA124,
		ramGiB:   64,
		hostOS:   "windows",
		cudart:   true,
	}
}

// linuxCPUCase is the branch with no macOS provisioning and a single archive.
func linuxCPUCase() installCase {
	return installCase{
		platform: PlatformLinuxAMD64,
		arch:     "amd64",
		backend:  BackendCPU,
		ramGiB:   24,
		hostOS:   "linux",
	}
}

type installFixture struct {
	t         *testing.T
	case_     installCase
	agentDir  string
	layout    Layout
	toolsBin  string
	installer *Installer
	sink      *recordingSink
	cmds      *commandRecorder
	assets    []Asset
	digests   map[Component]string

	mu       sync.Mutex
	progress []Progress
	requests map[string]int
}

// newInstallFixture wires an Installer for tc. It swaps the package asset table
// for the duration of the test, so Resolve plans the fixture's artifacts.
func newInstallFixture(t *testing.T, tc installCase) *installFixture {
	t.Helper()
	fx := &installFixture{
		t:        t,
		case_:    tc,
		agentDir: t.TempDir(),
		requests: map[string]int{},
		digests:  map[Component]string{},
	}
	layout, err := NewLayout(
		filepath.Join(fx.agentDir, "runtimes"),
		filepath.Join(fx.agentDir, "models", testModelDirName),
	)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	fx.layout = layout
	fx.toolsBin = filepath.Join(fx.agentDir, "tools", "bin")
	if err := os.MkdirAll(fx.toolsBin, 0o750); err != nil {
		t.Fatalf("creating the agent PATH decoy: %v", err)
	}

	served := fx.buildArtifacts(tc)
	srv := fx.startServer(served)
	fx.assets = fx.buildAssets(srv.URL, tc, served)

	previous := assetTable
	assetTable = fixtureAssetTable{assets: fx.assets, tc: tc}
	t.Cleanup(func() { assetTable = previous })

	fx.sink = &recordingSink{installErr: tc.sinkErr}
	fx.cmds = &commandRecorder{
		fail:     tc.failCommand,
		missing:  tc.missingCommand,
		notFound: tc.missingCommand != "",
	}

	freeSpace := tc.freeSpace
	if freeSpace == 0 {
		freeSpace = 1 << 40 // 1 TiB: the whole-set guard always passes
	}
	downloader := NewDownloader(srv.Client(), dlDiscardLogger())
	downloader.FreeSpace = func(string) (int64, error) { return freeSpace, nil }

	fx.installer = &Installer{
		Layout:       layout,
		Sink:         fx.sink,
		Logger:       dlDiscardLogger(),
		Downloader:   downloader,
		Probe:        fx.probe,
		RunCommand:   fx.runCommand,
		AllocatePort: func(context.Context) (int, error) { return 41977, nil },
		Now:          func() time.Time { return testInstallInstant },
		HostOS:       tc.hostOS,
	}
	if tc.stopErr != nil {
		fx.installer.Stop = func(context.Context) error { return tc.stopErr }
	}
	return fx
}

// probe is the injected hardware probe.
func (fx *installFixture) probe(context.Context, *slog.Logger) (Hardware, error) {
	return Hardware{
		Platform: fx.case_.platform,
		Arch:     fx.case_.arch,
		RAMGiB:   fx.case_.ramGiB,
		Backend:  fx.case_.backend,
	}, nil
}

// runCommand is the injected CommandRunner. It records every invocation and
// answers the smoke test with a plausible llama-server banner.
func (fx *installFixture) runCommand(_ context.Context, name string, args ...string) (string, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	fx.cmds.record(line)
	if fx.cmds.isMissing(name) {
		return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
	}
	if fx.cmds.fails(line) {
		return "simulated failure output", errors.New("simulated failure")
	}
	if len(args) > 0 && args[len(args)-1] == "--version" {
		return "build 10709 (9a9394a)\n", nil
	}
	return "", nil
}

// Install runs the installer and records the progress stream.
func (fx *installFixture) Install(ctx context.Context, opts InstallOptions) (*InstallReport, error) {
	opts.Progress = fx.recordProgress
	return fx.installer.Install(ctx, opts)
}

func (fx *installFixture) recordProgress(p Progress) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	fx.progress = append(fx.progress, p)
}

func (fx *installFixture) progressLog() []Progress {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	out := make([]Progress, len(fx.progress))
	copy(out, fx.progress)
	return out
}

func (fx *installFixture) requestCount(component Component) int {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.requests[string(component)]
}

func (fx *installFixture) totalRequests() int {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	total := 0
	for _, n := range fx.requests {
		total += n
	}
	return total
}

// mustInstall runs a successful install or fails the test.
func (fx *installFixture) mustInstall(t *testing.T) *InstallReport {
	t.Helper()
	report, err := fx.Install(context.Background(), InstallOptions{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	return report
}

// ── artifact construction ──

// buildArtifacts produces the bytes the test server serves, keyed by the URL
// path (== component name).
func (fx *installFixture) buildArtifacts(tc installCase) map[Component][]byte {
	goos := strings.Split(tc.platform, "-")[0]
	runtimeName := ServerBinaryName
	libName := "libggml-base.dylib"
	if goos == "windows" {
		runtimeName = ServerBinaryName + ".exe"
		libName = "ggml-cuda.dll"
	}
	entries := []archiveFile{
		{name: "build/bin/" + runtimeName, body: "#!/bin/sh\necho llama-server\n", mode: 0o755},
		{name: "build/bin/" + libName, body: "fake shared library bytes", mode: 0o644},
		{name: "README.md", body: "pinned fork release " + RuntimeTag, mode: 0o644},
	}
	var runtimeArchive []byte
	if goos == "windows" {
		runtimeArchive = zipBytes(fx.t, entries)
	} else {
		runtimeArchive = tarGzBytes(fx.t, entries)
	}

	files := map[Component][]byte{
		ComponentRuntime: runtimeArchive,
		ComponentModel:   []byte("ternary bonsai 2 27b weights fixture (" + string(packingFor(tc.backend)) + ")"),
		ComponentMMProj:  []byte("mmproj Q8_0 projector fixture"),
	}
	if tc.cudart {
		files[ComponentCudart] = zipBytes(fx.t, []archiveFile{
			{name: "cudart64_12.dll", body: "fake cudart dll", mode: 0o644},
		})
	}
	return files
}

// buildAssets turns the served bodies into pinned assets. For the corrupted
// component the pin describes the ORIGINAL bytes while the server returns
// something else, which is exactly how a substituted artifact looks.
func (fx *installFixture) buildAssets(baseURL string, tc installCase, served map[Component][]byte) []Asset {
	packing := packingFor(tc.backend)

	names := map[Component]string{
		ComponentRuntime: fx.runtimeArchiveName(tc),
		ComponentModel:   modelNameFor(packing),
		ComponentMMProj:  mmprojFileQ8_0,
	}
	if tc.cudart {
		names[ComponentCudart] = "cudart-llama-bin-win-cuda-12.4-x64.zip"
	}

	order := []Component{ComponentRuntime}
	if tc.cudart {
		order = append(order, ComponentCudart)
	}
	order = append(order, ComponentModel, ComponentMMProj)

	assets := make([]Asset, 0, len(order))
	for _, component := range order {
		body := served[component]
		pinned := body
		if tc.corrupt == component {
			pinned = append([]byte("substituted: "), body...)
		}
		digest := sha256.Sum256(pinned)
		fx.digests[component] = hex.EncodeToString(digest[:])
		assets = append(assets, Asset{
			Component:   component,
			URL:         baseURL + "/" + string(component),
			SHA256:      fx.digests[component],
			SizeBytes:   int64(len(body)),
			ArchiveName: names[component],
		})
	}
	return assets
}

func (fx *installFixture) runtimeArchiveName(tc installCase) string {
	switch tc.platform {
	case PlatformDarwinARM64:
		return "llama-" + RuntimeTag + "-bin-macos-arm64.tar.gz"
	case PlatformDarwinAMD64:
		return "llama-" + RuntimeTag + "-bin-macos-x64.tar.gz"
	case PlatformLinuxAMD64:
		return "llama-" + RuntimeTag + "-bin-ubuntu-x64.tar.gz"
	case PlatformWindowsAMD64:
		return "llama-" + RuntimeTag + "-bin-win-cuda-12.4-x64.zip"
	default:
		return "llama-" + RuntimeTag + "-bin-ubuntu-arm64.tar.gz"
	}
}

func modelNameFor(packing Packing) string {
	if packing == PackingPTQ1_0 {
		return modelFilePTQ1_0
	}
	return modelFilePQ2_0
}

// startServer serves one fixed body per component and counts the requests.
func (fx *installFixture) startServer(files map[Component][]byte) *httptest.Server {
	mux := http.NewServeMux()
	for component, body := range files {
		path := "/" + string(component)
		payload := body
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			fx.mu.Lock()
			fx.requests[strings.TrimPrefix(path, "/")]++
			fx.mu.Unlock()
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		})
	}
	srv := httptest.NewTLSServer(mux)
	fx.t.Cleanup(srv.Close)
	return srv
}

// fixtureAssetTable serves the fixture's assets through the AssetTable seam
// Resolve consults, so the production pins are never touched by a test.
type fixtureAssetTable struct {
	assets []Asset
	tc     installCase
}

func (f fixtureAssetTable) RuntimeAsset(platform string, backend Backend) (Asset, bool) {
	if platform != f.tc.platform || backend != f.tc.backend {
		return Asset{}, false
	}
	for _, asset := range f.assets {
		if asset.Component == ComponentRuntime {
			return asset, true
		}
	}
	return Asset{}, false
}

func (f fixtureAssetTable) ArtifactSet(platform string, backend Backend, _ Packing) ([]Asset, error) {
	if platform != f.tc.platform {
		return nil, fmt.Errorf("%w: platform %q", ErrArtifactNotPinned, platform)
	}
	if backend != f.tc.backend {
		return nil, fmt.Errorf("%w: backend %q", ErrArtifactNotPinned, backend)
	}
	out := make([]Asset, len(f.assets))
	copy(out, f.assets)
	return out, nil
}

// ── archive builders ──

type archiveFile struct {
	name string
	body string
	mode int64
}

func tarGzBytes(t *testing.T, entries []archiveFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range sortedEntries(entries) {
		hdr := &tar.Header{
			Name:     entry.name,
			Mode:     entry.mode,
			Size:     int64(len(entry.body)),
			Typeflag: tar.TypeReg,
			ModTime:  testInstallInstant,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %q: %v", entry.name, err)
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatalf("tar body %q: %v", entry.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip: %v", err)
	}
	return buf.Bytes()
}

func zipBytes(t *testing.T, entries []archiveFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range sortedEntries(entries) {
		fh := &zip.FileHeader{Name: entry.name, Method: zip.Deflate, Modified: testInstallInstant}
		fh.SetMode(fs.FileMode(entry.mode))
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatalf("zip header %q: %v", entry.name, err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatalf("zip body %q: %v", entry.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return buf.Bytes()
}

func sortedEntries(entries []archiveFile) []archiveFile {
	out := make([]archiveFile, len(entries))
	copy(out, entries)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// ── recorders ──

type recordingSink struct {
	mu           sync.Mutex
	installed    []InstallState
	removedCalls int
	installErr   error
	removeErr    error
	// onRemoved observes the filesystem at the moment the config is cleared.
	onRemoved func()
}

func (s *recordingSink) ApplyInstalled(_ context.Context, state InstallState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.installErr != nil {
		return s.installErr
	}
	s.installed = append(s.installed, state)
	return nil
}

func (s *recordingSink) ApplyRemoved(_ context.Context) error {
	s.mu.Lock()
	if s.onRemoved != nil {
		s.onRemoved()
	}
	s.removedCalls++
	err := s.removeErr
	s.mu.Unlock()
	return err
}

func (s *recordingSink) installCalls() []InstallState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]InstallState, len(s.installed))
	copy(out, s.installed)
	return out
}

func (s *recordingSink) removed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removedCalls
}

type commandRecorder struct {
	mu       sync.Mutex
	calls    []string
	fail     string
	missing  string
	notFound bool
}

func (c *commandRecorder) record(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, line)
}

func (c *commandRecorder) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.calls))
	copy(out, c.calls)
	return out
}

func (c *commandRecorder) fails(line string) bool {
	return c.fail != "" && strings.Contains(line, c.fail)
}

func (c *commandRecorder) isMissing(name string) bool {
	return c.notFound && filepath.Base(name) == c.missing
}

// ── helpers ──

func requireDir(t *testing.T, path, what string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %q does not exist: %v", what, path, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s: %q is not a directory", what, path)
	}
}

func requireAbsent(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s: %q still exists (stat err = %v)", what, path, err)
	}
}

func requireFile(t *testing.T, path, what string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %q does not exist: %v", what, path, err)
	}
	if info.IsDir() {
		t.Fatalf("%s: %q is a directory", what, path)
	}
}

func requireNoManifest(t *testing.T, layout Layout) {
	t.Helper()
	path, err := layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	requireAbsent(t, path, "manifest")
}

// walkFiles lists every regular file under root.
func walkFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return out
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %q: %v", root, err)
	}
	sort.Strings(out)
	return out
}

// ── acceptance: a successful install ──

func TestInstallWritesManifestAndBothTrees(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	report := fx.mustInstall(t)

	manifestPath, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	requireFile(t, manifestPath, "manifest.json")
	got, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if got.Packing != PackingPQ2_0 {
		t.Errorf("manifest packing = %q, want %q", got.Packing, PackingPQ2_0)
	}
	// The manifest records the EFFECTIVE backend, never the raw probed one.
	if got.Backend != BackendMetal {
		t.Errorf("manifest backend = %q, want %q", got.Backend, BackendMetal)
	}
	if got.RuntimeVersion != RuntimeTag {
		t.Errorf("manifest runtime_version = %q, want the pinned tag %q", got.RuntimeVersion, RuntimeTag)
	}
	if got.Port != 41977 {
		t.Errorf("manifest port = %d, want the allocated 41977", got.Port)
	}
	if got.ContextSize != 32768 {
		t.Errorf("manifest context_size = %d, want the 32 GiB tier 32768", got.ContextSize)
	}
	wantModel, err := fx.layout.ModelFile(PackingPQ2_0)
	if err != nil {
		t.Fatalf("ModelFile: %v", err)
	}
	if got.ModelFile != wantModel {
		t.Errorf("manifest model_file = %q, want %q", got.ModelFile, wantModel)
	}
	requireFile(t, got.ModelFile, "the installed GGUF")

	stamped, err := time.Parse(time.RFC3339, got.InstalledAt)
	if err != nil {
		t.Fatalf("manifest installed_at %q is not RFC 3339: %v", got.InstalledAt, err)
	}
	if !stamped.Equal(testInstallInstant) {
		t.Errorf("manifest installed_at = %s, want %s", stamped, testInstallInstant)
	}

	// Every downloaded component's verified digest is recorded, keyed by name.
	wantChecksums := map[string]string{
		string(ComponentRuntime): fx.digests[ComponentRuntime],
		string(ComponentModel):   fx.digests[ComponentModel],
		string(ComponentMMProj):  fx.digests[ComponentMMProj],
	}
	if len(got.Checksums) != len(wantChecksums) {
		t.Errorf("manifest checksums = %v, want exactly %v", got.Checksums, wantChecksums)
	}
	for component, want := range wantChecksums {
		if got.Checksums[component] != want {
			t.Errorf("manifest checksums[%q] = %q, want %q", component, got.Checksums[component], want)
		}
	}

	// Both trees exist: the runtime tree with the server binary, the model tree
	// with both GGUF files.
	requireDir(t, report.RuntimeDir, "the runtime tree")
	requireFile(t, report.ServerBinary, "llama-server")
	requireDir(t, fx.layout.ModelRoot, "the model tree")
	mmproj, err := fx.layout.Destination(Asset{Component: ComponentMMProj, ArchiveName: mmprojFileQ8_0})
	if err != nil {
		t.Fatalf("mmproj destination: %v", err)
	}
	requireFile(t, mmproj, "the vision projector")

	// No staging or retired tree survives a successful install.
	staging, err := fx.layout.RuntimeStagingDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeStagingDir: %v", err)
	}
	requireAbsent(t, staging, "the staging tree")
	retired, err := fx.layout.RuntimeRetiredDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeRetiredDir: %v", err)
	}
	requireAbsent(t, retired, "the retired tree")

	// The config sink was called exactly once, with the manifest's facts and
	// the auto-unload defaults.
	calls := fx.sink.installCalls()
	if len(calls) != 1 {
		t.Fatalf("ApplyInstalled called %d times, want 1", len(calls))
	}
	state := calls[0]
	if state.Packing != got.Packing || state.Backend != got.Backend || state.Port != got.Port ||
		state.ModelFile != got.ModelFile || state.RuntimeVersion != got.RuntimeVersion ||
		state.InstalledAt != got.InstalledAt || state.ContextSize != got.ContextSize {
		t.Errorf("InstallState = %+v, does not match the manifest %+v", state, got)
	}
	if !state.AutoUnloadEnabled || state.AutoUnloadMinutes != DefaultAutoUnloadMinutes {
		t.Errorf("auto-unload defaults = (%t, %d), want (true, %d)",
			state.AutoUnloadEnabled, state.AutoUnloadMinutes, DefaultAutoUnloadMinutes)
	}
	if report.Hardware.RAMGiB != 32 || report.Resolution.Backend != BackendMetal {
		t.Errorf("report = %+v, want the probed hardware and the effective backend", report)
	}
}

// TestInstallNothingLandsInTheAgentPATH pins the ASI05 isolation invariant:
// Manager.PrependToPATH() exposes <toolsDir>/bin to the agent's bash_exec, so
// no embedded-LLM byte may ever be written there.
func TestInstallNothingLandsInTheAgentPATH(t *testing.T) {
	for _, tc := range []installCase{darwinMetalCase(), windowsCUDACase(), linuxCPUCase()} {
		t.Run(string(tc.backend), func(t *testing.T) {
			fx := newInstallFixture(t, tc)
			fx.mustInstall(t)

			if files := walkFiles(t, fx.toolsBin); len(files) != 0 {
				t.Errorf("%d files were written under the agent PATH %q: %v",
					len(files), fx.toolsBin, files)
			}
			// Every file the install created lives inside the layout.
			for _, path := range walkFiles(t, fx.agentDir) {
				if !fx.layout.Owns(path) {
					t.Errorf("install wrote %q outside the embedded-LLM layout", path)
				}
			}
		})
	}
}

func TestInstallEmitsProgressPerComponent(t *testing.T) {
	cases := []struct {
		name string
		tc   installCase
		want []Component
	}{
		{"darwin metal", darwinMetalCase(), []Component{ComponentRuntime, ComponentModel, ComponentMMProj}},
		{"windows cuda with cudart", windowsCUDACase(),
			[]Component{ComponentRuntime, ComponentCudart, ComponentModel, ComponentMMProj}},
		{"linux cpu", linuxCPUCase(), []Component{ComponentRuntime, ComponentModel, ComponentMMProj}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newInstallFixture(t, tc.tc)
			fx.mustInstall(t)

			log := fx.progressLog()
			if len(log) == 0 {
				t.Fatal("no progress was emitted")
			}

			// Every component reports its own downloading and done, and each
			// reports its own pinned byte total.
			byComponent := map[Component][]Progress{}
			for _, p := range log {
				byComponent[p.Component] = append(byComponent[p.Component], p)
			}
			if len(byComponent) != len(tc.want) {
				t.Fatalf("progress covered %d components (%v), want %d (%v)",
					len(byComponent), keysOf(byComponent), len(tc.want), tc.want)
			}
			assetByComponent := map[Component]Asset{}
			for _, asset := range fx.assets {
				assetByComponent[asset.Component] = asset
			}
			for _, component := range tc.want {
				events := byComponent[component]
				if len(events) == 0 {
					t.Errorf("component %q reported no progress", component)
					continue
				}
				if events[0].Stage != StageDownloading {
					t.Errorf("component %q's first stage = %q, want %q",
						component, events[0].Stage, StageDownloading)
				}
				if last := events[len(events)-1]; last.Stage != StageDone {
					t.Errorf("component %q's last stage = %q, want %q", component, last.Stage, StageDone)
				}
				dones := 0
				for _, event := range events {
					if event.Stage == StageDone {
						dones++
					}
					if event.BytesTotal != assetByComponent[component].SizeBytes {
						t.Errorf("component %q stage %q total = %d, want the pinned %d",
							component, event.Stage, event.BytesTotal, assetByComponent[component].SizeBytes)
					}
				}
				if dones != 1 {
					t.Errorf("component %q reported %d done events, want exactly 1", component, dones)
				}
				if !hasStage(events, StageVerifying) {
					t.Errorf("component %q never reported the verifying stage", component)
				}
			}

			// Components are reported in the resolved download order. The
			// runtime group's "done" is deferred until the tree is provisioned,
			// so a component may reappear; first appearance is the order that
			// matters, and it must place the whole runtime group first.
			var order []Component
			seen := map[Component]bool{}
			for _, p := range log {
				if seen[p.Component] {
					continue
				}
				seen[p.Component] = true
				order = append(order, p.Component)
			}
			if !sameComponents(order, tc.want) {
				t.Errorf("progress component order = %v, want %v", order, tc.want)
			}

			// Archives are extracted; weights are not.
			for _, asset := range fx.assets {
				events := byComponent[asset.Component]
				switch asset.Component {
				case ComponentRuntime, ComponentCudart:
					if !hasStage(events, StageExtracting) {
						t.Errorf("component %q never reported the extracting stage", asset.Component)
					}
				default:
					if hasStage(events, StageExtracting) {
						t.Errorf("component %q reported an extracting stage; weights are not archives",
							asset.Component)
					}
				}
			}
		})
	}
}

func TestInstallSigningStageOnlyOnMacOS(t *testing.T) {
	darwin := newInstallFixture(t, darwinMetalCase())
	darwin.mustInstall(t)
	if !hasStage(darwin.progressLog(), StageSigning) {
		t.Error("the macOS install never reported the signing stage")
	}

	linux := newInstallFixture(t, linuxCPUCase())
	linux.mustInstall(t)
	if hasStage(linux.progressLog(), StageSigning) {
		t.Error("the Linux install reported a signing stage")
	}
}

// ── acceptance: verification failure leaves no manifest and no provider ──

func TestInstallModelVerificationFailureLeavesNoManifestAndNoProvider(t *testing.T) {
	tc := darwinMetalCase()
	tc.corrupt = ComponentModel
	fx := newInstallFixture(t, tc)

	_, err := fx.Install(context.Background(), InstallOptions{})
	if err == nil {
		t.Fatal("Install succeeded with a substituted model artifact, want a verification failure")
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("Install error = %v, want it to wrap ErrChecksumMismatch", err)
	}
	if !strings.Contains(err.Error(), string(ComponentModel)) {
		t.Errorf("Install error %q does not name the failing component", err)
	}

	requireNoManifest(t, fx.layout)
	if calls := fx.sink.installCalls(); len(calls) != 0 {
		t.Errorf("ApplyInstalled was called %d times; a failed verification must register no provider",
			len(calls))
	}
	modelPath, err := fx.layout.ModelFile(PackingPQ2_0)
	if err != nil {
		t.Fatalf("ModelFile: %v", err)
	}
	requireAbsent(t, modelPath, "the substituted model file")
	requireAbsent(t, modelPath+PartialSuffix, "the model partial")

	// The runtime tree is left in place on purpose: its bytes verified, so the
	// next attempt is a cache hit rather than a fresh multi-hundred-MB download.
	runtimeDir, err := fx.layout.RuntimeDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	requireDir(t, runtimeDir, "the verified runtime tree")
}

func TestInstallRuntimeVerificationFailureStopsBeforeTheWeights(t *testing.T) {
	tc := darwinMetalCase()
	tc.corrupt = ComponentRuntime
	fx := newInstallFixture(t, tc)

	if _, err := fx.Install(context.Background(), InstallOptions{}); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Install error = %v, want ErrChecksumMismatch", err)
	}
	requireNoManifest(t, fx.layout)
	if n := fx.sink.removed(); n != 0 {
		t.Errorf("ApplyRemoved called %d times during a failed install", n)
	}
	if len(fx.sink.installCalls()) != 0 {
		t.Error("a failed install registered a provider")
	}
	// Fail-fast ordering: the weights are never requested when the runtime is bad.
	if n := fx.requestCount(ComponentModel); n != 0 {
		t.Errorf("the model was requested %d times after the runtime failed verification", n)
	}
	runtimeDir, err := fx.layout.RuntimeDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	requireAbsent(t, runtimeDir, "the runtime tree")
}

// ── gates ──

func TestInstallRAMGateRefusesBeforeAnyDownload(t *testing.T) {
	tc := darwinMetalCase()
	tc.ramGiB = MinRAMGiB - 0.5
	fx := newInstallFixture(t, tc)

	_, err := fx.Install(context.Background(), InstallOptions{})
	if !errors.Is(err, ErrInsufficientRAM) {
		t.Fatalf("Install error = %v, want ErrInsufficientRAM", err)
	}
	if n := fx.totalRequests(); n != 0 {
		t.Errorf("an undersized machine made %d HTTP requests, want 0", n)
	}
	if len(fx.sink.installCalls()) != 0 {
		t.Error("an undersized machine registered a provider")
	}
	requireNoManifest(t, fx.layout)
	// Nothing is even created: the refusal happens before the layout is touched.
	requireAbsent(t, fx.layout.ModelRoot, "the model tree")
	requireAbsent(t, fx.layout.RuntimesRoot, "the runtime root")
}

func TestInstallDiskGuardRefusesTheWholeSet(t *testing.T) {
	tc := darwinMetalCase()
	tc.freeSpace = 1024
	fx := newInstallFixture(t, tc)

	_, err := fx.Install(context.Background(), InstallOptions{})
	if !errors.Is(err, ErrInsufficientDisk) {
		t.Fatalf("Install error = %v, want ErrInsufficientDisk", err)
	}
	if n := fx.totalRequests(); n != 0 {
		t.Errorf("a full disk made %d HTTP requests, want 0", n)
	}
	requireNoManifest(t, fx.layout)
	if len(fx.sink.installCalls()) != 0 {
		t.Error("a full disk registered a provider")
	}
	if !strings.Contains(err.Error(), "full embedded-LLM set") {
		t.Errorf("disk error %q does not explain that the guard covers the whole set", err)
	}
}

func TestInstallRefusesWithoutAConfigSink(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	fx.installer.Sink = nil

	_, err := fx.Install(context.Background(), InstallOptions{})
	if err == nil {
		t.Fatal("Install without a ConfigSink succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "ConfigSink") {
		t.Errorf("error %q does not name the missing sink", err)
	}
	if n := fx.totalRequests(); n != 0 {
		t.Errorf("a sinkless install made %d HTTP requests, want 0", n)
	}
}

func TestInstallRequiresUsableLayout(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	fx.installer.Layout = Layout{}

	if _, err := fx.Install(context.Background(), InstallOptions{}); !errors.Is(err, ErrLayoutInvalid) {
		t.Fatalf("Install with a zero Layout error = %v, want ErrLayoutInvalid", err)
	}
}

func TestInstallHonoursCancellation(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := fx.Install(ctx, InstallOptions{}); err == nil {
		t.Fatal("Install with a cancelled context succeeded")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("Install error = %v, want it to wrap context.Canceled", err)
	}
	requireNoManifest(t, fx.layout)
	if len(fx.sink.installCalls()) != 0 {
		t.Error("a cancelled install registered a provider")
	}
}

func TestInstallSinkFailureKeepsAnAccurateManifest(t *testing.T) {
	tc := darwinMetalCase()
	tc.sinkErr = errors.New("config is read-only")
	fx := newInstallFixture(t, tc)

	if _, err := fx.Install(context.Background(), InstallOptions{}); err == nil {
		t.Fatal("Install succeeded although the config sink failed")
	}
	// The bytes are on disk and the manifest describes them accurately, so a
	// retry is idempotent instead of having to start over.
	manifestPath, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	if _, err := ReadManifest(manifestPath); err != nil {
		t.Errorf("ReadManifest after a sink failure: %v", err)
	}
}

// ── macOS provisioning ──

func TestInstallProvisionsMacOSRuntime(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	report := fx.mustInstall(t)

	staging, err := fx.layout.RuntimeStagingDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeStagingDir: %v", err)
	}
	lines := fx.cmds.lines()
	if len(lines) < 4 {
		t.Fatalf("recorded commands = %v, want xattr + 2 codesign + the smoke test", lines)
	}

	// 1. the quarantine is cleared from the whole staged tree
	if want := "xattr -cr " + staging; lines[0] != want {
		t.Errorf("command[0] = %q, want %q", lines[0], want)
	}
	// 2. every Mach-O image is ad-hoc signed, libraries before the executable
	signed := []string{
		filepath.Join(staging, "build", "bin", "libggml-base.dylib"),
		filepath.Join(staging, "build", "bin", "llama-server"),
	}
	for i, image := range signed {
		want := "codesign --force --sign - " + image
		if lines[1+i] != want {
			t.Errorf("command[%d] = %q, want %q", 1+i, lines[1+i], want)
		}
	}
	// A non-Mach-O file in the tree is not signed.
	for _, line := range lines {
		if strings.Contains(line, "README.md") {
			t.Errorf("command %q signs a non-Mach-O file", line)
		}
	}
	// 3. the staged server is smoke-tested BEFORE it is promoted
	wantSmoke := filepath.Join(staging, "build", "bin", "llama-server") + " --version"
	if lines[len(lines)-1] != wantSmoke {
		t.Errorf("last command = %q, want the staged smoke test %q", lines[len(lines)-1], wantSmoke)
	}
	// The smoke test ran against the STAGING copy, i.e. before promotion: a
	// runtime that does not run must never reach its final location.
	if strings.Contains(lines[len(lines)-1], report.RuntimeDir+string(filepath.Separator)) {
		t.Errorf("the smoke test ran against the promoted tree %q, want the staging tree",
			report.RuntimeDir)
	}
	if !strings.Contains(lines[len(lines)-1], staging) {
		t.Errorf("the smoke test %q did not run inside the staging tree %q",
			lines[len(lines)-1], staging)
	}
}

func TestInstallSkipsMacOSProvisioningOnOtherPlatforms(t *testing.T) {
	for _, tc := range []installCase{linuxCPUCase(), windowsCUDACase()} {
		t.Run(tc.hostOS, func(t *testing.T) {
			fx := newInstallFixture(t, tc)
			fx.mustInstall(t)
			if lines := fx.cmds.lines(); len(lines) != 0 {
				t.Errorf("%s ran %d provisioning commands %v, want none", tc.hostOS, len(lines), lines)
			}
		})
	}
}

func TestInstallSmokeTestFailureIsActionable(t *testing.T) {
	tc := darwinMetalCase()
	tc.failCommand = "--version"
	fx := newInstallFixture(t, tc)

	_, err := fx.Install(context.Background(), InstallOptions{})
	if !errors.Is(err, ErrSmokeTestFailed) {
		t.Fatalf("Install error = %v, want ErrSmokeTestFailed", err)
	}
	message := err.Error()
	for _, want := range []string{
		"xattr -dr com.apple.quarantine",
		"System Settings",
		"Allow Anyway",
		"--version failed",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("smoke-test error does not mention %q:\n%s", want, message)
		}
	}

	// A runtime that does not run is never promoted and never registered.
	requireNoManifest(t, fx.layout)
	if len(fx.sink.installCalls()) != 0 {
		t.Error("a failed smoke test registered a provider")
	}
	runtimeDir, err := fx.layout.RuntimeDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	requireAbsent(t, runtimeDir, "the runtime tree")
	staging, err := fx.layout.RuntimeStagingDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeStagingDir: %v", err)
	}
	requireDir(t, staging, "the staged runtime, kept for diagnosis")

	// Fail-fast: the weights were never downloaded.
	if n := fx.requestCount(ComponentModel); n != 0 {
		t.Errorf("the model was requested %d times although the runtime does not run", n)
	}
}

func TestInstallToleratesMissingMacOSTools(t *testing.T) {
	// A machine without /usr/bin/xattr must still install: the smoke test is
	// the authority on whether the runtime runs.
	tc := darwinMetalCase()
	tc.missingCommand = "xattr"
	fx := newInstallFixture(t, tc)
	fx.mustInstall(t)

	manifestPath, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	if _, err := ReadManifest(manifestPath); err != nil {
		t.Errorf("ReadManifest: %v", err)
	}
}

func TestInstallCodesignFailureIsFatal(t *testing.T) {
	tc := darwinMetalCase()
	tc.failCommand = "codesign"
	fx := newInstallFixture(t, tc)

	if _, err := fx.Install(context.Background(), InstallOptions{}); err == nil {
		t.Fatal("Install succeeded although codesign failed")
	} else if !strings.Contains(err.Error(), "ad-hoc signing") {
		t.Errorf("error %q does not explain that signing failed", err)
	}
	requireNoManifest(t, fx.layout)
}

// ── the secure-bytes-before-destroy invariant ──

func TestInstallRetiresThePreviousRuntimeOnlyAfterTheNewOneIsProven(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	runtimeDir, err := fx.layout.RuntimeDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	previous := filepath.Join(runtimeDir, "PREVIOUS-INSTALL")
	if err := os.MkdirAll(filepath.Dir(previous), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(previous, []byte("old runtime"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	report := fx.mustInstall(t)
	if report.RuntimeDir != runtimeDir {
		t.Fatalf("report RuntimeDir = %q, want %q", report.RuntimeDir, runtimeDir)
	}
	requireAbsent(t, previous, "the previous runtime's marker file")
	requireFile(t, report.ServerBinary, "the new llama-server")

	retired, err := fx.layout.RuntimeRetiredDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeRetiredDir: %v", err)
	}
	requireAbsent(t, retired, "the retired tree")
	staging, err := fx.layout.RuntimeStagingDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeStagingDir: %v", err)
	}
	requireAbsent(t, staging, "the staging tree")
}

func TestInstallIsIdempotentAndUsesTheVerifiedCache(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	first := fx.mustInstall(t)
	requestsAfterFirst := fx.totalRequests()
	if requestsAfterFirst != len(fx.assets) {
		t.Fatalf("the first install made %d requests, want one per asset (%d)",
			requestsAfterFirst, len(fx.assets))
	}

	second := fx.mustInstall(t)
	if got := fx.totalRequests(); got != requestsAfterFirst {
		t.Errorf("the second install made %d more requests; verified artifacts must be cache hits",
			got-requestsAfterFirst)
	}
	if second.Manifest.InstalledAt != first.Manifest.InstalledAt {
		// The clock is fixed in the fixture, so a re-install rewrites the same
		// record rather than leaving a half-updated one.
		t.Errorf("installed_at changed between identical installs: %q → %q",
			first.Manifest.InstalledAt, second.Manifest.InstalledAt)
	}
	if calls := fx.sink.installCalls(); len(calls) != 2 {
		t.Errorf("ApplyInstalled called %d times across two installs, want 2", len(calls))
	}
}

func TestInstallResumesAnInterruptedRuntimeDownload(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	runtimeAsset := fx.assets[0]
	dst, err := fx.layout.Destination(runtimeAsset)
	if err != nil {
		t.Fatalf("Destination: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A partial left behind by an interrupted run: the downloader resumes it.
	if err := os.WriteFile(dst+PartialSuffix, []byte("truncated"), 0o600); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	fx.mustInstall(t)
	requireFile(t, dst, "the runtime archive")
	requireAbsent(t, dst+PartialSuffix, "the runtime partial")
}

// ── acceptance: Remove ──

func TestRemoveDeletesBothTreesAndClearsTheConfig(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	report := fx.mustInstall(t)

	// Decoys that must survive: the flat embedding-model files sharing
	// <agentDir>/models, and the agent's PATH directory.
	embeddingModel := filepath.Join(fx.agentDir, "models", "ggml-model-q4_0.gguf")
	if err := os.WriteFile(embeddingModel, []byte("embedding model"), 0o600); err != nil {
		t.Fatalf("write embedding model decoy: %v", err)
	}
	agentTool := filepath.Join(fx.toolsBin, "rg")
	if err := os.WriteFile(agentTool, []byte("tool"), 0o755); err != nil {
		t.Fatalf("write agent tool decoy: %v", err)
	}

	// Observe the filesystem at the moment the config is cleared: the trees
	// must already be gone, so a config can never claim an install whose files
	// were not deleted.
	var modelRootGone, runtimeGone bool
	fx.sink.onRemoved = func() {
		_, err := os.Stat(fx.layout.ModelRoot)
		modelRootGone = errors.Is(err, os.ErrNotExist)
		_, err = os.Stat(report.RuntimeDir)
		runtimeGone = errors.Is(err, os.ErrNotExist)
	}

	if err := fx.installer.Remove(context.Background()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	requireAbsent(t, fx.layout.ModelRoot, "~/.c0wrk/models/bonsai-2-27b")
	requireAbsent(t, report.RuntimeDir, "the runtime tree")
	if !modelRootGone || !runtimeGone {
		t.Errorf("the config was cleared before the trees were deleted (modelGone=%t runtimeGone=%t)",
			modelRootGone, runtimeGone)
	}
	if n := fx.sink.removed(); n != 1 {
		t.Errorf("ApplyRemoved called %d times, want 1", n)
	}

	// Every llama-* tree is gone, including leftovers of other backends.
	entries, err := os.ReadDir(fx.layout.RuntimesRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("listing the runtime root: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "llama-") {
			t.Errorf("Remove left %q behind in the runtime root", entry.Name())
		}
	}
	downloads, err := fx.layout.DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir: %v", err)
	}
	requireAbsent(t, downloads, "the archive staging area")

	requireFile(t, embeddingModel, "the flat embedding model")
	requireFile(t, agentTool, "the agent's PATH tool")
}

func TestRemoveDeletesEveryRuntimeTreeAndStagingLeftovers(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	fx.mustInstall(t)

	// A second backend's tree and an interrupted staging tree, both of which a
	// user-visible "Remove" must reclaim.
	extras := make([]string, 0, 3)
	for _, backend := range []Backend{BackendCPU, BackendVulkan} {
		dir, err := fx.layout.RuntimeDir(backend)
		if err != nil {
			t.Fatalf("RuntimeDir(%s): %v", backend, err)
		}
		extras = append(extras, dir)
	}
	staging, err := fx.layout.RuntimeStagingDir(BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeStagingDir: %v", err)
	}
	extras = append(extras, staging)
	for _, dir := range extras {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
	}

	if err := fx.installer.Remove(context.Background()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, dir := range extras {
		requireAbsent(t, dir, "a runtime tree Remove should have reclaimed")
	}
}

func TestRemoveStopsTheServerFirst(t *testing.T) {
	tc := darwinMetalCase()
	tc.stopErr = errors.New("the server did not exit")
	fx := newInstallFixture(t, tc)
	report := fx.mustInstall(t)
	fx.installer.Stop = func(context.Context) error { return tc.stopErr }

	err := fx.installer.Remove(context.Background())
	if err == nil {
		t.Fatal("Remove succeeded although the server would not stop")
	}
	if !strings.Contains(err.Error(), "stopping the inference server") {
		t.Errorf("error %q does not say the stop failed", err)
	}
	// Nothing is deleted while a live process still holds the files: on
	// Windows the deletion would fail outright, and on Unix it would leave a
	// running server pointing at unlinked inodes.
	requireDir(t, report.RuntimeDir, "the runtime tree")
	requireDir(t, fx.layout.ModelRoot, "the model tree")
	if n := fx.sink.removed(); n != 0 {
		t.Errorf("ApplyRemoved called %d times after a failed stop, want 0", n)
	}
}

func TestRemoveIsIdempotentWhenNothingIsInstalled(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	if err := fx.installer.Remove(context.Background()); err != nil {
		t.Fatalf("Remove on a fresh layout: %v", err)
	}
	if n := fx.sink.removed(); n != 1 {
		t.Errorf("ApplyRemoved called %d times, want 1 (clearing config is always safe)", n)
	}
	if err := fx.installer.Remove(context.Background()); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	if n := fx.sink.removed(); n != 2 {
		t.Errorf("ApplyRemoved called %d times, want 2", n)
	}
}

func TestRemoveClearsConfigEvenWhenADeletionFails(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	report := fx.mustInstall(t)

	// Make the runtime tree undeletable by replacing it with a non-empty
	// directory whose child is write-protected... instead, point the layout at
	// a root that cannot be listed: a file where a directory is expected.
	if err := os.RemoveAll(report.RuntimeDir); err != nil {
		t.Fatalf("removing the runtime tree: %v", err)
	}
	if err := os.WriteFile(report.RuntimeDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("writing over the runtime dir: %v", err)
	}
	// RemoveAll on a file succeeds, so break the listing instead: make the
	// runtime root unreadable.
	if err := os.Chmod(fx.layout.RuntimesRoot, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(fx.layout.RuntimesRoot, 0o750) })

	err := fx.installer.Remove(context.Background())
	if err == nil {
		t.Skip("the platform allowed the deletion despite the unreadable directory")
	}
	if n := fx.sink.removed(); n != 1 {
		t.Errorf("ApplyRemoved called %d times, want 1: a leftover directory wastes disk, "+
			"a config claiming a deleted install points the router at nothing", n)
	}
	requireAbsent(t, fx.layout.ModelRoot, "the model tree")
}

func TestRemoveOwnedRefusesPathsOutsideTheLayout(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	outside := filepath.Join(fx.agentDir, "config.yaml")
	if err := os.WriteFile(outside, []byte("operator config"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := fx.installer.removeOwned(outside)
	if err == nil {
		t.Fatal("removeOwned deleted a path outside the layout")
	}
	if !strings.Contains(err.Error(), "outside the embedded-LLM storage layout") {
		t.Errorf("error %q does not explain the refusal", err)
	}
	requireFile(t, outside, "the operator's config")

	if err := fx.installer.removeOwned(""); err == nil {
		t.Error("removeOwned(\"\") succeeded, want a refusal")
	}

	// The two neighbours that must never be touched: <toolsDir>/bin, which the
	// agent's PATH exposes, and the <agentDir>/models root, whose flat files
	// are the embedding model resolved by desktop/startup.go.
	embeddingModel := filepath.Join(fx.agentDir, "models", "ggml-model-q4_0.gguf")
	if err := os.MkdirAll(filepath.Dir(embeddingModel), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(embeddingModel, []byte("embedding model"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, path := range []string{fx.toolsBin, filepath.Join(fx.agentDir, "models"), outside} {
		if err := fx.installer.removeOwned(path); err == nil {
			t.Errorf("removeOwned(%q) succeeded, want a refusal", path)
		}
		if _, serr := os.Stat(path); serr != nil {
			t.Errorf("removeOwned deleted %q, a path outside the layout: %v", path, serr)
		}
	}
	requireFile(t, embeddingModel, "the flat embedding model")
}

func TestRemoveRequiresAConfigSink(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	fx.mustInstall(t)
	fx.installer.Sink = nil

	if err := fx.installer.Remove(context.Background()); err == nil {
		t.Fatal("Remove without a ConfigSink succeeded, want a refusal")
	}
	requireDir(t, fx.layout.ModelRoot, "the model tree, which a sinkless Remove must not delete")
}

// ── manifest ──

func TestManifestRoundTripAndAtomicWrite(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	fx.mustInstall(t)

	manifestPath, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}
	// The JSON keys are the documented wire format.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("the manifest is not valid JSON: %v", err)
	}
	for _, key := range []string{
		"packing", "backend", "runtime_version", "checksums",
		"port", "context_size", "model_file", "installed_at",
	} {
		if _, ok := fields[key]; !ok {
			t.Errorf("manifest JSON is missing the %q field: %s", key, raw)
		}
	}
	requireAbsent(t, filepath.Join(fx.layout.ModelRoot, manifestTempName), "the manifest temporary")

	got, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Packing != PackingPQ2_0 || got.Backend != BackendMetal {
		t.Errorf("ReadManifest = %+v, want the installed packing and backend", got)
	}
}

func TestReadManifestReportsNotInstalled(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadManifest(filepath.Join(dir, ManifestFileName))
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("ReadManifest on a missing file error = %v, want ErrNotInstalled", err)
	}

	path := filepath.Join(dir, ManifestFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadManifest(path); err == nil {
		t.Error("ReadManifest accepted a corrupt manifest")
	} else if errors.Is(err, ErrNotInstalled) {
		t.Errorf("a corrupt manifest reported ErrNotInstalled: %v", err)
	}
}

func TestWriteManifestIsAtomicOverAnExistingRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ManifestFileName)
	previous := Manifest{Packing: PackingPQ2_0, Backend: BackendCPU, Port: 1, RuntimeVersion: RuntimeTag}
	if err := writeManifest(path, previous); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	next := Manifest{Packing: PackingPTQ1_0, Backend: BackendVulkan, Port: 2, RuntimeVersion: RuntimeTag}
	if err := writeManifest(path, next); err != nil {
		t.Fatalf("second writeManifest: %v", err)
	}
	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Packing != PackingPTQ1_0 || got.Backend != BackendVulkan || got.Port != 2 {
		t.Errorf("manifest = %+v, want the rewritten record", got)
	}
	requireAbsent(t, filepath.Join(dir, manifestTempName), "the manifest temporary")
}

// ── extraction guards ──

func TestExtractArchiveRefusesTraversal(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "runtime")
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	archive := tarGzBytes(t, []archiveFile{
		{name: "../escaped.txt", body: "pwned", mode: 0o644},
		{name: "build/bin/llama-server", body: "ok", mode: 0o755},
	})
	path := filepath.Join(root, "archive.tar.gz")
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := extractArchive(path, dest); err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	requireAbsent(t, filepath.Join(root, "escaped.txt"), "the traversal target")
	requireFile(t, filepath.Join(dest, "build", "bin", ServerBinaryName), "the legitimate entry")
}

func TestExtractZipRefusesTraversal(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "runtime")
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	archive := zipBytes(t, []archiveFile{
		{name: "../escaped.txt", body: "pwned", mode: 0o644},
		{name: "cudart64_12.dll", body: "ok", mode: 0o644},
	})
	path := filepath.Join(root, "archive.zip")
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := extractArchive(path, dest); err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	requireAbsent(t, filepath.Join(root, "escaped.txt"), "the traversal target")
	requireFile(t, filepath.Join(dest, "cudart64_12.dll"), "the legitimate entry")
}

func TestExtractArchiveRefusesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "runtime")
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     "link",
		Typeflag: tar.TypeSymlink,
		Linkname: "../../etc/passwd",
		Mode:     0o777,
		ModTime:  testInstallInstant,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	path := filepath.Join(root, "archive.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := extractArchive(path, dest)
	if err == nil {
		t.Fatal("extractArchive accepted a symlink pointing outside the tree")
	}
	if !strings.Contains(err.Error(), "outside the runtime tree") {
		t.Errorf("error %q does not explain the refusal", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "link")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the escaping symlink was created")
	}
}

func TestExtractArchiveRejectsUnknownFormat(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "artifact.rar")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := extractArchive(path, filepath.Join(root, "dest"))
	if err == nil {
		t.Fatal("extractArchive accepted an unknown format")
	}
	if !strings.Contains(err.Error(), "unsupported archive format") {
		t.Errorf("error %q does not name the problem", err)
	}
}

// ── port allocation ──

func TestEphemeralLoopbackPortIsUsable(t *testing.T) {
	port, err := ephemeralLoopbackPort(context.Background())
	if err != nil {
		t.Fatalf("ephemeralLoopbackPort: %v", err)
	}
	if port < 1 || port > 65535 {
		t.Errorf("port = %d, want a valid TCP port", port)
	}
}

func TestInstallUsesTheAllocatedPortWhenNoneIsGiven(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	fx.installer.AllocatePort = nil
	report := fx.mustInstall(t)

	if report.Manifest.Port <= 0 {
		t.Errorf("manifest port = %d, want an allocated loopback port", report.Manifest.Port)
	}
	if report.Manifest.Port != fx.sink.installCalls()[0].Port {
		t.Errorf("the config state's port %d differs from the manifest's %d",
			fx.sink.installCalls()[0].Port, report.Manifest.Port)
	}
}

func TestInstallPrefersAnExplicitPort(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	report := fx.mustInstall(t) // InstallOptions.Port comes from the case (0 → allocated)

	if report.Manifest.Port != 41977 {
		t.Errorf("manifest port = %d, want the injected allocator's 41977", report.Manifest.Port)
	}

	other := newInstallFixture(t, darwinMetalCase())
	explicit, err := other.Install(context.Background(), InstallOptions{Port: 51111})
	if err != nil {
		t.Fatalf("Install with an explicit port: %v", err)
	}
	if explicit.Manifest.Port != 51111 {
		t.Errorf("manifest port = %d, want the explicit 51111", explicit.Manifest.Port)
	}
}

// ── small helpers ──

func hasStage(events []Progress, stage string) bool {
	for _, event := range events {
		if event.Stage == stage {
			return true
		}
	}
	return false
}

func keysOf(m map[Component][]Progress) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, string(key))
	}
	sort.Strings(out)
	return out
}

// sameComponents reports whether got contains every want component exactly
// once, in order, allowing a component to be absent from got only when it is
// absent from want.
func sameComponents(got, want []Component) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
