package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
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

// defaultFanoutMultiplier scales topK into a per-side candidate pool.
const defaultFanoutMultiplier = 4

// defaultVectorScoreRatio is the relative cosine cutoff below which
// vector hits are discarded before fusion. A hit survives when its
// similarity is at least this fraction of the top similarity in the
// (post-filter) vector result set. 0 disables the relative cutoff.
const defaultVectorScoreRatio = 0.25

// defaultLexicalScoreRatio is the relative BM25 cutoff below which
// lexical hits are discarded before fusion. BM25 magnitudes vary widely
// across queries, so a relative threshold is more robust than an
// absolute one. 0 disables the cutoff.
const defaultLexicalScoreRatio = 0.1

// HybridConfig holds tunable parameters for Reciprocal Rank Fusion.
//
// Zero-valued integer fields (RRFK, FanoutMultiplier, FanoutMin) fall
// back to built-in defaults via ResolveHybridConfig. Zero-valued
// threshold fields mean "disabled" — every hit passes the score gate —
// so callers that construct a zero HybridConfig (e.g. tests) get no
// score filtering. Production config sets non-zero thresholds via
// config.VectorIndexConfig.
type HybridConfig struct {
	// RRFK is the RRF constant k (default 60).
	RRFK int
	// FanoutMultiplier scales topK into the per-side candidate pool
	// (default 4). The effective fanout is max(topK*FanoutMultiplier,
	// FanoutMin).
	FanoutMultiplier int
	// FanoutMin is the minimum per-side fanout (default 100).
	FanoutMin int
	// VectorScoreFloor is an absolute cosine-similarity floor. Hits
	// with similarity below this value are discarded before fusion.
	// 0 disables the absolute floor.
	VectorScoreFloor float64
	// VectorScoreRatio is a relative cosine cutoff: a hit is discarded
	// when its similarity is below VectorScoreRatio × top similarity
	// among the post-filter vector hits. 0 disables the relative
	// cutoff. This suppresses noise-tail hits that are weakly semantic
	// yet still receive an RRF contribution.
	VectorScoreRatio float64
	// LexicalScoreRatio is a relative BM25 cutoff: a hit is discarded
	// when its BM25 score is below LexicalScoreRatio × top BM25 among
	// the post-filter lexical hits. 0 disables the cutoff.
	LexicalScoreRatio float64
}

// DefaultHybridConfig returns the built-in default HybridConfig. The
// threshold defaults (VectorScoreRatio, LexicalScoreRatio) are enabled
// to suppress noise-tail promotion in RRF; set them to 0 to disable.
func DefaultHybridConfig() HybridConfig {
	return HybridConfig{
		RRFK:              rrfK,
		FanoutMultiplier:  defaultFanoutMultiplier,
		FanoutMin:         defaultHybridFanout,
		VectorScoreFloor:  0,
		VectorScoreRatio:  defaultVectorScoreRatio,
		LexicalScoreRatio: defaultLexicalScoreRatio,
	}
}

// ResolveHybridConfig fills zero-valued integer fields with built-in
// defaults. Threshold fields are returned as-is: 0 means "disabled".
func ResolveHybridConfig(hc HybridConfig) HybridConfig {
	if hc.RRFK == 0 {
		hc.RRFK = rrfK
	}
	if hc.FanoutMultiplier == 0 {
		hc.FanoutMultiplier = defaultFanoutMultiplier
	}
	if hc.FanoutMin == 0 {
		hc.FanoutMin = defaultHybridFanout
	}
	return hc
}

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
	fanout := hybridFanout(topK, s.hybridConfig)
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
	fanout := hybridFanout(topK, s.hybridConfig)
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
	fanout := hybridFanout(topK, s.hybridConfig)

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

	aggregateVectorHits(agg, vecResults, filePattern, mustMatch, s.hybridConfig, s.logger)
	aggregateLexicalHits(ctx, col, agg, lexHits, filePattern, mustMatch, s.hybridConfig, s.logger)

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
//
// Two passes keep rank spaces comparable while suppressing noise:
//  1. Apply path/mustmatch filters and collect survivors with the top
//     similarity among them.
//  2. Apply the score floor (the stricter of VectorScoreFloor and
//     VectorScoreRatio × top similarity), assign 1-based ranks over the
//     scored survivors, and add the RRF contribution 1/(RRFK+rank).
//
// Hits that fail the score gate are never inserted into agg, so they
// receive no RRF contribution from the vector side. This prevents weak
// tail hits that also appear lexically from earning a double RRF boost.
func aggregateVectorHits(
	agg map[string]*fusedEntry,
	vecResults []chromem.Result,
	filePattern string,
	mustMatch []string,
	hc HybridConfig,
	logger *slog.Logger,
) {
	// First pass: path/mustmatch filter, collect survivors + top sim.
	survivors := make([]chromem.Result, 0, len(vecResults))
	var maxSim float32
	for _, r := range vecResults {
		if !passesFilters(r, filePattern, mustMatch, logger) {
			continue
		}
		survivors = append(survivors, r)
		if r.Similarity > maxSim {
			maxSim = r.Similarity
		}
	}

	// Second pass: score gate, rank, aggregate.
	vecRank := 0
	for _, r := range survivors {
		if !passesVectorScoreGate(r.Similarity, maxSim, hc) {
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
		entry.score += 1.0 / float64(hc.RRFK+vecRank)
	}
}

// aggregateLexicalHits adds lexical results into the RRF aggregation map.
//
// Two passes, mirroring aggregateVectorHits:
//  1. Resolve each hit via chromem.GetByID (creating a fusedEntry when
//     the document is not already present), apply path/must-match
//     filters, and collect survivors with their BM25 scores; track the
//     maximum BM25 among survivors.
//  2. Apply the relative BM25 ratio gate and assign a 1-based lexical
//     rank to each surviving hit, adding its RRF contribution.
//
// Hits missing from chromem or failing filters are skipped. Hits below
// the score gate are skipped so noise-tail documents that merely contain
// a query term cannot receive an RRF contribution. When the threshold is
// zero the gate is a no-op.
func aggregateLexicalHits(
	ctx context.Context,
	col *chromem.Collection,
	agg map[string]*fusedEntry,
	lexHits []lexical.Hit,
	filePattern string,
	mustMatch []string,
	hc HybridConfig,
	logger *slog.Logger,
) {
	type survivor struct {
		h       lexical.Hit
		entry   *fusedEntry
		created bool
	}
	survivors := make([]survivor, 0, len(lexHits))
	var maxScore float32
	for _, h := range lexHits {
		entry := agg[h.ID]
		created := false
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
			created = true
		}
		survivors = append(survivors, survivor{h: h, entry: entry, created: created})
		if h.Score > maxScore {
			maxScore = h.Score
		}
	}

	lexRank := 0
	for _, sv := range survivors {
		if !passesLexicalScoreGate(sv.h.Score, maxScore, hc) {
			if sv.created {
				delete(agg, sv.h.ID)
			}
			continue
		}
		lexRank++
		sv.entry.lexicalScore = sv.h.Score
		sv.entry.lexicalRank = lexRank
		sv.entry.score += 1.0 / float64(hc.RRFK+lexRank)
	}
}

// hybridFanout returns the per-side fanout for RRF. Using a larger
// candidate pool than topK significantly improves recall. The pool size
// is max(topK*FanoutMultiplier, FanoutMin) from the resolved HybridConfig.
func hybridFanout(topK int, hc HybridConfig) int {
	fanout := topK * hc.FanoutMultiplier
	if fanout < hc.FanoutMin {
		fanout = hc.FanoutMin
	}
	return fanout
}

// matchFilePathPattern tests whether a file path matches a glob pattern.
// It tries multiple strategies to handle the common case where file_path is
// stored as an absolute path but the user enters a relative pattern like
// "*.md" or "src/**":
//   - Direct match (handles "**/*.go", absolute patterns)
//   - "**/" prefix (handles "*.go" → "**/*.go", "src/**" → "**/src/**")
//   - Basename match (handles "*.go" against the file's basename)
//
// This mirrors the frontend pathFilter logic (picomatch with contains:true
// plus basename testing) so the sidebar file-pattern filter behaves
// consistently with the file-tree filter.
func matchFilePathPattern(pattern, filePath string) bool {
	// Normalize to forward slashes for doublestar (which always uses /).
	fp := filepath.ToSlash(filePath)

	// Direct match — handles **/*.go, /absolute/path, etc.
	if matched, _ := doublestar.Match(pattern, fp); matched {
		return true
	}
	// Try with **/ prefix for relative patterns — this makes "*.md" match
	// files at any directory depth, and "src/**" match any src/ directory.
	if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**/") {
		if matched, _ := doublestar.Match("**/"+pattern, fp); matched {
			return true
		}
	}
	// Try matching against the basename — this is a fallback for patterns
	// like "*.md" that should match the file's name regardless of path.
	if base := filepath.Base(fp); base != fp {
		if matched, _ := doublestar.Match(pattern, base); matched {
			return true
		}
	}
	return false
}

// passesFilters returns true if the chromem result satisfies both the
// file-path glob filter (if any) and all must-match tokens.
func passesFilters(r chromem.Result, filePattern string, mustMatch []string, logger *slog.Logger) bool {
	if filePattern != "" {
		fp := r.Metadata["file_path"]
		if !matchFilePathPattern(filePattern, fp) {
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

// passesVectorScoreGate reports whether a vector hit with similarity
// sim survives the pre-fusion score gate given the top similarity
// maxSim among the post-filter vector hits.
//
// A hit is rejected when its similarity is below the absolute floor
// (VectorScoreFloor) OR below the relative cutoff
// (VectorScoreRatio × maxSim). When both thresholds are zero the gate
// is disabled and every hit passes. A zero maxSim disables the relative
// cutoff (avoids rejecting everything when the vector list is
// degenerate).
func passesVectorScoreGate(sim, maxSim float32, hc HybridConfig) bool {
	if hc.VectorScoreFloor == 0 && hc.VectorScoreRatio == 0 {
		return true
	}
	if hc.VectorScoreFloor > 0 && float64(sim) < hc.VectorScoreFloor {
		return false
	}
	if hc.VectorScoreRatio > 0 && maxSim > 0 && float64(sim) < hc.VectorScoreRatio*float64(maxSim) {
		return false
	}
	return true
}

// passesLexicalScoreGate reports whether a lexical hit with BM25 score
// score survives the pre-fusion score gate given the top BM25 maxScore
// among the post-filter lexical hits.
//
// A hit is rejected when its score is below the relative cutoff
// (LexicalScoreRatio × maxScore). When the threshold is zero the gate
// is disabled and every hit passes. A zero maxScore disables the gate
// (avoids rejecting everything when the lexical list is degenerate).
func passesLexicalScoreGate(score, maxScore float32, hc HybridConfig) bool {
	if hc.LexicalScoreRatio == 0 {
		return true
	}
	if maxScore <= 0 {
		return true
	}
	return float64(score) >= hc.LexicalScoreRatio*float64(maxScore)
}
