package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
)

// SearchMode selects which backends are used for a hybrid search call.
type SearchMode string

const (
	// ModeHybrid queries the vector and lexical indices in parallel and
	// fuses their ranked lists with Reciprocal Rank Fusion (k=60).
	ModeHybrid SearchMode = "hybrid"
	// ModeVector queries only the vector (chromem) index; the result
	// scores are cosine similarities from chromem.
	ModeVector SearchMode = "vector"
	// ModeLexical queries only the lexical (bleve/BM25) index; the
	// result scores are BM25 scores from bleve.
	ModeLexical SearchMode = "lexical"
)

// rrfK is the Reciprocal Rank Fusion constant popularized by Cormack,
// Clarke and Buettcher (TREC 2009). 60 is the conventional default and
// is robust across a wide range of query and corpus sizes.
const rrfK = 60

// defaultHybridFanout is the minimum per-side fanout used when callers
// ask for a small TopK. A larger fanout on each side gives RRF more
// candidates to fuse, which materially improves recall at the cost of
// slightly more CPU.
const defaultHybridFanout = 100

// SearchOptions controls HybridSearch behavior.
//
// Query may contain `+token` sugar (e.g. "pattern +MatcherFactory") to
// append must-match tokens in addition to MustMatch.
type SearchOptions struct {
	Query       string
	TopK        int
	Mode        SearchMode
	FilePattern string
	MustMatch   []string
}

// ParseMode maps a mode string (from frontend or tool input) to a SearchMode.
// Unknown values (including empty string) map to ModeHybrid; the service
// auto-falls-back to vector when lexical is unavailable.
func ParseMode(s string) SearchMode {
	switch s {
	case string(ModeVector):
		return ModeVector
	case string(ModeLexical):
		return ModeLexical
	case string(ModeHybrid), "":
		return ModeHybrid
	default:
		return ModeHybrid
	}
}

// ParseQuery splits the raw user query into a base query (passed to both
// retrievers) and a list of `+token` must-match terms (applied as a
// post-filter on result content). Terms that are just a bare "+" or the
// empty string are ignored.
func ParseQuery(raw string) (base string, mustMatch []string) {
	fields := strings.Fields(raw)
	baseParts := make([]string, 0, len(fields))
	for _, f := range fields {
		if strings.HasPrefix(f, "+") && len(f) > 1 {
			mustMatch = append(mustMatch, f[1:])
			continue
		}
		baseParts = append(baseParts, f)
	}
	return strings.Join(baseParts, " "), mustMatch
}

// HybridSearch runs a hybrid vector + lexical search according to
// opts.Mode and fuses the two ranked lists with Reciprocal Rank Fusion.
//
// Auto-fallback rules:
//   - ModeHybrid with no/empty lexical index → degrades to ModeVector.
//   - ModeLexical with no/empty lexical index → returns an empty slice
//     (or an error if no lexical index has been opened at all).
//
// Blocks via WaitReady if the index is not yet ready.
func (s *Service) HybridSearch(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	if err := s.WaitReady(ctx); err != nil {
		return nil, fmt.Errorf("waiting for index readiness: %w", err)
	}

	mode := opts.Mode
	if mode == "" {
		mode = ModeHybrid
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = 10
	}

	baseQuery, sugarMust := ParseQuery(opts.Query)
	mustMatch := make([]string, 0, len(opts.MustMatch)+len(sugarMust))
	mustMatch = append(mustMatch, opts.MustMatch...)
	mustMatch = append(mustMatch, sugarMust...)

	s.mu.RLock()
	col := s.collection
	lex := s.lexical
	s.mu.RUnlock()

	if col == nil {
		return nil, errors.New("no collection available; call SetProject and SwitchBranch first")
	}
	if baseQuery == "" {
		return []SearchResult{}, nil
	}

	// Resolve effective mode with auto-fallback.
	effectiveMode := mode
	if mode == ModeHybrid || mode == ModeLexical {
		if lex == nil {
			if mode == ModeLexical {
				return nil, errors.New("lexical index not available")
			}
			effectiveMode = ModeVector
		} else {
			lexCount, countErr := lex.Count()
			if countErr != nil {
				s.logger.Warn("lexical count failed; falling back to vector", "error", countErr)
				if mode == ModeLexical {
					return nil, fmt.Errorf("lexical count: %w", countErr)
				}
				effectiveMode = ModeVector
			} else if lexCount == 0 {
				if mode == ModeLexical {
					return []SearchResult{}, nil
				}
				effectiveMode = ModeVector
			}
		}
	}

	// Dispatch to the appropriate path.
	switch effectiveMode {
	case ModeVector:
		return s.vectorOnlySearch(ctx, col, baseQuery, topK, opts.FilePattern, mustMatch)
	case ModeLexical:
		return s.lexicalOnlySearch(ctx, col, lex, baseQuery, topK, opts.FilePattern, mustMatch)
	case ModeHybrid:
		return s.hybridSearchRRF(ctx, col, lex, baseQuery, topK, opts.FilePattern, mustMatch)
	default:
		return nil, fmt.Errorf("unknown search mode %q", mode)
	}
}

// vectorOnlySearch runs a chromem-only query and returns results with
// their raw cosine similarity scores.
func (s *Service) vectorOnlySearch(
	ctx context.Context,
	col *chromem.Collection,
	query string,
	topK int,
	filePattern string,
	mustMatch []string,
) ([]SearchResult, error) {
	fanout := hybridFanout(topK)
	count := col.Count()
	if count == 0 {
		return []SearchResult{}, nil
	}
	if fanout > count {
		fanout = count
	}

	results, err := col.Query(ctx, query, fanout, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	out := make([]SearchResult, 0, len(results))
	rank := 0
	for _, r := range results {
		if !passesFilters(r, filePattern, mustMatch, s.logger) {
			continue
		}
		rank++
		sr := resultToSearchResult(r)
		sr.VectorScore = r.Similarity
		sr.VectorRank = rank
		out = append(out, sr)
	}
	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

// lexicalOnlySearch runs a bleve-only query, enriches each hit from
// chromem via GetByID, and returns results with their BM25 scores.
func (s *Service) lexicalOnlySearch(
	ctx context.Context,
	col *chromem.Collection,
	lex lexical.Index,
	query string,
	topK int,
	filePattern string,
	mustMatch []string,
) ([]SearchResult, error) {
	fanout := hybridFanout(topK)
	hits, err := lex.Query(ctx, query, fanout)
	if err != nil {
		return nil, fmt.Errorf("lexical search: %w", err)
	}

	out := make([]SearchResult, 0, len(hits))
	rank := 0
	for _, h := range hits {
		doc, getErr := col.GetByID(ctx, h.ID)
		if getErr != nil {
			// Drift between lexical and vector; reconciliation will
			// repair on next project open.
			s.logger.Debug("lexical hit missing in chromem", "id", h.ID, "error", getErr)
			continue
		}
		r := chromem.Result{ID: doc.ID, Metadata: doc.Metadata, Content: doc.Content}
		if !passesFilters(r, filePattern, mustMatch, s.logger) {
			continue
		}
		rank++
		sr := resultToSearchResult(r)
		sr.Score = h.Score
		sr.LexicalScore = h.Score
		sr.LexicalRank = rank
		out = append(out, sr)
	}
	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

// fusedEntry holds intermediate per-document data during Reciprocal Rank Fusion.
type fusedEntry struct {
	result       chromem.Result
	vectorScore  float32
	lexicalScore float32
	vectorRank   int
	lexicalRank  int
	score        float64
}

// hybridSearchRRF runs vector and lexical queries in parallel, fuses
// their ranked lists with Reciprocal Rank Fusion (k=60), and returns
// the top-K results after applying post-filters.
func (s *Service) hybridSearchRRF(
	ctx context.Context,
	col *chromem.Collection,
	lex lexical.Index,
	query string,
	topK int,
	filePattern string,
	mustMatch []string,
) ([]SearchResult, error) {
	fanout := hybridFanout(topK)

	vecCount := col.Count()
	vecFanout := fanout
	if vecFanout > vecCount {
		vecFanout = vecCount
	}

	var (
		vecResults []chromem.Result
		lexHits    []lexical.Hit
		vecErr     error
		lexErr     error
		wg         sync.WaitGroup
	)

	if vecFanout > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vecResults, vecErr = col.Query(ctx, query, vecFanout, nil, nil)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		lexHits, lexErr = lex.Query(ctx, query, fanout)
	}()
	wg.Wait()

	if vecErr != nil {
		return nil, fmt.Errorf("vector search: %w", vecErr)
	}
	if lexErr != nil {
		return nil, fmt.Errorf("lexical search: %w", lexErr)
	}

	// Apply per-side filters BEFORE fusing so the rank spaces are
	// comparable. The plan requires per-side MustMatch and glob filters
	// applied pre-fusion (see "Design invariants").
	agg := make(map[string]*fusedEntry, len(vecResults)+len(lexHits))

	aggregateVectorHits(agg, vecResults, filePattern, mustMatch, s.logger)
	aggregateLexicalHits(ctx, col, agg, lexHits, filePattern, mustMatch, s.logger)

	out := make([]SearchResult, 0, len(agg))
	for _, e := range agg {
		sr := resultToSearchResult(e.result)
		sr.Score = float32(e.score)
		sr.VectorScore = e.vectorScore
		sr.LexicalScore = e.lexicalScore
		sr.VectorRank = e.vectorRank
		sr.LexicalRank = e.lexicalRank
		out = append(out, sr)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})

	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

// aggregateVectorHits adds vector results into the RRF aggregation map.
// Filtered results are skipped; each contributing entry gets a vector
// rank and the corresponding RRF score component.
func aggregateVectorHits(
	agg map[string]*fusedEntry,
	vecResults []chromem.Result,
	filePattern string,
	mustMatch []string,
	logger *slog.Logger,
) {
	vecRank := 0
	for _, r := range vecResults {
		if !passesFilters(r, filePattern, mustMatch, logger) {
			continue
		}
		vecRank++
		entry := agg[r.ID]
		if entry == nil {
			entry = &fusedEntry{result: r}
			agg[r.ID] = entry
		}
		entry.vectorScore = r.Similarity
		entry.vectorRank = vecRank
		entry.score += 1.0 / float64(rrfK+vecRank)
	}
}

// aggregateLexicalHits adds lexical results into the RRF aggregation map.
// Hits missing from chromem are looked up via GetByID; hits that cannot be
// found or that fail post-filters are skipped.
func aggregateLexicalHits(
	ctx context.Context,
	col *chromem.Collection,
	agg map[string]*fusedEntry,
	lexHits []lexical.Hit,
	filePattern string,
	mustMatch []string,
	logger *slog.Logger,
) {
	lexRank := 0
	for _, h := range lexHits {
		entry := agg[h.ID]
		if entry == nil {
			doc, getErr := col.GetByID(ctx, h.ID)
			if getErr != nil {
				logger.Debug("lexical hit missing in chromem", "id", h.ID, "error", getErr)
				continue
			}
			r := chromem.Result{ID: doc.ID, Metadata: doc.Metadata, Content: doc.Content}
			if !passesFilters(r, filePattern, mustMatch, logger) {
				continue
			}
			entry = &fusedEntry{result: r}
			agg[h.ID] = entry
		}
		lexRank++
		entry.lexicalScore = h.Score
		entry.lexicalRank = lexRank
		entry.score += 1.0 / float64(rrfK+lexRank)
	}
}

// hybridFanout returns the per-side fanout for RRF. Using a larger
// candidate pool than topK significantly improves recall.
func hybridFanout(topK int) int {
	fanout := topK * 4
	if fanout < defaultHybridFanout {
		fanout = defaultHybridFanout
	}
	return fanout
}

// passesFilters returns true if the chromem result satisfies both the
// file-path glob filter (if any) and all must-match tokens.
func passesFilters(r chromem.Result, filePattern string, mustMatch []string, logger *slog.Logger) bool {
	if filePattern != "" {
		fp := r.Metadata["file_path"]
		matched, err := doublestar.Match(filePattern, fp)
		if err != nil {
			logger.Warn("invalid file filter pattern", "pattern", filePattern, "error", err)
			return false
		}
		if !matched {
			return false
		}
	}
	for _, tok := range mustMatch {
		if tok == "" {
			continue
		}
		if !strings.Contains(r.Content, tok) {
			return false
		}
	}
	return true
}
