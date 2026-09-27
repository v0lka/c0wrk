package embeddedllm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/v0lka/sp4rk/pathutil"
)

// This file is the ONLY place that knows where embedded-LLM bytes live on
// disk. Every path is derived from a Layout, and a Layout is built from roots
// handed in by the layer that owns c0wrk's directory layout
// (backend/config/paths.go RuntimesDir / EmbeddedModelDir, which read the
// cross-layer constants in core/pathsegments.go). core/embeddedllm cannot
// import either package — root core imports this package, and backend/config
// sits above core — so it never re-derives "<agentDir>/runtimes" itself.
//
// Containment is delegated to pathutil.IsWithinPath everywhere. There is no
// strings.HasPrefix / filepath.Rel containment in this subsystem (AGENTS.md
// path rules).

// Layout-internal names. These are subsystem-private segments (the shape of
// one installed runtime, the manifest file name); the top-level directory
// names belong to the centralized path API, not here.
const (
	// ManifestFileName is the durable install record inside the model root.
	// Startup trusts this file, not the network and not a probe.
	ManifestFileName = "manifest.json"

	// ServerBinaryName is the inference server executable inside a runtime
	// tree, without the platform suffix.
	ServerBinaryName = "llama-server"

	// runtimeDirPrefix starts every installed-runtime directory name:
	// "llama-<RuntimeTag>-<backend>".
	runtimeDirPrefix = "llama-"

	// downloadsDirName is the archive staging area under the runtime root.
	// Downloaded runtime/cudart archives (and their resumable .part files)
	// live here until they are extracted; weights are downloaded straight to
	// their final path because the archive IS the file.
	downloadsDirName = "downloads"

	// stagingSuffix marks a runtime tree that is being (re)built. It is swapped
	// into place only after every byte is verified and, on macOS, signed and
	// smoke-tested — the "secure the new bytes before destroying the old"
	// invariant.
	stagingSuffix = ".staging"

	// retiredSuffix marks the previous runtime tree during the swap, so a
	// failed rename can be rolled back instead of leaving no runtime at all.
	retiredSuffix = ".old"
)

// ErrLayoutInvalid reports a Layout whose roots are unusable (empty, or one
// root nested inside the other). It is a programming/config error, not a
// runtime condition.
var ErrLayoutInvalid = errors.New("invalid embedded-LLM storage layout")

// Layout is the on-disk footprint of the embedded-LLM subsystem: a runtime
// root holding one "llama-<tag>-<backend>" tree per provisioned backend, and
// a model root holding the GGUF weights plus manifest.json.
//
// The zero value is invalid; build one with NewLayout.
//
// Nothing here is ever placed under the tool-manager's <toolsDir>/bin: that
// directory is prepended to the agent's PATH by Manager.PrependToPATH(), which
// would make the inference runtime directly invokable from bash_exec (ADR-066
// D3, ASI05). AgentIsolationTest pins the invariant.
type Layout struct {
	// RuntimesRoot is <agentDir>/runtimes — config.RuntimesDir.
	RuntimesRoot string
	// ModelRoot is <agentDir>/models/bonsai-2-27b — config.EmbeddedModelDir.
	ModelRoot string
}

// NewLayout builds a Layout from the two roots the centralized path API
// resolved for the agent directory. It fails closed on empty roots, on
// relative roots (pathutil containment requires absolute paths) and on a
// nesting that would let one tree's cleanup delete the other.
func NewLayout(runtimesRoot, modelRoot string) (Layout, error) {
	if runtimesRoot == "" || modelRoot == "" {
		return Layout{}, fmt.Errorf("%w: both roots are required (runtimes=%q, models=%q)",
			ErrLayoutInvalid, runtimesRoot, modelRoot)
	}
	if !filepath.IsAbs(runtimesRoot) || !filepath.IsAbs(modelRoot) {
		return Layout{}, fmt.Errorf("%w: roots must be absolute (runtimes=%q, models=%q)",
			ErrLayoutInvalid, runtimesRoot, modelRoot)
	}
	cleanedRuntimes := filepath.Clean(runtimesRoot)
	cleanedModel := filepath.Clean(modelRoot)
	if cleanedRuntimes == cleanedModel {
		return Layout{}, fmt.Errorf("%w: runtime and model roots are the same directory %q",
			ErrLayoutInvalid, cleanedRuntimes)
	}
	// Neither root may live inside the other: Remove deletes each tree whole,
	// and a nested layout would let one deletion take the other with it.
	for _, pair := range [][2]string{{cleanedRuntimes, cleanedModel}, {cleanedModel, cleanedRuntimes}} {
		inside, err := pathutil.IsWithinPath(pair[0], pair[1])
		if err != nil {
			return Layout{}, fmt.Errorf("%w: cannot compare roots: %w", ErrLayoutInvalid, err)
		}
		if inside {
			return Layout{}, fmt.Errorf("%w: %q is inside %q", ErrLayoutInvalid, pair[1], pair[0])
		}
	}
	return Layout{RuntimesRoot: cleanedRuntimes, ModelRoot: cleanedModel}, nil
}

// RuntimeDirName is the directory name of one provisioned runtime:
// "llama-<RuntimeTag>-<backend>". The tag and the effective backend together
// identify the bytes, so two backends never share a tree and a pin bump never
// reuses a stale one.
func RuntimeDirName(backend Backend) string {
	return runtimeDirPrefix + RuntimeTag + "-" + string(backend)
}

// RuntimeDir is the installed tree for one backend:
// <runtimesRoot>/llama-<tag>-<backend>.
func (l Layout) RuntimeDir(backend Backend) (string, error) {
	return l.safeJoin(l.RuntimesRoot, RuntimeDirName(backend))
}

// RuntimeStagingDir is where a runtime tree is extracted and signed before it
// replaces RuntimeDir.
func (l Layout) RuntimeStagingDir(backend Backend) (string, error) {
	return l.safeJoin(l.RuntimesRoot, RuntimeDirName(backend)+stagingSuffix)
}

// RuntimeRetiredDir is the previous runtime tree, parked during the swap so a
// failed rename can be rolled back.
func (l Layout) RuntimeRetiredDir(backend Backend) (string, error) {
	return l.safeJoin(l.RuntimesRoot, RuntimeDirName(backend)+retiredSuffix)
}

// DownloadsDir is the archive staging area: <runtimesRoot>/downloads.
func (l Layout) DownloadsDir() (string, error) {
	return l.safeJoin(l.RuntimesRoot, downloadsDirName)
}

// ManifestPath is <modelRoot>/manifest.json.
func (l Layout) ManifestPath() (string, error) {
	return l.safeJoin(l.ModelRoot, ManifestFileName)
}

// Destination is the on-disk path one asset is fetched to.
//
// Weights (model, mmproj) are downloaded straight to their final path inside
// the model root: the artifact IS the file, so there is nothing to extract and
// the Downloader's verify-then-promote already makes the destination atomic.
// Runtime archives are staged in DownloadsDir and extracted from there.
func (l Layout) Destination(asset Asset) (string, error) {
	switch asset.Component {
	case ComponentModel, ComponentMMProj:
		return l.safeJoin(l.ModelRoot, asset.ArchiveName)
	case ComponentRuntime, ComponentCudart:
		downloads, err := l.DownloadsDir()
		if err != nil {
			return "", err
		}
		return l.safeJoin(downloads, asset.ArchiveName)
	default:
		return "", fmt.Errorf("%w: unknown component %q", ErrLayoutInvalid, asset.Component)
	}
}

// ModelFile is the installed GGUF for a packing, i.e. the absolute path that
// goes into manifest.json and embedded_llm.model_file.
func (l Layout) ModelFile(packing Packing) (string, error) {
	asset, ok := ModelAsset(packing)
	if !ok {
		return "", fmt.Errorf("%w: model packing %q", ErrArtifactNotPinned, packing)
	}
	return l.safeJoin(l.ModelRoot, asset.ArchiveName)
}

// Owns reports whether p lies inside one of the two embedded-LLM trees. It is
// the guard every destructive operation goes through: Remove deletes a path
// only when Owns accepts it, so no code path in this subsystem can delete
// anything outside the layout — in particular never the flat embedding-model
// files that share <agentDir>/models, and never anything under <toolsDir>/bin.
func (l Layout) Owns(p string) bool {
	if l.RuntimesRoot == "" || l.ModelRoot == "" || p == "" {
		return false
	}
	for _, root := range []string{l.RuntimesRoot, l.ModelRoot} {
		inside, err := pathutil.IsWithinPath(root, p)
		if err != nil {
			return false
		}
		if inside {
			return true
		}
	}
	return false
}

// OwnsModel reports whether p lies inside the MODEL root — the weights tree the
// install writes the GGUFs and manifest.json into.
//
// It is the containment check a launch applies to the recorded model file before
// that path becomes the `-m` argv element. The manifest is an ordinary file in
// the agent's own directory, so without this a tampered record could point the
// launch at an arbitrary GGUF and have it served under the pinned model's
// trusted identity — a model swap the provider name and the install-time digests
// both claim cannot happen. The binary half of the identity already has the
// equivalent discipline (ServerBinaryPath walks the runtime tree); this is the
// weights half.
func (l Layout) OwnsModel(p string) bool {
	if l.ModelRoot == "" || p == "" {
		return false
	}
	inside, err := pathutil.IsWithinPath(l.ModelRoot, p)
	return err == nil && inside
}

// EnsureRoots creates both roots (and the archive staging area). It is called
// before the whole-set disk guard so the guard can measure a directory that
// exists.
func (l Layout) EnsureRoots() error {
	dirs := make([]string, 0, 3)
	for _, dir := range []string{l.RuntimesRoot, l.ModelRoot} {
		if dir == "" {
			return fmt.Errorf("%w: empty root", ErrLayoutInvalid)
		}
		dirs = append(dirs, dir)
	}
	downloads, err := l.DownloadsDir()
	if err != nil {
		return err
	}
	dirs = append(dirs, downloads)
	for _, dir := range dirs {
		if err := mkdirAll(dir); err != nil {
			return err
		}
	}
	return nil
}

// safeJoin joins root and name and proves the result stayed inside root. Names
// come from the compile-time registry (validated by ValidateRegistry as plain
// file names) and from the backend vocabulary, so this is defence-in-depth
// rather than an expected failure — but a traversal must not be able to write
// or delete outside the layout.
func (l Layout) safeJoin(root, name string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("%w: empty root", ErrLayoutInvalid)
	}
	if name == "" {
		return "", fmt.Errorf("%w: empty path segment under %q", ErrLayoutInvalid, root)
	}
	joined := filepath.Join(root, name)
	inside, err := pathutil.IsWithinPath(root, joined)
	if err != nil {
		return "", fmt.Errorf("%w: cannot resolve %q under %q: %w", ErrLayoutInvalid, name, root, err)
	}
	if !inside {
		return "", fmt.Errorf("%w: %q escapes %q", ErrLayoutInvalid, name, root)
	}
	return joined, nil
}

// ServerBinaryPath locates the llama-server executable inside an installed
// runtime tree. The pinned archives nest it (build/bin/llama-server) rather
// than putting it at the root, and the depth differs per platform, so it is
// found by a deterministic lexical walk instead of a hard-coded join.
//
// goos selects the ".exe" suffix; an empty goos means the host.
func ServerBinaryPath(runtimeDir, goos string) (string, error) {
	if goos == "" {
		goos = runtime.GOOS
	}
	name := ServerBinaryName
	if goos == "windows" {
		name += ".exe"
	}
	if runtimeDir == "" {
		return "", errors.New("embeddedllm: empty runtime directory")
	}
	var found string
	walkErr := filepath.WalkDir(runtimeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if found != "" {
			return fs.SkipAll
		}
		if d.IsDir() || d.Name() != name {
			return nil
		}
		// Containment was established by the walk root; this only re-checks
		// that a symlinked entry did not point the result outside the tree.
		// An unresolvable entry is skipped rather than executed: fail closed.
		inside, withinErr := pathutil.IsWithinPath(runtimeDir, path)
		if withinErr != nil || !inside {
			return nil //nolint:nilerr // skipping an unverifiable entry is the safe outcome
		}
		found = path
		return fs.SkipAll
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return "", fmt.Errorf("embeddedllm: scanning runtime tree %q: %w", runtimeDir, walkErr)
	}
	if found == "" {
		return "", fmt.Errorf("embeddedllm: %s not found in the extracted runtime %q — "+
			"the pinned archive layout changed or the extraction was incomplete", name, runtimeDir)
	}
	return found, nil
}

func mkdirAll(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("embeddedllm: creating %q: %w", dir, err)
	}
	return nil
}
