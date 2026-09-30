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
	"reflect"
	"slices"
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
	// sinkErr makes the config sink fail. A failing server stop is set per test
	// on fx.installer.Stop, because Install now stops the resident server too
	// and mustInstall would otherwise inherit the failure.
	sinkErr error
	// devices is the accelerator inventory the fixture's device probe answers
	// with. Empty means "the probe did not answer", which is what a machine with
	// no runtime to ask reports.
	devices []DeviceMemory
	// guardBackends lists backends a compatibility guard may substitute into,
	// which the fixture's asset table must then also serve: a guard that swaps
	// cuda-13.3 for cuda-12.8 needs a 12.8 archive to swap TO.
	guardBackends []Backend
	// cu12 is the CUDA 12.x userland verdict the injected hardware probe
	// reports. Zero (unknown) is the honest default: an install fixture that
	// does not name it models a machine nobody could measure.
	cu12 CUDA12Userland
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
	t          *testing.T
	case_      installCase
	agentDir   string
	layout     Layout
	toolsBin   string
	installer  *Installer
	sink       *recordingSink
	cmds       *commandRecorder
	assets     []Asset
	digests    map[Component]string
	runOptions []RunOptions

	mu       sync.Mutex
	progress []Progress
	requests map[string]int
	// events is the ORDERED log of the externally observable install steps —
	// a command run, an artifact requested, the device probe answering — so a
	// test can assert the sequence and not only the set. The counts above say
	// what happened; this says when.
	events []string
}

// recordEvent appends one step to the ordered install log.
func (fx *installFixture) recordEvent(name string) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	fx.events = append(fx.events, name)
}

// eventLog returns a copy of the ordered install log.
func (fx *installFixture) eventLog() []string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	out := make([]string, len(fx.events))
	copy(out, fx.events)
	return out
}

// eventIndex returns the position of the first event whose name contains
// substr, or -1 when there is none.
func (fx *installFixture) eventIndex(substr string) int {
	for i, name := range fx.eventLog() {
		if strings.Contains(name, substr) {
			return i
		}
	}
	return -1
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
		ProbeDevices: fx.probeDevices,
		RunCommand:   fx.runCommand,
		AllocatePort: func(context.Context) (int, error) { return 41977, nil },
		Now:          func() time.Time { return testInstallInstant },
		HostOS:       tc.hostOS,
	}
	return fx
}

// probe is the injected hardware probe.
func (fx *installFixture) probe(context.Context, *slog.Logger) (Hardware, error) {
	return Hardware{
		Platform:       fx.case_.platform,
		Arch:           fx.case_.arch,
		RAMGiB:         fx.case_.ramGiB,
		Backend:        fx.case_.backend,
		CUDA12Userland: fx.case_.cu12,
	}, nil
}

// probeDevices is the injected device probe. It answers with the case's
// inventory and never spawns anything: a test must not depend on a real
// llama-server being on disk, and an empty inventory is the honest "no answer"
// that leaves a plan on its statically decidable guards.
func (fx *installFixture) probeDevices(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
	fx.recordEvent("device-probe")
	if len(fx.case_.devices) == 0 {
		return MemoryTopology{}, false
	}
	return MemoryTopology{
		Devices:    fx.case_.devices,
		HostRAMGiB: fx.case_.ramGiB,
	}, true
}

// runCommand is the injected CommandRunner. It records every invocation and
// answers the smoke test with a plausible llama-server banner. The launch
// options (the smoke test's launch configuration) are recorded separately for
// TestSmokeTestRunsUnderTheLaunchEnvironment.
func (fx *installFixture) runCommand(_ context.Context, name string, opts *RunOptions, args ...string) (string, error) {
	if opts != nil {
		fx.mu.Lock()
		fx.runOptions = append(fx.runOptions, *opts)
		fx.mu.Unlock()
	}
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	fx.cmds.record(line)
	fx.recordEvent("cmd:" + line)
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

// smokeRunOptions returns copies of the launch options runner calls carried,
// in call order.
func (fx *installFixture) smokeRunOptions() []RunOptions {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	out := make([]RunOptions, len(fx.runOptions))
	copy(out, fx.runOptions)
	return out
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
		ComponentModel:   []byte("ternary bonsai 2 27b weights fixture (" + string(packingForBackend(tc.backend)) + ")"),
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
	packing := packingForBackend(tc.backend)

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
			fx.events = append(fx.events, "download:"+strings.TrimPrefix(path, "/"))
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

// serves reports whether the fixture has artifacts for a backend: the case's
// own, plus any backend a compatibility guard is expected to substitute into.
func (f fixtureAssetTable) serves(backend Backend) bool {
	return backend == f.tc.backend || slices.Contains(f.tc.guardBackends, backend)
}

func (f fixtureAssetTable) RuntimeAsset(platform string, backend Backend) (Asset, bool) {
	if platform != f.tc.platform || !f.serves(backend) {
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
	if !f.serves(backend) {
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

// TestInstallMemoryGateRefusesBeforeAnyDownload is the ordering invariant: the
// gate is the FIRST thing an install does, so a machine that cannot hold the
// model makes no HTTP request, registers no provider, writes no manifest and
// CREATES NO DIRECTORY. A refusal that left a multi-gigabyte partial install
// behind would be worse than no gate at all.
//
// 8 GiB on Apple Silicon is refused on the measurements, not on a threshold:
// the accelerator's pool IS the host pool there, so the RAM probe priced both,
// and the smallest modelled shape still does not fit inside the reserve.
func TestInstallStopsTheServerFirst(t *testing.T) {
	// A repair runs on a machine that may already host the model: the resident
	// server executes out of the runtime tree this install retires, and holds
	// the memory step 2's gate is about to price. Stopping it is step 0, so it
	// happens even when the install is refused afterwards, and a server that
	// will not stop refuses the install outright.
	t.Run("a stop failure refuses the install before anything is fetched", func(t *testing.T) {
		fx := newInstallFixture(t, darwinMetalCase())
		fx.installer.Stop = func(context.Context) error { return errors.New("the server did not exit") }

		_, err := fx.Install(context.Background(), InstallOptions{})
		if err == nil {
			t.Fatal("Install succeeded although the resident server would not stop")
		}
		if !strings.Contains(err.Error(), "stopping the inference server before installation") {
			t.Errorf("error %q does not say which stop failed", err)
		}
		if n := fx.totalRequests(); n != 0 {
			t.Errorf("a refused install made %d HTTP requests, want 0", n)
		}
		if len(fx.sink.installCalls()) != 0 {
			t.Error("a refused install registered a provider")
		}
		requireNoManifest(t, fx.layout)
	})

	t.Run("the stop precedes the memory gate", func(t *testing.T) {
		tc := darwinMetalCase()
		tc.ramGiB = 8 // a machine the gate refuses
		fx := newInstallFixture(t, tc)
		var stopped bool
		fx.installer.Stop = func(context.Context) error { stopped = true; return nil }

		if _, err := fx.Install(context.Background(), InstallOptions{}); !errors.Is(err, ErrInsufficientMemory) {
			t.Fatalf("Install error = %v, want ErrInsufficientMemory", err)
		}
		if !stopped {
			t.Error("the gate refused before the resident server was stopped, " +
				"so it priced memory the old server still holds")
		}
	})

	t.Run("a successful install stops the server exactly once", func(t *testing.T) {
		fx := newInstallFixture(t, darwinMetalCase())
		var stops int
		fx.installer.Stop = func(context.Context) error { stops++; return nil }

		fx.mustInstall(t)
		if stops != 1 {
			t.Errorf("Install called Stop %d times, want 1", stops)
		}
	})

	// The shape step 0 exists for: a REPAIR on a machine whose server is actually
	// RESIDENT. The subtests above pin the refusal, the gate ordering and the call
	// count through a stub seam; this one pins that the seam really takes a live
	// process down while the tree it executes from is still in place — i.e. before
	// step 7 retires it, which on Windows is the difference between a repair and
	// ERROR_ACCESS_DENIED.
	t.Run("a resident server is stopped before the runtime tree is retired", func(t *testing.T) {
		fx := newInstallFixture(t, darwinMetalCase())
		previous := fx.mustInstall(t)

		// A marker inside the installed runtime tree: promoteRuntime retires the
		// whole tree, so the marker disappears exactly when the tree does.
		marker := filepath.Join(previous.RuntimeDir, "c0wrk-repair-marker")
		if err := os.WriteFile(marker, []byte("the tree the resident server executes from"), 0o600); err != nil {
			t.Fatalf("planting the runtime marker: %v", err)
		}

		// A genuinely resident server over the same layout: loaded, supervised and
		// holding a live process.
		endpoint := newModelsEndpoint(t)
		endpoint.serveProps(16384, 1)
		manifestPath, err := fx.layout.ManifestPath()
		if err != nil {
			t.Fatalf("ManifestPath: %v", err)
		}
		record, err := ReadManifest(manifestPath)
		if err != nil {
			t.Fatalf("ReadManifest: %v", err)
		}
		record.Port = endpoint.port
		if err := writeManifest(manifestPath, record, nil); err != nil {
			t.Fatalf("aiming the record at the fake server: %v", err)
		}

		spawner := &fakeSpawner{}
		srv := NewServer(fx.layout, dlDiscardLogger())
		srv.Spawn = spawner.spawn
		srv.HostOS = fx.case_.hostOS
		srv.Platform = fx.case_.platform
		srv.ReadyPollInterval = 2 * time.Millisecond
		if err := srv.SetInstalled(true); err != nil {
			t.Fatalf("SetInstalled: %v", err)
		}
		if err := srv.Load(context.Background()); err != nil {
			t.Fatalf("Load: %v", err)
		}
		proc := spawner.lastProcess()
		if proc == nil || !proc.alive() {
			t.Fatal("the repair fixture has no resident server to stop")
		}

		// The seam observed from the inside: at the moment Install calls Stop, the
		// process must still be alive and the tree it executes from must still be
		// in place — the latter is only true before step 7 retires it.
		var (
			residentAtStop bool
			treeAtStop     bool
			stateAtStop    State
		)
		fx.installer.Stop = func(ctx context.Context) error {
			residentAtStop = proc.alive()
			stateAtStop = srv.State()
			_, statErr := os.Stat(marker)
			treeAtStop = statErr == nil
			return srv.Stop(ctx)
		}

		fx.mustInstall(t)

		if !residentAtStop || stateAtStop != StateLoaded {
			t.Errorf("Install stopped a server that was resident=%v state=%s, want a live loaded one",
				residentAtStop, stateAtStop)
		}
		if !treeAtStop {
			t.Error("the runtime tree was already retired when Install stopped the server: " +
				"on Windows that rename is refused while the process executes from it")
		}
		if proc.alive() {
			t.Error("the resident server survived the repair install")
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the previous runtime tree was not retired by the repair (stat: %v)", err)
		}
		if got := srv.State(); got != StateInstalled {
			t.Errorf("state after the repair = %s, want %s", got, StateInstalled)
		}
	})

	// The BOUND on that seam, which is what Installer.StopTimeout delivers. Step 0
	// runs on the caller's context, and the backend's install runs on the
	// never-deadlined APPLICATION context, so without a bound a Load that claimed
	// the supervisor's single-instance gate first parks the install here for the
	// length of that load — up to DefaultReadyTimeout (15 min). Step 0 precedes
	// plan, so it precedes the first install_progress event: the operator watches a
	// bar at 0% that never moves while the operation gate makes every other
	// embedded call refuse. An expired budget is not a worse outcome than that
	// wait, it is core's force path: the resident child is terminated on a
	// detached budget of its own and the repair proceeds.
	t.Run("a load holding the gate cannot wedge step 0 past StopTimeout", func(t *testing.T) {
		fx := newInstallFixture(t, darwinMetalCase())
		fx.mustInstall(t) // a repair: the trees and the manifest already exist

		// A load whose model list never appears holds the gate for its whole ready
		// budget, which is the production hazard: a cold load in flight when the
		// operator clicks Install or repair.
		const readyBudget = 20 * time.Second
		endpoint := newModelsEndpoint(t)
		endpoint.neverReady = true
		manifestPath, err := fx.layout.ManifestPath()
		if err != nil {
			t.Fatalf("ManifestPath: %v", err)
		}
		record, err := ReadManifest(manifestPath)
		if err != nil {
			t.Fatalf("ReadManifest: %v", err)
		}
		record.Port = endpoint.port
		if err := writeManifest(manifestPath, record, nil); err != nil {
			t.Fatalf("aiming the record at the never-ready server: %v", err)
		}

		spawner := &fakeSpawner{}
		srv := NewServer(fx.layout, dlDiscardLogger())
		srv.Spawn = spawner.spawn
		srv.HostOS = fx.case_.hostOS
		srv.Platform = fx.case_.platform
		srv.ReadyPollInterval = 2 * time.Millisecond
		srv.ReadyTimeout = readyBudget
		srv.StopTimeout = 250 * time.Millisecond
		if err := srv.SetInstalled(true); err != nil {
			t.Fatalf("SetInstalled: %v", err)
		}
		loadDone := make(chan error, 1)
		go func() { loadDone <- srv.Load(context.Background()) }()
		// The run handle must be PUBLISHED, not merely spawned: forceUnload takes it
		// with takeRun, and a step 0 that lands before the publish finds no child to
		// kill and reports the gate timeout instead — the honest answer for that
		// interleaving, but not the one under test here.
		waitFor(t, 5*time.Second, func() bool { return srv.Status().Pid != 0 },
			"the in-flight load to publish its process")

		const stepZeroBudget = 200 * time.Millisecond
		// Measured from INSIDE the seam so the assertion is about step 0 and not
		// about the download that follows it. Install calls Stop synchronously on
		// this goroutine, so the write needs no synchronization.
		var stopElapsed time.Duration
		fx.installer.Stop = func(ctx context.Context) error {
			started := time.Now()
			defer func() { stopElapsed = time.Since(started) }()
			return srv.Stop(ctx)
		}
		fx.installer.StopTimeout = stepZeroBudget

		// Derived from this fixture's own budgets rather than hardcoded, the way the
		// backend's equivalent bound is: the gate wait is stepZeroBudget, and the
		// force path that follows arms srv.StopTimeout + killWait + 1s on a detached
		// context. The slack multiple covers a loaded CI machine; the pre-fix shape
		// it must reject is readyBudget — 20s, and ~3× this bound.
		const slack = 4
		bound := slack * (stepZeroBudget + srv.StopTimeout + time.Second)

		report, err := fx.Install(context.Background(), InstallOptions{})

		if stopElapsed > bound {
			t.Errorf("step 0 waited %v, over the %v bound derived from its own budgets: the "+
				"install sat out a load that holds the gate for %v instead of taking core's "+
				"force path, which is a progress bar at 0%% with no event ever emitted",
				stopElapsed, bound, readyBudget)
		}
		if err != nil {
			t.Fatalf("Install = %v, want the repair to proceed once the force path took the "+
				"resident child down", err)
		}
		if report.Manifest.RuntimeVersion == "" {
			t.Errorf("the repair reported no runtime version: %+v", report.Manifest)
		}
		if proc := spawner.lastProcess(); proc == nil || proc.alive() {
			t.Error("the in-flight load's child survived the bounded step-0 stop")
		}
		select {
		case <-loadDone:
		case <-time.After(5 * time.Second):
			t.Error("the interrupted load never returned")
		}
	})
}

func TestInstallMemoryGateRefusesBeforeAnyDownload(t *testing.T) {
	tc := darwinMetalCase()
	tc.ramGiB = 8
	fx := newInstallFixture(t, tc)

	_, err := fx.Install(context.Background(), InstallOptions{})
	if !errors.Is(err, ErrInsufficientMemory) {
		t.Fatalf("Install error = %v, want ErrInsufficientMemory", err)
	}
	// The refusal names both pools with both numbers, all the way out through
	// the installer's own error wrapping.
	for _, want := range []string{"device memory", "host RAM", "GiB available"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Install error %q does not name %q", err, want)
		}
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

	// 1. the quarantine is cleared from the whole staged tree — through the
	// ABSOLUTE helper path, never a PATH lookup an attacker-controlled PATH
	// element could answer (see provisionDarwin).
	if want := darwinXattr + " -cr " + staging; lines[0] != want {
		t.Errorf("command[0] = %q, want %q", lines[0], want)
	}
	// 2. every Mach-O image is ad-hoc signed, libraries before the executable
	signed := []string{
		filepath.Join(staging, "build", "bin", "libggml-base.dylib"),
		filepath.Join(staging, "build", "bin", "llama-server"),
	}
	for i, image := range signed {
		want := darwinCodesign + " --force --sign - " + image
		if lines[1+i] != want {
			t.Errorf("command[%d] = %q, want %q", 1+i, lines[1+i], want)
		}
	}
	// No provisioning helper is resolved through PATH.
	for _, line := range lines {
		if strings.HasPrefix(line, "xattr ") || strings.HasPrefix(line, "codesign ") {
			t.Errorf("command %q resolves a macOS helper through PATH, want the absolute path", line)
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

// TestInstallSkipsMacOSProvisioningOnOtherPlatforms pins the platform split:
// xattr/codesign never run off darwin, while the --version smoke test runs on
// EVERY platform — a Linux or Windows runtime that cannot execute must fail
// the install before the weights, exactly like Gatekeeper does on macOS.
func TestInstallSkipsMacOSProvisioningOnOtherPlatforms(t *testing.T) {
	for _, tc := range []installCase{linuxCPUCase(), windowsCUDACase()} {
		t.Run(tc.hostOS, func(t *testing.T) {
			fx := newInstallFixture(t, tc)
			fx.mustInstall(t)
			for _, line := range fx.cmds.lines() {
				if strings.HasPrefix(line, "xattr ") || strings.HasPrefix(line, "codesign ") {
					t.Errorf("%s ran a macOS provisioning command %q", tc.hostOS, line)
				}
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

// ── cross-platform smoke testing ──

// TestInstallSmokeTestsTheStagedRuntimeOnEveryPlatform pins the ordering
// invariant behind provisioning the runtime on all platforms: the --version
// smoke test runs against the STAGED tree on linux and windows too, BEFORE the
// weights — a runtime that cannot execute (missing CUDA 12 libraries, a
// Gatekeeper block, a broken binary) must fail the install in seconds rather
// than after a multi-gigabyte download.
func TestInstallSmokeTestsTheStagedRuntimeOnEveryPlatform(t *testing.T) {
	for _, tc := range []installCase{linuxCPUCase(), windowsCUDACase()} {
		t.Run(tc.hostOS, func(t *testing.T) {
			fx := newInstallFixture(t, tc)
			report := fx.mustInstall(t)

			smokeTest := fx.eventIndex("--version")
			if smokeTest < 0 {
				t.Fatalf("the %s smoke test never ran; events = %v", tc.hostOS, fx.eventLog())
			}
			probe := fx.eventIndex("device-probe")
			model := fx.eventIndex("download:" + string(ComponentModel))
			projector := fx.eventIndex("download:" + string(ComponentMMProj))
			if probe < 0 || model < 0 || projector < 0 {
				t.Fatalf("the install is incomplete; events = %v", fx.eventLog())
			}
			if smokeTest >= probe {
				t.Errorf("the device probe (event %d) ran BEFORE the smoke test (event %d):\n%v",
					probe, smokeTest, fx.eventLog())
			}
			if probe >= model || probe >= projector {
				t.Errorf("the device probe (event %d) ran AFTER the weights (model %d, mmproj %d):\n%v",
					probe, model, projector, fx.eventLog())
			}

			// The smoke test ran against the STAGING copy, i.e. before
			// promotion: a runtime that does not run must never reach its
			// final location.
			staging, err := fx.layout.RuntimeStagingDir(tc.backend)
			if err != nil {
				t.Fatalf("RuntimeStagingDir: %v", err)
			}
			if !strings.Contains(fx.eventLog()[smokeTest], staging) {
				t.Errorf("the smoke test %q did not run inside the staging tree %q",
					fx.eventLog()[smokeTest], staging)
			}
			if report.Resolution.Backend != tc.backend {
				t.Errorf("report backend = %q, want %q", report.Resolution.Backend, tc.backend)
			}
		})
	}
}

// TestSmokeTestRunsUnderTheLaunchEnvironment pins the launch environment of
// the fatal smoke test: it must exercise the same loader path as the real
// launch (spawn's launchEnv + Dir), because a Linux CUDA/ROCm build that
// resolves its sibling libraries through the launch-provided search path
// fails a bare-exec --version with a library-not-found error that would
// abort a perfectly installable runtime. The captured options carry the
// runtime's binary directory prepended to the platform's library-path
// variable and set the working directory to that same binary directory.
func TestSmokeTestRunsUnderTheLaunchEnvironment(t *testing.T) {
	for _, tc := range []installCase{linuxCPUCase(), windowsCUDACase(), darwinMetalCase()} {
		t.Run(tc.hostOS, func(t *testing.T) {
			fx := newInstallFixture(t, tc)
			report := fx.mustInstall(t)

			opts := fx.smokeRunOptions()
			if len(opts) != 1 {
				t.Fatalf("the smoke test carried %d RunOptions, want exactly one; events = %v",
					len(opts), fx.eventLog())
			}
			// The staging tree was renamed away by the promotion that
			// followed the smoke test, so the expected binary path is
			// reconstructed from the promoted tree: same relative location,
			// staging name.
			runtimeDir, err := fx.layout.RuntimeDir(report.Resolution.Backend)
			if err != nil {
				t.Fatalf("RuntimeDir: %v", err)
			}
			staging, err := fx.layout.RuntimeStagingDir(report.Resolution.Backend)
			if err != nil {
				t.Fatalf("RuntimeStagingDir: %v", err)
			}
			promoted, err := ServerBinaryPath(runtimeDir, tc.hostOS)
			if err != nil {
				t.Fatalf("ServerBinaryPath: %v", err)
			}
			rel, err := filepath.Rel(runtimeDir, promoted)
			if err != nil {
				t.Fatalf("Rel: %v", err)
			}
			binaryDir := filepath.Join(staging, filepath.Dir(rel))

			// The working directory is the runtime's binary directory.
			if opts[0].Dir != binaryDir {
				t.Errorf("smoke test Dir = %q, want the binary directory %q", opts[0].Dir, binaryDir)
			}

			// The environment is exactly launchEnv's output: the parent's
			// environment with the binary directory prepended to the
			// platform's dynamic-library search path.
			want := launchEnv(binaryDir, tc.hostOS, os.Environ())
			if !reflect.DeepEqual(opts[0].Env, want) {
				t.Errorf("smoke test Env = %v, want launchEnv's %v", opts[0].Env, want)
			}
			key := libraryPathVar(tc.hostOS)
			found := false
			for _, entry := range opts[0].Env {
				name, value, ok := strings.Cut(entry, "=")
				if ok && strings.EqualFold(name, key) {
					found = true
					if !strings.HasPrefix(value, binaryDir+pathListSeparator(tc.hostOS)) &&
						value != binaryDir {
						t.Errorf("%s = %q, want it to start with the binary dir %q",
							key, value, binaryDir)
					}
				}
			}
			if !found {
				t.Errorf("the smoke test environment carries no %s entry: %v", key, opts[0].Env)
			}
		})
	}
}

// TestInstallLinuxSmokeFailureStopsBeforeTheWeights is the Linux twin of the
// macOS smoke-failure test: an unrunnable staged runtime must abort the
// install with an actionable message BEFORE a single weight byte is fetched,
// and leave nothing behind — no manifest, no provider, no promoted tree.
func TestInstallLinuxSmokeFailureStopsBeforeTheWeights(t *testing.T) {
	for _, tc := range []struct {
		name      string
		backend   Backend
		wantParts []string
	}{
		{
			name:      "cpu backend names the backend and points at the loader",
			backend:   BackendCPU,
			wantParts: []string{"cpu", "--version failed"},
		},
		{
			name:      "cuda backend names the backend and the CUDA 12 libraries",
			backend:   BackendCUDA128,
			wantParts: []string{"cuda-12.8", "CUDA 12", "libcudart"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newInstallFixture(t, installCase{
				platform: PlatformLinuxAMD64,
				arch:     "amd64",
				backend:  tc.backend,
				ramGiB:   64,
				hostOS:   "linux",
			})
			fx.cmds.fail = "--version"

			_, err := fx.Install(context.Background(), InstallOptions{})
			if !errors.Is(err, ErrSmokeTestFailed) {
				t.Fatalf("Install error = %v, want ErrSmokeTestFailed", err)
			}
			message := err.Error()
			for _, want := range tc.wantParts {
				if !strings.Contains(message, want) {
					t.Errorf("smoke-test error does not mention %q:\n%s", want, message)
				}
			}

			// A runtime that does not run is never promoted and never
			// registered.
			requireNoManifest(t, fx.layout)
			if len(fx.sink.installCalls()) != 0 {
				t.Error("a failed smoke test registered a provider")
			}
			runtimeDir, err := fx.layout.RuntimeDir(tc.backend)
			if err != nil {
				t.Fatalf("RuntimeDir: %v", err)
			}
			requireAbsent(t, runtimeDir, "the runtime tree")
			staging, err := fx.layout.RuntimeStagingDir(tc.backend)
			if err != nil {
				t.Fatalf("RuntimeStagingDir: %v", err)
			}
			requireDir(t, staging, "the staged runtime, kept for diagnosis")

			// Fail-fast: the weights were never downloaded.
			if n := fx.requestCount(ComponentModel); n != 0 {
				t.Errorf("the model was requested %d times although the runtime does not run", n)
			}
			if n := fx.requestCount(ComponentMMProj); n != 0 {
				t.Errorf("the mmproj was requested %d times although the runtime does not run", n)
			}
		})
	}
}

// TestInstallWindowsSmokeFailureNamesTheRunDiagnostics pins the Windows half
// of the smoke-test hint split: the Windows CUDA builds ship their CUDA
// runtime as a bundled companion archive and never consult the system CUDA
// userland (ADR-073), so a Windows CUDA failure must NOT tell the user to
// install CUDA 12 libraries — the honest next step is the run-the-binary
// loader diagnostics (missing MSVC runtime, incomplete extraction, a
// Defender block).
func TestInstallWindowsSmokeFailureNamesTheRunDiagnostics(t *testing.T) {
	fx := newInstallFixture(t, windowsCUDACase())
	fx.cmds.fail = "--version"

	_, err := fx.Install(context.Background(), InstallOptions{})
	if !errors.Is(err, ErrSmokeTestFailed) {
		t.Fatalf("Install error = %v, want ErrSmokeTestFailed", err)
	}
	message := err.Error()
	for _, banned := range []string{"libcudart", "libcublas", "CUDA 12 runtime libraries"} {
		if strings.Contains(message, banned) {
			t.Errorf("the Windows CUDA smoke-test hint points at the system CUDA "+
				"userland the platform never consults (%q):\n%s", banned, message)
		}
	}
	for _, want := range []string{"cuda-12.4", "--version failed", "run the binary directly"} {
		if !strings.Contains(message, want) {
			t.Errorf("smoke-test error does not mention %q:\n%s", want, message)
		}
	}
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
	fx := newInstallFixture(t, tc)
	report := fx.mustInstall(t)
	stopErr := errors.New("the server did not exit")
	fx.installer.Stop = func(context.Context) error { return stopErr }

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

// ── scoped removal ──

// requireCachedWeights asserts the scoped-removal cache contract: the model
// GGUF and the projector survive and the install record is gone.
func requireScopedCache(t *testing.T, fx *installFixture, report *InstallReport) {
	t.Helper()
	requireFile(t, report.ModelFile, "the model GGUF, which a runtime-only removal keeps as a cache")
	proj, err := fx.layout.Destination(MMProjAsset())
	if err != nil {
		t.Fatalf("Destination(mmproj): %v", err)
	}
	requireFile(t, proj, "the projector GGUF, which a runtime-only removal keeps as a cache")
	requireNoManifest(t, fx.layout)
	if n := fx.sink.removed(); n != 1 {
		t.Errorf("ApplyRemoved called %d times, want 1", n)
	}
}

func TestParseRemoveScope(t *testing.T) {
	cases := []struct {
		in   string
		want RemoveScope
	}{
		{"", RemoveAll},
		{"all", RemoveAll},
		{"runtime", RemoveRuntime},
		{"weights", RemoveWeights},
		{"projection", RemoveProjection},
		{" Runtime ", RemoveRuntime},
	}
	for _, tc := range cases {
		got, err := ParseRemoveScope(tc.in)
		if err != nil {
			t.Errorf("ParseRemoveScope(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseRemoveScope(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{"everything", "model", "RUNTIME-files"} {
		if _, err := ParseRemoveScope(in); err == nil {
			t.Errorf("ParseRemoveScope(%q) succeeded, want a refusal", in)
		}
	}
	// The zero value is invalid: a caller that forgot to choose gets an error,
	// never a silent "all".
	var zero RemoveScope
	if zero.valid() {
		t.Error("the zero RemoveScope must not be valid")
	}
	if err := fxParseScope(t, zero); err == nil {
		t.Error("RemoveWithScope accepted the zero scope, want a refusal")
	}
}

// fxParseScope runs RemoveWithScope with the zero scope against a fixture the
// test constructed; split out so the table test stays declarative.
func fxParseScope(t *testing.T, scope RemoveScope) error {
	t.Helper()
	fx := newInstallFixture(t, darwinMetalCase())
	fx.mustInstall(t)
	return fx.installer.RemoveWithScope(context.Background(), scope)
}

func TestRemoveRuntimeScopeKeepsWeightsAsCache(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	report := fx.mustInstall(t)

	// A leftover of an interrupted install that the scope must also reclaim.
	staging, err := fx.layout.RuntimeStagingDir(report.Manifest.Backend)
	if err != nil {
		t.Fatalf("RuntimeStagingDir: %v", err)
	}
	if err := os.MkdirAll(staging, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := fx.installer.RemoveWithScope(context.Background(), RemoveRuntime); err != nil {
		t.Fatalf("RemoveWithScope(runtime): %v", err)
	}
	requireAbsent(t, report.RuntimeDir, "the runtime tree")
	requireAbsent(t, staging, "the staging tree")
	downloads, err := fx.layout.DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir: %v", err)
	}
	requireAbsent(t, downloads, "the archive staging area")
	requireScopedCache(t, fx, report)

	// And a re-install re-downloads only the RUNTIME: the surviving weight and
	// projector bytes verify against their pins without a single new request,
	// while the deleted runtime archive is fetched again.
	modelRequestsBefore := fx.requestCount(ComponentModel) + fx.requestCount(ComponentMMProj)
	fx.mustInstall(t)
	modelRequestsAfter := fx.requestCount(ComponentModel) + fx.requestCount(ComponentMMProj)
	if got := modelRequestsAfter - modelRequestsBefore; got != 0 {
		t.Errorf("the reinstall made %d weight/projector requests; surviving artifacts must be cache hits",
			got)
	}
	if fx.requestCount(ComponentRuntime) == 0 {
		t.Error("the reinstall did not re-fetch the deleted runtime archive")
	}
}

func TestRemoveWeightsScopeKeepsRuntime(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	report := fx.mustInstall(t)

	if err := fx.installer.RemoveWithScope(context.Background(), RemoveWeights); err != nil {
		t.Fatalf("RemoveWithScope(weights): %v", err)
	}
	modelFile, err := fx.layout.ModelFile(PackingPQ2_0)
	if err != nil {
		t.Fatalf("ModelFile: %v", err)
	}
	otherPacking, err := fx.layout.ModelFile(PackingPTQ1_0)
	if err != nil {
		t.Fatalf("ModelFile(PTQ1_0): %v", err)
	}
	requireAbsent(t, modelFile, "the installed packing's GGUF")
	requireAbsent(t, otherPacking, "the other packing's GGUF")
	requireDir(t, report.RuntimeDir, "the runtime tree, which a weights-only removal keeps")
	proj, err := fx.layout.Destination(MMProjAsset())
	if err != nil {
		t.Fatalf("Destination(mmproj): %v", err)
	}
	requireFile(t, proj, "the projector GGUF, which a weights-only removal keeps")
	requireNoManifest(t, fx.layout)
}

func TestRemoveProjectionScopeKeepsWeightsAndRuntime(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())
	report := fx.mustInstall(t)

	if err := fx.installer.RemoveWithScope(context.Background(), RemoveProjection); err != nil {
		t.Fatalf("RemoveWithScope(projection): %v", err)
	}
	proj, err := fx.layout.Destination(MMProjAsset())
	if err != nil {
		t.Fatalf("Destination(mmproj): %v", err)
	}
	requireAbsent(t, proj, "the projector GGUF")
	requireFile(t, report.ModelFile, "the model GGUF, which a projection-only removal keeps")
	requireDir(t, report.RuntimeDir, "the runtime tree, which a projection-only removal keeps")
	requireNoManifest(t, fx.layout)
}

func TestRemoveScopeClearsConfigUnderEveryScope(t *testing.T) {
	for _, scope := range []RemoveScope{RemoveAll, RemoveRuntime, RemoveWeights, RemoveProjection} {
		fx := newInstallFixture(t, darwinMetalCase())
		report := fx.mustInstall(t)

		if err := fx.installer.RemoveWithScope(context.Background(), scope); err != nil {
			t.Fatalf("RemoveWithScope(%s): %v", scope, err)
		}
		if n := fx.sink.removed(); n != 1 {
			t.Errorf("scope %s: ApplyRemoved called %d times, want 1", scope, n)
		}
		requireNoManifest(t, fx.layout)

		// The embedding-model decoy survives every scope.
		embeddingModel := filepath.Join(fx.agentDir, "models", "ggml-model-q4_0.gguf")
		if err := os.WriteFile(embeddingModel, []byte("embedding model"), 0o600); err != nil {
			t.Fatalf("write embedding model decoy: %v", err)
		}
		requireFile(t, embeddingModel, "the flat embedding model under scope "+string(scope))
		_ = report
	}
}

func TestDetectLeftovers(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase())

	// Nothing installed, nothing on disk: no leftovers.
	lo, err := fx.installer.DetectLeftovers()
	if err != nil {
		t.Fatalf("DetectLeftovers: %v", err)
	}
	if lo.Any() {
		t.Errorf("an empty machine reported leftovers %+v", lo)
	}

	report := fx.mustInstall(t)
	lo, err = fx.installer.DetectLeftovers()
	if err != nil {
		t.Fatalf("DetectLeftovers: %v", err)
	}
	if !lo.Runtime || !lo.Weights || !lo.Projection {
		t.Errorf("an installed machine reported %+v, want every component present", lo)
	}

	// The scope leaves exactly its residue behind.
	if err := fx.installer.RemoveWithScope(context.Background(), RemoveRuntime); err != nil {
		t.Fatalf("RemoveWithScope(runtime): %v", err)
	}
	lo, err = fx.installer.DetectLeftovers()
	if err != nil {
		t.Fatalf("DetectLeftovers: %v", err)
	}
	if lo.Runtime {
		t.Error("a runtime-scoped removal left runtime residue behind")
	}
	if !lo.Weights || !lo.Projection {
		t.Errorf("a runtime-scoped removal lost the weights/projector residue: %+v", lo)
	}

	if err := fx.installer.RemoveWithScope(context.Background(), RemoveWeights); err != nil {
		t.Fatalf("RemoveWithScope(weights): %v", err)
	}
	if err := fx.installer.RemoveWithScope(context.Background(), RemoveProjection); err != nil {
		t.Fatalf("RemoveWithScope(projection): %v", err)
	}
	lo, err = fx.installer.DetectLeftovers()
	if err != nil {
		t.Fatalf("DetectLeftovers: %v", err)
	}
	if lo.Any() {
		t.Errorf("a full removal left residue behind: %+v", lo)
	}
	_ = report
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
	requireNoManifestTemp(t, fx.layout.ModelRoot)

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
	if err := writeManifest(path, previous, nil); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	next := Manifest{Packing: PackingPTQ1_0, Backend: BackendVulkan, Port: 2, RuntimeVersion: RuntimeTag}
	if err := writeManifest(path, next, nil); err != nil {
		t.Fatalf("second writeManifest: %v", err)
	}
	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Packing != PackingPTQ1_0 || got.Backend != BackendVulkan || got.Port != 2 {
		t.Errorf("manifest = %+v, want the rewritten record", got)
	}
	requireNoManifestTemp(t, dir)
}

// TestWriteManifestSweepsTheTemporariesACrashedWriteLeftBehind pins the cleanup
// half of the unique temporary. A crash between CreateTemp and the rename is the
// one path that cannot remove its own temporary, and a uniquely named one is then
// a PERMANENTLY leaked one — nothing else knows its name — where the fixed name it
// replaced used to self-overwrite on the next write. The sweep is age-gated, so a
// concurrent writer's live temporary survives it.
func TestWriteManifestSweepsTheTemporariesACrashedWriteLeftBehind(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, ManifestFileName)
	logs := &logCapture{}

	// Two leftovers of crashed writes, backdated past the staleness threshold.
	stale := []string{
		filepath.Join(dir, ManifestFileName+".111111111"+manifestTempSuffix),
		filepath.Join(dir, ManifestFileName+".222222222"+manifestTempSuffix),
	}
	backdated := time.Now().Add(-2 * manifestTempStaleAfter)
	for _, name := range stale {
		if err := os.WriteFile(name, []byte("{ a torn manifest"), 0o600); err != nil {
			t.Fatalf("planting %q: %v", name, err)
		}
		if err := os.Chtimes(name, backdated, backdated); err != nil {
			t.Fatalf("backdating %q: %v", name, err)
		}
	}

	// A FRESH temporary — the shape a concurrent writer's live one has — plus the
	// entries the sweep must never consider: the manifest itself, an unrelated
	// file, and a directory that happens to match the temporary name shape.
	live := filepath.Join(dir, ManifestFileName+".333333333"+manifestTempSuffix)
	if err := os.WriteFile(live, []byte("{ still being written"), 0o600); err != nil {
		t.Fatalf("planting %q: %v", live, err)
	}
	unrelated := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(unrelated, []byte("weights"), 0o600); err != nil {
		t.Fatalf("planting %q: %v", unrelated, err)
	}
	dirShaped := filepath.Join(dir, ManifestFileName+".444444444"+manifestTempSuffix)
	if err := os.MkdirAll(dirShaped, 0o750); err != nil {
		t.Fatalf("planting %q: %v", dirShaped, err)
	}

	record := Manifest{Packing: PackingPQ2_0, Backend: BackendCPU, Port: 3, RuntimeVersion: RuntimeTag}
	if err := writeManifest(path, record, logs.logger()); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	// The record still lands atomically and completely.
	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Port != record.Port || got.Packing != record.Packing || got.Backend != record.Backend {
		t.Errorf("manifest = %+v, want %+v", got, record)
	}

	for _, name := range stale {
		if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the stale temporary %q survived the sweep (stat: %v)", filepath.Base(name), err)
		}
	}
	for _, kept := range []string{live, unrelated, dirShaped, path} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%q was removed by the sweep: %v", filepath.Base(kept), err)
		}
	}

	// The removals are visible, at Debug, by name.
	captured := logs.String()
	if !strings.Contains(captured, "level=DEBUG") {
		t.Errorf("the sweep did not report its removals at Debug: %q", captured)
	}
	for _, name := range stale {
		if !strings.Contains(captured, filepath.Base(name)) {
			t.Errorf("no Debug line names the removed temporary %q: %q", filepath.Base(name), captured)
		}
	}
	// And this write's OWN temporary is gone: the fresh one left behind is the
	// planted one, not the record that was just promoted.
	if got, err := ReadManifest(live); err == nil {
		t.Errorf("the planted live temporary holds a promoted record: %+v", got)
	}
}

// requireNoManifestTemp fails when a manifest write left its temporary behind.
// The name is unique per write (os.CreateTemp with manifestTempPattern), so the
// check is a scan of the directory rather than a stat of one known path.
func requireNoManifestTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %q: %v", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, manifestTempPrefix) && strings.HasSuffix(name, manifestTempSuffix) {
			t.Errorf("a manifest temporary was left behind: %s", filepath.Join(dir, name))
		}
	}
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

// ── compatibility guards in the install record ──
//
// A degraded install must be visible: every guard the pinned model's
// KNOWN_ISSUES documents for this machine is recorded in the report AND
// persisted in the manifest, whether or not c0wrk could act on it.

// guardFor returns the recorded decision for an id, failing the test when the
// install recorded none — an unrecorded guard is exactly the silence these
// tests exist to prevent.
func guardFor(t *testing.T, decisions []GuardDecision, id GuardID) GuardDecision {
	t.Helper()
	for _, decision := range decisions {
		if decision.Guard == id {
			return decision
		}
	}
	t.Fatalf("the install recorded no %q decision (recorded: %v)", id, guardIDsForLog(decisions))
	return GuardDecision{}
}

// TestInstallRecordsAnAppliedCompatibilityGuard covers a guard c0wrk acted on:
// KNOWN_ISSUES #222 says the CUDA 13.3 builds crash on some systems and names
// the 12.8 build as the Linux fallback, so the install provisions 12.8 and the
// record says so — including on disk, where the Settings UI reads it back from.
func TestInstallRecordsAnAppliedCompatibilityGuard(t *testing.T) {
	fx := newInstallFixture(t, installCase{
		platform: PlatformLinuxAMD64,
		arch:     "amd64",
		backend:  BackendCUDA133,
		ramGiB:   64,
		hostOS:   "linux",
		// The guard substitutes into this backend, so the fixture table has to
		// be able to serve it.
		guardBackends: []Backend{BackendCUDA128},
		// A genuine CUDA 12.x userland is what lets the substitution fire.
		cu12: CUDA12Present,
	})
	report := fx.mustInstall(t)

	if report.Resolution.Backend != BackendCUDA128 {
		t.Errorf("resolution backend = %q, want the guarded %q",
			report.Resolution.Backend, BackendCUDA128)
	}
	if report.Manifest.Backend != BackendCUDA128 {
		t.Errorf("manifest backend = %q, want %q", report.Manifest.Backend, BackendCUDA128)
	}

	decision := guardFor(t, report.Guards, GuardCUDA133Crash)
	if !decision.Applied {
		t.Error("the #222 guard changed the backend but is recorded as unapplied")
	}
	if decision.Action != GuardActionPreferBackend || decision.Backend != BackendCUDA128 {
		t.Errorf("#222 action/target = %q/%q, want %q/%q",
			decision.Action, decision.Backend, GuardActionPreferBackend, BackendCUDA128)
	}
	if decision.Reason != GuardReasonCrashOnLoad || decision.Severity != GuardSeverityCritical {
		t.Errorf("#222 reason/severity = %q/%q, want %q/%q",
			decision.Reason, decision.Severity, GuardReasonCrashOnLoad, GuardSeverityCritical)
	}
	if decision.Issue != "PrismML-Eng/llama.cpp#222" {
		t.Errorf("#222 issue citation = %q", decision.Issue)
	}
	if strings.TrimSpace(decision.Guidance) == "" {
		t.Error("#222 carries no user-facing guidance")
	}

	// The same record survives the manifest round-trip, which is what makes the
	// degradation visible after the process that installed it has exited.
	manifestPath, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	onDisk, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	persisted := guardFor(t, onDisk.Guards, GuardCUDA133Crash)
	if !persisted.Applied || persisted.Reason != GuardReasonCrashOnLoad {
		t.Errorf("persisted #222 = %+v, want the applied crash_on_load decision", persisted)
	}
	if persisted.Issue != decision.Issue || persisted.Guidance != decision.Guidance {
		t.Error("the persisted #222 decision lost its citation or its guidance")
	}
	if onDisk.PackingReason != PackingReasonDefault {
		t.Errorf("manifest packing_reason = %q, want %q", onDisk.PackingReason, PackingReasonDefault)
	}
}

// TestInstallKeepsProbedBackendWithoutCUDA12Userland covers the other half of
// the #222 record: a Linux CUDA 13.3 machine with NO usable CUDA 12.x userland
// (the absent verdict — the real-machine shape behind issue #222's
// non-reproduction, a ".so.12"-named symlink onto a 13-series ELF included)
// keeps the probed 13.3 build. The substituted 12.8 build could not even load
// here, so the guard is recorded unapplied — in the report AND on disk — with
// guidance saying why, instead of trading a working build for one that cannot
// start.
func TestInstallKeepsProbedBackendWithoutCUDA12Userland(t *testing.T) {
	fx := newInstallFixture(t, installCase{
		platform: PlatformLinuxAMD64,
		arch:     "amd64",
		backend:  BackendCUDA133,
		ramGiB:   64,
		hostOS:   "linux",
		// The substitution target stays servable, so the record shows the
		// refusal is the probe's verdict and not a missing archive.
		guardBackends: []Backend{BackendCUDA128},
		cu12:          CUDA12Absent,
	})
	report := fx.mustInstall(t)

	if report.Resolution.Backend != BackendCUDA133 {
		t.Errorf("resolution backend = %q, want the probed %q (no CUDA 12 userland to substitute onto)",
			report.Resolution.Backend, BackendCUDA133)
	}
	if report.Manifest.Backend != BackendCUDA133 {
		t.Errorf("manifest backend = %q, want %q", report.Manifest.Backend, BackendCUDA133)
	}

	decision := guardFor(t, report.Guards, GuardCUDA133Crash)
	if decision.Applied {
		t.Error("the #222 substitution fired without a CUDA 12 userland; it must stay unapplied")
	}
	if decision.Backend != BackendCUDA128 {
		t.Errorf("#222 target = %q, want the documented %q fallback", decision.Backend, BackendCUDA128)
	}
	if decision.Reason != GuardReasonCrashOnLoad || decision.Severity != GuardSeverityCritical {
		t.Errorf("#222 reason/severity = %q/%q, want %q/%q",
			decision.Reason, decision.Severity, GuardReasonCrashOnLoad, GuardSeverityCritical)
	}
	if !strings.Contains(decision.Guidance, "no usable CUDA 12.x") {
		t.Errorf("#222 guidance does not say why the substitution was skipped: %q", decision.Guidance)
	}

	manifestPath, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	onDisk, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	persisted := guardFor(t, onDisk.Guards, GuardCUDA133Crash)
	if persisted.Applied {
		t.Error("the persisted #222 decision claims a substitution that was not made")
	}
	if persisted.Issue != decision.Issue || persisted.Guidance != decision.Guidance {
		t.Error("the persisted #222 decision lost its citation or its guidance")
	}
	if onDisk.PackingReason != PackingReasonDefault {
		t.Errorf("manifest packing_reason = %q, want %q", onDisk.PackingReason, PackingReasonDefault)
	}
}

// TestInstallRecordsALateDeviceGuardAsGuidance covers the guard that arrives
// too late to be applied: a first install only learns the GPU generation from
// the runtime it has just staged, and by then the archive for the planned
// backend is on disk. The recommendation is recorded as guidance — with the
// reason it was not applied — instead of being dropped on the floor.
func TestInstallRecordsALateDeviceGuardAsGuidance(t *testing.T) {
	fx := newInstallFixture(t, installCase{
		platform: PlatformLinuxAMD64,
		arch:     "amd64",
		backend:  BackendROCm,
		ramGiB:   64,
		hostOS:   "linux",
		devices: []DeviceMemory{
			{Name: "HIP0", Description: "AMD Radeon RX 6800 XT", TotalMiB: 16384, FreeMiB: 16384},
		},
		// The guard's Vulkan target must be servable, otherwise the plan reports
		// "no pinned runtime" instead of exercising the too-late branch this test
		// is about. The real registry does pin linux-amd64 Vulkan.
		guardBackends: []Backend{BackendVulkan},
	})
	report := fx.mustInstall(t)

	// Nothing was substituted: the staged runtime is the ROCm build.
	if report.Resolution.Backend != BackendROCm {
		t.Errorf("backend = %q, want the staged %q", report.Resolution.Backend, BackendROCm)
	}
	if report.Resolution.GPU != GPUFamilyAMDRDNA2 {
		t.Errorf("gpu family = %q, want %q", report.Resolution.GPU, GPUFamilyAMDRDNA2)
	}

	decision := guardFor(t, report.Guards, GuardROCmRDNA2Abort)
	if decision.Applied {
		t.Error("a substitution made after the runtime was staged is recorded as applied")
	}
	if decision.Reason != GuardReasonProcessAbort || decision.Severity != GuardSeverityCritical {
		t.Errorf("Bonsai-demo #197 reason/severity = %q/%q, want %q/%q",
			decision.Reason, decision.Severity, GuardReasonProcessAbort, GuardSeverityCritical)
	}
	if decision.Issue != "PrismML-Eng/Bonsai-demo#197" {
		t.Errorf("#197 issue citation = %q", decision.Issue)
	}
	if !strings.Contains(decision.Guidance, "reinstall") {
		t.Errorf("#197 guidance does not say how to apply it: %q", decision.Guidance)
	}

	manifestPath, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	onDisk, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if onDisk.GPUFamily != GPUFamilyAMDRDNA2 {
		t.Errorf("manifest gpu_family = %q, want %q", onDisk.GPUFamily, GPUFamilyAMDRDNA2)
	}
	if persisted := guardFor(t, onDisk.Guards, GuardROCmRDNA2Abort); persisted.Applied {
		t.Error("the persisted #197 decision claims a substitution that did not happen")
	}
}

// TestInstallRecordsAnAdvisoryGuard covers the disclosure-only case: #192 hangs
// Intel Arc GPUs on PTQ1_0/Vulkan after about 1,900 tokens, and the only
// documented workaround gives up GPU acceleration, so c0wrk records it and lets
// the user decide rather than choosing silently.
func TestInstallRecordsAnAdvisoryGuard(t *testing.T) {
	fx := newInstallFixture(t, installCase{
		platform: PlatformWindowsAMD64,
		arch:     "amd64",
		backend:  BackendVulkan,
		ramGiB:   32,
		hostOS:   "windows",
		devices: []DeviceMemory{
			{Name: "Vulkan0", Description: "Intel(R) Arc(TM) B390 Graphics", TotalMiB: 32768, FreeMiB: 32768},
		},
	})
	report := fx.mustInstall(t)

	if report.Resolution.Backend != BackendVulkan || report.Resolution.Packing != PackingPTQ1_0 {
		t.Errorf("plan = %q/%q, want the unguarded Vulkan + PTQ1_0",
			report.Resolution.Backend, report.Resolution.Packing)
	}
	if report.PackingReason != PackingReasonNoPQ2_0Kernels {
		t.Errorf("packing reason = %q, want %q", report.PackingReason, PackingReasonNoPQ2_0Kernels)
	}

	decision := guardFor(t, report.Guards, GuardVulkanIntelArcHang)
	if decision.Applied || decision.Action != GuardActionAdvisory {
		t.Errorf("#192 = applied %v action %q, want an unapplied advisory", decision.Applied, decision.Action)
	}
	if decision.Reason != GuardReasonHang || decision.Severity != GuardSeverityWarning {
		t.Errorf("#192 reason/severity = %q/%q, want %q/%q",
			decision.Reason, decision.Severity, GuardReasonHang, GuardSeverityWarning)
	}
	if report.Resolution.GPU != GPUFamilyIntelArc {
		t.Errorf("gpu family = %q, want %q", report.Resolution.GPU, GPUFamilyIntelArc)
	}
}

// TestRefineWithStagedDevicesAppliesTheGPUGenerationPacking covers the branch
// the late probe CAN still act on: the backend is unchanged, so the weights —
// not yet downloaded — follow the generation-aware packing rule. An Ada card
// decodes PTQ1_0 faster (the model card's throughput table), and a first install
// only learns that from the staged runtime.
//
// This one runs against the real pinned registry, so it asserts the weights the
// refinement actually selects rather than a fixture's.
func TestRefineWithStagedDevicesAppliesTheGPUGenerationPacking(t *testing.T) {
	agentDir := t.TempDir()
	layout, err := NewLayout(
		filepath.Join(agentDir, "runtimes"),
		filepath.Join(agentDir, "models", testModelDirName),
	)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	in := &Installer{
		Layout: layout,
		Logger: dlDiscardLogger(),
		ProbeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
			return MemoryTopology{Devices: []DeviceMemory{
				{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4090", TotalMiB: 24576, FreeMiB: 24576},
			}}, true
		},
	}

	profile := MachineProfile{Platform: PlatformLinuxAMD64, Backend: BackendCUDA124, RAMGiB: 64}
	res, err := ResolveProfile(profile)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if res.Packing != PackingPQ2_0 || res.GPU != GPUFamilyUnknown {
		t.Fatalf("precondition: an unprobed machine resolved %q for family %q", res.Packing, res.GPU)
	}
	_, weights := splitRuntimeAssets(res.Assets)

	hw := Hardware{Platform: PlatformLinuxAMD64, Arch: "amd64", RAMGiB: 64, Backend: BackendCUDA124}
	refined, refinedWeights, refinedTopology, err := in.refineWithStagedDevices(
		context.Background(), profile.Platform, hw, res, "/nonexistent/llama-server", weights)
	if err != nil {
		t.Fatalf("refineWithStagedDevices: %v", err)
	}

	// The measurement is handed back with the resolution it refined, so the
	// manifest can record a topology and a plan that describe one another.
	if refinedTopology == nil {
		t.Fatal("a successful refinement returned no topology")
	}
	if len(refinedTopology.Devices) != 1 || refinedTopology.Devices[0].TotalMiB != 24576 {
		t.Errorf("refined topology = %+v, want the probed 24 GiB device", refinedTopology.Devices)
	}

	if refined.GPU != GPUFamilyNVIDIAAda {
		t.Errorf("gpu family = %q, want %q", refined.GPU, GPUFamilyNVIDIAAda)
	}
	if refined.Backend != BackendCUDA124 {
		t.Errorf("backend = %q, want it unchanged", refined.Backend)
	}
	if refined.Packing != PackingPTQ1_0 {
		t.Errorf("packing = %q, want %q (the Ada decode measurement)", refined.Packing, PackingPTQ1_0)
	}
	if refined.PackingReason != PackingReasonGPUGenerationDecode {
		t.Errorf("packing reason = %q, want %q", refined.PackingReason, PackingReasonGPUGenerationDecode)
	}

	// The weights to fetch must follow the refined packing, or the install would
	// download a file the manifest does not describe.
	var model Asset
	for _, asset := range refinedWeights {
		if asset.Component == ComponentModel {
			model = asset
		}
	}
	if !strings.Contains(model.ArchiveName, string(PackingPTQ1_0)) {
		t.Errorf("refined model archive = %q, want the %s weights", model.ArchiveName, PackingPTQ1_0)
	}
	modelFile, err := layout.ModelFile(refined.Packing)
	if err != nil {
		t.Fatalf("ModelFile: %v", err)
	}
	if filepath.Base(modelFile) != model.ArchiveName {
		t.Errorf("the refined plan pairs %q with the weights path %q", model.ArchiveName, modelFile)
	}
}

// TestRefineWithStagedDevicesStopsOnAMemoryRefusal pins the one failure the late
// probe is allowed to produce: a measured budget that REFUSES the machine must
// stop the install exactly like the same verdict from Installer.plan does, rather
// than degrade to a Debug line and continue into a multi-gigabyte weight download
// for a machine the topology has just turned away.
func TestRefineWithStagedDevicesStopsOnAMemoryRefusal(t *testing.T) {
	agentDir := t.TempDir()
	layout, err := NewLayout(
		filepath.Join(agentDir, "runtimes"),
		filepath.Join(agentDir, "models", testModelDirName),
	)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	in := &Installer{
		Layout: layout,
		Logger: dlDiscardLogger(),
		// A recognized family (so the refinement runs at all) beside budgets no
		// modelled shape fits: one gibibyte on each side. The RAM total stays a
		// plausible 64 GiB so the refusal comes from the MEASURED budgets rather
		// than from an unreadable host.
		ProbeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
			return MemoryTopology{
				Devices: []DeviceMemory{
					{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4090", TotalMiB: 1024, FreeMiB: 1024},
				},
				HostRAMGiB:        64,
				Unified:           false,
				DeviceBudgetBytes: 1 << 30,
				HostBudgetBytes:   1 << 30,
			}, true
		},
	}

	profile := MachineProfile{Platform: PlatformLinuxAMD64, Backend: BackendCUDA124, RAMGiB: 64}
	res, err := ResolveProfile(profile)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	_, weights := splitRuntimeAssets(res.Assets)

	hw := Hardware{Platform: PlatformLinuxAMD64, Arch: "amd64", RAMGiB: 64, Backend: BackendCUDA124}
	_, _, topology, err := in.refineWithStagedDevices(
		context.Background(), profile.Platform, hw, res, "/nonexistent/llama-server", weights)
	if !errors.Is(err, ErrInsufficientMemory) {
		t.Fatalf("refineWithStagedDevices = %v, want ErrInsufficientMemory", err)
	}
	if topology != nil {
		t.Errorf("a refused refinement returned a topology: %+v", *topology)
	}
}

// TestRefineWithStagedDevicesKeepsThePlanWithoutAnAnswer covers the fail-soft
// contract: a probe that does not answer leaves the resolution byte-for-byte
// alone, so a machine whose runtime will not enumerate its devices still gets
// the plan it was promised.
func TestRefineWithStagedDevicesKeepsThePlanWithoutAnAnswer(t *testing.T) {
	agentDir := t.TempDir()
	layout, err := NewLayout(
		filepath.Join(agentDir, "runtimes"),
		filepath.Join(agentDir, "models", testModelDirName),
	)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	in := &Installer{
		Layout: layout,
		Logger: dlDiscardLogger(),
		ProbeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
			return MemoryTopology{}, false
		},
	}

	profile := MachineProfile{Platform: PlatformLinuxAMD64, Backend: BackendCUDA124, RAMGiB: 64}
	res, err := ResolveProfile(profile)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	_, weights := splitRuntimeAssets(res.Assets)

	hw := Hardware{Platform: PlatformLinuxAMD64, Arch: "amd64", RAMGiB: 64, Backend: BackendCUDA124}
	got, gotWeights, gotTopology, err := in.refineWithStagedDevices(
		context.Background(), profile.Platform, hw, res, "/nonexistent/llama-server", weights)
	if err != nil {
		t.Fatalf("refineWithStagedDevices: %v", err)
	}

	if gotTopology != nil {
		t.Errorf("an unanswered probe returned a topology: %+v", *gotTopology)
	}
	if got.Packing != res.Packing || got.Backend != res.Backend || got.GPU != GPUFamilyUnknown {
		t.Errorf("an unanswered probe changed the plan: %q/%q/%q", got.Backend, got.Packing, got.GPU)
	}
	if len(got.Guards) != len(res.Guards) {
		t.Errorf("an unanswered probe changed the guard record: %d decisions, want %d",
			len(got.Guards), len(res.Guards))
	}
	if len(gotWeights) != len(weights) {
		t.Errorf("an unanswered probe changed the weight set: %d assets, want %d", len(gotWeights), len(weights))
	}
}

// ── the topology probe's place in the install sequence ──

// appleSiliconDevices is the accelerator inventory a provisioned Metal runtime
// reports on the reference machine: one unified-memory device whose pool IS the
// host pool.
func appleSiliconDevices() []DeviceMemory {
	return []DeviceMemory{
		{Name: "MTL0", Description: "Apple M3 Pro", TotalMiB: 24576, FreeMiB: 24576},
	}
}

// TestInstallProbesDevicesAfterTheRuntimeIsProvenAndBeforeTheWeights is the
// ordering invariant this seam exists for. The device probe must run
//
//	 AFTER the runtime is proven — an unrunnable runtime (Gatekeeper, a missing
//	   GPU library) must fail the install in seconds, and probing a binary that
//	   cannot execute answers nothing anyway; and
//	BEFORE the multi-gigabyte weights are fetched — because the authoritative
//	  topology is what the generation-aware packing rule and the memory gate need
//	  in order to choose WHICH weights to download. A probe that ran later could
//	  only describe a decision already made.
//
// Both halves are asserted against one ordered log of externally observable
// steps rather than against call counts, so a reordering anywhere in the
// sequence fails here.
func TestInstallProbesDevicesAfterTheRuntimeIsProvenAndBeforeTheWeights(t *testing.T) {
	tc := darwinMetalCase()
	tc.devices = appleSiliconDevices()
	fx := newInstallFixture(t, tc)

	report := fx.mustInstall(t)

	smokeTest := fx.eventIndex("--version")
	probe := fx.eventIndex("device-probe")
	model := fx.eventIndex("download:" + string(ComponentModel))
	projector := fx.eventIndex("download:" + string(ComponentMMProj))

	if smokeTest < 0 {
		t.Fatalf("the darwin smoke test never ran; events = %v", fx.eventLog())
	}
	if probe < 0 {
		t.Fatalf("the device probe never ran; events = %v", fx.eventLog())
	}
	if model < 0 || projector < 0 {
		t.Fatalf("the weights were never requested; events = %v", fx.eventLog())
	}
	if smokeTest >= probe {
		t.Errorf("the device probe (event %d) ran BEFORE the runtime smoke test (event %d):\n%v",
			probe, smokeTest, fx.eventLog())
	}
	if probe >= model || probe >= projector {
		t.Errorf("the device probe (event %d) ran AFTER the weights (model %d, mmproj %d):\n%v",
			probe, model, projector, fx.eventLog())
	}
	// The runtime archive itself is downloaded before the probe — that is the
	// whole point of the seam: the staged runtime is what answers it.
	if runtime := fx.eventIndex("download:" + string(ComponentRuntime)); runtime < 0 || runtime > probe {
		t.Errorf("the runtime download (event %d) must precede the probe (event %d):\n%v",
			runtime, probe, fx.eventLog())
	}

	// And the probe's answer reached the plan that chose those weights.
	if report.Resolution.GPU == GPUFamilyUnknown {
		t.Errorf("the probe answered but the resolution kept GPUFamilyUnknown: %+v", report.Resolution)
	}
}

// TestInstallRecordsTheTopologyAndThePlanItInformed covers the persistence half
// of the seam: the manifest carries the measured topology AND the plan derived
// from it, as one coherent pair, because a load's fail-soft path reads them as
// one fact — the snapshot is what a wedged driver query falls back to, and the
// plan is the shape it falls back to.
func TestInstallRecordsTheTopologyAndThePlanItInformed(t *testing.T) {
	tc := darwinMetalCase()
	tc.devices = appleSiliconDevices()
	fx := newInstallFixture(t, tc)

	report := fx.mustInstall(t)

	path, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	onDisk, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if onDisk.Topology == nil {
		t.Fatal("the manifest recorded no topology, though the device probe answered")
	}
	if len(onDisk.Topology.Devices) != 1 || onDisk.Topology.Devices[0].TotalMiB != 24576 {
		t.Errorf("recorded topology = %+v, want the probed 24 GiB device", onDisk.Topology.Devices)
	}
	// Recorded VERBATIM: the install copies the probe's answer rather than
	// re-deriving or re-stamping it, so whatever the probe measured — including
	// its own ProbedAt stamp, which the production ProbeDevices always sets and
	// this fixture leaves empty — is what a load's fail-soft path falls back to.
	probed, ok := fx.probeDevices(context.Background(), "", nil)
	if !ok {
		t.Fatal("the fixture probe did not answer, so this case proves nothing")
	}
	if !reflect.DeepEqual(*onDisk.Topology, probed) {
		t.Errorf("the recorded topology is not the probed one:\n got %+v\nwant %+v",
			*onDisk.Topology, probed)
	}

	if onDisk.Plan == nil {
		t.Fatal("the manifest recorded no plan")
	}
	// The plan must describe the bytes that are on disk, so its packing is the
	// manifest's — a plan priced for a different quantization would compute a
	// footprint the installed GGUF does not have.
	if onDisk.Plan.Packing != onDisk.Packing {
		t.Errorf("plan packing = %q, manifest packing = %q", onDisk.Plan.Packing, onDisk.Packing)
	}
	if onDisk.Plan.Packing != report.Resolution.Packing {
		t.Errorf("plan packing = %q, resolved packing = %q",
			onDisk.Plan.Packing, report.Resolution.Packing)
	}
	// The recorded plan is the resolution's own memory plan, not a re-derivation.
	if !reflect.DeepEqual(*onDisk.Plan, report.Resolution.Memory) {
		t.Errorf("the recorded plan differs from the resolved one:\n got %+v\nwant %+v",
			*onDisk.Plan, report.Resolution.Memory)
	}

	// ContextSize is the RECORDABLE context: the planner's own value for a
	// computed shape, and the fit floor for a fit-sized one — never 0, because 0
	// means "leave the existing override alone" to SyncEmbeddedLLMProvider and
	// would read as a lost tier in a manifest.
	want := recordableContext(report.Resolution.Memory, report.Resolution.ContextSize)
	if onDisk.ContextSize != want {
		t.Errorf("manifest context_size = %d, want %d (fit=%v)",
			onDisk.ContextSize, want, report.Resolution.Memory.Fit)
	}
	if onDisk.ContextSize <= 0 {
		t.Errorf("manifest context_size = %d, want a positive recordable context", onDisk.ContextSize)
	}

	// The pair survives a JSON round trip, which is the only form a load ever
	// sees: a plan or a topology that does not survive serialization is one a
	// load silently loses.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}
	var decoded Manifest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("the manifest is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(decoded, onDisk) {
		t.Errorf("the manifest does not round-trip:\n got %+v\nwant %+v", decoded, onDisk)
	}
}

// TestInstallRecordsNoTopologyWhenTheProbeDoesNotAnswer keeps the pair honest in
// the other direction: a machine whose runtime will not enumerate its devices
// gets a plan but NO topology beside it, rather than a zero MemoryTopology that
// a reader could mistake for "measured, and there was nothing".
func TestInstallRecordsNoTopologyWhenTheProbeDoesNotAnswer(t *testing.T) {
	fx := newInstallFixture(t, darwinMetalCase()) // no devices → the probe answers false

	report := fx.mustInstall(t)

	path, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	onDisk, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if onDisk.Topology != nil {
		t.Errorf("an unanswered probe recorded a topology: %+v", *onDisk.Topology)
	}
	if onDisk.Plan == nil {
		t.Error("an unanswered probe recorded no plan; the RAM-tiered path still produces one")
	}
	if onDisk.ContextSize <= 0 {
		t.Errorf("manifest context_size = %d, want a positive recordable context", onDisk.ContextSize)
	}
	if report.Resolution.GPU != GPUFamilyUnknown {
		t.Errorf("gpu family = %q with no probe answer, want %q",
			report.Resolution.GPU, GPUFamilyUnknown)
	}
}

// TestInstallWritesNoTopologyForAnUnverifiedComponent is the fail-closed half:
// the manifest — and with it the topology and the plan — is written only after
// every component verified, so a weights download that fails its digest leaves
// no record of a measurement made for bytes that are not on disk.
func TestInstallWritesNoTopologyForAnUnverifiedComponent(t *testing.T) {
	tc := darwinMetalCase()
	tc.devices = appleSiliconDevices()
	tc.corrupt = ComponentModel
	fx := newInstallFixture(t, tc)

	if _, err := fx.Install(context.Background(), InstallOptions{}); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Install error = %v, want ErrChecksumMismatch", err)
	}

	// The probe DID run — it is upstream of the weights, which is exactly why a
	// failed weights download cannot be repaired by re-probing.
	if fx.eventIndex("device-probe") < 0 {
		t.Errorf("the device probe never ran; events = %v", fx.eventLog())
	}
	// And nothing was recorded: no manifest means no topology and no plan.
	requireNoManifest(t, fx.layout)
	if len(fx.sink.installCalls()) != 0 {
		t.Error("a failed install registered a provider")
	}
}

// TestRecordableContextIsTheFitFloorUnderFit pins the one rule that makes a
// fit-sized plan persistable: there is no concrete context to record, so the
// floor fit is held to is recorded instead — never 0.
func TestRecordableContextIsTheFitFloorUnderFit(t *testing.T) {
	cases := []struct {
		name     string
		plan     MemoryPlan
		fallback int
		want     int
	}{
		{
			name:     "a computed shape records its own context",
			plan:     MemoryPlan{Fit: false, ContextSize: 32768, FitMinContext: DefaultFitMinContext},
			fallback: 16384,
			want:     32768,
		},
		{
			name:     "a fit-sized shape records the floor, not 0",
			plan:     MemoryPlan{Fit: true, ContextSize: 0, FitMinContext: DefaultFitMinContext},
			fallback: 16384,
			want:     DefaultFitMinContext,
		},
		{
			name:     "a fit-sized shape with no floor falls back",
			plan:     MemoryPlan{Fit: true, ContextSize: 0, FitMinContext: 0},
			fallback: 16384,
			want:     16384,
		},
		{
			name:     "a plan-less resolution keeps its tier",
			plan:     MemoryPlan{},
			fallback: 16384,
			want:     16384,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recordableContext(tc.plan, tc.fallback); got != tc.want {
				t.Errorf("recordableContext(%+v, %d) = %d, want %d", tc.plan, tc.fallback, got, tc.want)
			}
			if got := recordableContext(tc.plan, tc.fallback); got <= 0 && tc.want > 0 {
				t.Errorf("recordableContext returned %d, want a positive value", got)
			}
		})
	}
}

// ── the manifest write is atomic under concurrency ──

// TestWriteManifestIsSafeUnderConcurrentWriters pins the unique-temporary fix.
// Installer.Install holds no supervisor gate while Server.recordEffectiveContext
// writes under one, so two writers can overlap; a shared FIXED temporary name let
// one rename promote a file the other was still writing, producing a torn
// manifest.json — and a manifest that fails to parse reports the model as not
// installed until a reinstall.
func TestWriteManifestIsSafeUnderConcurrentWriters(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, ManifestFileName)

	const (
		writers = 8
		rounds  = 25
	)
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for range rounds {
				m := Manifest{
					Packing:        PackingPQ2_0,
					Backend:        BackendMetal,
					RuntimeVersion: RuntimeTag,
					InstalledAt:    "2026-09-26T00:00:00Z",
					ModelFile:      fmt.Sprintf("model-%d.gguf", w),
					Port:           1024 + w,
					ContextSize:    16384 + w,
				}
				if err := writeManifest(path, m, nil); err != nil {
					t.Errorf("writeManifest: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Whatever won, it is ONE complete record — never an interleaving of two.
	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest after %d concurrent writes: %v", writers*rounds, err)
	}
	if got.Port < 1024 || got.Port >= 1024+writers {
		t.Fatalf("the surviving record has port %d, which no writer wrote", got.Port)
	}
	writer := got.Port - 1024
	if want := fmt.Sprintf("model-%d.gguf", writer); got.ModelFile != want {
		t.Errorf("the surviving record mixes two writers: port %d beside model file %q, want %q",
			got.Port, got.ModelFile, want)
	}
	if want := 16384 + writer; got.ContextSize != want {
		t.Errorf("the surviving record mixes two writers: port %d beside context %d, want %d",
			got.Port, got.ContextSize, want)
	}
	requireNoManifestTemp(t, dir)
}

// ── the install command runner is bounded ──

// TestDefaultCommandRunnerIsBounded pins the two bounds the runner adds on top of
// its caller's context deadline. A deadline kills the child but does NOT end the
// read: Stdout is not an *os.File, so os/exec copies through a pipe, and a
// grandchild holding the write end keeps cmd.Run blocked after the child is gone —
// which would hold the backend's install slot for the lifetime of the process,
// since that slot is released on the runner's exit paths rather than in a defer.
// WaitDelay is what closes the pipe, and it is pinned where it is set because it
// is not observable through the runner's result.
func TestDefaultCommandRunnerIsBounded(t *testing.T) {
	source, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("reading install.go: %v", err)
	}
	body := string(source)
	start := strings.Index(body, "func defaultCommandRunner(")
	if start < 0 {
		t.Fatal("defaultCommandRunner is missing from install.go")
	}
	body = body[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "cmd.WaitDelay = probeWaitDelay") {
		t.Error("defaultCommandRunner sets no cmd.WaitDelay: a grandchild holding the output pipe would block cmd.Run past the context deadline")
	}

	// The child must be spawned console-less. HideConsole is a no-op off
	// Windows, but the source pin is what keeps the guarantee — the smoke
	// test runs llama-server (a console-subsystem binary on Windows) through
	// this runner, and without CREATE_NO_WINDOW every install click on
	// Windows allocates and flashes a visible console window.
	if !strings.Contains(body, "sysproc.HideConsole(cmd)") {
		t.Error("defaultCommandRunner does not call sysproc.HideConsole(cmd): a Windows install would flash a console window for the llama-server smoke test")
	}

	// The captured output is capped. It is diagnostic only, so an uncapped buffer
	// is an unbounded allocation driven by an external process.
	t.Setenv(helperFloodEnv, "1")
	out, err := defaultCommandRunner(context.Background(), os.Args[0], nil,
		"-test.run=TestHelperFloodsOutput", "-test.v=false")
	if err != nil {
		t.Fatalf("defaultCommandRunner: %v", err)
	}
	if len(out) > maxCommandOutputBytes+512 {
		t.Errorf("captured %d bytes, want at most the %d-byte cap plus a marker",
			len(out), maxCommandOutputBytes)
	}
	if !strings.Contains(out, "dropped") {
		tail := out
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		t.Errorf("the truncated output carries no marker; its tail is %q", tail)
	}
	if !strings.Contains(out, "x") {
		t.Error("the capped buffer kept none of the command's own output")
	}
}

// TestDefaultCommandRunnerReportsAFailingCommand keeps the runner honest about
// exit status: the cap and the delay bound the call, they do not swallow failures.
func TestDefaultCommandRunnerReportsAFailingCommand(t *testing.T) {
	t.Setenv(helperFloodEnv, "1")
	_, err := defaultCommandRunner(context.Background(), os.Args[0], nil,
		"-test.run=TestHelperFailsCommand", "-test.v=false")
	if err == nil {
		t.Error("a failing command reported no error")
	}
}

// helperFloodEnv guards the two helper modes below, which are re-executed as
// child processes rather than run as tests.
const helperFloodEnv = "EMBEDDEDLLM_HELPER_FLOOD"

// TestHelperFloodsOutput is not a test. It is the child process
// TestDefaultCommandRunnerIsBounded re-executes: it writes four times the output
// cap, which is the shape of a provisioning helper that floods its diagnostics.
func TestHelperFloodsOutput(t *testing.T) {
	if os.Getenv(helperFloodEnv) != "1" {
		t.Skip("helper mode only; re-executed by TestDefaultCommandRunnerIsBounded")
	}
	chunk := strings.Repeat("x", 64*1024)
	for range 64 {
		fmt.Print(chunk)
	}
}

// TestHelperFailsCommand is not a test either: it is the child that fails.
func TestHelperFailsCommand(t *testing.T) {
	if os.Getenv(helperFloodEnv) != "1" {
		t.Skip("helper mode only; re-executed by TestDefaultCommandRunnerReportsAFailingCommand")
	}
	t.Fatal("this helper always fails")
}

// helperOptionsEnv marks the child TestDefaultCommandRunnerAppliesRunOptions
// re-executes; the helper asserts the launch configuration the runner was
// asked to apply and reports what it observed.
const helperOptionsEnv = "EMBEDDEDLLM_HELPER_RUN_OPTIONS"

// helperMarkerFile is the file the helper reads by RELATIVE path: reading it
// succeeds only when the child's working directory is the Dir the runner was
// asked to apply.
const helperMarkerFile = "run-options-marker.txt"

// TestDefaultCommandRunnerAppliesRunOptions proves the trailing *RunOptions
// actually reaches the child on the production runner: a non-nil Env replaces
// the child's whole environment and Dir moves its working directory. This is
// the execution half of the smoke test's launch-environment guarantee — the
// fixture-based TestSmokeTestRunsUnderTheLaunchEnvironment only proves what
// smokeTest ASKS for, this proves the runner DELIVERS it.
func TestDefaultCommandRunnerAppliesRunOptions(t *testing.T) {
	t.Setenv(helperOptionsEnv, "1")
	t.Setenv("EMBEDDEDLLM_HELPER_SHOULD_NOT_INHERIT", "1")

	dir := t.TempDir()
	marker := "marker-content"
	if err := os.WriteFile(filepath.Join(dir, helperMarkerFile), []byte(marker), 0o600); err != nil {
		t.Fatalf("writing the marker: %v", err)
	}
	env := []string{helperOptionsEnv + "=1", "EMBEDDEDLLM_HELPER_MARK=" + marker}
	out, err := defaultCommandRunner(context.Background(), os.Args[0],
		&RunOptions{Env: env, Dir: dir},
		"-test.run=TestHelperAssertsRunOptions", "-test.v=false")
	if err != nil {
		t.Fatalf("defaultCommandRunner: %v (output: %s)", err, out)
	}
	// A re-executed test binary prints its own "PASS" line after whatever the
	// helper wrote, so the assertion is prefix-shaped rather than exact.
	if !strings.HasPrefix(strings.TrimSpace(out), "ok "+marker) {
		t.Errorf("helper reported %q, want the prefix %q (env/dir not applied?)",
			strings.TrimSpace(out), "ok "+marker)
	}
}

// TestHelperAssertsRunOptions is the child TestDefaultCommandRunnerAppliesRunOptions
// re-executes: it verifies its own environment and working directory and
// prints "ok <marker>" only when both match the RunOptions the runner was
// given. The marker value arrives only through Env (so a non-applied Env is a
// wrong answer, not a missing one) and the marker file is read by a relative
// path (so only the applied Dir can find it).
func TestHelperAssertsRunOptions(t *testing.T) {
	if os.Getenv(helperOptionsEnv) != "1" {
		t.Skip("helper mode only; re-executed by TestDefaultCommandRunnerAppliesRunOptions")
	}
	marker := os.Getenv("EMBEDDEDLLM_HELPER_MARK")
	if marker == "" {
		t.Fatal("Env not applied: EMBEDDEDLLM_HELPER_MARK is absent")
	}
	if os.Getenv("EMBEDDEDLLM_HELPER_SHOULD_NOT_INHERIT") != "" {
		t.Fatal("Env replaced: the parent environment leaked into the child")
	}
	content, err := os.ReadFile(helperMarkerFile)
	if err != nil {
		t.Fatalf("reading %s relative to the working directory: %v", helperMarkerFile, err)
	}
	if string(content) != marker {
		t.Errorf("marker file carries %q, want %q", string(content), marker)
	}
	fmt.Print("ok " + marker)
}
