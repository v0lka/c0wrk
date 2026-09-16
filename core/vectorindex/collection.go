package vectorindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
	"github.com/v0lka/sp4rk/ignore"
)

// sanitizeRe matches characters that are not alphanumeric, hyphens, or underscores.
var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// embeddingCommitWindowSize bounds the number of already-embedded documents
// retained and handed to chromem in one commit. Inference batching is managed
// independently by documentAccumulator, so this limit controls only commit
// memory and cancellation latency.
const embeddingCommitWindowSize = 200

// collectionName returns a deterministic, sanitized collection name for a branch.
func collectionName(branch string) string {
	sanitized := strings.ReplaceAll(branch, "/", "_")
	sanitized = sanitizeRe.ReplaceAllString(sanitized, "")
	if sanitized == "" {
		sanitized = "default"
	}
	return "branch_" + sanitized
}

// lexicalBranchDirName returns a sanitized directory name for a branch,
// used as the per-branch subdirectory under the project's lexical folder.
func lexicalBranchDirName(branch string) string {
	sanitized := strings.ReplaceAll(branch, "/", "_")
	sanitized = sanitizeRe.ReplaceAllString(sanitized, "")
	if sanitized == "" {
		sanitized = "default"
	}
	return sanitized
}

// SwitchBranch switches to (or creates) a collection for the given branch.
// The caller must NOT hold s.mu.
func (s *Service) SwitchBranch(ctx context.Context, branchName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current.db == nil {
		return errors.New("no database initialized; call SetProject first")
	}

	if branchName == s.current.currentBranch && s.current.collection != nil {
		return nil
	}

	// Persist the outgoing branch's in-memory hashes before they are
	// overwritten by loadFileHashes for the new branch.
	if s.current.currentBranch != "" && s.current.collection != nil {
		if err := s.current.saveFileHashes(); err != nil {
			s.logger.Warn("failed to persist file-hash sidecar on branch switch",
				"branch", s.current.currentBranch, "error", err)
		}
	}

	name := collectionName(branchName)
	col, err := s.current.db.GetOrCreateCollection(name, nil, s.embeddingFunc)
	if err != nil {
		return fmt.Errorf("getting or creating collection %q: %w", name, err)
	}

	// Close any previously-open lexical index and open the one for this
	// branch. The lexical index is persisted alongside the chromem DB under
	// the project's vector_index directory (set by SetProject).
	if s.current.lexical != nil {
		if closeErr := s.current.lexical.Close(); closeErr != nil {
			s.logger.Warn("failed to close previous lexical index", "error", closeErr)
		}
		s.current.lexical = nil
	}
	// The lexical index directory is derived from the chromem DB path
	// (stored in the database, which was opened from the project path).
	// If the DB is persistent, extract its directory to place the lexical
	// index alongside it; otherwise skip lexical persistence.
	if s.current.projectPath != "" && s.current.projectID != "" {
		lexDir := filepath.Join(s.current.projectPath, "lexical", lexicalBranchDirName(branchName))
		// Ensure the parent directory (…/{projectID}/lexical/) exists;
		// bleve's New() creates the leaf (branch) directory itself.
		if mkErr := os.MkdirAll(filepath.Dir(lexDir), 0o750); mkErr != nil {
			s.logger.Warn("failed to create lexical parent directory", "path", lexDir, "error", mkErr)
		} else {
			lex, lexErr := lexical.Open(lexDir)
			if lexErr != nil {
				s.logger.Warn("failed to open lexical index", "path", lexDir, "error", lexErr)
			} else {
				s.current.lexical = lex
			}
		}
	}

	s.current.collection = col
	s.current.currentBranch = branchName
	// Load (or migrate) the file-hash sidecar so ValidateCollection can compare
	// stored hashes against disk without an embedding-bearing collection Query.
	s.loadFileHashes()
	s.logger.Info("switched branch collection", "branch", branchName, "collection", name)
	return nil
}

// readFileFn is an os.ReadFile indirection used by ValidateCollection's slow
// path. It exists purely as a test seam: tests swap it for a counting wrapper
// to assert that the stat-based fast path skips content reads entirely for
// unchanged files. Production code must not reassign it.
var readFileFn = os.ReadFile

// fileHashEntrySep separates the components of a file-hash sidecar value.
const fileHashEntrySep = "|"

// fullHashRevalidationEvery bounds how many consecutive ValidateCollection
// passes may rely on the stat-based fast-path before one pass skips it and
// re-reads + re-hashes every file. This is the backstop that catches content
// rewrites preserving both size and mtime (see the fast-path comment in
// ValidateCollection). 20 keeps the amortized revalidation cost at ~5% of a
// pass while bounding the detection window to ~20 incremental passes.
const fullHashRevalidationEvery = 20

// parseFileHashEntry parses a file-hash sidecar value. New-format values are
// "hash|size|mtimeUnixNano", "hash|size|mtimeUnixNano|chunkerFP" or
// "hash|size|mtimeUnixNano|chunkerFP|chunkSet", where size is the file size
// in bytes recorded at index time, mtimeUnixNano is the file's ModTime in
// Unix-nanoseconds recorded at index time, the optional chunkerFP is the
// chunker-configuration fingerprint the file was chunked under (see
// ChunkerFingerprint; absent for intermediate-format entries written before
// the fingerprint existed — retrieve it separately via
// fileHashEntryChunkerFP), and the optional chunkSet is the set of committed
// chunk indices for the file (see encodeChunkIndices; absent for entries
// whose committed set is unknown — retrieve it separately via
// fileHashEntryChunkSet). A 5-field value is accepted only when its chunkSet
// field parses; anything else malformed makes ok false and the caller must
// fall back to the full read+hash comparison. Legacy values are the bare
// content hash with no separators and parse the same way (ok false).
//
// Back-compat: an OLDER binary (parser without the chunkSet field) rejects
// any 5-field value via its "len(parts) must be 3 or 4" guard, i.e. treats
// it as a legacy bare hash — slower (full read+hash, one redundant re-index
// pass that rewrites the entry) but correct. TestFileHashEntry_OldParser…
// in filehash_sidecar_test.go pins that grammar.
func parseFileHashEntry(entry string) (hash string, size, mtimeUnixNano int64, ok bool) {
	parts := strings.Split(entry, fileHashEntrySep)
	if len(parts) < 3 || len(parts) > 5 {
		return entry, 0, 0, false // legacy bare hash
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return parts[0], 0, 0, false
	}
	mtime, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return parts[0], 0, 0, false
	}
	if len(parts) == 5 {
		// Strict grammar: a 5th field that is not a valid chunk set
		// downgrades the whole entry to legacy (full read+hash fallback)
		// instead of silently carrying an unusable set.
		if _, valid := parseChunkIndices(parts[4]); !valid {
			return parts[0], 0, 0, false
		}
	}
	return parts[0], size, mtime, true
}

// fileHashEntryChunkerFP returns the chunker-configuration fingerprint field
// of a sidecar entry, or "" when the entry has none (legacy bare hashes and
// intermediate "hash|size|mtime" entries written before the fingerprint
// existed). Callers treat "" as "configuration unknown — exempt from
// fingerprint-based staleness" rather than "default configuration".
func fileHashEntryChunkerFP(entry string) string {
	parts := strings.Split(entry, fileHashEntrySep)
	if len(parts) != 4 && len(parts) != 5 {
		return ""
	}
	return parts[3]
}

// chunkSetListPrefix marks the explicit-list form ("L:0,1,3") of the 5th
// sidecar field; a bare decimal N is the contiguous form (0..N-1).
const chunkSetListPrefix = "L:"

// maxChunkSetEntries bounds the number of indices parseChunkIndices may
// materialize. Legitimate sets are bounded by maxChunksPerFile (default
// 4000); the cap only stops a corrupt or hand-edited sidecar value (e.g.
// "9999999999") from turning a parse into an allocation bomb. Oversized or
// malformed values downgrade the entry to legacy, and callers fall back to
// enumerating the collection.
const maxChunkSetEntries = 1 << 20

// encodeChunkIndices serializes a set of committed chunk indices (the 5th
// sidecar field). A contiguous run 0..N-1 encodes as the bare count "N"
// (empty set → "0"); any gapped set — chunks missing because the
// poisoned-text fallback dropped them — encodes as "L:0,1,3" with the
// surviving indices in ascending order. The input order is irrelevant
// (encode sorts and de-duplicates); parseChunkIndices inverts it.
func encodeChunkIndices(indices []int) string {
	sorted := make([]int, len(indices))
	copy(sorted, indices)
	slices.Sort(sorted)
	// De-duplicate in place (sorted, so equal values are adjacent).
	uniq := sorted[:0]
	for i, v := range sorted {
		if i == 0 || v != uniq[len(uniq)-1] {
			uniq = append(uniq, v)
		}
	}
	// Ascending distinct non-negative values are exactly {0..len-1} iff the
	// maximum equals len-1 (a gap would push the max above len-1).
	if len(uniq) == 0 || uniq[len(uniq)-1] == len(uniq)-1 {
		return strconv.Itoa(len(uniq))
	}
	var b strings.Builder
	b.WriteString(chunkSetListPrefix)
	for i, v := range uniq {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(v))
	}
	return b.String()
}

// parseChunkIndices parses the 5th sidecar field in either encoded form
// (bare count "N" for a contiguous 0..N-1 set, "L:0,1,3" for a gapped set).
// ok is false for the empty string, non-numeric or negative values, an empty
// or malformed list, and sets above maxChunkSetEntries; callers must then
// treat the entry's chunk set as unknown.
func parseChunkIndices(field string) ([]int, bool) {
	if field == "" {
		return nil, false
	}
	if rest, ok := strings.CutPrefix(field, chunkSetListPrefix); ok {
		if rest == "" {
			return nil, false
		}
		parts := strings.Split(rest, ",")
		if len(parts) > maxChunkSetEntries {
			return nil, false
		}
		indices := make([]int, 0, len(parts))
		for _, p := range parts {
			v, err := strconv.Atoi(p)
			if err != nil || v < 0 {
				return nil, false
			}
			indices = append(indices, v)
		}
		return indices, true
	}
	n, err := strconv.Atoi(field)
	if err != nil || n < 0 || n > maxChunkSetEntries {
		return nil, false
	}
	indices := make([]int, n)
	for i := range indices {
		indices[i] = i
	}
	return indices, true
}

// chunkIndexFromID extracts the chunk index from a DocumentID
// ("<pathHash>:<idx>", see DocumentID). ok is false for IDs outside that
// grammar (hand-built test documents, foreign writers), in which case the
// file's committed chunk set must be treated as unknown.
func chunkIndexFromID(id string) (int, bool) {
	i := strings.LastIndexByte(id, ':')
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// fileHashEntryChunkSet returns the committed chunk-index set recorded in a
// sidecar entry's 5th field. ok is false for every older format (legacy bare
// hash, 3-field, 4-field) and for malformed values; callers must then
// enumerate the collection instead of deriving IDs arithmetically.
func fileHashEntryChunkSet(entry string) ([]int, bool) {
	parts := strings.Split(entry, fileHashEntrySep)
	if len(parts) != 5 {
		return nil, false
	}
	return parseChunkIndices(parts[4])
}

// fileHashEntryHash returns the content-hash component of a sidecar value
// regardless of whether the value is new-format or a legacy bare hash.
func fileHashEntryHash(entry string) string {
	if hash, _, _, ok := parseFileHashEntry(entry); ok {
		return hash
	}
	return entry
}

// ChunkerFingerprint returns a short stable fingerprint of the chunker
// configuration (max chunk size + overlap — the two ChunkerConfig inputs
// that determine how a file's content is split — plus the content-filter
// policy that decides whether a file is chunked at all). It is embedded as
// the 4th field of new sidecar entries so ValidateCollection can detect
// files whose chunks were produced under a different chunking configuration
// (vector_index.chunk_overlap / max_chunk_size / content_filter changes) and
// re-chunk them even though their content hash is unchanged. Including the
// content-filter fingerprint means a policy change also invalidates the
// stat fast-path for affected files, so previously-indexed files that the
// new policy excludes are re-read, re-evaluated, and their documents
// removed (they stop being "seen"); identical re-chunks then hit the
// content-addressed embedding cache instead of ONNX.
func ChunkerFingerprint(maxChunkSize, overlap int, contentPolicy string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("v2|max=%d|overlap=%d|filter=%s", maxChunkSize, overlap, contentPolicy)))
	return hex.EncodeToString(sum[:6]) // 12 hex chars, plenty for a config fingerprint
}

// fileHashEntryFromMetadata composes the sidecar value for a document.
// processFile stats every file it reads and records "file_size" and
// "file_mtime_unix_nano" metadata, so the preferred output is the new-format
// "hash|size|mtimeUnixNano|chunkerFP" entry that lets ValidateCollection
// classify the file as unchanged from stat alone (and detect chunker-config
// changes). chunkerFP is the active fingerprint the document was indexed
// under; pass "" for entries rebuilt from collection metadata whose chunker
// configuration is unknown (migration/fallback paths) — such entries are
// exempt from fingerprint-based staleness. chunkSet is the encoded set of
// committed chunk indices (see encodeChunkIndices; "" = set unknown — the
// entry then carries no 5th field and collectDocumentIDs falls back to the
// collection Query). The 5th field is only written when the 4th (chunkerFP)
// is present, keeping the field grammar positional. When the size/mtime
// metadata is absent — legacy documents written before the upgrade, or
// hand-built documents — the legacy bare-hash value is returned;
// ValidateCollection still honors it via the full read+hash comparison, and
// the entry is upgraded to the new format the next time the file is indexed.
func fileHashEntryFromMetadata(md map[string]string, chunkerFP, chunkSet string) string {
	hash := md["content_hash"]
	size, mtime := md["file_size"], md["file_mtime_unix_nano"]
	if hash == "" || size == "" || mtime == "" {
		return hash
	}
	entry := hash + fileHashEntrySep + size + fileHashEntrySep + mtime
	if chunkerFP != "" {
		entry += fileHashEntrySep + chunkerFP
		if chunkSet != "" {
			entry += fileHashEntrySep + chunkSet
		}
	}
	return entry
}

// fileHashInfo is a file's per-file indexing fact sheet: the content hash,
// size, and mtime recorded from the same stat+read that produced the file's
// chunks. These fields used to ride on every chunk's metadata map (read back
// from a "representative" chunk); they now travel explicitly from
// processFile to the sidecar write, and committed chromem documents carry
// only the four chunk-position metadata fields (see strippedForCommit).
// The sidecar JSON on disk is unchanged: fileHashEntryFromMetadata renders
// the exact same "hash|size|mtimeUnixNano[|chunkerFP[|chunkSet]]" value.
type fileHashInfo struct {
	filePath string
	hash     string
	size     string // file size in bytes, decimal string
	mtime    string // mtime in Unix nanoseconds, decimal string
}

// fileHashInfoFromMetadata extracts the per-file sidecar facts from a
// document's metadata map — the pre-upgrade carrier, still used by the
// standalone AddDocuments path whose callers pass documents with full
// metadata (the indexer pipeline and tests). ok is false when the map
// carries no content hash; such documents cannot produce a sidecar entry
// and are skipped (ValidateCollection then reconciles the file from disk).
func fileHashInfoFromMetadata(md map[string]string) (fileHashInfo, bool) {
	fp, hash := md["file_path"], md["content_hash"]
	if fp == "" || hash == "" {
		return fileHashInfo{}, false
	}
	return fileHashInfo{
		filePath: fp,
		hash:     hash,
		size:     md["file_size"],
		mtime:    md["file_mtime_unix_nano"],
	}, true
}

// entry renders the sidecar value for this file under the given chunker
// fingerprint and committed chunk-index set encoding (see
// fileHashEntryFromMetadata for the format).
func (info fileHashInfo) entry(chunkerFP, chunkSet string) string {
	return fileHashEntryFromMetadata(map[string]string{
		"content_hash":         info.hash,
		"file_size":            info.size,
		"file_mtime_unix_nano": info.mtime,
	}, chunkerFP, chunkSet)
}

// ValidateCollection checks stored file hashes against current files on disk.
// It returns lists of stale (modified), new, and deleted file paths.
// workspacePath is the root directory to walk for source files. checker is the
// ignore resolver (over .gitignore + .aiignore) the caller already built for
// this workspace; ValidateCollection reuses it so its walk applies exactly the
// same filtering as the Indexer's IndexFull walk (preventing reindex loops).
func (s *Service) ValidateCollection(ctx context.Context, workspacePath string, checker ignore.IgnoreChecker) (staleFiles, newFiles, deletedFiles []string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.current.collection == nil {
		return nil, nil, nil, errors.New("no collection available; call SwitchBranch first")
	}

	// Get stored file hashes from the collection.
	cacheStarted := time.Now()
	storedHashes, err := s.getCollectionFileHashes()
	s.telemetry.observe(StageCacheLookup, len(storedHashes), time.Since(cacheStarted))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getting collection file hashes: %w", err)
	}

	// Periodic full-hash revalidation: the stat fast-path below trusts
	// size+mtime as a content proxy, so a rewrite that preserves both would
	// otherwise escape detection until the file next really changes. Every
	// fullHashRevalidationEvery-th validation pass skips the fast-path
	// entirely and re-reads + re-hashes every file, catching such rewrites
	// eventually. The counter is atomic because this function runs under the
	// read lock; validation passes are serialized per indexer in practice,
	// so concurrent calls could at worst force the revalidation twice.
	forceFullHash := s.current.validationsSinceFullHash.Add(1) >= fullHashRevalidationEvery
	if forceFullHash {
		s.current.validationsSinceFullHash.Store(0)
	}

	// Track which stored files we've seen on disk.
	seen := make(map[string]bool, len(storedHashes))

	// Walk workspace files using the same filtering as walkProjectFiles.
	absRoot, absErr := filepath.Abs(workspacePath)
	if absErr != nil {
		return nil, nil, nil, fmt.Errorf("resolving workspace path: %w", absErr)
	}

	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkDirErr error) error {
		if walkDirErr != nil {
			return walkDirErr
		}

		if d.IsDir() {
			if isHiddenName(d.Name()) || checker.Ignored(path, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}

		if isHiddenName(d.Name()) || checker.Ignored(path, false) {
			return nil
		}

		absPath, pathErr := filepath.Abs(path)
		if pathErr != nil {
			return nil //nolint:nilerr // skip unresolvable paths
		}

		// Size guard: skip oversized files before the full os.ReadFile. Like
		// binary/empty files they are never added to the collection, so
		// excluding them here avoids an infinite reindex loop on every pass.
		// The NUL-byte binary check below cannot recognize every binary format
		// (ONNX/safetensors protobufs have readable headers), so the size
		// limit is the reliable backstop against loading a multi-hundred-MB
		// asset into memory just to hash it. The limit is configurable via
		// vector_index.max_file_size (default 4 MiB).
		info, infoErr := d.Info()
		if infoErr == nil && tooLargeForIndex(info.Size(), s.maxFileSize) {
			return nil //nolint:nilerr // skip oversized files
		}

		// Fast path (stat-based unchanged detection): when the sidecar entry
		// is current-format ("hash|size|mtimeUnixNano|chunkerFP") and the
		// size and mtime recorded at index time still match the file's
		// stat, the content is HEURISTICALLY the same bytes that were
		// indexed — same length and same modification time almost always
		// mean an untouched file, but a rewrite that preserves both (an
		// archive extraction restoring archived mtimes, some file-sync
		// tools) escapes detection here. The periodic full-hash
		// revalidation above catches such a rewrite within
		// fullHashRevalidationEvery passes instead of letting it survive
		// until the file next changes. The entry's chunker fingerprint
		// must also match the active chunker configuration; on a config
		// change the file needs re-chunking and falls through. The file
		// necessarily passed the oversized/binary/empty guards back then,
		// so we can mark it seen WITHOUT the header pre-read, the full
		// os.ReadFile, or the SHA-256 pass. This is what keeps the
		// 1s-debounced incremental pass (triggered after every file edit)
		// from re-reading and re-hashing the entire workspace each time.
		// Legacy bare-hash entries, fingerprint-less intermediate entries,
		// any size/mtime mismatch, a failed d.Info, or a forced full-hash
		// pass fall through to the full-read comparison below, which also
		// upgrades the entry the next time the file is indexed.
		if !forceFullHash && info != nil {
			if stored, exists := storedHashes[absPath]; exists {
				if _, size, mtime, ok := parseFileHashEntry(stored); ok &&
					size == info.Size() && mtime == info.ModTime().UnixNano() &&
					fileHashEntryChunkerFP(stored) == s.chunkerFingerprint {
					seen[absPath] = true
					return nil //nolint:nilerr // unchanged: skip content read + hash
				}
			}
		}

		// Bounded header pre-read: reject multi-GB binary assets before the
		// full os.ReadFile. Binary and empty files are never added to the
		// collection by processFile, so reporting them as "new" every pass
		// would cause an infinite reindexing loop.
		binary, bErr := isBinaryHeader(absPath)
		if bErr != nil {
			return nil //nolint:nilerr // skip unreadable files
		}
		if binary {
			return nil //nolint:nilerr // skip binary files
		}

		content, readErr := readFileFn(absPath)
		if readErr != nil {
			return nil //nolint:nilerr // skip unreadable files
		}
		if len(content) == 0 {
			return nil //nolint:nilerr // skip empty files
		}

		// Content policy: files the filter excludes are intentionally not
		// part of the collection. Returning without marking the file seen
		// means a previously-indexed file that the active policy now
		// excludes (e.g. content_filter.enabled flipped) is reported as
		// deleted so its documents and sidecar entry are removed; a
		// never-indexed excluded file never enters newFiles. The check
		// runs on the same raw bytes processFile filters, and the filter
		// config is part of the chunker fingerprint, so the stat fast-path
		// above only shortcuts files whose verdict cannot have changed.
		if _, skip := DetectContentSkip(content, s.contentFilter); skip {
			return nil //nolint:nilerr // excluded by content policy
		}

		currentHash := computeHash(content)

		if stored, exists := storedHashes[absPath]; exists {
			seen[absPath] = true
			if fileHashEntryHash(stored) != currentHash {
				staleFiles = append(staleFiles, absPath)
			} else if entryFP := fileHashEntryChunkerFP(stored); entryFP != "" && entryFP != s.chunkerFingerprint {
				// Content unchanged, but the stored chunks were produced
				// under a different chunker configuration (vector_index.
				// chunk_overlap / max_chunk_size changed since the file was
				// indexed): report stale so the file is re-chunked. Entries
				// without a fingerprint are exempt — their configuration is
				// unknown, and forcing a re-chunk of every legacy file in
				// one pass would cost a full re-embed for no user action.
				staleFiles = append(staleFiles, absPath)
			}
		} else {
			newFiles = append(newFiles, absPath)
		}

		return nil
	})
	if walkErr != nil {
		return nil, nil, nil, fmt.Errorf("walking workspace %s: %w", workspacePath, walkErr)
	}

	// Files in collection but not on disk are deleted.
	for storedPath := range storedHashes {
		if !seen[storedPath] {
			deletedFiles = append(deletedFiles, storedPath)
		}
	}

	return staleFiles, newFiles, deletedFiles, nil
}

// GetCollectionFiles returns all unique file paths and their sidecar entries
// (content hash, or "hash|size|mtimeUnixNano" for new-format entries) stored
// in the current collection.
func (s *Service) GetCollectionFiles() (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getCollectionFileHashes()
}

// getCollectionFileHashes returns the file→sidecar-entry map from the sidecar
// store, avoiding an embedding-bearing collection Query on every validation
// pass. Values are content hashes for legacy entries or
// "hash|size|mtimeUnixNano" for new-format entries (see parseFileHashEntry);
// callers that only need the hash should go through fileHashEntryHash.
// Caller must hold at least s.mu.RLock(). The returned map is a defensive
// copy: it may outlive the lock and must not race the in-place mutations done
// by upsertFileHashes/removeFileHashes under the write lock. The sidecar is
// loaded (or migrated) in SwitchBranch; if it is somehow nil, we fall back to
// a one-shot query.
func (s *Service) getCollectionFileHashes() (map[string]string, error) {
	if s.current.collection == nil {
		return nil, errors.New("no collection available")
	}
	if s.current.fileHashes != nil {
		out := make(map[string]string, len(s.current.fileHashes))
		for k, v := range s.current.fileHashes {
			out[k] = v
		}
		return out, nil
	}
	// Fallback (e.g. collection built before sidecar existed): enumerate via
	// Query. This may pay an embedding cost (when the embedding dimension is
	// unknown), so loadFileHashes populates the sidecar eagerly in
	// SwitchBranch to keep this path cold.
	return s.current.queryCollectionFileHashes(context.Background(), s.unitQueryVector())
}

// queryCollectionFileHashes enumerates stored file hashes directly from this
// state's chromem collection via a broad query. When unitVec is non-nil (a
// dimension-sized unit vector, see Service.unitQueryVector) the query runs
// through QueryEmbedding — no ONNX inference; nil keeps the embedding-bearing
// text Query (dimension unknown). Used only for the one-time sidecar
// migration (or the rare fallback). Caller must hold the owning Service's mu
// (at least RLock). ctx propagates cancellation to the underlying Query
// (e.g. service shutdown while the background migration is in flight).
func (ps *projectState) queryCollectionFileHashes(ctx context.Context, unitVec []float32) (map[string]string, error) {
	count := ps.collection.Count()
	if count == 0 {
		return make(map[string]string), nil
	}

	var results []chromem.Result
	var err error
	if unitVec != nil {
		results, err = ps.collection.QueryEmbedding(ctx, unitVec, count, nil, nil)
	} else {
		results, err = ps.collection.Query(ctx, " ", count, nil, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("querying collection for file list: %w", err)
	}

	fileHashes := make(map[string]string, len(results))
	for _, r := range results {
		fp := r.Metadata["file_path"]
		if fp == "" {
			continue
		}
		// Documents committed after the content/metadata strip carry no
		// content_hash at all — they cannot produce a sidecar entry. Skip
		// them: their entry is (or will be) written by the indexing write
		// path from the explicit per-file facts (see fileHashInfo), and a
		// fabricated empty entry would make ValidateCollection flag the
		// file forever.
		if r.Metadata["content_hash"] == "" {
			continue
		}
		// Keep one hash per file (all chunks of a file share the same hash
		// and size/mtime metadata, so composing from any chunk is fine).
		// The chunker configuration these chunks were produced under is
		// not recoverable from metadata, so no fingerprint is attached:
		// the entry stays exempt from fingerprint-based staleness until
		// the file is next indexed. The committed chunk-index set is not
		// recorded either — the 5th field is positional after the
		// fingerprint, so an entry without a fingerprint cannot carry
		// one; collectDocumentIDs falls back to the Query path for
		// migrated entries until the file's next index pass.
		fileHashes[fp] = fileHashEntryFromMetadata(r.Metadata, "", "")
	}

	return fileHashes, nil
}

// fileHashesPath returns the on-disk path of the sidecar for this state's
// branch, or "" if project/branch is unset. Caller must hold s.mu.
func (ps *projectState) fileHashesPath() string {
	if ps.projectPath == "" || ps.currentBranch == "" {
		return ""
	}
	return filepath.Join(ps.projectPath, "file_hashes_"+collectionName(ps.currentBranch)+".json")
}

// contentlessMarkerPath returns the on-disk path of the content-less
// migration marker for this state's branch — a zero-byte sentinel next to
// the branch's file-hash sidecar. Its presence means every sidecar-tracked
// document of this collection has been rewritten in the content-less commit
// shape (no chunk text, metadata narrowed to commitMetadataKeys — see
// strippedForCommit), so the one-time migration never runs again. Caller
// must hold s.mu.
func (ps *projectState) contentlessMarkerPath() string {
	if ps.projectPath == "" || ps.currentBranch == "" {
		return ""
	}
	return filepath.Join(ps.projectPath, "contentless_"+collectionName(ps.currentBranch)+".done")
}

// loadFileHashes populates the CURRENT state's fileHashes for the active
// branch from the sidecar on disk. If the sidecar is absent, the backfill is
// deferred to a short-lived background goroutine (see migrateFileHashes) so
// SwitchBranch never pays the embedding cost of enumerating the collection
// synchronously — that cost used to block the whole service for the duration
// of one ONNX inference on the upgrade / first-switch path. Caller must hold
// s.mu (write).
func (s *Service) loadFileHashes() {
	ps := s.current

	// Settle the content-less migration for whatever branch state the sidecar
	// load leaves behind: re-trigger it for the new branch (marker missing,
	// non-empty collection), write the marker synchronously for an empty
	// collection, or close the signal channel when the marker already exists
	// or the state is in-memory. Runs on every exit path below while the
	// caller still holds s.mu.
	defer s.maybeMigrateContentlessLocked()

	// Cancel any in-flight migration left over from a previous branch and
	// reset its signal channel. The branch's content-less migration is
	// abandoned the same way — its documents belong to the outgoing
	// collection, and the deferred trigger above re-evaluates it for the
	// incoming branch.
	if ps.migrationCancel != nil {
		ps.migrationCancel()
		ps.migrationCancel = nil
	}
	if ps.contentlessCancel != nil {
		ps.contentlessCancel()
		ps.contentlessCancel = nil
	}

	// Fast path: a usable sidecar exists on disk.
	if path := ps.fileHashesPath(); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			var m map[string]string
			if jsonErr := json.Unmarshal(data, &m); jsonErr == nil {
				ps.fileHashes = m
				ps.fileHashMigrationPending.Store(false)
				ps.migrationCh = closedChan()
				return
			}
		}
	}

	// An empty collection has nothing to migrate; IndexFull will populate the
	// sidecar via upsertFileHashes, so start empty and settled.
	if ps.collection == nil || ps.collection.Count() == 0 {
		ps.fileHashes = make(map[string]string)
		ps.fileHashMigrationPending.Store(false)
		ps.migrationCh = closedChan()
		return
	}

	// Non-empty collection with no sidecar (first run after the sidecar
	// upgrade, or a branch whose collection was built elsewhere). Defer the
	// single-embedding backfill to a background goroutine; IndexIncremental
	// waits on migrationCh before calling ValidateCollection, so the empty map
	// never causes a spurious full re-embed. The goroutine is bound to ps
	// (NOT re-read from s.current): if this state is parked or evicted before
	// the goroutine runs, it must settle ITS OWN flags rather than a newer
	// project's.
	ps.fileHashes = make(map[string]string)
	ps.fileHashMigrationPending.Store(true)
	done := make(chan struct{})
	ps.migrationCh = done
	branch := ps.currentBranch
	mctx, cancel := context.WithCancel(context.Background())
	ps.migrationCancel = cancel
	s.migrationWG.Add(1)
	go s.migrateFileHashes(mctx, ps, branch, done)
}

// migrateFileHashes is the background sidecar backfill: it enumerates the
// chromem collection once (a single embedding of the query vector) and adopts
// the result into ps.fileHashes. ps is the state that started the migration; if
// its ctx is cancelled (park / eviction / close) or its branch changes before
// completion, the result is discarded. Binding the migration to ps — rather
// than re-reading s.current — lets an orphaned migration settle its own flags
// without clobbering a newer project. The caller must NOT hold s.mu. Closes
// done when settled.
func (s *Service) migrateFileHashes(ctx context.Context, ps *projectState, branch string, done chan struct{}) {
	defer s.migrationWG.Done()
	defer close(done)

	s.mu.Lock()
	defer s.mu.Unlock()

	// clearIfCurrent clears the pending flag only when THIS migration is still
	// the one registered on ps. A restore may have re-run loadFileHashes and
	// installed a replacement migration (ps.migrationCh changed) while we were
	// blocked on the lock; clearing the flag unconditionally here would then
	// clobber the replacement's in-flight state and let parkCurrentLocked skip
	// a legitimate sidecar save.
	clearIfCurrent := func() {
		if ps.migrationCh == done {
			ps.fileHashMigrationPending.Store(false)
		}
	}

	// The branch may have changed (or the state been parked/closed) while we
	// waited for the write lock; abandon a stale migration rather than
	// overwriting another branch's sidecar.
	if ctx.Err() != nil || ps.currentBranch != branch || ps.collection == nil {
		clearIfCurrent()
		return
	}

	hashes, qErr := ps.queryCollectionFileHashes(ctx, s.unitQueryVector())
	if qErr != nil {
		s.logger.Warn("failed to migrate file-hash sidecar from collection", "error", qErr)
		clearIfCurrent()
		return
	}
	// Re-check after the embedding-bearing Query in case we raced a switch.
	if ctx.Err() != nil || ps.currentBranch != branch || ps.collection == nil {
		clearIfCurrent()
		return
	}
	ps.fileHashes = hashes
	ps.fileHashMigrationPending.Store(false)
	if err := ps.saveFileHashes(); err != nil {
		s.logger.Warn("failed to persist file-hash sidecar after migration", "error", err)
	}
	s.logger.Info("file-hash sidecar migrated from collection", "branch", branch, "files", len(hashes))
}

// WaitFileHashMigration blocks until any background sidecar migration for the
// current branch has settled. IndexIncremental uses it so ValidateCollection
// sees the full hash map instead of re-embedding every file. The caller must
// NOT hold s.mu.
func (s *Service) WaitFileHashMigration(ctx context.Context) error {
	ch := func() chan struct{} {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.current.migrationCh
	}()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// closedChan returns an already-closed signal channel.
func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// contentlessProbeGapTolerance bounds how many consecutive GetByID misses
// may end the chunk-index scan of a sidecar entry whose committed chunk-index
// set is unknown (legacy entries written without the 5th field). Chunks
// dropped by the poisoned-text fallback leave isolated holes in an otherwise
// contiguous run, so the scan bridges short gaps instead of stopping at the
// first miss.
const contentlessProbeGapTolerance = 8

// contentlessProbeChunkCap bounds the chunk indices probed for a sidecar
// entry with an unknown committed set. It mirrors the per-file chunk cap the
// indexer enforces; for real files the gap tolerance above stops the scan
// far earlier, so this is only a corrupt-sidecar backstop.
const contentlessProbeChunkCap = DefaultMaxChunksPerFile

// writeContentlessMarker best-effort writes the zero-byte migration marker.
// A failed write never fails the caller: it only costs one no-op probe pass
// on the next open. Caller must hold s.mu (the path derives from the current
// state's branch).
func (s *Service) writeContentlessMarker(path string) {
	if path == "" {
		return
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		s.logger.Warn("failed to write content-less migration marker",
			"path", path, "error", err)
	}
}

// maybeMigrateContentlessLocked settles the one-time content-less migration
// for the current branch: it starts the background rewrite when the
// collection is non-empty and the marker (see contentlessMarkerPath) is
// absent, writes the marker synchronously for an empty collection (every
// future commit is content-less by construction — see strippedForCommit — so
// a later full index must not pay a pointless probe pass), and closes the
// settle channel when there is nothing to do (marker present, or an
// in-memory state with no persistence). It runs at the end of loadFileHashes
// — i.e. on every branch switch and sidecar reload — and on the park-restore
// path of SetProject (where SwitchBranch early-returns and would otherwise
// never re-trigger a migration cancelled by the park). Caller must hold s.mu
// (write).
func (s *Service) maybeMigrateContentlessLocked() {
	ps := s.current

	// Abandon any in-flight migration from a previous evaluation of this or
	// another branch: its documents belong to the state it snapshotted.
	if ps.contentlessCancel != nil {
		ps.contentlessCancel()
		ps.contentlessCancel = nil
	}

	settle := func() {
		ps.contentlessPending.Store(false)
		ps.contentlessCh = closedChan()
	}

	marker := ps.contentlessMarkerPath()
	if marker == "" || ps.collection == nil {
		// In-memory state (no persistence) or no branch/collection: nothing
		// to migrate, ever, for this state.
		settle()
		return
	}
	if _, err := os.Stat(marker); err == nil {
		// Already migrated: never run again (this is the steady state for
		// every collection created after the content-less switch).
		settle()
		return
	} else if !errors.Is(err, fs.ErrNotExist) {
		// Unreadable marker (permissions, dangling symlink): treat as absent
		// and let the idempotent probe decide — worst case it strips nothing.
		s.logger.Warn("failed to stat content-less migration marker; treating as absent",
			"path", marker, "error", err)
	}

	if ps.collection.Count() == 0 {
		s.writeContentlessMarker(marker)
		settle()
		return
	}

	// Non-empty collection without a marker: defer the rewrite to a
	// background goroutine bound to ps (NOT re-read from s.current), exactly
	// like the file-hash backfill below it. The goroutine first waits for
	// the sidecar to settle, so a collection that still needs its file-hash
	// backfill is enumerated for the sidecar FIRST (its full per-file
	// metadata intact) and stripped afterwards.
	ps.contentlessPending.Store(true)
	done := make(chan struct{})
	ps.contentlessCh = done
	branch := ps.currentBranch
	mctx, cancel := context.WithCancel(context.Background())
	ps.contentlessCancel = cancel
	s.migrationWG.Add(1)
	go s.migrateContentless(mctx, ps, branch, done)
}

// migrateContentless is the one-time background rewrite of a legacy
// collection to the content-less commit shape: for every sidecar-tracked
// file it probes the stored chunks via GetByID (arithmetic over the entry's
// committed chunk-index set when present, gap-tolerant scan otherwise) and
// re-commits each document that still carries chunk text with the SAME ID,
// the SAME vector, metadata narrowed to commitMetadataKeys and Content=""
// (chromem replaces documents[doc.ID] atomically and overwrites the per-doc
// gob in place, so the collection stays queryable and consistent at every
// instant). On completion the marker is written and the freed memory is
// returned to the OS. ps is the state that started the migration; if its ctx
// is cancelled (park / eviction / close / branch switch) or its branch or
// collection changes before completion, the result is abandoned WITHOUT the
// marker — the next open replays the idempotent probe, skipping documents
// already stripped. The heavy phase (probing + window assembly) deliberately
// does NOT hold s.mu: chromem serializes collection access internally, so
// searches and switches keep running; only each bounded window flush takes
// the read lock, making it atomic against RebuildCollection / park /
// closeStateLocked (which hold the write lock and would otherwise race a
// flush with the collection delete). The caller must NOT hold s.mu. Closes
// done when settled.
func (s *Service) migrateContentless(ctx context.Context, ps *projectState, branch string, done chan struct{}) {
	defer s.migrationWG.Done()
	defer close(done)

	// The migration walks the sidecar, so it must not start before the
	// sidecar itself has settled: a deferred file-hash backfill (collection
	// without a sidecar file) enumerates each document's FULL metadata, and
	// stripping first would destroy the content_hash it feeds on. Capture
	// the channel under the read lock; a re-entrant loadFileHashes can only
	// swap it after cancelling our ctx, so the wait always terminates.
	if ch := func() chan struct{} {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return ps.migrationCh
	}(); ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return
		}
	}

	// Snapshot the sidecar entries and the collection pointer. The snapshot
	// is immutable for the rest of the pass; per-window re-checks below
	// verify the live state still matches it.
	s.mu.RLock()
	if ctx.Err() != nil || ps.currentBranch != branch || ps.collection == nil {
		s.mu.RUnlock()
		return
	}
	col := ps.collection
	entries := make(map[string]string, len(ps.fileHashes))
	for fp, entry := range ps.fileHashes {
		entries[fp] = entry
	}
	s.mu.RUnlock()

	if len(entries) == 0 {
		// Nothing sidecar-tracked (e.g. documents whose metadata never
		// carried content_hash are invisible to the sidecar by design): the
		// commit path owns every future document, so the marker still
		// applies.
		s.finishContentless(ctx, ps, branch, done, 0, 0)
		return
	}

	paths := make([]string, 0, len(entries))
	for fp := range entries {
		paths = append(paths, fp)
	}
	slices.Sort(paths) // deterministic replay and log output

	window := make([]chromem.Document, 0, embeddingCommitWindowSize)
	probed, stripped := 0, 0

	// flush re-commits one bounded window under the READ lock. Holding the
	// lock (not merely checking flags) makes every flush atomic against
	// RebuildCollection / parkCurrentLocked / closeStateLocked — a delete-
	// and-recreate can never interleave with a flush and resurrect ghost gob
	// files in a recreated collection directory. The chromem call runs on a
	// cancellation-stripped context: once the pre-checks pass inside the
	// critical section, the window must commit fully, or the marker would
	// certify a half-stripped collection. The error is context.Canceled when
	// the state went stale (silent abandon) or the wrapped commit error.
	flush := func() error {
		if len(window) == 0 {
			return nil
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		if ctx.Err() != nil || ps.currentBranch != branch || ps.collection != col {
			return context.Canceled
		}
		started := time.Now()
		err := col.AddDocuments(context.WithoutCancel(ctx), window, 1)
		s.telemetry.observe(StageChromemCommit, len(window), time.Since(started))
		if err != nil {
			return fmt.Errorf("re-committing %d stripped documents: %w", len(window), err)
		}
		stripped += len(window)
		window = window[:0]
		return nil
	}

	// add stages one stripped document, flushing a full window as it fills.
	add := func(doc chromem.Document) error {
		window = append(window, doc)
		if len(window) < embeddingCommitWindowSize {
			return nil
		}
		return flush()
	}

	// abandon settles the pending flag (only when THIS migration is still
	// registered on ps) and surfaces non-cancellation failures: a cancelled
	// or superseded migration is an expected lifecycle event, a genuine
	// commit error deserves a WARN. No marker is written either way, so the
	// next open replays the idempotent probe.
	abandon := func(err error) {
		s.mu.Lock()
		if ps.contentlessCh == done {
			ps.contentlessPending.Store(false)
		}
		silent := ctx.Err() != nil || ps.currentBranch != branch || ps.collection == nil
		s.mu.Unlock()
		if !silent {
			s.logger.Warn("content-less migration aborted; it will replay on the next open",
				"branch", branch, "error", err)
		}
	}

	for _, fp := range paths {
		if err := ctx.Err(); err != nil {
			return // cancelled (park/close/switch): retry on the next open
		}
		entry := entries[fp]

		// handle probes one stored chunk and stages it for stripping when
		// it still carries chunk text; documents committed by the current
		// (post-upgrade) code are already content-less and skipped for free.
		handle := func(i int) error {
			doc, err := col.GetByID(ctx, DocumentID(fp, i))
			if err != nil {
				// Not stored (deleted after the entry was written, or the
				// gap-tolerant scan ran past the file's chunk count): skip.
				return nil //nolint:nilerr // a probe miss is not an error
			}
			probed++
			if doc.Content == "" {
				return nil
			}
			doc.Metadata = narrowCommitMetadata(doc.Metadata)
			doc.Content = ""
			return add(doc)
		}

		if set, ok := fileHashEntryChunkSet(entry); ok {
			// Current-format entry: the committed chunk-index set is known,
			// probe exactly those indices — no misses, no guessing.
			for _, i := range set {
				if err := handle(i); err != nil {
					abandon(err)
					return
				}
			}
			continue
		}
		// Legacy entry (no 5th field): scan chunk indices 0.. upward,
		// bridging gaps up to contentlessProbeGapTolerance consecutive
		// misses (poison-dropped chunks) and bounded by
		// contentlessProbeChunkCap.
		misses := 0
		for i := 0; i < contentlessProbeChunkCap && misses < contentlessProbeGapTolerance; i++ {
			doc, err := col.GetByID(ctx, DocumentID(fp, i))
			if err != nil {
				misses++
				continue
			}
			misses = 0
			probed++
			if doc.Content == "" {
				continue
			}
			doc.Metadata = narrowCommitMetadata(doc.Metadata)
			doc.Content = ""
			if err := add(doc); err != nil {
				abandon(err)
				return
			}
		}
	}
	if err := flush(); err != nil {
		abandon(err)
		return
	}

	s.finishContentless(ctx, ps, branch, done, probed, stripped)
}

// finishContentless finalizes a completed migration: it clears the pending
// flag, re-verifies the state is still the one that started the migration,
// writes the marker, and schedules the freeOSMemory seam (the probe/strip
// cycle churns the whole collection's documents; the transient spike is
// handed back to the OS right away, off the service lock — same as the park
// eviction path). Caller must NOT hold s.mu.
func (s *Service) finishContentless(ctx context.Context, ps *projectState, branch string, done chan struct{}, probed, stripped int) {
	s.mu.Lock()
	if ps.contentlessCh == done {
		ps.contentlessPending.Store(false)
	}
	if ctx.Err() != nil || ps.currentBranch != branch || ps.collection == nil {
		s.mu.Unlock()
		return
	}
	s.writeContentlessMarker(ps.contentlessMarkerPath())
	s.mu.Unlock()

	s.logger.Info("content-less migration complete",
		"branch", branch, "probed", probed, "stripped", stripped)
	go freeOSMemory()
}

// WaitContentlessMigration blocks until the current branch's background
// content-less migration has settled. IndexIncremental uses it so its
// deletions and re-indexing never interleave with the migration's
// ID-preserving re-commits (which would otherwise resurrect just-deleted
// legacy documents). The caller must NOT hold s.mu.
func (s *Service) WaitContentlessMigration(ctx context.Context) error {
	ch := func() chan struct{} {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.current.contentlessCh
	}()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// saveFileHashes atomically writes this state's sidecar to disk. Caller must
// hold the owning Service's mu (write). Missing project/branch or a nil map is
// a no-op.
func (ps *projectState) saveFileHashes() error {
	path := ps.fileHashesPath()
	if path == "" || ps.fileHashes == nil {
		return nil
	}
	data, err := json.Marshal(ps.fileHashes)
	if err != nil {
		return fmt.Errorf("marshaling file hashes: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing file-hash sidecar: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming file-hash sidecar: %w", err)
	}
	return nil
}

// upsertFileHashesFiltered records file_path→sidecar entry for the given
// documents into the in-memory sidecar. The per-file facts (content hash,
// size, mtime) are extracted from each file's representative document
// metadata via fileHashInfoFromMetadata — the standalone AddDocuments path
// whose callers still pass full metadata — and rendered by
// composeFileHashEntryInfo as new-format
// "hash|size|mtimeUnixNano|chunkerFP|chunkSet" entries when the documents
// carry the size/mtime metadata processFile records, or the legacy bare
// hash otherwise (upgraded on the file's next index pass). Documents whose
// metadata carries no content hash are skipped: they cannot produce a
// meaningful entry, and ValidateCollection reconciles their files from
// disk. The committed chunk-index set (5th field) is derived from the
// documents' IDs ("<pathHash>:<idx>", see DocumentID); IDs in droppedIDs
// (the poisoned-text fallback) are excluded, so the set mirrors exactly the
// chunks the collection holds. A file whose every chunk was dropped still
// gets an entry (with the empty set "0") — otherwise ValidateCollection
// would report it as new on every pass and re-index it forever.
//
// It deliberately does NOT persist on every call: a
// single index pass issues one upsert per batch (addDocumentBatchSize docs),
// so persisting here would write the full map to disk N times per pass
// (O(batches × files) I/O). The map is flushed at lifecycle boundaries
// (SwitchBranch, SetProject, Rebuild, Close) and after migration. If the
// process crashes in between, the next SwitchBranch reads a slightly stale
// sidecar and ValidateCollection reconciles the diff against the persistent
// chromem collection. Caller must hold s.mu (write).
func (s *Service) upsertFileHashesFiltered(docs []chromem.Document, droppedIDs map[string]struct{}) {
	if s.current.fileHashes == nil {
		s.current.fileHashes = make(map[string]string)
	}
	type fileUpsert struct {
		info fileHashInfo // per-file facts (identical across a file's chunks)
		ids  []string     // IDs of the chunks that committed
	}
	perFile := make(map[string]*fileUpsert)
	for _, d := range docs {
		info, ok := fileHashInfoFromMetadata(d.Metadata)
		if !ok {
			continue
		}
		fu := perFile[info.filePath]
		if fu == nil {
			fu = &fileUpsert{info: info}
			perFile[info.filePath] = fu
		}
		if _, drop := droppedIDs[d.ID]; drop {
			continue
		}
		fu.ids = append(fu.ids, d.ID)
	}
	for fp, fu := range perFile {
		s.current.fileHashes[fp] = s.composeFileHashEntryInfo(fu.info, fu.ids)
	}
}

// upsertFileHashEntryInfo records ONE file's sidecar entry from its explicit
// per-file facts and the IDs of the chunks that actually committed (possibly
// none, when every chunk was poison-dropped). It is the single-file form of
// upsertFileHashesFiltered used by the streaming index accumulator's publish
// step. Caller must hold s.mu (write).
func (s *Service) upsertFileHashEntryInfo(info fileHashInfo, committedIDs []string) {
	if info.filePath == "" {
		return
	}
	if s.current.fileHashes == nil {
		s.current.fileHashes = make(map[string]string)
	}
	s.current.fileHashes[info.filePath] = s.composeFileHashEntryInfo(info, committedIDs)
}

// composeFileHashEntryInfo builds one file's sidecar value from its explicit
// per-file facts and the IDs of the chunks that committed. When every ID
// carries the DocumentID grammar, the 5th field records the committed index
// set (contiguous "N", gapped "L:0,1,3", empty "0" for a fully
// poison-dropped file); IDs outside that grammar (hand-built test documents,
// foreign writers) leave the set unknown — no 5th field, and
// collectDocumentIDs falls back to enumerating the collection.
func (s *Service) composeFileHashEntryInfo(info fileHashInfo, committedIDs []string) string {
	indices := make([]int, 0, len(committedIDs))
	known := true
	for _, id := range committedIDs {
		idx, ok := chunkIndexFromID(id)
		if !ok {
			known = false
			break
		}
		indices = append(indices, idx)
	}
	chunkSet := ""
	if known {
		chunkSet = encodeChunkIndices(indices)
	}
	return info.entry(s.chunkerFingerprint, chunkSet)
}

// removeFileHashes drops the given file paths from the in-memory sidecar. Like
// upsertFileHashes it does not persist per call; the map is flushed at lifecycle
// boundaries. Caller must hold s.mu (write).
func (s *Service) removeFileHashes(paths []string) {
	if s.current.fileHashes == nil || len(paths) == 0 {
		return
	}
	for _, p := range paths {
		delete(s.current.fileHashes, p)
	}
}

// RebuildCollection deletes the current branch collection and creates a fresh one.
// Caller must hold s.mu (write lock).
func (s *Service) RebuildCollection(ctx context.Context) error {
	if s.current.db == nil {
		return errors.New("no database initialized")
	}
	if s.current.currentBranch == "" {
		return errors.New("no branch set")
	}

	name := collectionName(s.current.currentBranch)

	if err := s.current.db.DeleteCollection(name); err != nil {
		s.logger.Warn("failed to delete collection during rebuild", "collection", name, "error", err)
	}

	col, err := s.current.db.GetOrCreateCollection(name, nil, s.embeddingFunc)
	if err != nil {
		return fmt.Errorf("creating fresh collection %q: %w", name, err)
	}
	s.current.collection = col
	// Reset the sidecar: a rebuilt collection is empty until re-indexed. Also
	// drop any in-flight migration: there is nothing left to backfill.
	if s.current.migrationCancel != nil {
		s.current.migrationCancel()
		s.current.migrationCancel = nil
	}
	// The in-flight content-less migration is abandoned the same way, and
	// the fresh collection gets the marker immediately: it is empty and
	// every future commit is content-less by construction, so reopening must
	// not probe it again.
	if s.current.contentlessCancel != nil {
		s.current.contentlessCancel()
		s.current.contentlessCancel = nil
	}
	s.current.contentlessPending.Store(false)
	s.current.contentlessCh = closedChan()
	s.writeContentlessMarker(s.current.contentlessMarkerPath())
	s.current.fileHashes = make(map[string]string)
	s.current.fileHashMigrationPending.Store(false)
	s.current.migrationCh = closedChan()
	if err := s.current.saveFileHashes(); err != nil {
		s.logger.Warn("failed to persist file-hash sidecar after rebuild", "error", err)
	}
	s.logger.Info("rebuilt collection", "branch", s.current.currentBranch, "collection", name)
	return nil
}

// BatchEmbedder embeds a batch of text documents in a single (internally
// chunked) inference pass. It is implemented by sp4rk's embedding.Embedder
// (EmbedDocuments); ServiceConfig.BatchEmbedder wires it into AddDocuments so
// documents are embedded BEFORE the chromem commit, letting chromem-go skip
// its per-document embedding calls (v0.7.0 AddDocument treats a pre-populated
// Document.Embedding as final and only normalizes + persists it). Query-side
// embedding keeps going through the chromem EmbeddingFunc.
type BatchEmbedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// commitMetadataKeys are the only metadata keys a committed chromem document
// retains — the four chunk-position fields content reconstruction and result
// shaping need (file_path, start_line, end_line, language). Every other key
// processFile records (content_hash, file_size, file_mtime_unix_nano,
// file_name, last_modified) is per-file bookkeeping that now lives only in
// the file-hash sidecar (see fileHashInfo).
var commitMetadataKeys = [4]string{"file_path", "start_line", "end_line", "language"}

// strippedForCommit returns the commit view of docs for chromem:
//
//   - Content is dropped from every document that carries a pre-populated
//     Embedding — the embedder already consumed the text, so persisting it
//     again would double the collection's disk footprint for zero readers
//     (content is reconstructed lazily from the source file; see
//     contentResolver). Documents WITHOUT an embedding keep their content:
//     chromem's AddDocument embeds via the document text on this legacy
//     path, and stripping it would embed the empty string.
//   - Metadata is narrowed to commitMetadataKeys unconditionally, so the
//     stored gob shape is identical on both paths.
//
// The input slice is left untouched: callers keep reading per-file metadata
// from their originals after the commit (upsertFileHashesFiltered derives
// the sidecar entries from them), and test assertions on the caller's docs
// stay valid. The returned documents share only the embedding slices.
func strippedForCommit(docs []chromem.Document) []chromem.Document {
	out := make([]chromem.Document, len(docs))
	for i, d := range docs {
		if len(d.Embedding) > 0 {
			d.Content = ""
		}
		d.Metadata = narrowCommitMetadata(d.Metadata)
		out[i] = d
	}
	return out
}

// narrowCommitMetadata copies md down to the commitMetadataKeys. A nil map
// stays nil (chromem handles nil metadata); unknown keys are dropped.
func narrowCommitMetadata(md map[string]string) map[string]string {
	if md == nil {
		return nil
	}
	narrowed := make(map[string]string, len(commitMetadataKeys))
	for _, k := range commitMetadataKeys {
		if v, ok := md[k]; ok {
			narrowed[k] = v
		}
	}
	return narrowed
}

// commitEmbeddedDocuments persists one bounded window of documents whose
// embeddings have already been populated and mirrors the same IDs into the
// lexical index. It deliberately does not update the file-hash sidecar: the
// streaming index accumulator publishes a file hash only after every chunk of
// that file has either committed or been deliberately dropped by the poisoned-
// text fallback.
// Caller must hold s.mu (write lock).
func (s *Service) commitEmbeddedDocuments(ctx context.Context, vecDocs []chromem.Document, lexDocs []lexical.Doc) error {
	if len(vecDocs) == 0 {
		return nil
	}
	if len(vecDocs) > embeddingCommitWindowSize {
		return fmt.Errorf("embedded commit window contains %d documents; limit is %d", len(vecDocs), embeddingCommitWindowSize)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("committing embedded documents: %w", err)
	}

	commitStarted := time.Now()
	commitErr := s.current.collection.AddDocuments(ctx, strippedForCommit(vecDocs), 1)
	s.telemetry.observe(StageChromemCommit, len(vecDocs), time.Since(commitStarted))
	if commitErr != nil {
		return fmt.Errorf("committing %d embedded documents: %w", len(vecDocs), commitErr)
	}

	if s.current.lexical != nil && len(lexDocs) > 0 {
		upsertStarted := time.Now()
		upsertErr := s.current.lexical.Upsert(ctx, lexDocs)
		s.telemetry.observe(StageBleveUpsert, len(lexDocs), time.Since(upsertStarted))
		if upsertErr != nil {
			s.logger.Warn("lexical upsert failed; will be repaired via RebuildLexical",
				"branch", s.current.currentBranch, "docs", len(lexDocs), "error", upsertErr)
		}
	}
	return nil
}

// AddDocuments adds documents to the current collection and mirrors them
// to the per-branch lexical index. Chromem commits first; lexical errors
// are logged but not returned, since the reconciliation loop in the
// manager will repair drift via RebuildLexical on the next project open.
//
// Oversized vecDocs are processed in fixed-size commit windows (at most
// embeddingCommitWindowSize each). This standalone API also embeds each
// window in configured-capacity chunks. The indexer uses documentAccumulator
// instead, carrying its inference tail across file and commit boundaries while
// preserving the same commit bound. Both paths check cancellation between
// bounded operations.
//
// When a BatchEmbedder is configured, each sub-batch is embedded up-front
// (see embedSubBatch) in chunks of at most the configured embedding batch
// size, and the vectors are assigned to Document.Embedding before the
// chromem call — chromem then performs zero embedding inferences and only
// normalizes + persists. A nil BatchEmbedder keeps the legacy path where
// chromem embeds each document individually.
//
// What chromem persists is the strippedForCommit view of each sub-batch:
// content is dropped from documents with a pre-populated embedding (the
// batch path) and metadata is narrowed to the four chunk-position fields on
// every path; the caller's docs slice is never mutated, so the sidecar
// upsert below still reads the full per-file metadata. See strippedForCommit
// and contentResolver for the reconstruction side.
// Caller must hold s.mu (write lock).
func (s *Service) AddDocuments(ctx context.Context, vecDocs []chromem.Document, lexDocs []lexical.Doc) error {
	if s.current.collection == nil {
		return errors.New("no collection available")
	}
	if len(vecDocs) == 0 && len(lexDocs) == 0 {
		return nil
	}

	// droppedIDs accumulates documents that could not be embedded even via
	// the per-text fallback (see embedSubBatch). They are excluded from the
	// chromem commit AND from the lexical upsert below so the two indexes
	// never diverge.
	var droppedIDs map[string]struct{}

	for start := 0; start < len(vecDocs); start += embeddingCommitWindowSize {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("adding documents (cancelled mid-batch): %w", err)
		}
		end := start + embeddingCommitWindowSize
		if end > len(vecDocs) {
			end = len(vecDocs)
		}
		sub := vecDocs[start:end]
		if s.batchEmbedder != nil {
			embedStarted := time.Now()
			kept, dropped, embErr := s.embedSubBatch(ctx, sub)
			s.telemetry.observe(StageEmbedding, len(sub), time.Since(embedStarted))
			if embErr != nil {
				return fmt.Errorf("embedding %d documents (offset %d of %d): %w", len(sub), start, len(vecDocs), embErr)
			}
			sub = kept
			if len(dropped) > 0 {
				if droppedIDs == nil {
					droppedIDs = make(map[string]struct{}, len(dropped))
				}
				for id := range dropped {
					droppedIDs[id] = struct{}{}
				}
			}
		}
		if len(sub) == 0 {
			continue
		}
		commitStarted := time.Now()
		commitErr := s.current.collection.AddDocuments(ctx, strippedForCommit(sub), 1)
		s.telemetry.observe(StageChromemCommit, len(sub), time.Since(commitStarted))
		if commitErr != nil {
			return fmt.Errorf("adding %d documents (offset %d of %d): %w", len(sub), start, len(vecDocs), commitErr)
		}
	}
	// Record the file-hash sidecar only after the full batch commits, not per
	// sub-batch: ValidateCollection reconciles a file solely by comparing its
	// on-disk hash to the sidecar hash (it never counts a file's chunks), so
	// recording the hash after a partial commit — some sub-batches succeeded
	// before a later one failed — would mark a half-indexed file as
	// up-to-date. Its still-missing chunks would never be re-embedded and the
	// file would be permanently under-indexed. Recording once, after every
	// sub-batch has committed, restores the original all-or-nothing semantics
	// while keeping embedding sub-batched. All chunks of a file share one
	// hash; upsert is idempotent. The 5th field records the committed
	// chunk-index set derived from the IDs, with dropped (poisoned) chunks
	// excluded.
	s.upsertFileHashesFiltered(vecDocs, droppedIDs)

	if s.current.lexical != nil && len(lexDocs) > 0 {
		if len(droppedIDs) > 0 {
			// Clamp the capacity at zero: today's callers pass lexDocs
			// mirroring vecDocs 1:1 (same IDs), so droppedIDs can never
			// outnumber lexDocs — but a negative make capacity panics, and
			// the invariant is not enforced at this boundary.
			filtered := make([]lexical.Doc, 0, max(len(lexDocs)-len(droppedIDs), 0))
			for _, d := range lexDocs {
				if _, drop := droppedIDs[d.ID]; drop {
					continue
				}
				filtered = append(filtered, d)
			}
			lexDocs = filtered
		}
		upsertStarted := time.Now()
		upsertErr := s.current.lexical.Upsert(ctx, lexDocs)
		s.telemetry.observe(StageBleveUpsert, len(lexDocs), time.Since(upsertStarted))
		if upsertErr != nil {
			s.logger.Warn("lexical upsert failed; will be repaired via RebuildLexical",
				"branch", s.current.currentBranch, "docs", len(lexDocs), "error", upsertErr)
		}
	}
	return nil
}

// embedSubBatch pre-populates the Embedding field of every document in sub
// that does not already carry one, so the subsequent chromem AddDocuments
// call performs zero embedding inferences and only normalizes + persists
// (chromem-go v0.7.0 AddDocument skips c.embed for a non-empty
// Document.Embedding). Documents that already have an embedding are passed
// through untouched — exactly as chromem would treat them on the legacy
// path.
//
// The collected texts are embedded in chunks of at most the configured
// embedding batch size (EmbeddingBatchSize, mirroring the embedder's ONNX
// batch session capacity): each EmbedDocuments call maps to at most one
// full batch inference. ctx is checked per chunk and forwarded to
// EmbedDocuments, so cancellation interrupts the sub-batch between chunks
// (the embedder itself is ctx-aware and aborts a pending chunk).
//
// Content-triggered failures are isolated rather than aborting the whole
// index pass: when a chunk fails as a unit, every text is retried on its
// own (see embedChunkPerText) and only the texts that also fail
// individually are dropped from the returned document set (each logged at
// WARN). This keeps one pathological chunk — e.g. content that trips a
// tokenizer bug — from permanently blocking indexing of everything batched
// with it. Systemic failures (every text in a chunk failing individually)
// still return an error: a broken embedder must abort the pass, not
// silently drop the entire batch.
//
// Returns the documents to commit (sub itself when nothing was dropped;
// the input slice is never reordered) and the IDs of dropped documents so
// the caller can exclude them from the lexical upsert as well.
// Caller must hold s.mu (write lock).
func (s *Service) embedSubBatch(ctx context.Context, sub []chromem.Document) ([]chromem.Document, map[string]struct{}, error) {
	texts := make([]string, 0, len(sub))
	targets := make([]int, 0, len(sub))
	for i := range sub {
		if len(sub[i].Embedding) == 0 {
			targets = append(targets, i)
			texts = append(texts, normalizeEmbeddingChunk(sub[i].Content))
		}
	}

	failedTargets := make(map[int]struct{}) // indices into targets/texts dropped via the per-text fallback
	vecs, failed, err := s.resolveEmbeddingChunk(ctx, texts)
	if err != nil {
		return nil, nil, err
	}
	for i, vec := range vecs {
		if _, drop := failed[i]; drop {
			failedTargets[i] = struct{}{}
			continue
		}
		if len(vec) == 0 {
			return nil, nil, fmt.Errorf("batch embedder returned an empty vector (text %d of %d)", i, len(texts))
		}
		sub[targets[i]].Embedding = vec
	}

	if len(failedTargets) == 0 {
		return sub, nil, nil
	}
	dropped := make(map[string]struct{}, len(failedTargets))
	dropIdx := make(map[int]struct{}, len(failedTargets))
	for k := range failedTargets {
		docIdx := targets[k]
		dropIdx[docIdx] = struct{}{}
		dropped[sub[docIdx].ID] = struct{}{}
		s.logger.Warn("dropping document whose embedding failed",
			"id", sub[docIdx].ID,
			"file_path", sub[docIdx].Metadata["file_path"])
	}
	kept := make([]chromem.Document, 0, len(sub)-len(dropIdx))
	for i := range sub {
		if _, drop := dropIdx[i]; drop {
			continue
		}
		kept = append(kept, sub[i])
	}
	return kept, dropped, nil
}

// resolveEmbeddingChunk fills one ordered chunk from the persistent cache and
// embeds only unique misses. The returned failed map is keyed by chunk-local
// index so duplicate poisoned chunks are dropped consistently.
func (s *Service) resolveEmbeddingChunk(ctx context.Context, texts []string) (vecs [][]float32, failed map[int]struct{}, err error) {
	vecs = make([][]float32, len(texts))
	positions := make(map[string][]int, len(texts))
	uniqueTexts := make([]string, 0, len(texts))
	for i, text := range texts {
		if _, exists := positions[text]; !exists {
			uniqueTexts = append(uniqueTexts, text)
		}
		positions[text] = append(positions[text], i)
	}

	misses := make([]string, 0, len(uniqueTexts))
	for _, text := range uniqueTexts {
		if s.current.embeddingCache != nil {
			vec, ok := s.current.embeddingCache.get(text)
			s.telemetry.observeEmbeddingCache(ok)
			if ok {
				for _, pos := range positions[text] {
					vecs[pos] = vec
				}
				continue
			}
		}
		misses = append(misses, text)
	}
	if len(misses) == 0 {
		return vecs, nil, nil
	}

	missVecs := make([][]float32, len(misses))
	failedMisses := make(map[int]struct{})
	for start := 0; start < len(misses); start += s.embeddingBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("embedding documents (cancelled mid-batch): %w", err)
		}
		end := start + s.embeddingBatchSize
		if end > len(misses) {
			end = len(misses)
		}
		chunk := misses[start:end]
		chunkVecs, err := s.batchEmbedder.EmbedDocuments(ctx, chunk)
		chunkFailed := make(map[int]struct{})
		if err != nil {
			chunkVecs, err = s.embedChunkPerText(ctx, chunk, 0, len(chunk), chunkFailed)
			if err != nil {
				return nil, nil, err
			}
		}
		if len(chunkVecs) != len(chunk) {
			return nil, nil, fmt.Errorf("batch embedder returned %d vectors for %d unique texts", len(chunkVecs), len(chunk))
		}
		copy(missVecs[start:end], chunkVecs)
		for local := range chunkFailed {
			failedMisses[start+local] = struct{}{}
		}
	}

	failed = make(map[int]struct{})
	for missIdx, text := range misses {
		if _, drop := failedMisses[missIdx]; drop {
			for _, pos := range positions[text] {
				failed[pos] = struct{}{}
			}
			continue
		}
		vec := missVecs[missIdx]
		if len(vec) == 0 {
			return nil, nil, fmt.Errorf("batch embedder returned an empty vector for unique text %d", missIdx)
		}
		if s.embeddingDimension > 0 && len(vec) != s.embeddingDimension {
			return nil, nil, fmt.Errorf("batch embedder returned dimension %d, want %d", len(vec), s.embeddingDimension)
		}
		s.current.embeddingCache.put(text, vec)
		for _, pos := range positions[text] {
			vecs[pos] = vec
		}
	}
	s.current.embeddingCache.prune()
	if len(failed) == 0 {
		failed = nil
	}
	return vecs, failed, nil
}

// embedChunkPerText isolates failures inside a chunk whose batched
// EmbedDocuments call failed: each text is embedded individually, texts
// that also fail alone are logged at WARN and recorded in failedTargets
// (keyed by their absolute index in the sub-batch's texts slice), and the
// successfully embedded texts keep their vectors. It returns an error only
// when EVERY text fails individually — that signature means the embedder
// itself is broken (it fails identically for any input), in which case the
// whole index pass must abort rather than silently drop the batch.
func (s *Service) embedChunkPerText(ctx context.Context, chunk []string, start, total int, failedTargets map[int]struct{}) ([][]float32, error) {
	vecs := make([][]float32, len(chunk))
	var firstErr error
	failures := 0
	for j, text := range chunk {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("embedding documents (cancelled mid-batch): %w", err)
		}
		v, err := s.batchEmbedder.EmbedDocuments(ctx, []string{text})
		if err != nil || len(v) != 1 || len(v[0]) == 0 {
			failures++
			textErr := err
			if textErr == nil {
				if len(v) != 1 {
					textErr = fmt.Errorf("embedder returned %d vectors for a single text", len(v))
				} else {
					textErr = errors.New("embedder returned an empty vector")
				}
			}
			if firstErr == nil {
				firstErr = textErr
			}
			s.logger.Warn("per-text embedding failed; document will be dropped",
				"chunk_offset", start+j, "total", total, "text_bytes", len(text), "error", textErr)
			failedTargets[start+j] = struct{}{}
			continue
		}
		vecs[j] = v[0]
	}
	if failures == len(chunk) {
		return nil, fmt.Errorf("embedding %d texts (chunk offset %d of %d): all texts failed individually too (first error: %w)",
			len(chunk), start, total, firstErr)
	}
	return vecs, nil
}

// DeleteDocumentsByIDs removes documents with the given IDs from the current
// chromem collection and from the lexical index (best-effort).
// Caller must hold s.mu (write lock).
func (s *Service) DeleteDocumentsByIDs(ctx context.Context, ids []string) error {
	if s.current.collection == nil {
		return errors.New("no collection available")
	}
	if len(ids) == 0 {
		return nil
	}

	if err := s.current.collection.Delete(ctx, nil, nil, ids...); err != nil {
		return fmt.Errorf("deleting %d documents: %w", len(ids), err)
	}

	if s.current.lexical != nil {
		if err := s.current.lexical.Delete(ctx, ids); err != nil {
			s.logger.Warn("lexical delete failed; will be repaired via RebuildLexical",
				"branch", s.current.currentBranch, "ids", len(ids), "error", err)
		}
	}
	return nil
}

// DocumentID returns a deterministic document ID for a file path and chunk index.
func DocumentID(filePath string, chunkIndex int) string {
	h := sha256.Sum256([]byte(filePath))
	pathHash := hex.EncodeToString(h[:8]) // first 8 bytes = 16 hex chars
	return fmt.Sprintf("%s:%d", pathHash, chunkIndex)
}

// sidecarDocumentIDs derives the chromem document IDs for the given file
// paths arithmetically from the sidecar's committed chunk-index sets:
// DocumentID is a pure function of path and chunk index, so a file whose
// entry carries its set (5th field) needs no collection Query — which would
// materialize every document in the collection, embedding vectors included.
// ok is false unless EVERY requested path has an entry carrying a known set
// (legacy entries, migrated collections, and hand-built documents lack it);
// the caller then falls back to the Query enumeration. The set mirrors the
// collection exactly: it is written only after the corresponding documents
// committed (or were deliberately poison-dropped) and removed together with
// the documents (removeFileHashes / RebuildCollection). Caller must hold at
// least s.mu.RLock(); the incremental path calls this under the write lock.
func (s *Service) sidecarDocumentIDs(filePaths []string) ([]string, bool) {
	if s.current.fileHashes == nil || len(filePaths) == 0 {
		return nil, false
	}
	var ids []string
	for _, p := range filePaths {
		entry, exists := s.current.fileHashes[p]
		if !exists {
			return nil, false
		}
		indices, ok := fileHashEntryChunkSet(entry)
		if !ok {
			return nil, false
		}
		for _, i := range indices {
			ids = append(ids, DocumentID(p, i))
		}
	}
	return ids, true
}

// collectionUniqueFileCount returns the number of unique files in the collection.
// Caller must hold at least s.mu (read or write). It reads the in-memory map
// length directly (no defensive copy) since it stays under the lock; only the
// rare nil-map case falls back to a query.
func (s *Service) collectionUniqueFileCount() int {
	if s.current.fileHashes != nil {
		return len(s.current.fileHashes)
	}
	hashes, err := s.getCollectionFileHashes()
	if err != nil {
		return 0
	}
	return len(hashes)
}

// computeHash returns the SHA-256 hex digest of the content.
func computeHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}
