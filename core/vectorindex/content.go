package vectorindex

import (
	"fmt"
	"io"
	"strings"

	"github.com/v0lka/sp4rk/safeio"
)

// contentUnavailablePrefix marks the head of the placeholder string (see
// contentUnavailableFormat); tests and consumers match on it.
const contentUnavailablePrefix = "[content unavailable"

// contentUnavailableFormat is the placeholder returned in place of chunk
// content when the source file is missing or unreadable at reconstruction
// time. Reconstruction is best-effort by contract (ADR-049): a dead file
// must never fail a search — the hit stays, the consumer sees an explicit
// marker instead of silently empty content. See
// specs/decisions/049-vector-index-memory-reduction.md.
const contentUnavailableFormat = contentUnavailablePrefix + ": source file missing or unreadable: %s]"

// contentUnavailablePlaceholder renders the reconstruction-failure marker
// for a file path.
func contentUnavailablePlaceholder(filePath string) string {
	return fmt.Sprintf(contentUnavailableFormat, filePath)
}

// contentResolver lazily reconstructs chunk content from source files.
// Committed documents carry no text (see strippedForCommit, ADR-049), so
// every read side that needs text (must-match filtering, final top-K
// hydration, the lexical backfill) rebuilds it from the file_path /
// start_line / end_line metadata: bounded reads, one resolver per logical
// operation, nothing read until requested. NOT safe for concurrent use —
// each search call makes its own (the parallel hybrid queries complete
// before any filtering or hydration touches the resolver). See
// specs/decisions/049-vector-index-memory-reduction.md.
//
// contentResolverCacheBudgetBytes bounds the total source bytes one resolver
// retains. A must-match filter reconstructs every pre-fusion candidate, so
// the per-call cache could otherwise pin the whole candidate fanout; past
// the budget it stops caching (a later request re-reads), correctness
// unaffected.
const contentResolverCacheBudgetBytes int64 = 8 << 20

// contentReconstructionTruncationMarker terminates a reconstructed chunk that
// exceeded the reconstruction bound (see contentResolver.maxChunkSize).
const contentReconstructionTruncationMarker = "…"

type contentResolver struct {
	maxFileSize int64
	// maxChunkSize is the configured vector_index.max_chunk_size. Stored chunk
	// text never exceeds it, so a longer reconstruction is the line-boundary
	// overshoot of a fixed-size split on a very long line (a minified asset:
	// start_line == end_line == 1, the whole line up to max_file_size); the
	// 2× cap keeps a hit proportional to a chunk. 0 disables the cap (tests
	// with hand-built docs).
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
	// lines; at cacheBudgetBytes the resolver stops caching (see
	// contentResolverCacheBudgetBytes).
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

// chunkContent reconstructs the 1-based inclusive [startLine, endLine] range
// of filePath; it never errors — a missing or unreadable file yields the
// placeholder, and out-of-range lines are clamped to what the file holds.
func (cr *contentResolver) chunkContent(filePath string, startLine, endLine int) string {
	content, ok := cr.chunkContentOK(filePath, startLine, endLine)
	if !ok {
		return contentUnavailablePlaceholder(filePath)
	}
	return content
}

// chunkContentOK is the error-returning form for callers that must not
// persist placeholders (the lexical backfill skips such documents instead of
// indexing the marker text); ok is false when the file cannot be read at all.
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
	// the chunk (see maxChunkSize).
	if cr.maxChunkSize > 0 {
		content = truncateRunes(content, 2*cr.maxChunkSize)
	}
	return content, true
}

// truncateRunes returns s truncated to at most limit runes, appending the
// reconstruction truncation marker when anything was dropped; never splits
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

// fileLines returns the file's lines, reading (bounded) at most once per path
// per resolver lifetime while the budget lasts; failures are cached, so
// repeated hits on a dead path cost nothing.
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
	// Retain only while under the budget; past it the bytes serve this
	// request and are dropped.
	if cr.cachedBytes+int64(len(data)) <= cr.cacheBudgetBytes {
		cr.lines[filePath] = lines
		cr.cachedBytes += int64(len(data))
	}
	return lines, true
}

// readBounded reads at most limit bytes of filePath; a non-positive limit
// reads the whole file (the historic os.ReadFile behavior of every caller
// predating the bound). safeio refuses a non-regular source (a planted
// FIFO/socket/device) instead of blocking the open — and the pass — forever.
func readBounded(filePath string, limit int64) ([]byte, error) {
	f, err := safeio.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if limit <= 0 {
		return io.ReadAll(f)
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// hydrateSearchContent fills Content for results whose stored content is
// empty (documents committed by the embedding-batched path — see
// strippedForCommit); legacy-stored text stays authoritative for what was
// actually embedded. Only the final trimmed top-K list is hydrated, bounding
// reconstruction to topK distinct files per search.
func hydrateSearchContent(results []SearchResult, cr *contentResolver) {
	for i := range results {
		if results[i].Content == "" {
			results[i].Content = cr.chunkContent(results[i].FilePath, results[i].StartLine, results[i].EndLine)
		}
	}
}
