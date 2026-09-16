package vectorindex

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// contentUnavailablePrefix marks the head of the placeholder string (see
// contentUnavailableFormat); tests and consumers match on it.
const contentUnavailablePrefix = "[content unavailable"

// contentUnavailableFormat is the placeholder returned in place of chunk
// content when the source file is missing or unreadable at reconstruction
// time. Reconstruction is best-effort by design: a deleted or unreadable
// file must never fail a search — the result set keeps the hit (its vector
// position is still valid) and the consumer sees an explicit marker instead
// of silently empty content.
const contentUnavailableFormat = contentUnavailablePrefix + ": source file missing or unreadable: %s]"

// contentUnavailablePlaceholder renders the reconstruction-failure marker
// for a file path.
func contentUnavailablePlaceholder(filePath string) string {
	return fmt.Sprintf(contentUnavailableFormat, filePath)
}

// contentResolver lazily reconstructs chunk content from the source files
// on disk. chromem documents no longer persist chunk text (only ID, vector,
// and the four chunk-position metadata fields — see strippedForCommit), so
// every read side that needs the actual text (must-match filtering, final
// top-K hydration, the lexical backfill) reconstructs it from the file the
// chunk's file_path / start_line / end_line metadata points at.
//
// Cost control:
//   - Bounded read: each file is read at most maxFileSize bytes (the same
//     bound the indexer applies before indexing a file at all).
//   - Per-call cache: one resolver instance serves one logical operation
//     (one search call, one lexical rebuild pass); within that lifetime each
//     path is read at most once while the cache budget lasts, and a
//     known-unreadable path is never re-read. The retained byte total is
//     capped (contentResolverCacheBudgetBytes), so a large candidate fanout
//     cannot pin unbounded memory.
//   - Lazy: nothing is read until content is actually requested.
//
// A contentResolver is NOT safe for concurrent use; each search call
// creates its own instance (the parallel vector/lexical queries in hybrid
// search complete before any filtering or hydration touches the resolver).
// contentResolverCacheBudgetBytes bounds the total source bytes a single
// resolver retains (see contentResolver.cachedBytes). A must-match filter
// reconstructs content for every pre-fusion candidate, and after the
// content-less migration every candidate has empty stored content, so the
// per-call cache could otherwise retain the whole candidate fanout (up to
// hybridFanout topK files, each ≤ max_file_size). Past the budget the resolver
// stops caching (a later request for a path re-reads it) rather than growing
// without bound; correctness is unaffected.
const contentResolverCacheBudgetBytes int64 = 8 << 20

// contentReconstructionTruncationMarker terminates a reconstructed chunk that
// exceeded the reconstruction bound (see contentResolver.maxChunkSize).
const contentReconstructionTruncationMarker = "…"

type contentResolver struct {
	maxFileSize int64
	// maxChunkSize is the configured vector_index.max_chunk_size. A chunk's
	// stored text never exceeds it, so a reconstruction longer than a small
	// multiple of it is the line-boundary overshoot of a fixed-size split on
	// a very long line (a minified asset / one-line JSON, where every chunk's
	// start_line == end_line == 1): the whole line, up to max_file_size. The
	// reconstruction is capped at 2× maxChunkSize so a hit stays proportional
	// to a chunk. 0 disables the cap (tests with hand-built docs).
	maxChunkSize int
	// lines caches path → the file's content split on "\n" (line N is
	// lines[N-1]; a trailing newline yields a final "" element that line
	// ranges never select). A present-but-nil entry never occurs: paths
	// that fail to read land in failed instead.
	lines map[string][]string
	// failed caches paths whose bounded read returned an error (missing,
	// permission, directory, …). They resolve to the placeholder for the
	// resolver's whole lifetime.
	failed map[string]struct{}
	// cachedBytes is the running total of source bytes currently retained in
	// lines; once it reaches cacheBudgetBytes the resolver stops caching.
	cachedBytes int64
	// cacheBudgetBytes is the retention cap (contentResolverCacheBudgetBytes
	// in production).
	cacheBudgetBytes int64
}

// newContentResolver creates a resolver for one logical operation. A
// non-positive maxFileSize disables the read bound and a non-positive
// maxChunkSize disables the reconstruction cap (both for tests with hand-built
// docs); the production wiring passes the service's resolved
// DefaultMaxIndexableFileSize-or-configured bound and its max_chunk_size.
func newContentResolver(maxFileSize int64, maxChunkSize int) *contentResolver {
	return &contentResolver{
		maxFileSize:      maxFileSize,
		maxChunkSize:     maxChunkSize,
		lines:            make(map[string][]string),
		failed:           make(map[string]struct{}),
		cacheBudgetBytes: contentResolverCacheBudgetBytes,
	}
}

// chunkContent reconstructs the 1-based inclusive [startLine, endLine] line
// range of filePath. It never returns an error: a missing or unreadable
// file yields the explicit placeholder (see contentUnavailableFormat), and
// line ranges past the file's end are clamped to what the file holds.
func (cr *contentResolver) chunkContent(filePath string, startLine, endLine int) string {
	content, ok := cr.chunkContentOK(filePath, startLine, endLine)
	if !ok {
		return contentUnavailablePlaceholder(filePath)
	}
	return content
}

// chunkContentOK is the error-returning form used by callers that must not
// persist placeholders (the lexical backfill skips such documents instead of
// indexing the marker text). ok is false when the file cannot be read at all.
func (cr *contentResolver) chunkContentOK(filePath string, startLine, endLine int) (string, bool) {
	if filePath == "" {
		return "", false
	}
	lines, ok := cr.fileLines(filePath)
	if !ok || len(lines) == 0 {
		return "", false
	}
	if startLine < 1 {
		startLine = 1
	}
	if endLine < startLine {
		endLine = startLine
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) {
		return "", false
	}
	content := strings.Join(lines[startLine-1:endLine], "\n")
	// A fixed-size split landing mid-line makes the line range a superset of
	// the embedded chunk, and for a single-line file (start == end == 1) the
	// "range" is the whole line — up to max_file_size. Bound the result so a
	// search hit stays proportional to a chunk (see maxChunkSize).
	if cr.maxChunkSize > 0 {
		content = truncateRunes(content, 2*cr.maxChunkSize)
	}
	return content, true
}

// truncateRunes returns s truncated to at most limit runes, appending the
// reconstruction truncation marker when anything was dropped. It never splits
// a rune. A non-positive limit is a no-op.
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	n := 0
	for i := range s {
		if n == limit {
			return s[:i] + contentReconstructionTruncationMarker
		}
		n++
	}
	return s
}

// fileLines returns the file's lines, reading (bounded) at most once per
// path per resolver lifetime while the cache budget lasts. ok is false when
// the read fails; the failure is cached so repeated hits on the same dead
// path cost nothing.
func (cr *contentResolver) fileLines(filePath string) ([]string, bool) {
	if lines, cached := cr.lines[filePath]; cached {
		return lines, true
	}
	if _, dead := cr.failed[filePath]; dead {
		return nil, false
	}
	data, err := readBounded(filePath, cr.maxFileSize)
	if err != nil {
		cr.failed[filePath] = struct{}{}
		return nil, false
	}
	lines := strings.Split(string(data), "\n")
	// Retain the read only while the running total stays under the budget;
	// past it the bytes are used for this request and dropped, so a large
	// must-match candidate fanout cannot pin hundreds of MB per search.
	if cr.cachedBytes+int64(len(data)) <= cr.cacheBudgetBytes {
		cr.lines[filePath] = lines
		cr.cachedBytes += int64(len(data))
	}
	return lines, true
}

// readBounded reads at most limit bytes of filePath. A non-positive limit
// reads the whole file (matching the historic os.ReadFile behavior used by
// every caller that predates the bound).
func readBounded(filePath string, limit int64) ([]byte, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if limit <= 0 {
		return io.ReadAll(f)
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// hydrateSearchContent fills the Content field of every result whose stored
// content is empty (documents committed by the embedding-batched path —
// see strippedForCommit). Results that already carry content (documents
// committed by the legacy chromem-embedding path, which retains chunk text)
// are left untouched: their stored text is authoritative for what was
// actually embedded. Only the final, trimmed top-K list is hydrated, so the
// reconstruction cost is bounded by topK distinct files per search.
func hydrateSearchContent(results []SearchResult, cr *contentResolver) {
	for i := range results {
		if results[i].Content == "" {
			results[i].Content = cr.chunkContent(results[i].FilePath, results[i].StartLine, results[i].EndLine)
		}
	}
}
