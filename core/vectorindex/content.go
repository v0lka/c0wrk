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
//     path is read at most once, and a known-unreadable path is never
//     re-read.
//   - Lazy: nothing is read until content is actually requested.
//
// A contentResolver is NOT safe for concurrent use; each search call
// creates its own instance (the parallel vector/lexical queries in hybrid
// search complete before any filtering or hydration touches the resolver).
type contentResolver struct {
	maxFileSize int64
	// lines caches path → the file's content split on "\n" (line N is
	// lines[N-1]; a trailing newline yields a final "" element that line
	// ranges never select). A present-but-nil entry never occurs: paths
	// that fail to read land in failed instead.
	lines map[string][]string
	// failed caches paths whose bounded read returned an error (missing,
	// permission, directory, …). They resolve to the placeholder for the
	// resolver's whole lifetime.
	failed map[string]struct{}
}

// newContentResolver creates a resolver for one logical operation. A
// non-positive maxFileSize disables the read bound (tests with hand-built
// docs); the production wiring always passes the service's resolved
// DefaultMaxIndexableFileSize-or-configured bound.
func newContentResolver(maxFileSize int64) *contentResolver {
	return &contentResolver{
		maxFileSize: maxFileSize,
		lines:       make(map[string][]string),
		failed:      make(map[string]struct{}),
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
	return strings.Join(lines[startLine-1:endLine], "\n"), true
}

// fileLines returns the file's lines, reading (bounded) at most once per
// path per resolver lifetime. ok is false when the read fails; the failure
// is cached so repeated hits on the same dead path cost nothing.
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
	cr.lines[filePath] = lines
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
