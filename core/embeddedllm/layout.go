package embeddedllm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/safeio"
)

// This file is the ONLY place that knows where embedded-LLM bytes live on
// disk: every path is derived from a Layout built from roots the centralized
// path API resolved (backend/config/paths.go, over core/pathsegments.go
// constants) — this package cannot import either, so it never re-derives
// "<agentDir>/runtimes" itself. Containment is delegated to
// pathutil.IsWithinPath everywhere (AGENTS.md path rules).
// See specs/domains/embedded-llm.md (Key Files: layout.go).

// Layout-internal names: subsystem-private segments (the shape of one
// installed runtime, the manifest file name); the top-level directory names
// belong to the centralized path API, not here.
const (
	// ManifestFileName is the durable install record inside the model root;
	// startup trusts this file, not the network and not a probe.
	ManifestFileName = "manifest.json"

	// ServerBinaryName is the inference server executable inside a runtime
	// tree, without the platform suffix.
	ServerBinaryName = "llama-server"

	// runtimeDirPrefix starts every installed-runtime directory name:
	// "llama-<RuntimeTag>-<backend>".
	runtimeDirPrefix = "llama-"

	// downloadsDirName is the archive staging area under the runtime root:
	// archives and their resumable .part files live here until extracted;
	// weights go straight to their final path because the archive IS the file.
	downloadsDirName = "downloads"

	// stagingSuffix marks a tree being (re)built — swapped into place only
	// after every byte is verified (and on macOS signed and smoke-tested).
	stagingSuffix = ".staging"

	// retiredSuffix marks the previous tree during the swap, so a failed
	// rename can be rolled back instead of leaving no runtime at all.
	retiredSuffix = ".old"
)

// ErrLayoutInvalid reports a Layout whose roots are unusable (empty, or one
// root nested inside the other). It is a programming/config error, not a
// runtime condition.
var ErrLayoutInvalid = errors.New("invalid embedded-LLM storage layout")

// Layout is the on-disk footprint of the embedded-LLM subsystem: a runtime
// root (one "llama-<tag>-<backend>" tree per provisioned backend, plus the
// mtplx engine tree) and model roots holding the GGUF weights plus
// manifest.json. The zero value is invalid; build one with NewLayout.
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
	// MLXModelRoot is <agentDir>/models/bonsai-2-27b-mlx — the mtplx weight
	// pack's own tree, because the two engines' weights are different bytes
	// for the same model and a scoped removal must take one without the other.
	MLXModelRoot string
}

// NewLayout builds a Layout from the two roots the centralized path API
// resolved for the agent directory. It fails closed on empty roots, on
// relative roots (pathutil containment requires absolute paths) and on a
// nesting that would let one tree's cleanup delete the other.
func NewLayout(runtimesRoot, modelRoot, mlxModelRoot string) (Layout, error) {
	roots := []string{runtimesRoot, modelRoot, mlxModelRoot}
	for i, r := range roots {
		if r == "" {
			return Layout{}, fmt.Errorf("%w: root %d is required (runtimes=%q, models=%q, mlxModels=%q)",
				ErrLayoutInvalid, i, runtimesRoot, modelRoot, mlxModelRoot)
		}
		if !filepath.IsAbs(r) {
			return Layout{}, fmt.Errorf("%w: roots must be absolute (runtimes=%q, models=%q, mlxModels=%q)",
				ErrLayoutInvalid, runtimesRoot, modelRoot, mlxModelRoot)
		}
		roots[i] = filepath.Clean(r)
	}
	// No root may equal or live inside another: Remove deletes each tree
	// whole, and a nested layout would let one deletion take the other with it.
	for i := range roots {
		for j := range roots {
			if i == j {
				continue
			}
			inside, err := pathutil.IsWithinPath(roots[j], roots[i])
			if err != nil {
				return Layout{}, fmt.Errorf("%w: cannot compare roots: %w", ErrLayoutInvalid, err)
			}
			if inside {
				return Layout{}, fmt.Errorf("%w: %q is inside %q", ErrLayoutInvalid, roots[i], roots[j])
			}
		}
	}
	return Layout{RuntimesRoot: roots[0], ModelRoot: roots[1], MLXModelRoot: roots[2]}, nil
}

// RuntimeDirName is the directory name of one provisioned runtime:
// "llama-<RuntimeTag>-<backend>". The tag and the effective backend together
// identify the bytes, so two backends never share a tree and a pin bump never
// reuses a stale one.
func RuntimeDirName(backend Backend) string {
	return runtimeDirPrefix + RuntimeTag + "-" + string(backend)
}

// mlxRuntimePrefix starts every mtplx-engine runtime directory name:
// "mtplx-<MTPLXVersion>".
const mlxRuntimePrefix = "mtplx-"

// MLXRuntimeDirName is the directory name of one provisioned mtplx runtime;
// like the llama name it changes with the pin, so a bump never reuses a stale
// tree. The tree is RELOCATABLE plain files — no llama-style staging/retire
// swap, because nothing executes from it while a rebuild replaces it (the
// step-0 stop; ADR-092 records the deviation).
func MLXRuntimeDirName() string {
	return mlxRuntimePrefix + MTPLXVersion
}

// mlxPackDirName is the pack directory inside the MLX model root. The pack is
// kept one level below the root so manifest.json and future engine metadata
// never sit inside a directory the engine reads as a model.
const mlxPackDirName = "pack"

// RuntimeDir is the installed tree for one backend:
// <runtimesRoot>/llama-<tag>-<backend>.
func (l Layout) RuntimeDir(backend Backend) (string, error) {
	return l.safeJoin(l.RuntimesRoot, RuntimeDirName(backend))
}

// MLXRuntimeDir is the installed mtplx engine tree:
// <runtimesRoot>/mtplx-<MTPLXVersion>.
func (l Layout) MLXRuntimeDir() (string, error) {
	return l.safeJoin(l.RuntimesRoot, MLXRuntimeDirName())
}

// MLXRuntimeWorkDir is where the mtplx engine tree is built before it replaces
// MLXRuntimeDir. It plays the staging role (a failed build never touches the
// previous tree) without the retire/rename swap of a live binary.
func (l Layout) MLXRuntimeWorkDir() (string, error) {
	return l.safeJoin(l.RuntimesRoot, MLXRuntimeDirName()+stagingSuffix)
}

// MLXPackDir is the directory the pinned MLX pack is downloaded into:
// <mlxModelRoot>/pack. The engine is pointed at this directory with
// `--model`, so it holds EXACTLY the pinned files and nothing else.
func (l Layout) MLXPackDir() (string, error) {
	return l.safeJoin(l.MLXModelRoot, mlxPackDirName)
}

// MLXPackStagingDir is where the mtplx weight pack is fetched and verified
// before it replaces MLXPackDir. The pack is a SET of files that must come
// from one revision, so the whole set is staged and swapped in one atomic
// rename — a mid-fetch failure leaves the previous pack directory untouched.
func (l Layout) MLXPackStagingDir() (string, error) {
	return l.safeJoin(l.MLXModelRoot, mlxPackDirName+stagingSuffix)
}

// MLXPackRetiredDir is the previous pack directory, parked during the swap so
// the staged rename can never collide with it and a failed promotion can be
// deleted without touching the new tree.
func (l Layout) MLXPackRetiredDir() (string, error) {
	return l.safeJoin(l.MLXModelRoot, mlxPackDirName+retiredSuffix)
}

// MLXServerPythonPath is the interpreter a launch runs the engine with:
// <runtimeDir>/python/bin/python3.12 — the pinned managed CPython, whose
// site-packages carry the closed wheel set.
func MLXServerPythonPath(runtimeDir string) (string, error) {
	if runtimeDir == "" {
		return "", fmt.Errorf("%w: empty runtime directory", ErrLayoutInvalid)
	}
	return l2safeJoin(runtimeDir, filepath.Join("python", "bin", pythonBinName))
}

// pythonBinName is the interpreter binary inside the extracted managed-CPython
// tree.
const pythonBinName = "python3.12"

// l2safeJoin joins a path under a runtime tree and containment-checks the
// result against that tree (the same defence safeJoin gives the layout roots).
func l2safeJoin(root, rel string) (string, error) {
	joined := filepath.Join(root, rel)
	inside, err := pathutil.IsWithinPath(root, joined)
	if err != nil || !inside {
		return "", fmt.Errorf("%w: %q escapes %q", ErrLayoutInvalid, rel, root)
	}
	return joined, nil
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

// Destination is the on-disk path one asset is fetched to — its FINAL layout
// path (the mtplx pack stages elsewhere and swaps in whole; fetchOneTo takes
// an explicit destination). Weights go straight to their final path in the
// model root: the artifact IS the file, and the Downloader's
// verify-then-promote already makes the destination atomic. Runtime archives
// stage in DownloadsDir and are extracted from there.
func (l Layout) Destination(asset Asset) (string, error) {
	switch asset.Component {
	case ComponentModel, ComponentMMProj:
		return l.safeJoin(l.ModelRoot, asset.ArchiveName)
	case ComponentRuntime, ComponentCudart, ComponentPython, ComponentEngineWheels:
		downloads, err := l.DownloadsDir()
		if err != nil {
			return "", err
		}
		return l.safeJoin(downloads, asset.ArchiveName)
	case ComponentMLXPack:
		packDir, err := l.MLXPackDir()
		if err != nil {
			return "", err
		}
		return l.safeJoin(packDir, asset.ArchiveName)
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

// Owns reports whether p lies inside one of the embedded-LLM trees — the guard
// every destructive operation goes through, so no code path in this subsystem
// can delete anything outside the layout: never the flat embedding-model files
// sharing <agentDir>/models, never anything under <toolsDir>/bin.
func (l Layout) Owns(p string) bool {
	if l.RuntimesRoot == "" || l.ModelRoot == "" || l.MLXModelRoot == "" || p == "" {
		return false
	}
	for _, root := range []string{l.RuntimesRoot, l.ModelRoot, l.MLXModelRoot} {
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

// OwnsModel reports whether p lies inside the MODEL root — the containment
// check a launch applies to the recorded model file before it becomes the
// `-m` argv element: without it a tampered install record could point the
// launch at an arbitrary GGUF served under the pinned model's trusted
// identity (the binary half has the equivalent discipline in ServerBinaryPath).
// See specs/domains/embedded-llm.md (Load / serve / unload).
func (l Layout) OwnsModel(p string) bool {
	if l.ModelRoot == "" || p == "" {
		return false
	}
	inside, err := pathutil.IsWithinPath(l.ModelRoot, p)
	return err == nil && inside
}

// OwnsMLXPack reports whether p lies inside the MLX pack directory — the
// containment check a launch applies to the recorded pack directory before it
// becomes the `--model` argv element (the mtplx engine's OwnsModel twin).
func (l Layout) OwnsMLXPack(p string) bool {
	if l.MLXModelRoot == "" || p == "" {
		return false
	}
	packDir, err := l.MLXPackDir()
	if err != nil {
		return false
	}
	inside, withinErr := pathutil.IsWithinPath(packDir, p)
	return withinErr == nil && inside
}

// EnsureRoots creates both roots (and the archive staging area). It is called
// before the whole-set disk guard so the guard can measure a directory that
// exists.
func (l Layout) EnsureRoots() error {
	dirs := make([]string, 0, 4)
	for _, dir := range []string{l.RuntimesRoot, l.ModelRoot, l.MLXModelRoot} {
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
	// Refuse a link planted at a fixed layout root BEFORE any creation side
	// effect (review fix): MkdirAllReal resolves pre-existing links by
	// contract — an operator-symlinked agent dir must keep working — but the
	// roots carry multi-gigabyte install writes and the matching Remove
	// deletions, so a link planted AT a root, or at the shared models parent,
	// must stop here, before scaffolding lands in its target. A missing
	// component is fine (created below); a symlinked one is not.
	for _, dir := range []string{l.RuntimesRoot, filepath.Dir(l.ModelRoot), l.ModelRoot,
		filepath.Dir(l.MLXModelRoot), l.MLXModelRoot, downloads} {
		rel, rerr := filepath.Rel(filepath.Dir(dir), dir)
		if rerr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("embeddedllm: root %q is not below its parent", dir)
		}
		cur := filepath.Dir(dir)
		for _, comp := range strings.Split(rel, string(os.PathSeparator)) {
			cur = filepath.Join(cur, comp)
			fi, serr := os.Lstat(cur)
			if errors.Is(serr, fs.ErrNotExist) {
				break // nothing below a missing component exists yet
			}
			if serr != nil {
				return fmt.Errorf("embeddedllm: stat %q: %w", cur, serr)
			}
			if fi.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("embeddedllm: root %q is a symlink: %w", dir, &safeio.SymlinkError{Path: cur})
			}
		}
	}
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

// ServerBinaryPath locates llama-server inside an installed runtime tree by a
// deterministic lexical walk — the pinned archives nest it (build/bin) at a
// platform-dependent depth, so no hard-coded join. goos selects the ".exe"
// suffix; an empty goos means the host.
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
	// MkdirAllReal, not os.MkdirAll: creation only ever produces REAL
	// directories — a dangling link, a non-directory component, or a link
	// swapped into a component being created fails the call instead of being
	// followed. Pre-existing links on the existing prefix are resolved by
	// contract (an operator-symlinked agent dir keeps working); the fixed
	// layout roots themselves are strictly re-verified as real directories
	// by EnsureRoots, because a plant there would redirect the
	// multi-gigabyte install writes — and the matching Remove deletions —
	// outside the agent dir. Deeper mkdirs run inside those self-created,
	// self-owned trees.
	if err := safeio.MkdirAllReal(dir, 0o750); err != nil {
		return fmt.Errorf("embeddedllm: creating %q: %w", dir, err)
	}
	return nil
}
