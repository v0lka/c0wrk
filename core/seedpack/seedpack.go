// Package seedpack is the crash-safe, hash-verified seeding core shared by
// every embedded pack consumer (core/research, core/papers, core/specs):
// every write lands in a hidden sibling staging directory swapped in by a
// single rename, and classification compares CONTENT HASHES against the
// embedded pack (never mtime/size, never the marker alone). Consumers keep
// only their embed directives, version constants, and pack-specific logging
// (ADR-104 decision 5). Full rules: specs/domains/research.md (seeding
// invariants) and specs/domains/papers.md (global skill-pack seeding
// invariant).
package seedpack

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/v0lka/sp4rk/safeio"
)

// VersionFile is the sidecar marker filename whose presence identifies a
// directory as pack-seeded (and thus safe to overwrite on a version bump).
const VersionFile = ".seed-version"

// WriteFile is the write primitive for all seeding writes — a seam so tests
// can inject mid-tree write failures; production uses os.WriteFile. Tests
// swapping it MUST NOT run in parallel (the seam is package-global).
var WriteFile = os.WriteFile

// Action enumerates the per-entry seeding outcomes.
type Action int

const (
	ActionSeed     Action = iota // newly written
	ActionUpdate                 // overwritten (version differed or truncated tree repaired)
	ActionCurrent                // content matches the pack; left untouched
	ActionPreserve               // user-owned (no marker, content differs); left untouched
	ActionModified               // pack-marked but content diverges (user edit or spoofed marker); left untouched
)

// DiskState is the on-disk state of one seeding target, computed by content
// comparison — never mtime/size, which a same-length rewrite fools.
type DiskState struct {
	Exists    bool
	HasMarker bool
	Version   string
	// Hash is the content hash of the on-disk tree (dot entries excluded).
	Hash string
	// Truncated: the on-disk files are a strict subset of the pack (every
	// present file byte-identical, at least one missing) — an interrupted
	// tree write, not a user edit.
	Truncated bool
}

// InspectTarget returns the target's disk state plus the pack's content
// hash; a missing target reports the zero value (exists=false).
func InspectTarget(fsys fs.FS, root, name, target string) (st DiskState, packHash string, err error) {
	info, serr := os.Stat(target)
	if serr != nil {
		if os.IsNotExist(serr) {
			return st, "", nil
		}
		return st, "", fmt.Errorf("stat seed target %q: %w", target, serr)
	}
	if !info.IsDir() {
		return st, "", fmt.Errorf("seed target %q exists but is not a directory", target)
	}
	st.Exists = true
	st.HasMarker, st.Version = ReadVersion(target)

	pack, perr := PackFileSet(fsys, root, name)
	if perr != nil {
		return st, "", perr
	}
	disk, derr := DiskFileSet(target)
	if derr != nil {
		return st, "", derr
	}
	st.Hash = HashFileSet(disk)
	packHash = HashFileSet(pack)

	diverged := false
	for rel, content := range disk {
		packContent, ok := pack[rel]
		if !ok || !bytes.Equal(packContent, content) {
			diverged = true
			break
		}
	}
	st.Truncated = !diverged && len(disk) < len(pack)
	return st, packHash, nil
}

// Classify maps a disk state to the seeding action plus whether the version
// marker should be re-stamped: content hashes decide, the marker contributes
// only an ownership signal. The full mapping (Seed / Current / repair-Update
// / Preserve / Modified / upgrade-Update) is specified in
// specs/domains/research.md (seeding invariants).
func Classify(st DiskState, packHash, currentVersion string) (Action, bool) {
	if !st.Exists {
		return ActionSeed, false
	}
	if st.Hash == packHash {
		return ActionCurrent, !st.HasMarker || st.Version != currentVersion
	}
	if st.HasMarker && st.Truncated {
		return ActionUpdate, false
	}
	if !st.HasMarker {
		return ActionPreserve, false
	}
	if st.Version == currentVersion {
		return ActionModified, false
	}
	return ActionUpdate, false
}

// StageAndSwap materializes one pack entry crash-safely: the full tree plus
// marker is written into a hidden sibling staging directory and swapped in
// with renames only — an interruption leaves at most an orphaned staging/
// backup dir (swept next run), never a partial tree
// (specs/domains/research.md).
func StageAndSwap(name, target, version string, writeTree func(stagingDir string) error) error {
	parent := filepath.Dir(target)
	staging, err := os.MkdirTemp(parent, "."+name+".seed-tmp-")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	swapped := false
	defer func() {
		if !swapped {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := writeTree(staging); err != nil {
		return err
	}
	if err := WriteMarkerAtomic(staging, version); err != nil {
		return err
	}
	// MkdirTemp creates the staging dir 0700; the final directory must match
	// the permissions a direct write would produce.
	if err := os.Chmod(staging, 0o755); err != nil {
		return fmt.Errorf("chmod staging dir: %w", err)
	}

	if DirExists(target) {
		backup := filepath.Join(parent, "."+name+".seed-old")
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("clear stale backup dir: %w", err)
		}
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("move existing dir aside: %w", err)
		}
		if err := os.Rename(staging, target); err != nil {
			_ = os.Rename(backup, target) // best-effort rollback
			return fmt.Errorf("swap staged dir into place: %w", err)
		}
		swapped = true
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove backup dir: %w", err)
		}
		return nil
	}
	if err := os.Rename(staging, target); err != nil {
		return fmt.Errorf("move staged dir into place: %w", err)
	}
	swapped = true
	return nil
}

// WriteMarkerAtomic stamps the version marker via temp file + rename. The
// staging temp uses a randomized name (safeio.WriteFileAtomic), so the
// write-open cannot be redirected by a FIFO/symlink planted at the
// deterministic temp name a previous implementation used.
func WriteMarkerAtomic(dir, version string) error {
	if err := safeio.WriteFileAtomic(filepath.Join(dir, VersionFile), []byte(version), 0o644); err != nil {
		return fmt.Errorf("write marker file: %w", err)
	}
	return nil
}

// SweepLeftovers removes the hidden ".<name>.seed-tmp-*"/".seed-old" dirs an
// interrupted run left behind; only seeding-scheme names are touched.
func SweepLeftovers(parentDir string) {
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		base := e.Name()
		if !strings.HasPrefix(base, ".") {
			continue
		}
		if strings.Contains(base, ".seed-tmp-") || strings.HasSuffix(base, ".seed-old") {
			_ = os.RemoveAll(filepath.Join(parentDir, base))
		}
	}
}

// PackFileSet loads the embedded subtree for one entry as a rel-path →
// content map (forward-slash keys, matching DiskFileSet); dot entries are
// skipped on both sides.
func PackFileSet(fsys fs.FS, root, name string) (map[string][]byte, error) {
	srcRoot := path.Join(root, name)
	files := make(map[string][]byte)
	err := fs.WalkDir(fsys, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != srcRoot && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		data, rerr := fs.ReadFile(fsys, p)
		if rerr != nil {
			return rerr
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, srcRoot), "/")
		files[rel] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read embedded pack tree %q: %w", srcRoot, err)
	}
	return files, nil
}

// DiskFileSet loads the on-disk tree at dir (dot entries — marker, noise —
// skipped, so they never influence classification).
func DiskFileSet(dir string) (map[string][]byte, error) {
	dfs := os.DirFS(dir)
	files := make(map[string][]byte)
	err := fs.WalkDir(dfs, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		// Skip non-regular entries before reading: fs.ReadFile would block
		// forever opening a readerless FIFO at startup, and a symlink must not
		// be followed — the hash classifies genuine regular content only.
		if !d.Type().IsRegular() {
			return nil
		}
		data, rerr := fs.ReadFile(dfs, p)
		if rerr != nil {
			return rerr
		}
		files[p] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan seeded dir %q: %w", dir, err)
	}
	return files, nil
}

// HashFileSet renders the deterministic content hash of a file set (sorted
// paths, length-prefixed frames); two sets hash equal iff identical.
func HashFileSet(files map[string][]byte) string {
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	h := sha256.New()
	var lenBuf [8]byte
	for _, rel := range rels {
		content := files[rel]
		h.Write([]byte(rel))
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(rel)))
		h.Write(lenBuf[:])
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(content)))
		h.Write(lenBuf[:])
		h.Write(content)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ReadVersion returns the pack marker's presence and version; a missing
// directory or marker reports (false, "").
func ReadVersion(target string) (hasMarker bool, version string) {
	data, err := safeio.ReadFile(filepath.Join(target, VersionFile))
	if err != nil {
		return false, ""
	}
	return true, strings.TrimSpace(string(data))
}

// DirExists reports whether target exists and is a directory.
func DirExists(target string) bool {
	info, err := os.Stat(target)
	return err == nil && info.IsDir()
}

// Result reports the per-entry outcome of a pack seeding call; each slice
// holds entry names, sorted for deterministic output.
type Result struct {
	Seeded    []string // newly written (directory did not exist)
	Updated   []string // overwritten to the current pack version (upgrade or truncated-tree repair)
	Current   []string // content matches the pack; left untouched
	Preserved []string // user-owned (no marker, content differs); left untouched
	Modified  []string // pack-marked but content diverges (user edit or spoofed marker); left untouched
}

// Append records an entry name under the bucket for its action.
func (r *Result) Append(action Action, name string) {
	switch action {
	case ActionSeed:
		r.Seeded = append(r.Seeded, name)
	case ActionUpdate:
		r.Updated = append(r.Updated, name)
	case ActionCurrent:
		r.Current = append(r.Current, name)
	case ActionPreserve:
		r.Preserved = append(r.Preserved, name)
	case ActionModified:
		r.Modified = append(r.Modified, name)
	}
}

// Sort sorts every bucket of a result for deterministic output.
func (r *Result) Sort() {
	sort.Strings(r.Seeded)
	sort.Strings(r.Updated)
	sort.Strings(r.Current)
	sort.Strings(r.Preserved)
	sort.Strings(r.Modified)
}
