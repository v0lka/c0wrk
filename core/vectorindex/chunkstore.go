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
//	      occurrences.tab          L2 rows: path → blobHash, [(C, s, e, lang)]
//	      table.json               {chunker_fp K, ignore_fp I, counts, bytes}
//	      lexical/                 per-snapshot bleve index
//	    refs/<sanitized>.ref      ownership pointers (stage-5 records relocated)
//	    roots/<sanitized-root>/   L4: overlay.tab, overlay-lexical/, sidecar.json,
//	                              view.json
//	  CURRENT                     atomic-rename pointer to the live generation
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
// generation directory (occurrence tables and refs are carried over —
// they are era-independent — while chunk vectors re-fill from the
// embedding cache's misses).

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

	// storeRefsDirName holds the generation's ownership pointers — the
	// stage-5 refs/ records (root markers, branch references), relocated
	// under the generation once it exists. Readers union both locations so
	// a pre-§3 store's markers keep working through the adoption.
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
// content identity C (L1), its line range, and its language. The chunk
// TEXT is deliberately absent — ADR-049's content-less contract is
// preserved at the store level (reconstruction stays best-effort from
// sources).
type chunkOccurrence struct {
	C        string
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
//   - CURRENT generation foreign-era → create the NEXT generation,
//     carry over the era-independent records (every snapshot's
//     occurrences.tab + table.json and the refs/ tree — vectors re-fill
//     from embedding-cache misses), and atomically advance CURRENT. The
//     superseded generation's chunk files stop being referenced; the §6
//     orphan sweep collects them once no table points into them.
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
	// A malformed/unknown-format tree is read as absent but never deleted:
	// park the §3 tree aside only if a foreign CURRENT blocks creation —
	// instead, refuse to adopt (the conservative posture; unknown formats
	// belong to a newer binary).
	if pathExists(storePath(storageRoot)) {
		if data, err := safeio.ReadFile(currentPointerPath(storageRoot)); err == nil {
			name := strings.TrimSpace(string(data))
			if n, convErr := strconv.Atoi(name); convErr == nil && n >= 0 {
				dir := generationPath(storageRoot, n)
				if readGenerationManifest(dir) != nil {
					// A readable manifest we refused above must be a
					// foreign format: adopt nothing, serve legacy.
					return "", errForeignStoreGeneration
				}
			}
		}
	}
	dir := generationPath(storageRoot, 0)
	if err := writeGenerationDirectory(dir, &storeGenerationManifest{
		Format:      storeFormatV2Generation,
		EmbeddingFP: embeddingFP,
		Dims:        dims,
	}); err != nil {
		return "", err
	}
	if err := publishCurrentGeneration(storageRoot, 0); err != nil {
		return "", err
	}
	return dir, nil
}

// errForeignStoreGeneration marks a tree whose CURRENT names a generation
// this binary refuses (unknown manifest format). §3 writes skip; §3 reads
// see absent; nothing is deleted.
var errForeignStoreGeneration = errors.New("vector store generation has an unknown format")

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
func publishCurrentGeneration(storageRoot string, n int) error {
	if err := ensureRealDir(storePath(storageRoot), 0o750); err != nil {
		return fmt.Errorf("creating store directory: %w", err)
	}
	return safeio.WriteFileAtomic(currentPointerPath(storageRoot), []byte(strconv.Itoa(n)), 0o644)
}

// advanceStoreGeneration creates the successor generation of curDir,
// carries the era-independent records over, and advances CURRENT. The old
// generation directory stays on disk (the sweep collects its chunks once
// unreachable); a crash before the CURRENT swap simply leaves the old
// generation current — the next ensure replays.
func advanceStoreGeneration(storageRoot, curDir, embeddingFP string, dims int) (string, error) {
	// The successor's number: one past the highest existing generation —
	// crash debris of an earlier advance attempt must never be reused as
	// "new" while a lower-numbered complete generation exists.
	next := 0
	if entries, err := os.ReadDir(filepath.Join(storePath(storageRoot), storeGenerationsDirName)); err == nil {
		for _, e := range entries {
			if n, convErr := strconv.Atoi(e.Name()); convErr == nil && n >= next {
				next = n + 1
			}
		}
	}
	nextDir := generationPath(storageRoot, next)
	if err := writeGenerationDirectory(nextDir, &storeGenerationManifest{
		Format:      storeFormatV2Generation,
		EmbeddingFP: embeddingFP,
		Dims:        dims,
	}); err != nil {
		return "", err
	}
	// Carry over snapshot tables (occurrences.tab + table.json — era-
	// independent rows; chunk vectors re-fill on demand) and the refs tree
	// (ownership must survive an app-update re-embed or the sweep would
	// reclaim live corpora). Lexical directories are era-independent BM25
	// state: they move with the table. Best-effort: a failed copy leaves
	// the old generation current and the next ensure replays.
	if err := carryGenerationRecords(curDir, nextDir); err != nil {
		return "", err
	}
	if err := publishCurrentGeneration(storageRoot, next); err != nil {
		return "", err
	}
	return nextDir, nil
}

// carryGenerationRecords copies every snapshot's table files (and lexical
// directory) plus the refs tree from one generation into its successor.
// Copy (not rename): the old generation must stay whole until CURRENT
// advances — a reader that resolved it a moment ago keeps a consistent
// view, and a crash mid-carry leaves nothing half-moved in the successor
// (a partial snapshot dir simply has no table.json and reads as absent).
func carryGenerationRecords(fromDir, toDir string) error {
	copyTree := func(rel string) error {
		src := filepath.Join(fromDir, rel)
		if !pathExists(src) {
			return nil
		}
		return copyDirTree(src, filepath.Join(toDir, rel))
	}
	if entries, err := os.ReadDir(filepath.Join(fromDir, storeSnapshotsDirName)); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			snapName := e.Name()
			// The successor's snapshot directory must exist before its
			// table files land (WriteFileAtomic creates files, not
			// parents); an empty carried directory is harmless — it has
			// no table.json and reads as absent.
			if err := ensureRealDir(filepath.Join(toDir, storeSnapshotsDirName, snapName), 0o750); err != nil {
				return err
			}
			for _, leg := range []string{occurrencesTabName, snapshotTableName} {
				data, err := safeio.ReadFile(filepath.Join(fromDir, storeSnapshotsDirName, snapName, leg))
				if err != nil {
					continue // a snapshot without complete tables carries nothing
				}
				dst := filepath.Join(toDir, storeSnapshotsDirName, snapName, leg)
				if err := safeio.WriteFileAtomic(dst, data, 0o644); err != nil {
					return fmt.Errorf("carrying snapshot table %s/%s: %w", snapName, leg, err)
				}
			}
			if err := copyTree(filepath.Join(storeSnapshotsDirName, snapName, "lexical")); err != nil {
				return err
			}
		}
	}
	return copyTree(storeRefsDirName)
}

// copyDirTree recursively copies a directory tree (regular files and
// directories only; symlinks are skipped — the store never creates them).
func copyDirTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("reading %s for carry-over: %w", src, err)
	}
	if err := ensureRealDir(dst, 0o750); err != nil {
		return err
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		switch {
		case e.IsDir():
			if err := copyDirTree(s, d); err != nil {
				return err
			}
		case e.Type().IsRegular():
			data, err := safeio.ReadFile(s)
			if err != nil {
				return err
			}
			if err := safeio.WriteFileAtomic(d, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
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

// chunkRecordPath returns the chunk record's sharded path under a
// generation. The two-hex shard keeps any single directory bounded on
// corpora with hundreds of thousands of chunks.
func chunkRecordPath(genDir, c string) string {
	return filepath.Join(genDir, storeChunksDirName, c[:2], c+chunkFileExt)
}

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
// record's data not) is closed by the reader instead — openV2Corpus
// unpublishes a snapshot whose referenced chunk is unreadable and the next
// attach rebuilds it cache-spared. Never a torn read: a table row without
// its chunk fails closed.
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

// readChunkRecord loads one vector record; a missing, short or
// wrong-width record is an error (the caller fails closed rather than
// serving a wrong vector).
//
// The record is OUR OWN file (written by writeChunkRecord under the
// store's generation tree), and the §3 open reads thousands of them at
// once: the generic safeio.ReadFile open (O_NONBLOCK) registers every fd
// with the runtime poller — hundreds of microseconds per 64-byte record,
// seconds per corpus — so this reader uses the plain blocking open and
// reads EXACTLY the expected width. The regular-file check is kept (a
// planted non-regular file reads as an error, never as a wrong vector).
func readChunkRecord(genDir, c string, dims int) ([]float32, error) {
	f, err := os.Open(chunkRecordPath(genDir, c))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	const maxVecDims = 1 << 16
	width := 4 * dims
	if width == 0 {
		// A service that never learned its arity: size the read from the
		// file itself (bounded; the decode validates alignment).
		info, statErr := f.Stat()
		if statErr != nil {
			return nil, statErr
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("chunk record %s is not a regular file", c)
		}
		if info.Size() <= 0 || info.Size() > 4*maxVecDims || info.Size()%4 != 0 {
			return nil, fmt.Errorf("chunk record is %d bytes; not a float32 vector", info.Size())
		}
		width = int(info.Size())
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
func escapeTabField(s string) string {
	if !strings.ContainsAny(s, "%\t\n;,") {
		return s
	}
	var sb strings.Builder
	for _, r := range s {
		switch r {
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
			sb.WriteRune(r)
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
// with each occurrence `C,start,end,lang`. Determinism makes the table
// byte-comparable across replays — the migration's replay-safety proof.
func encodeOccurrenceTab(rows []occurrenceRow) []byte {
	sorted := make([]occurrenceRow, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
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
// a silently partial view).
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
			if len(fields) != 4 || len(fields[0]) != 64 {
				return nil, fmt.Errorf("occurrence tab line %d: malformed chunk occurrence %q", lineNo+1, occ)
			}
			start, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("occurrence tab line %d: bad start line: %w", lineNo+1, err)
			}
			end, err := strconv.Atoi(fields[2])
			if err != nil {
				return nil, fmt.Errorf("occurrence tab line %d: bad end line: %w", lineNo+1, err)
			}
			row.Chunks = append(row.Chunks, chunkOccurrence{
				C:        fields[0],
				Start:    start,
				End:      end,
				Language: unescapeTabField(fields[3]),
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

// writeSnapshotTable publishes one snapshot's §3 tables from completed
// chunk writes: occurrences.tab lands by atomic rename FIRST, table.json
// LAST — its presence is the snapshot's completion marker (ADR-104 §3
// write ordering; a crash between the two leaves a tableless snapshot that
// reads as absent and replays).
func writeSnapshotTable(genDir, snapKey string, rows []occurrenceRow, header *snapshotTable) error {
	dir := snapshotDir(genDir, snapKey)
	if err := ensureRealDir(dir, 0o750); err != nil {
		return err
	}
	occPath, headerPath := snapshotTablePaths(dir)
	if err := safeio.WriteFileAtomic(occPath, encodeOccurrenceTab(rows), 0o644); err != nil {
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

// readSnapshotTable loads a published snapshot's tables; ok is false when
// the header (the completion marker) or the occurrence table is absent or
// malformed — the snapshot then reads as absent and the caller falls back
// or rebuilds. Never partially: a header without rows is a torn publish.
func readSnapshotTable(genDir, snapKey string) (rows []occurrenceRow, header *snapshotTable, ok bool) {
	dir := snapshotDir(genDir, snapKey)
	headerData, err := safeio.ReadFile(filepath.Join(dir, snapshotTableName))
	if err != nil {
		return nil, nil, false
	}
	var h snapshotTable
	if json.Unmarshal(headerData, &h) != nil || h.Format != storeFormatV2Generation {
		return nil, nil, false
	}
	occData, err := safeio.ReadFile(filepath.Join(dir, occurrencesTabName))
	if err != nil {
		return nil, nil, false
	}
	rows, err = decodeOccurrenceTab(occData)
	if err != nil {
		return nil, nil, false
	}
	return rows, &h, true
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
// EnsureCorpus rebuild the snapshot (embedding-cache-spared). occurrences
// tab debris is left for the rebuild to overwrite. Best-effort.
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
	return safeio.WriteFileAtomic(overlayTabPath(genDir, rootKey), encodeOccurrenceTab(rows), 0o644)
}

// readOverlayTable loads the root's overlay rows; ok is false when absent
// or malformed (the overlay then carries no §3 references — the chunk GC
// keeps nothing on its behalf, and the runtime overlay engine is the
// fallback truth).
func readOverlayTable(genDir, rootKey string) ([]occurrenceRow, bool) {
	data, err := safeio.ReadFile(overlayTabPath(genDir, rootKey))
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
// Refs relocation (stage-5 records under the generation)
// ---------------------------------------------------------------------------

// storageRootOfGeneration reverses generationPath: the generation sits
// three levels below the storage root (store/generations/<n> → root).
// Used by read paths that must resolve a sibling artifact OUTSIDE the
// generation (the migrated snapshot's legacy lexical fallback).
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
// shard directory.
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
