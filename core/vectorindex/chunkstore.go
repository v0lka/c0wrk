package vectorindex

// The content-addressed chunk-record store of ADR-104 §3 — the store's
// canonical durable tree. Stage 2 keyed corpora by commit OID but kept each
// corpus as a whole chromem collection whose documents are keyed
// DocumentID(path, i): vectors, per-file vector copies and lexical state all
// multiply with the number of DISTINCT SNAPSHOTS, and nothing below the
// corpus is shareable. This file owns the §3 on-disk form that removes the
// last multiplier:
//
//	store/
//	  generations/<n>/            E-scoped; exactly one CURRENT generation
//	    manifest.json             {format:2, embedding_fp E, dims, created}
//	    chunks/<aa>/<C>.chunk     L1: C → vector (dims×f32 LE), no text
//	    snapshots/<oid16>/
//	      occurrences.tab          L2 rows: path → blobHash, [(C, i, s, e, lang)]
//	      table.json               {chunker_fp K, ignore_fp I, counts, bytes}
//	      lexical/                 per-snapshot bleve index
//	    roots/<sanitized-root>/   L4: overlay.tab, overlay-lexical/, sidecar.json,
//	                              view.json
//	  CURRENT                     atomic-rename pointer to the live generation
//	refs/<key>.ref                ownership markers (stage-5 records) live at
//	                              the STORAGE-ROOT level, outside generations/
//
// Write ordering (ADR-104 §3, load-bearing for crash recovery): chunk
// records land BEFORE any table referencing them; tables land by
// temp-file + atomic rename; refs by atomic rewrite. A crash can leave
// unreferenced chunks (garbage for the §6 sweep) but never a table row
// whose chunk is missing. table.json lands LAST inside a snapshot — its
// presence is the snapshot's completion marker (the replay gate of the
// v1→v2 chunk-record migration).
//
// An unknown manifest format makes the whole tree read as ABSENT (forward
// compatibility refused): nothing is served, nothing is deleted. The
// generation is E-scoped: an embedding-era change advances to a NEW
// generation directory. Chunk vectors and the tables that reference them
// are era-bound, so NOTHING under snapshots/ is carried across the
// boundary — the next attach re-publishes each snapshot's tables through
// the ordinary build pass (embedding-cache-spared). Ownership markers
// (refs/) live outside the generations tree and survive every advance.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/safeio"
)

const (
	// storeDirName is the storage-root subdirectory holding the §3
	// chunk-record tree. It is ours alone: the legacy layout never created
	// it, and legacy binaries never look inside.
	storeDirName = "store"

	// storeGenerationsDirName holds the E-scoped generation directories.
	storeGenerationsDirName = "generations"

	// storeCurrentName is the CURRENT pointer: a one-line file naming the
	// live generation directory, replaced atomically (write-temp + rename).
	storeCurrentName = "CURRENT"

	// storeGenerationManifestName is the generation manifest's file name.
	storeGenerationManifestName = "manifest.json"

	// storeChunksDirName holds the content-addressed chunk records, sharded
	// by the first two hex chars of C (chunks/<aa>/<C>.chunk).
	storeChunksDirName = "chunks"

	// chunkFileExt terminates every chunk record file name.
	chunkFileExt = ".chunk"

	// storeSnapshotsDirName holds the per-snapshot (commit-keyed) table
	// directories, named by the bare oid16 (no snap_ prefix — the directory
	// name IS the snapshot identity inside the generation).
	storeSnapshotsDirName = "snapshots"

	// occurrencesTabName is the L2 occurrence table (sorted rows, TSV).
	occurrencesTabName = "occurrences.tab"

	// snapshotTableName is the snapshot's header/completion record.
	snapshotTableName = "table.json"

	// storeRefsDirName is the §3 tree's placeholder refs/ directory under
	// each generation. Ownership markers do NOT live here: they live at
	// the STORAGE-ROOT refs/ (rootrefs.go — the same records the legacy
	// layout reads), outside the generations tree, so they survive every
	// era advance without any carry. The per-generation directory is kept
	// only for layout stability of trees created by earlier builds.
	storeRefsDirName = "refs"

	// storeRootsDirName holds the per-root L4 state directories (the root
	// fingerprint, identity.go, is the directory name).
	storeRootsDirName = "roots"

	// overlayTabName is the root's durable overlay occurrence table.
	overlayTabName = "overlay.tab"

	// overlayLexicalDirName is the root's overlay bleve directory.
	overlayLexicalDirName = "overlay-lexical"

	// overlaySidecarName is the root's file-hash sidecar inside its §3
	// state directory.
	overlaySidecarName = "sidecar.json"

	// rootViewStateName records the root's pinned view (ADR-104 §3 view.json).
	rootViewStateName = "view.json"

	// storeFormatV2Generation is the §3 manifest format version. It equals
	// storeFormatV2 (the branch-manifest version) — one format era.
	storeFormatV2Generation = storeFormatV2

	// rootViewStateFormat versions the view.json envelope.
	rootViewStateFormat = 1
)

// storeGenerationManifest is the E-scoped generation record (ADR-104 §2/§3):
// every chunk vector under the generation was produced under exactly this
// embedding era. Dims doubles as the chunk record's implicit row width.
type storeGenerationManifest struct {
	Format      int    `json:"format"`
	EmbeddingFP string `json:"embedding_fp"`
	Dims        int    `json:"dims"`
	CreatedAt   string `json:"created"`
}

// compatible reports whether the generation may be read under the given era.
func (m *storeGenerationManifest) compatible(embeddingFP string, dims int) bool {
	return m != nil &&
		m.Format == storeFormatV2Generation &&
		m.EmbeddingFP == embeddingFP &&
		m.Dims == dims
}

// chunkOccurrence is one L2 occurrence inside a table row: the chunk's
// content identity C (L1), its TRUE chunk index (the DocumentID suffix i —
// a poison-dropped chunk leaves a gap in a file's 0..n-1 sequence, so the
// row's slice positions are NOT the chunk indices and the index must be
// carried explicitly), its line range, and its language. The chunk
// TEXT is deliberately absent — ADR-049's content-less contract is
// preserved at the store level (reconstruction stays best-effort from
// sources).
type chunkOccurrence struct {
	C        string
	Index    int
	Start    int
	End      int
	Language string
}

// occurrenceRow is one file's row in an occurrence table: the occurrence
// path (root-relative), the file's whole-content blob hash, and the file's
// chunk occurrences in chunker order. The blob hash is what visibility
// arbitration compares (the corpus manifest's Files map) and what the
// v1→v2 migration verifies before trusting re-derived rows.
type occurrenceRow struct {
	Path     string
	BlobHash string
	Chunks   []chunkOccurrence
}

// snapshotTable is the snapshot's header record (table.json). It lands LAST
// in the snapshot directory: presence certifies the snapshot's tables
// complete (chunks written, occurrences.tab renamed into place), which is
// the replay gate for both corpus builds and the chunk-record migration.
type snapshotTable struct {
	Format       int    `json:"format"`
	ChunkerFP    string `json:"chunker_fp"`
	IgnoreFP     string `json:"ignore_fp"`
	Files        int    `json:"files"`
	Chunks       int    `json:"chunks"`
	UniqueChunks int    `json:"unique_chunks"`
	ChunkBytes   int64  `json:"chunk_bytes"`
	Collection   string `json:"collection,omitempty"` // v1-fallback identity (migration provenance)
	CreatedAt    string `json:"created_at"`
}

// rootViewState is the per-root view record (view.json): the snapshot the
// root's marker pins and when it was last seen. It is bookkeeping beside
// the refs/<rootKey>.ref ownership marker — the marker (rootrefs.go)
// remains the GC's authority; this record gives the §3 tree a
// self-describing root-state file per ADR-104 §3's layout.
type rootViewState struct {
	Format   int    `json:"format"`
	PinnedID string `json:"pinned_ref"`
	LastSeen string `json:"last_seen"`
}

// storePath returns the §3 tree root under a physical storage root.
func storePath(storageRoot string) string {
	return filepath.Join(storageRoot, storeDirName)
}

// currentPointerPath returns the CURRENT pointer's path.
func currentPointerPath(storageRoot string) string {
	return filepath.Join(storePath(storageRoot), storeCurrentName)
}

// generationPath returns generation n's directory under the storage root.
func generationPath(storageRoot string, n int) string {
	return filepath.Join(storePath(storageRoot), storeGenerationsDirName, strconv.Itoa(n))
}

// readCurrentGeneration resolves the live generation: the CURRENT pointer's
// named directory plus its decoded manifest. ok is false when any step is
// missing or malformed (including an unknown manifest format — the whole
// tree then reads as absent, ADR-104 §3) or when the named generation is
// not a plain decimal directory name.
func readCurrentGeneration(storageRoot string) (dir string, manifest *storeGenerationManifest, ok bool) {
	data, err := safeio.ReadFile(currentPointerPath(storageRoot))
	if err != nil {
		return "", nil, false
	}
	name := strings.TrimSpace(string(data))
	n, err := strconv.Atoi(name)
	if err != nil || n < 0 || name != strconv.Itoa(n) {
		return "", nil, false
	}
	dir = generationPath(storageRoot, n)
	m := readGenerationManifest(dir)
	if m == nil || m.Format != storeFormatV2Generation {
		// Absent, unparsable, or an unknown format: the whole tree reads
		// as ABSENT (ADR-104 §3 — forward compatibility refused).
		return "", nil, false
	}
	return dir, m, true
}

// readGenerationManifest loads and decodes a generation manifest; nil when
// absent or unparsable (an unreadable manifest is a missing one).
func readGenerationManifest(dir string) *storeGenerationManifest {
	data, err := safeio.ReadFile(filepath.Join(dir, storeGenerationManifestName))
	if err != nil {
		return nil
	}
	var m storeGenerationManifest
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &m
}

// ensureStoreGeneration drives the storage root to exactly one CURRENT
// generation compatible with the given embedding era (ADR-104 §2: an E
// change is a generation boundary). Three outcomes:
//
//   - no tree at all → create generation 0 with the era's manifest, publish
//     CURRENT (the store is born §3-native);
//   - CURRENT generation compatible → return it (the common case);
//   - CURRENT generation foreign-era → create the NEXT generation EMPTY
//     and atomically advance CURRENT. NOTHING under snapshots/ is carried
//     across the boundary: chunk vectors AND the tables referencing them
//     are era-bound, and a carried table would be destroyed at its first
//     open and re-published anyway (advanceStoreGeneration); each
//     snapshot's tables return through the next attach's ordinary build
//     pass, embedding-cache-spared. refs/ lives outside the generations
//     tree and survives untouched. The superseded generation stops being
//     referenced; the §6 sweep collects its chunks and tables once
//     unreachable.
//
// Errors surface to the caller's fail-soft handling: a store that cannot
// create its generation keeps operating in the legacy shape (§3 writes are
// then skipped, reads fall back), never blocking indexing.
func ensureStoreGeneration(storageRoot, embeddingFP string, dims int) (string, error) {
	if dir, m, ok := readCurrentGeneration(storageRoot); ok {
		if m.compatible(embeddingFP, dims) {
			return dir, nil
		}
		return advanceStoreGeneration(storageRoot, dir, embeddingFP, dims)
	}
	// CURRENT did not resolve to a readable, format-known generation. The
	// creation leg rewrites generation 0's manifest and points CURRENT at
	// it — safe ONLY for a tree that never completed its first create (or
	// holds no store/ tree at all). Every other shape — an unreadable
	// CURRENT pointer (permissions, fd exhaustion, an AV/indexer lock), a
	// named generation whose manifest is absent or unreadable, or a
	// readable manifest of a foreign format — may hide chunk records and
	// tables of another embedding era under an identity this binary cannot
	// verify (era identity lives ONLY in the manifest; tables and chunk
	// records carry none). Overwriting generation 0 in place would serve
	// that data under the new era's manifest — the cross-era mixing the
	// generation model exists to prevent — so the tree is refused, not
	// rewritten blind: §3 reads see absent, §3 writes skip, the legacy
	// shape keeps serving, nothing is deleted.
	if dir, err := firstGenerationAdoption(storageRoot, embeddingFP, dims); dir != "" || err != nil {
		return dir, err
	}
	dir := generationPath(storageRoot, 0)
	if err := writeGenerationDirectory(dir, &storeGenerationManifest{
		Format:      storeFormatV2Generation,
		EmbeddingFP: embeddingFP,
		Dims:        dims,
	}); err != nil {
		return "", err
	}
	if adopted, err := publishCurrentGeneration(storageRoot, 0); err != nil {
		return "", err
	} else if adopted != "" {
		// A concurrent ensure won a race and published a generation while
		// this caller was creating generation 0: adopt the winner.
		return adopted, nil
	}
	return dir, nil
}

// firstGenerationAdoption decides ensureStoreGeneration's creation leg for
// a tree whose CURRENT pointer did not resolve. It returns:
//
//   - ("", nil): safe to (re)create generation 0 — no store/ tree at all,
//     or the clean crashed-first-create window (a store/ tree with no
//     CURRENT and an empty, manifest-less generation 0);
//   - (gen0, nil): a compatible generation 0 exists without its pointer —
//     the pointer write is replayed and generation 0 returned (idempotent
//     recovery, nothing rewritten);
//   - ("", errForeignStoreGeneration): the tree must not be adopted or
//     recreated (see ensureStoreGeneration), or a transient I/O error made
//     the decision unknowable (the conservative answer is the same).
func firstGenerationAdoption(storageRoot, embeddingFP string, dims int) (string, error) {
	store := storePath(storageRoot)
	if !pathExists(store) {
		return "", nil // no tree at all: create generation 0
	}
	if _, err := safeio.ReadFile(currentPointerPath(storageRoot)); err == nil {
		// A CURRENT that readCurrentGeneration refused: it names a
		// non-plain generation, or the named generation's manifest is
		// absent, unreadable, or foreign-format. The manifest-before-
		// pointer write ordering makes every legitimately-created
		// generation resolvable, so each of these shapes hides a possibly
		// populated directory behind an era this binary cannot verify.
		return "", errForeignStoreGeneration
	} else if !errors.Is(err, fs.ErrNotExist) {
		// The pointer itself is unreadable: the tree was adopted once —
		// never rewrite it blind.
		return "", errForeignStoreGeneration
	}
	// No CURRENT pointer: the crashed-first-create window (the store/ tree
	// was created but the pointer rename never landed). Recover ONLY when
	// generation 0 is provably this era's (a compatible manifest — replay
	// the pointer) or provably empty (no manifest, no chunks, no
	// snapshots — recreate). Anything else — a foreign-era generation 0
	// without its pointer, an unreadable manifest, or debris beyond
	// generation 0 — is corruption, not a crash window.
	gens, err := os.ReadDir(filepath.Join(store, storeGenerationsDirName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", errForeignStoreGeneration
	}
	for _, e := range gens {
		if n, convErr := strconv.Atoi(e.Name()); convErr != nil || n > 0 {
			return "", errForeignStoreGeneration
		}
	}
	gen0 := generationPath(storageRoot, 0)
	m, absent := readGenerationManifestClassified(gen0)
	switch {
	case absent:
		if dirHasEntries(filepath.Join(gen0, storeChunksDirName)) ||
			dirHasEntries(filepath.Join(gen0, storeSnapshotsDirName)) {
			// Data without a manifest: its era is unknowable — refuse
			// rather than adopt it under a fresh manifest.
			return "", errForeignStoreGeneration
		}
		return "", nil // the clean crash window: recreate generation 0
	case m.compatible(embeddingFP, dims):
		adopted, err := publishCurrentGeneration(storageRoot, 0)
		if err != nil {
			return "", err
		}
		if adopted != "" {
			// A concurrent ensure published a higher generation while this
			// caller was deciding: adopt the winner's directory.
			return adopted, nil
		}
		return gen0, nil
	default:
		// Present but foreign-era or unparsable: never overwritten.
		return "", errForeignStoreGeneration
	}
}

// readGenerationManifestClassified loads a generation manifest and
// distinguishes ABSENT (fs.ErrNotExist — the directory never completed a
// create) from UNREADABLE/unparsable (present but not loadable): callers
// deciding whether a directory may be rewritten in place must never fold
// an unreadable manifest into a missing one.
func readGenerationManifestClassified(dir string) (m *storeGenerationManifest, absent bool) {
	data, err := safeio.ReadFile(filepath.Join(dir, storeGenerationManifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	var mm storeGenerationManifest
	if json.Unmarshal(data, &mm) != nil {
		return nil, false
	}
	return &mm, false
}

// errForeignStoreGeneration marks a tree whose CURRENT names a generation
// this binary refuses (unknown manifest format). §3 writes skip; §3 reads
// see absent; nothing is deleted.
var errForeignStoreGeneration = errors.New("vector store generation has an unknown format")

// errChunkRecordTorn marks a chunk-record read that proves durable store
// damage — a record whose bytes are not a valid vector of the expected
// width (empty, torn, over-long, or not a regular file). Distinct from a
// transient read fault (permissions, fd exhaustion, a lock): only a torn
// record justifies unpublishing the table that references it (the
// power-loss self-heal); a transient fault must leave the publication
// intact for the next attempt.
var errChunkRecordTorn = errors.New("vector chunk record is torn")

// writeGenerationDirectory creates one generation directory with its
// manifest (idempotent on an existing compatible manifest).
func writeGenerationDirectory(dir string, m *storeGenerationManifest) error {
	for _, d := range []string{
		filepath.Join(dir, storeChunksDirName),
		filepath.Join(dir, storeSnapshotsDirName),
		filepath.Join(dir, storeRefsDirName),
		filepath.Join(dir, storeRootsDirName),
	} {
		if err := ensureRealDir(d, 0o750); err != nil {
			return fmt.Errorf("creating store generation directory %s: %w", d, err)
		}
	}
	m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return safeio.WriteFileAtomic(filepath.Join(dir, storeGenerationManifestName), data, 0o644)
}

// publishCurrentGeneration atomically advances the CURRENT pointer.
// MONOTONIC: two services can race an era advance into computing different
// successors (no store-level lock serializes ensureStoreGeneration across a
// project's simultaneous managers); the loser's late publish must never
// move CURRENT backwards onto a lower generation. When the existing
// pointer already names n' >= n the write is SKIPPED and the winner's
// directory is returned so the losing caller re-adopts it instead of its
// own superseded target (which the sweep collects as debris). A pointer
// read that fails OTHER than by absence fails the publish: an unconditional
// write on a transient read error (EACCES/EMFILE/EIO) never had its
// monotonic check, and could regress CURRENT onto a lower generation than
// a racing advance already published.
func publishCurrentGeneration(storageRoot string, n int) (adopted string, err error) {
	if err := ensureRealDir(storePath(storageRoot), 0o750); err != nil {
		return "", fmt.Errorf("creating store directory: %w", err)
	}
	data, readErr := safeio.ReadFile(currentPointerPath(storageRoot))
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return "", fmt.Errorf("reading the CURRENT pointer: %w", readErr)
	}
	if readErr == nil {
		if prev, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && prev >= n {
			return generationPath(storageRoot, prev), nil
		}
	}
	return "", safeio.WriteFileAtomic(currentPointerPath(storageRoot), []byte(strconv.Itoa(n)), 0o644)
}

// advanceStoreGeneration creates the successor generation of curDir and
// advances CURRENT. The old generation directory stays on disk (the sweep
// collects its chunks once unreachable); a crash before the CURRENT swap
// simply leaves the old generation current — the next ensure replays.
// Nothing is carried across the boundary: an advance happens exactly on an
// embedding-era change (ensureStoreGeneration advances only an incompatible
// generation), chunk vectors are era-bound, and openV2Corpus fails the open
// on any missing chunk record — a carried table would be destroyed at its
// first open and re-published by the next attach's build pass anyway, so
// carrying it only invites the destroy-then-rebuild dance. Ownership
// markers need no carry: they live at the storage-root refs/ (rootrefs.go),
// outside the generations tree.
func advanceStoreGeneration(storageRoot, curDir, embeddingFP string, dims int) (string, error) {
	// The successor's number: one past the highest existing generation —
	// crash debris of an earlier advance attempt must never be reused as
	// "new" while a lower-numbered complete generation exists. A failed
	// listing is never folded into "zero existing generations": next=0
	// would overwrite a possibly-populated generation 0 in place and flip
	// CURRENT onto old-era chunk data — fail the advance instead (the
	// caller's fail-soft handling keeps the legacy shape; the next ensure
	// replays).
	entries, err := os.ReadDir(filepath.Join(storePath(storageRoot), storeGenerationsDirName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("listing store generations: %w", err)
	}
	next := 0
	for _, e := range entries {
		if n, convErr := strconv.Atoi(e.Name()); convErr == nil && n >= next {
			next = n + 1
		}
	}
	nextDir := generationPath(storageRoot, next)
	if nextDir == curDir {
		// Structurally unreachable with a correct listing (next is greater
		// than every existing number, CURRENT's included) — refuse rather
		// than rewrite the current generation's manifest in place.
		return "", fmt.Errorf("store generation advance refused: successor %d equals the current generation", next)
	}
	if err := writeGenerationDirectory(nextDir, &storeGenerationManifest{
		Format:      storeFormatV2Generation,
		EmbeddingFP: embeddingFP,
		Dims:        dims,
	}); err != nil {
		return "", err
	}
	if adopted, pubErr := publishCurrentGeneration(storageRoot, next); pubErr != nil {
		return "", pubErr
	} else if adopted != "" {
		// A concurrent advance won the race and CURRENT already names a
		// generation >= next: adopt the winner's directory (our own target
		// is debris the sweep collects) instead of moving CURRENT back.
		return adopted, nil
	}
	return nextDir, nil
}

// ---------------------------------------------------------------------------
// Chunk records (L1)
// ---------------------------------------------------------------------------

// chunkRecordKey derives the L1 content identity C of a chunk: the SHA-256
// of the NORMALIZED chunk text — the exact bytes the embedding cache and
// the embedder see (normalizeEmbeddingChunk, embedding_cache.go). Sharing
// the normalization makes C, the cache key and the embedding input one
// identity: identical normalized text ⇒ identical C ⇒ one vector record
// (deduplication boundary, ADR-104 §1 L1).
func chunkRecordKey(chunkText string) string {
	return computeHash([]byte(normalizeEmbeddingChunk(chunkText)))
}

// isChunkID reports whether s has the full shape of a chunk identity C
// (chunkRecordKey's output: 64 lowercase hex characters). Every identity
// that reaches chunkRecordPath (whose c[:2] shard slice panics on shorter
// input) is validated against this shape first — the sweep walks a
// directory foreign debris can land in, and a stray "x.chunk" file must
// read as debris, never crash the walk.
func isChunkID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
			return false
		}
	}
	return true
}

// chunkRecordPath returns the chunk record's sharded path under a
// generation. The two-hex shard keeps any single directory bounded on
// corpora with hundreds of thousands of chunks.
func chunkRecordPath(genDir, c string) string {
	return filepath.Join(genDir, storeChunksDirName, c[:2], c+chunkFileExt)
}

// traversesNonDirectory reports whether a failed stat/read over path
// traversed a NON-directory component: POSIX names that shape ENOTDIR, but
// Windows reports ERROR_PATH_NOT_FOUND, which errors.Is reads as
// fs.ErrNotExist — indistinguishable there from a genuinely absent leaf.
// Only the immediate parent is inspected; the call sites (a chunk record's
// shard directory, a repo's .git probe) are one level deep.
func traversesNonDirectory(err error, path string) bool {
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	info, statErr := os.Stat(filepath.Dir(path))
	return statErr == nil && !info.IsDir()
}

// errPathObstructed re-names a through-a-file traversal for the classifier:
// wrapping syscall.ENOTDIR would NOT do, because on Windows that errno IS
// ERROR_PATH_NOT_FOUND — itself read as fs.ErrNotExist — so the wrapped
// error would still classify the obstructed record as PROVEN-absent damage.
// The sentinel sits in neither classifier's family (Repairable/Transient),
// so an obstructed path defers without touching store state on either
// platform.
var errPathObstructed = errors.New("path traverses a regular file where a directory belongs")

// encodeChunkRecord serializes one vector as dims×float32 little-endian
// (ADR-104 §3: the record carries the vector and nothing else).
func encodeChunkRecord(vec []float32) []byte {
	buf := make([]byte, 4*len(vec))
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], mathFloat32bits(v))
	}
	return buf
}

// decodeChunkRecord parses a chunk record. The record's width is its
// vector's arity — 4-byte-aligned, non-empty. A KNOWN dims (the
// generation manifest's) must match exactly (a foreign-era or torn record
// reads as an error, never as a wrong vector); dims 0 (a service that
// never learned its arity — in-memory tests) accepts any aligned width
// and lets the scoring site's own length guard arbitrate.
func decodeChunkRecord(data []byte, dims int) ([]float32, error) {
	if len(data) == 0 || len(data)%4 != 0 {
		return nil, fmt.Errorf("chunk record is %d bytes; not a float32 vector", len(data))
	}
	if dims > 0 && len(data) != 4*dims {
		return nil, fmt.Errorf("chunk record is %d bytes, want %d for dims %d", len(data), 4*dims, dims)
	}
	vec := make([]float32, len(data)/4)
	for i := range vec {
		vec[i] = mathFloat32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec, nil
}

// writeChunkRecord lands one L1 record under the generation, BEFORE any
// table may reference it (ADR-104 §3 write ordering). An existing record
// with the same C is the dedup hit — content addressing makes the write
// idempotent, so two occurrences of identical bytes cost one record.
//
// Durability: temp-file + rename, NO per-record fsync — a corpus holds
// thousands of records and syncing each one costs milliseconds of disk
// round-trips (the v1 form's whole-collection gob paid one sync; §3 would
// pay thousands). The rename is process-crash-atomic, which is the crash
// model ADR-104 §6 enumerates; a POWER-loss window (table durable, a chunk
// record's data not) is closed at BOTH ends — publication reads every
// referenced record's payload back before any table covers it
// (verifyChunkRecord, whose repair hook re-derives a record that passes
// stat but fails its read-back), and the reader unpublishes a snapshot
// whose referenced chunk is provably absent or torn (openV2Corpus; a
// transient read error leaves the publication intact) so the next attach
// rebuilds it cache-spared. Never a torn read: a table row without its
// chunk fails closed.
func writeChunkRecord(genDir, c string, vec []float32) error {
	path := chunkRecordPath(genDir, c)
	if _, statErr := os.Stat(path); statErr == nil {
		return nil // already landed — the L1 dedup hit
	}
	if err := ensureRealDir(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data := encodeChunkRecord(vec)
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// readChunkRecord loads one vector record; a missing, short, over-long or
// wrong-width record is an error (the caller fails closed rather than
// serving a wrong vector). Errors that prove durable damage wrap
// errChunkRecordTorn (or are fs.ErrNotExist / io.ErrUnexpectedEOF); every
// other open/stat/read error is a TRANSIENT fault the caller must not
// treat as store damage.
//
// The record is OUR OWN file (written by writeChunkRecord under the
// store's generation tree), and the §3 open reads thousands of them at
// once: the generic safeio.ReadFile open (O_NONBLOCK) registers every fd
// with the runtime poller — hundreds of microseconds per 64-byte record,
// seconds per corpus — so this reader uses the plain blocking open and
// reads EXACTLY the expected width. The file is STAT-ed first and its size
// must match the expected width EXACTLY in both directions: a SHORT file
// is a torn record, and an OVER-LONG file (a foreign-era record written
// at a larger dims, or an external append) must never be silently
// truncated to its first dims floats and served as a full current-era
// vector — the width check's own doc contract ("a foreign-era or torn
// record reads as an error, never as a wrong vector") holds in both
// directions only with the total-size check.
func readChunkRecord(genDir, c string, dims int) ([]float32, error) {
	path := chunkRecordPath(genDir, c)
	f, err := os.Open(path)
	if err != nil {
		if traversesNonDirectory(err, path) {
			// Same normalization as verifyChunkRecord: an obstructed shard
			// is not an absent record, so the §3 open keeps the table
			// published instead of reading Windows' ErrNotExist as damage.
			return nil, &fs.PathError{Op: "open", Path: path, Err: errPathObstructed}
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	width := 4 * dims
	info, statErr := f.Stat()
	if statErr != nil {
		return nil, statErr
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("chunk record %s is not a regular file: %w", c, errChunkRecordTorn)
	}
	if width == 0 {
		// A service that never learned its arity: size the read from the
		// file itself (bounded; the decode validates alignment).
		if info.Size() <= 0 || info.Size() > 4*maxVecDims || info.Size()%4 != 0 {
			return nil, fmt.Errorf("chunk record is %d bytes; not a float32 vector: %w", info.Size(), errChunkRecordTorn)
		}
		width = int(info.Size())
	} else if info.Size() != int64(width) {
		return nil, fmt.Errorf("chunk record is %d bytes, want %d for dims %d: %w", info.Size(), width, dims, errChunkRecordTorn)
	}
	buf := make([]byte, width)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return decodeChunkRecord(buf, dims)
}

// chunkRecordExists reports whether C's record is on disk.
func chunkRecordExists(genDir, c string) bool {
	return pathExists(chunkRecordPath(genDir, c))
}

// ---------------------------------------------------------------------------
// Occurrence tables (L2/L3)
// ---------------------------------------------------------------------------

// escapeTabField percent-escapes the characters that would break the TSV /
// occurrence grammar. '%' escapes first so the encoding is unambiguous.
// The scan is BYTE-wise on purpose: range-over-string iterates runes and
// rewrites every invalid UTF-8 byte as the 3-byte U+FFFD, which would
// permanently corrupt a row's path (Linux filenames are arbitrary bytes);
// byte-wise escaping keeps any filename byte verbatim and makes the
// round-trip total.
func escapeTabField(s string) string {
	if !strings.ContainsAny(s, "%\t\n;,") {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		switch b := s[i]; b {
		case '%':
			sb.WriteString("%p")
		case '\t':
			sb.WriteString("%t")
		case '\n':
			sb.WriteString("%n")
		case ';':
			sb.WriteString("%s")
		case ',':
			sb.WriteString("%c")
		default:
			sb.WriteByte(b)
		}
	}
	return sb.String()
}

// unescapeTabField reverses escapeTabField.
func unescapeTabField(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+1 >= len(s) {
			sb.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'p':
			sb.WriteByte('%')
		case 't':
			sb.WriteByte('\t')
		case 'n':
			sb.WriteByte('\n')
		case 's':
			sb.WriteByte(';')
		case 'c':
			sb.WriteByte(',')
		default:
			sb.WriteString(s[i : i+2])
		}
		i++
	}
	return sb.String()
}

// encodeOccurrenceTab renders rows as the deterministic occurrences.tab
// body: one row per line, sorted by path, `path \t blobHash \t occ;occ…`
// with each occurrence `C,index,start,end,lang`. The chunk index is
// carried explicitly: a poison-dropped chunk leaves a gap in a file's
// 0..n-1 sequence, so the row's slice positions are not the indices and
// positional consumers (openV2Corpus's DocumentID mapping) must not
// derive them. Determinism makes the table byte-comparable across
// replays — the migration's replay-safety proof.
func encodeOccurrenceTab(rows []occurrenceRow) []byte {
	sorted := make([]occurrenceRow, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	for i := range sorted {
		chunks := make([]chunkOccurrence, len(sorted[i].Chunks))
		copy(chunks, sorted[i].Chunks)
		sort.SliceStable(chunks, func(a, b int) bool { return chunks[a].Index < chunks[b].Index })
		sorted[i].Chunks = chunks
	}
	var sb strings.Builder
	for _, row := range sorted {
		sb.WriteString(escapeTabField(row.Path))
		sb.WriteByte('\t')
		sb.WriteString(escapeTabField(row.BlobHash))
		sb.WriteByte('\t')
		for i, occ := range row.Chunks {
			if i > 0 {
				sb.WriteByte(';')
			}
			sb.WriteString(occ.C)
			sb.WriteByte(',')
			sb.WriteString(strconv.Itoa(occ.Index))
			sb.WriteByte(',')
			sb.WriteString(strconv.Itoa(occ.Start))
			sb.WriteByte(',')
			sb.WriteString(strconv.Itoa(occ.End))
			sb.WriteByte(',')
			sb.WriteString(escapeTabField(occ.Language))
		}
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

// decodeOccurrenceTab parses an occurrences.tab body; every malformed line
// fails the whole table (a torn table reads as absent and rebuilds — never
// a silently partial view). The occurrence grammar is 5-field
// `C,index,start,end,lang`; C must be a full 64-hex chunk identity
// (chunkRecordKey's output shape — a foreign value is never dereferenced
// into the chunk tree) and the index must be non-negative.
func decodeOccurrenceTab(data []byte) ([]occurrenceRow, error) {
	var rows []occurrenceRow
	for lineNo, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return nil, fmt.Errorf("occurrence tab line %d: %d fields, want 3", lineNo+1, len(parts))
		}
		row := occurrenceRow{Path: unescapeTabField(parts[0]), BlobHash: unescapeTabField(parts[1])}
		if row.Path == "" || row.BlobHash == "" {
			return nil, fmt.Errorf("occurrence tab line %d: empty path or blob hash", lineNo+1)
		}
		for _, occ := range strings.Split(parts[2], ";") {
			fields := strings.Split(occ, ",")
			if len(fields) != 5 || !isChunkID(fields[0]) {
				return nil, fmt.Errorf("occurrence tab line %d: malformed chunk occurrence %q", lineNo+1, occ)
			}
			index, err := strconv.Atoi(fields[1])
			if err != nil || index < 0 {
				return nil, fmt.Errorf("occurrence tab line %d: bad chunk index in %q", lineNo+1, occ)
			}
			start, err := strconv.Atoi(fields[2])
			if err != nil {
				return nil, fmt.Errorf("occurrence tab line %d: bad start line: %w", lineNo+1, err)
			}
			end, err := strconv.Atoi(fields[3])
			if err != nil {
				return nil, fmt.Errorf("occurrence tab line %d: bad end line: %w", lineNo+1, err)
			}
			row.Chunks = append(row.Chunks, chunkOccurrence{
				C:        fields[0],
				Index:    index,
				Start:    start,
				End:      end,
				Language: unescapeTabField(fields[4]),
			})
		}
		if len(row.Chunks) == 0 {
			return nil, fmt.Errorf("occurrence tab line %d: no chunk occurrences", lineNo+1)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// snapshotDir returns the §3 snapshot directory of a snapshot key
// ("snap_<oid16>" — the corpus layer's key space) under a generation. The
// bare oid16 is the directory name (§3 layout); snap_ keys map by prefix
// strip, so the corpus layer's existing identities address §3 snapshots
// unchanged.
func snapshotDir(genDir, snapKey string) string {
	return filepath.Join(genDir, storeSnapshotsDirName, strings.TrimPrefix(snapKey, snapshotKeyPrefix))
}

// snapshotTablePaths returns the snapshot's occurrences.tab and table.json
// paths (in write order).
func snapshotTablePaths(dir string) (occPath, headerPath string) {
	return filepath.Join(dir, occurrencesTabName), filepath.Join(dir, snapshotTableName)
}

// chunkRecordRepair re-derives the vector of one occurrence whose chunk
// record proved damaged (absent or torn); nil when the caller has no source
// for it — the publish then defers rather than landing an unservable table.
type chunkRecordRepair func(path string, chunkIndex int) ([]float32, error)

// writeSnapshotTable publishes one snapshot's §3 tables from completed
// chunk writes: occurrences.tab lands by atomic rename FIRST, table.json
// LAST — its presence is the snapshot's completion marker (ADR-104 §3
// write ordering). Before anything lands, every referenced chunk record is
// verified on disk and proven damage is repaired IN THIS PASS (see
// ensureChunkRecord); only a transient I/O fault defers the publish.
// specs/domains/workspace.md (shared corpus + §3 chunk records).
func writeSnapshotTable(genDir, snapKey string, dims int, rows []occurrenceRow, header *snapshotTable, repair chunkRecordRepair) error {
	dir := snapshotDir(genDir, snapKey)
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		for _, occ := range row.Chunks {
			if _, dup := seen[occ.C]; dup {
				continue // content addressing collapses identical chunks
			}
			seen[occ.C] = struct{}{}
			if err := ensureChunkRecord(genDir, occ.C, dims, row.Path, occ.Index, repair); err != nil {
				return fmt.Errorf("publishing %s: %w", snapKey, err)
			}
		}
	}
	if err := ensureRealDir(dir, 0o750); err != nil {
		return err
	}
	occPath, headerPath := snapshotTablePaths(dir)
	if err := writeOccurrenceTable(occPath, rows); err != nil {
		return fmt.Errorf("writing occurrence table for %s: %w", snapKey, err)
	}
	header.Format = storeFormatV2Generation
	header.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(header, "", "  ")
	if err != nil {
		return err
	}
	if err := safeio.WriteFileAtomic(headerPath, data, 0o644); err != nil {
		return fmt.Errorf("writing snapshot header for %s: %w", snapKey, err)
	}
	return nil
}

// maxVecDims is the §3 record-width ceiling: no chunk vector exceeds this
// many float32s. readChunkRecord enforces it on the dims-0 path (a service
// that never learned its arity), and verifyChunkRecord inherits the same
// refusal by DELEGATING to that reader — publish-time verification refuses
// exactly what the §3 open refuses BY CONSTRUCTION, and a record beyond
// the ceiling never lands under a published table (the snapshot would
// otherwise republish broken forever).
const maxVecDims = 1 << 16

// verifyChunkRecord proves one record usable by reading its PAYLOAD back
// through the §3 open's own reader (readChunkRecord), not just its
// metadata: a record is publishable exactly while the open that will serve
// it accepts it, and the delegation makes that hold by construction rather
// than by two hand-synced implementations. The publish pass also reads
// with the same plain blocking open the open itself uses — never the
// poller-registered safeio open whose per-fd cost readChunkRecord's doc
// documents as prohibitive at corpus scale. The read is what closes the
// write side of the power-loss window (ADR-104 §3's no-fsync rename
// durability): a record that passes stat but fails its read-back would
// fail the §3 open the moment the table was served, the rebuild would land
// the same broken table again, and the snapshot would loop
// unavailable-despite-everything (ADR-120 D1). The reader's verdict passes
// through the D3 taxonomy untouched — an absent record stays
// fs.ErrNotExist, a torn one errChunkRecordTorn, an obstructed shard
// errPathObstructed, a transient OS fault (permissions, fd exhaustion,
// I/O, cancellation) stays Transient — so repair vs defer is decided
// exactly as before; the shapes the reader leaves unclassified split by
// proof: a mid-read truncation (io.ErrUnexpectedEOF — the file shrank
// between the reader's fstat and its ReadFull) proves the record's bytes
// unservable and maps onto errChunkRecordTorn — the in-pass repair hook
// re-derives it — while any other unclassified fault (a POSIX ENOTDIR
// traversal, an exotic errno) proves nothing about the store and passes
// through untouched, sitting in NEITHER classifier's family so the
// publish defers without destroying (the reader's own doc contract:
// every other open/stat/read error is a transient fault the caller must
// not treat as store damage). Cost: publication reads every distinct
// referenced record once — the same bytes, through the same open, the
// next open reads.
func verifyChunkRecord(genDir, c string, dims int) error {
	if _, err := readChunkRecord(genDir, c, dims); err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist),
			errors.Is(err, errChunkRecordTorn),
			errors.Is(err, errPathObstructed),
			Transient(err):
			// The reader's own verdict — keep it for the D3 taxonomy.
			return err
		case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
			// Proven unservable mid-read: torn, repairable in this pass.
			return fmt.Errorf("chunk record %s is unreadable: %w", c, errChunkRecordTorn)
		default:
			// Proves nothing about the store — neither family, defer.
			return err
		}
	}
	return nil
}

// ensureChunkRecord proves one referenced record usable before any table
// references it, repairing PROVEN damage in the same pass. The orphan sweep
// runs outside the corpus build lease (ADR-104 §6), so a build that outlives
// the sweep's grace window can lose an early record — re-deriving it here is
// what stops the build from deferring forever. A transient fault
// (Repairable false) leaves the record untouched and defers the publish:
// nothing about the store's content is proven, so nothing is destroyed.
// specs/domains/workspace.md (shared corpus + §3 chunk records).
func ensureChunkRecord(genDir, c string, dims int, path string, chunkIndex int, repair chunkRecordRepair) error {
	err := verifyChunkRecord(genDir, c, dims)
	if err == nil {
		return nil
	}
	if !Repairable(err) {
		return fmt.Errorf("chunk record %s is unreadable (transient, publication deferred): %w", c, err)
	}
	if repair == nil {
		return fmt.Errorf("chunk record %s is missing (deferred): %w", c, err)
	}
	vec, rErr := repair(path, chunkIndex)
	if rErr != nil {
		return fmt.Errorf("re-deriving damaged chunk record %s: %w", c, rErr)
	}
	// writeChunkRecord is a dedup no-op on an existing path, so a TORN
	// record must be removed before it can be replaced.
	if rmErr := os.Remove(chunkRecordPath(genDir, c)); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		return fmt.Errorf("removing damaged chunk record %s: %w", c, rmErr)
	}
	if wErr := writeChunkRecord(genDir, c, vec); wErr != nil {
		return fmt.Errorf("re-embedding chunk record %s: %w", c, wErr)
	}
	if vErr := verifyChunkRecord(genDir, c, dims); vErr != nil {
		return fmt.Errorf("chunk record %s still unreadable after re-embedding: %w", c, vErr)
	}
	return nil
}

// occurrenceTabMaxBytes bounds project-sized tables, beyond safeio's document cap.
// See specs/domains/workspace.md.
const occurrenceTabMaxBytes int64 = 256 << 20

// readSnapshotTable is the conservative hygiene view of loadSnapshotTable:
// an unreadable table makes its chunk references unknowable.
// See specs/domains/workspace.md.
func readSnapshotTable(genDir, snapKey string) (rows []occurrenceRow, header *snapshotTable, ok bool) {
	rows, header, err := loadSnapshotTable(genDir, snapKey)
	return rows, header, err == nil
}

// loadSnapshotTable preserves read errors for the native corpus open.
// See specs/domains/workspace.md.
func loadSnapshotTable(genDir, snapKey string) ([]occurrenceRow, *snapshotTable, error) {
	dir := snapshotDir(genDir, snapKey)
	headerPath := filepath.Join(dir, snapshotTableName)
	headerData, err := safeio.ReadFile(headerPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading snapshot header %s: %w", headerPath, err)
	}
	var h snapshotTable
	if err := json.Unmarshal(headerData, &h); err != nil {
		return nil, nil, fmt.Errorf("%w: decoding snapshot header %s: %w", ErrCorpusNotPublished, headerPath, err)
	}
	if h.Format != storeFormatV2Generation {
		return nil, nil, fmt.Errorf("%w: snapshot header %s has unsupported format %d", ErrCorpusNotPublished, headerPath, h.Format)
	}
	occPath := filepath.Join(dir, occurrencesTabName)
	occData, err := safeio.ReadFileLimited(occPath, occurrenceTabMaxBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("reading occurrence table %s: %w", occPath, err)
	}
	rows, err := decodeOccurrenceTab(occData)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: decoding occurrence table %s: %w", ErrCorpusNotPublished, occPath, err)
	}
	return rows, &h, nil
}

// writeOccurrenceTable refuses a table its reader cannot open.
// See specs/domains/workspace.md.
func writeOccurrenceTable(path string, rows []occurrenceRow) error {
	data := encodeOccurrenceTab(rows)
	if int64(len(data)) > occurrenceTabMaxBytes {
		return &safeio.TooLargeError{Path: path, Limit: occurrenceTabMaxBytes}
	}
	return safeio.WriteFileAtomic(path, data, 0o644)
}

// snapshotTablePublished reports whether the snapshot's completion marker
// (table.json) exists under the generation.
func snapshotTablePublished(genDir, snapKey string) bool {
	_, headerPath := snapshotTablePaths(snapshotDir(genDir, snapKey))
	return pathExists(headerPath)
}

// unpublishSnapshotTable revokes a snapshot's §3 publication by removing
// its completion marker — the power-loss self-heal of the chunk-record
// durability model (see writeChunkRecord): a table whose chunk data did
// not survive is never half-served, and the removal makes the next
// EnsureCorpus rebuild the snapshot (embedding-cache-spared). On failure
// the occurrences tab debris is left for the rebuild to overwrite; on
// success both legs are removed. Best-effort. Callers
// must first classify the triggering error (openV2Corpus): only PROVEN
// damage (a record absent or torn) justifies this — a transient read
// fault (permissions, fd exhaustion, a lock) must leave the publication
// intact so the next attempt retries against the same table.
func unpublishSnapshotTable(genDir, snapKey string) {
	_, headerPath := snapshotTablePaths(snapshotDir(genDir, snapKey))
	if err := os.Remove(headerPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return // a later sweep retries; the open already failed closed
	}
	_ = os.Remove(filepath.Join(snapshotDir(genDir, snapKey), occurrencesTabName))
}

// ---------------------------------------------------------------------------
// Root state (L4): overlay tables, view records, relocated sidecars
// ---------------------------------------------------------------------------

// rootStateDir returns the root's §3 state directory (keyed by the overlay
// root key, which already carries the root fingerprint).
func rootStateDir(genDir, rootKey string) string {
	return filepath.Join(genDir, storeRootsDirName, strings.TrimPrefix(rootKey, overlayKeyPrefix))
}

// overlayTabPath returns the root's overlay.tab path.
func overlayTabPath(genDir, rootKey string) string {
	return filepath.Join(rootStateDir(genDir, rootKey), overlayTabName)
}

// writeOverlayTable publishes the root's overlay occurrence table. Same
// ordering discipline as snapshots (a single atomic file; there is no
// header — the tab itself is the record). Rows reflect the overlay store's
// CURRENT documents: the caller holds the service write lock and passes
// exactly the rows its collection serves.
func writeOverlayTable(genDir, rootKey string, rows []occurrenceRow) error {
	if err := ensureRealDir(rootStateDir(genDir, rootKey), 0o750); err != nil {
		return err
	}
	return writeOccurrenceTable(overlayTabPath(genDir, rootKey), rows)
}

// readOverlayTable loads the root's overlay rows; ok is false when absent
// or malformed (the overlay then carries no §3 references — the chunk GC
// keeps nothing on its behalf, and the runtime overlay engine is the
// fallback truth). See specs/domains/workspace.md for the size bound.
func readOverlayTable(genDir, rootKey string) ([]occurrenceRow, bool) {
	data, err := safeio.ReadFileLimited(overlayTabPath(genDir, rootKey), occurrenceTabMaxBytes)
	if err != nil {
		return nil, false
	}
	rows, err := decodeOccurrenceTab(data)
	if err != nil {
		return nil, false
	}
	return rows, true
}

// overlaySidecarPath returns the root's sidecar path inside the §3 tree.
func overlaySidecarPath(genDir, rootKey string) string {
	return filepath.Join(rootStateDir(genDir, rootKey), overlaySidecarName)
}

// overlayLexicalDir returns the root's overlay lexical directory inside the
// §3 tree.
func overlayLexicalDir(genDir, rootKey string) string {
	return filepath.Join(rootStateDir(genDir, rootKey), overlayLexicalDirName)
}

// writeRootViewState records the root's pinned view (view.json).
func writeRootViewState(genDir, rootKey, snapKey string) error {
	state := rootViewState{Format: rootViewStateFormat, PinnedID: snapKey, LastSeen: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.MarshalIndent(&state, "", "  ")
	if err != nil {
		return err
	}
	if err := ensureRealDir(rootStateDir(genDir, rootKey), 0o750); err != nil {
		return err
	}
	return safeio.WriteFileAtomic(filepath.Join(rootStateDir(genDir, rootKey), rootViewStateName), data, 0o644)
}

// ---------------------------------------------------------------------------
// Generation-relative path helpers
// ---------------------------------------------------------------------------

// storageRootOfGeneration reverses generationPath: the generation sits
// three levels below the storage root (store/generations/<n> → root).
// Used by read paths that must resolve a sibling artifact OUTSIDE the
// generation (the migrated snapshot's legacy lexical fallback). Ownership
// markers (refs/) live at the storage root itself, never under a
// generation — rootrefs.go owns their layout.
func storageRootOfGeneration(genDir string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(genDir)))
}

// ---------------------------------------------------------------------------
// Chunk enumeration (the §6 sweep's walk)
// ---------------------------------------------------------------------------

// chunkShardDirs lists the generation's chunk shard directories (may be
// empty for a fresh generation).
func chunkShardDirs(genDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(genDir, storeChunksDirName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(genDir, storeChunksDirName, e.Name()))
		}
	}
	return dirs, nil
}

// chunkIDsFromShard lists the C identities of the chunk records in one
// shard directory. Stems that are not a full 64-hex chunk identity are
// foreign debris in a directory we never exclusively own — skipped, never
// returned (the sweep feeds every returned ID into chunkRecordPath, whose
// two-hex shard slice must never see a short stem).
func chunkIDsFromShard(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if c, ok := strings.CutSuffix(e.Name(), chunkFileExt); ok && e.Type().IsRegular() {
			if !isChunkID(c) {
				continue // foreign debris — never ours, never enumerated
			}
			ids = append(ids, c)
		}
	}
	return ids, nil
}

// mathFloat32bits / mathFloat32frombits: named shims over math.Float32bits
// so the encode/decode sites read as a format definition (little-endian
// float32 rows) rather than bare bit casts.
func mathFloat32bits(v float32) uint32     { return math.Float32bits(v) }
func mathFloat32frombits(b uint32) float32 { return math.Float32frombits(b) }
