// seedstaging.go implements the crash-safe, hash-verified seeding core shared
// by the research skill-pack (skillpack.go) and agent-pack (agentpack.go).
// It closes two review findings (review-post-v0.7.3.md):
//
//   - [58] crash window: the previous write order (clear target → write tree →
//     write marker) could be interrupted mid-tree, leaving a marker-less or
//     truncated directory that the next seeding misclassified ("Preserved"
//     when the marker was missing) and never repaired. Every write now lands
//     in a hidden sibling staging directory swapped into place by a single
//     rename ([58]b), so an interrupted run can never leave a partial tree at
//     the target; staging/backup leftovers from a hard kill are swept on the
//     next run. Writing the marker FIRST ([58]a) is deliberately avoided: that
//     ordering alone opens a "marker + truncated tree" window that the next
//     seeding would classify as Current.
//
//   - [44] marker spoofing: classification compares the CONTENT HASH of the
//     on-disk tree against the embedded pack ([44]a/[58]c) instead of trusting
//     the marker alone (and never mtime/size heuristics). Consequences: a
//     directory whose content equals the pack is Current regardless of marker
//     state; a pack-marked truncated subset of the pack is repaired (never
//     reported Current — the marker-first trap); a pack-marked same-version
//     tree that diverges from the pack content is a local edit (or a spoofed
//     marker) and is preserved untouched and reported as Modified.

package research

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
)

// seedAction enumerates the per-entry seeding outcomes.
type seedAction int

const (
	actionSeed     seedAction = iota // newly written
	actionUpdate                     // overwritten (version differed or truncated tree repaired)
	actionCurrent                    // content matches the pack; left untouched
	actionPreserve                   // user-owned (no marker, content differs); left untouched
	actionModified                   // pack-marked but content diverges (user edit or spoofed marker); left untouched
)

// seedWriteFile is the file-write primitive used by all seeding writes. It is
// a package-level seam so tests can inject write failures mid-tree (crash
// injection); production always uses os.WriteFile.
var seedWriteFile = os.WriteFile

// diskSeedState is the on-disk state of one seeding target, computed by
// content comparison against the embedded pack — never by mtime/size, which a
// same-length rewrite or a restored backup would fool.
type diskSeedState struct {
	exists    bool
	hasMarker bool
	version   string
	// hash is the content hash of the on-disk tree (dot entries excluded).
	hash string
	// truncated reports that the on-disk files are a strict subset of the
	// pack: every present file is byte-identical to its pack counterpart and
	// at least one pack file is missing — the signature of an interrupted
	// tree write, not of a user edit.
	truncated bool
}

// inspectSeedTarget compares the on-disk target against the embedded pack
// subtree and returns the disk state together with the pack's content hash.
// A missing target reports a zero-valued state (exists=false).
func inspectSeedTarget(fsys fs.FS, root, name, target string) (st diskSeedState, packHash string, err error) {
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
	st.exists = true
	st.hasMarker, st.version = readSeedVersion(target)

	pack, perr := packFileSet(fsys, root, name)
	if perr != nil {
		return st, "", perr
	}
	disk, derr := diskFileSet(target)
	if derr != nil {
		return st, "", derr
	}
	st.hash = hashFileSet(disk)
	packHash = hashFileSet(pack)

	diverged := false
	for rel, content := range disk {
		packContent, ok := pack[rel]
		if !ok || !bytes.Equal(packContent, content) {
			diverged = true
			break
		}
	}
	st.truncated = !diverged && len(disk) < len(pack)
	return st, packHash, nil
}

// classifySeed maps a disk state to the seeding action plus whether the
// version marker should be re-stamped. Content hashes decide; the marker only
// contributes an ownership signal ([58]c):
//
//   - missing directory                       → Seed
//   - content == pack                         → Current (marker re-stamped when
//     missing or stale — a legacy interrupted write left complete content
//     without a marker)
//   - marker present + truncated pack subset  → Update (repair an interrupted
//     write; the "marker + truncated tree" state is NEVER Current, which is
//     exactly the trap of the marker-first [58]a ordering)
//   - no marker + content differs             → Preserve (user-owned; never
//     clobbered)
//   - marker == current + content differs     → Modified (user edit on top of
//     the pack, or a spoofed marker [44]; preserved and reported honestly
//     instead of masquerading as the current pack)
//   - marker != current + content differs     → Update (documented pack
//     upgrade: full overwrite)
func classifySeed(st diskSeedState, packHash, currentVersion string) (seedAction, bool) {
	if !st.exists {
		return actionSeed, false
	}
	if st.hash == packHash {
		return actionCurrent, !st.hasMarker || st.version != currentVersion
	}
	if st.hasMarker && st.truncated {
		return actionUpdate, false
	}
	if !st.hasMarker {
		return actionPreserve, false
	}
	if st.version == currentVersion {
		return actionModified, false
	}
	return actionUpdate, false
}

// stageAndSwap materializes one pack entry crash-safely: it writes the full
// tree (plus the version marker) into a hidden sibling staging directory and
// swaps the staging directory into place with renames only. An interruption
// can therefore leave at most an orphaned hidden staging/backup directory —
// swept by sweepSeedLeftovers on the next run — never a partial tree at the
// target.
func stageAndSwap(name, target, version string, writeTree func(stagingDir string) error) error {
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
	if err := writeMarkerAtomic(staging, version); err != nil {
		return err
	}
	// MkdirTemp creates the staging dir 0700; the final directory must match
	// the permissions a direct write would produce.
	if err := os.Chmod(staging, 0o755); err != nil {
		return fmt.Errorf("chmod staging dir: %w", err)
	}

	if dirExists(target) {
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

// writeMarkerAtomic stamps the pack version marker into dir via a temp file +
// rename, so an interrupted write can never leave a truncated marker behind.
func writeMarkerAtomic(dir, version string) error {
	tmp := filepath.Join(dir, "."+seedVersionFile+".tmp")
	if err := seedWriteFile(tmp, []byte(version), 0o644); err != nil {
		return fmt.Errorf("write marker temp file: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, seedVersionFile)); err != nil {
		return fmt.Errorf("rename marker into place: %w", err)
	}
	return nil
}

// sweepSeedLeftovers removes hidden staging (".<name>.seed-tmp-*") and backup
// (".<name>.seed-old") directories left in parentDir by an interrupted
// previous run. Only dot-prefixed names matching the seeding naming scheme
// are touched; user content is never considered.
func sweepSeedLeftovers(parentDir string) {
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

// packFileSet loads the embedded pack subtree for one entry (skill or agent
// profile) as a rel-path → content map. Rel paths use forward slashes so they
// compare equal to diskFileSet keys. Dot entries are skipped (the embed
// directive already excludes them; this keeps both sides symmetric).
func packFileSet(fsys fs.FS, root, name string) (map[string][]byte, error) {
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

// diskFileSet loads the on-disk tree at dir as a rel-path → content map.
// Dot entries are skipped: the .seed-version marker, .DS_Store noise, and the
// marker temp file never influence classification.
func diskFileSet(dir string) (map[string][]byte, error) {
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

// hashFileSet renders the deterministic content hash of a rel-path → content
// file set: entries sorted by path, each framed by length prefixes so
// path/content boundaries are unambiguous. Two file sets hash equal iff they
// contain the same (path → bytes) pairs.
func hashFileSet(files map[string][]byte) string {
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
