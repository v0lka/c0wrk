package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
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

// defaultVectorScoreRatio is the relative cosine cutoff below which vector
// hits are discarded before fusion (a hit survives at ≥ this fraction of the
// top post-filter similarity; 0 disables it). See specs/domains/workspace.md.
const defaultVectorScoreRatio = 0.25

// defaultLexicalScoreRatio is the relative BM25 cutoff below which lexical
// hits are discarded before fusion (0 disables it); a relative threshold is
// more robust than an absolute one because BM25 magnitudes vary by query.
const defaultLexicalScoreRatio = 0.1

// HybridConfig holds the Reciprocal Rank Fusion tunables. Zero integer
// fields (RRFK, FanoutMultiplier, FanoutMin) fall back to built-in defaults
// via ResolveHybridConfig; zero thresholds mean "disabled" — every hit
// passes the gate — so a zero HybridConfig (tests) does no score filtering.
// Production sets thresholds via config.VectorIndexConfig.
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

// DefaultHybridConfig returns the built-in HybridConfig; the thresholds are
// enabled to suppress noise-tail promotion in RRF (0 disables a threshold).
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

// HybridSearch runs a hybrid vector + lexical search per opts.Mode and fuses
// the ranked lists with Reciprocal Rank Fusion; it blocks via WaitReady.
// Auto-fallback (see specs/domains/workspace.md): ModeHybrid degrades to
// ModeVector when the lexical index is empty/unavailable; ModeLexical
// returns an empty slice (or an error when no lexical index was ever
// opened).
func (s *Service) HybridSearch(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	return s.hybridSearch(ctx, opts, true)
}

// HybridSearchNoWait never waits for readiness: it returns ErrNotReady
// immediately when the index is not ready. It is the form for callers that
// gate readiness themselves, so an incremental pass starting between their
// gate and the search cannot block them until the pass finishes.
func (s *Service) HybridSearchNoWait(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	return s.hybridSearch(ctx, opts, false)
}

// hybridSearch implements HybridSearch; wait selects the blocking (WaitReady)
// or non-blocking (ErrNotReady) readiness gate. See the public wrappers for
// the contracts.
func (s *Service) hybridSearch(ctx context.Context, opts SearchOptions, wait bool) ([]SearchResult, error) {
	if wait {
		if err := s.WaitReady(ctx); err != nil {
			return nil, fmt.Errorf("waiting for index readiness: %w", err)
		}
	} else if !s.IsReady() {
		return nil, ErrNotReady
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

	// Hold the read lock across the whole search, including every lexical
	// query: bleveIndex has no internal synchronization, and its Close (taken
	// under the write lock by a switch, park eviction or Service.Close) nils
	// the underlying index, so a pointer used after release races the close.
	// Mirrors browseWithFilter; the chromem collection rides the same hold.
	s.mu.RLock()
	defer s.mu.RUnlock()

	col := s.current.collection
	lex := s.current.lexical

	if col == nil {
		return nil, errors.New("no collection available; call SetProject and SwitchBranch first")
	}
	if baseQuery == "" {
		return []SearchResult{}, nil
	}

	// Resolve effective mode with auto-fallback.
	//
	// ADR-104 view model: the lexical side of a search is the UNION of the
	// overlay's lexical index and the shared corpus's read-only lexical leg
	// (lexicalOnlySearch / hybridSearchRRF). A clean tree serves EVERYTHING
	// from the corpus, so an empty overlay lexical index must NOT disable
	// the lexical side or degrade hybrid to vector-only — the fallback fires
	// only when BOTH legs are absent/empty.
	effectiveMode := mode
	if mode == ModeHybrid || mode == ModeLexical {
		overlayCount := uint64(0)
		var overlayCountErr error
		if lex != nil {
			overlayCount, overlayCountErr = lex.Count()
			if overlayCountErr != nil {
				s.logger.Warn("lexical count failed; treating the overlay lexical leg as empty", "error", overlayCountErr)
			}
		}
		corpusLexReady := false
		var corpusCountErr error
		if cv := s.current.corpus; cv != nil && cv.lexical != nil {
			var n uint64
			n, corpusCountErr = cv.lexical.Count()
			if corpusCountErr == nil && n > 0 {
				corpusLexReady = true
			} else if corpusCountErr != nil {
				// The asymmetric twin of the overlay leg's Warn above: a
				// transient DocCount failure is never silently folded into
				// "not ready" — it is logged, and ModeLexical fails closed
				// with it below when the corpus leg was the lexical side.
				s.logger.Warn("corpus lexical count failed; treating the corpus lexical leg as absent", "error", corpusCountErr)
			}
		}
		switch {
		case lex == nil && !corpusLexReady:
			if mode == ModeLexical {
				return nil, errors.New("lexical index not available")
			}
			effectiveMode = ModeVector
		case overlayCountErr != nil && !corpusLexReady:
			// Both lexical legs are unusable: lexical-only fails closed and
			// hybrid degrades to vector (both count failures are
			// Warn-logged above).
			if mode == ModeLexical {
				return nil, fmt.Errorf("lexical count: %w", overlayCountErr)
			}
			effectiveMode = ModeVector
		case overlayCountErr != nil:
			// Keep hybrid and lexical-only alive on the corpus lexical leg;
			// the overlay leg is GENUINELY skipped — lex = nil reaches the
			// search functions, so the broken overlay index is never
			// queried (the comment and the code now agree).
			lex = nil
		case overlayCount == 0 && !corpusLexReady:
			if mode == ModeLexical {
				if corpusCountErr != nil {
					// A clean overlay makes the corpus leg the whole
					// lexical side: its transient count failure must not
					// masquerade as an authoritative empty success.
					return nil, fmt.Errorf("corpus lexical count: %w", corpusCountErr)
				}
				return []SearchResult{}, nil
			}
			effectiveMode = ModeVector
		}
	}

	// One content resolver per search call: must-match filtering and final
	// top-K hydration reconstruct chunk text from the source files (stored
	// documents carry no content — see strippedForCommit), with a per-call
	// path cache shared by both retrieval sides.
	resolver := newContentResolver(s.maxFileSize, s.maxChunkSize)

	// Dispatch to the appropriate path.
	switch effectiveMode {
	case ModeVector:
		return s.vectorOnlySearch(ctx, col, baseQuery, topK, opts.FilePattern, mustMatch, resolver)
	case ModeLexical:
		return s.lexicalOnlySearch(ctx, col, lex, baseQuery, topK, opts.FilePattern, mustMatch, resolver)
	case ModeHybrid:
		return s.hybridSearchRRF(ctx, col, lex, baseQuery, topK, opts.FilePattern, mustMatch, resolver)
	default:
		return nil, fmt.Errorf("unknown search mode %q", mode)
	}
}

// vectorOnlySearch runs a chromem-only query over the root's view — its
// overlay plus the visible slice of the shared corpus (ADR-104 stage 2) —
// and returns results with their raw cosine similarity scores. Corpus
// candidates are visibility-filtered per root BEFORE the merged ranking and
// the top-K trim, so the rank space sees one per-root world. A diverged
// path is invisible in the corpus and served by the overlay — but a stale
// overlay duplicate (e.g. a retirement that outlived a transient failure)
// can share an ID with a visible corpus entry, so the merge dedups by ID
// with the corpus version (the root's current bytes) winning.
func (s *Service) vectorOnlySearch(
	ctx context.Context,
	col *chromem.Collection,
	query string,
	topK int,
	filePattern string,
	mustMatch []string,
	resolver *contentResolver,
) ([]SearchResult, error) {
	fanout := hybridFanout(topK, s.hybridConfig)
	count := col.Count()
	if count == 0 && s.current.corpus == nil {
		return []SearchResult{}, nil
	}

	corpusResults, err := s.corpusVectorCandidates(ctx, query, fanout, filePattern, mustMatch, resolver)
	if err != nil {
		return nil, err
	}
	// Same-ID dedup across the two legs (they share the DocumentID space):
	// the corpus entry carries the root's CURRENT bytes (its visibility
	// gate matched the sidecar hash), so a stale overlay duplicate — e.g.
	// one left behind by a failed retirement — is dropped, not double-ranked.
	merged := make([]chromem.Result, 0, fanout)
	seen := make(map[string]struct{}, len(corpusResults)+count)
	for _, r := range corpusResults {
		seen[r.ID] = struct{}{}
		merged = append(merged, r)
	}
	if count > 0 {
		vecFanout := fanout
		if vecFanout > count {
			vecFanout = count
		}
		results, err := col.Query(ctx, query, vecFanout, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("vector search: %w", err)
		}
		for _, r := range results {
			if !passesFilters(r, filePattern, mustMatch, s.current.workspaceRoot, resolver, s.logger) {
				continue
			}
			if _, dup := seen[r.ID]; dup {
				continue
			}
			seen[r.ID] = struct{}{}
			merged = append(merged, r)
		}
	}

	// Rank the merged candidate list by similarity BEFORE the trim — the
	// final top-K must see every pre-fusion survivor of the view.
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Similarity > merged[j].Similarity
	})
	if len(merged) > topK {
		merged = merged[:topK]
	}

	out := make([]SearchResult, 0, len(merged))
	for i, r := range merged {
		sr := resultToSearchResult(r, s.current.workspaceRoot)
		sr.VectorScore = r.Similarity
		sr.VectorRank = i + 1
		out = append(out, sr)
	}
	hydrateSearchContent(out, resolver)
	return out, nil
}

// lexicalOnlySearch runs a bleve-only query over the root's view — its
// overlay lexical index plus the visible slice of the shared corpus's
// read-only lexical index (ADR-104 stage 2) — enriches each hit from the
// matching chromem collection (overlay hits from the overlay, corpus hits
// from the corpus), merges the candidates by BM25 score BEFORE the top-K
// trim, and returns results with their scores. Corpus legibility: BM25
// statistics are corpus-shape-dependent (ADR-104 §3), so the two indices'
// scores are not strictly comparable; the merged ranking is the pragmatic
// stage-2 union and the RRF path fuses ranks, which are score-order-stable.
func (s *Service) lexicalOnlySearch(
	ctx context.Context,
	col *chromem.Collection,
	lex lexical.Index,
	query string,
	topK int,
	filePattern string,
	mustMatch []string,
	resolver *contentResolver,
) ([]SearchResult, error) {
	fanout := hybridFanout(topK, s.hybridConfig)

	type mergedHit struct {
		result chromem.Result
		score  float32
	}

	corpusHits, err := s.corpusLexicalCandidates(ctx, query, fanout, filePattern, mustMatch, resolver)
	if err != nil {
		return nil, err
	}
	// Same-ID dedup across the two legs (they share the DocumentID space):
	// the corpus hit carries the root's CURRENT bytes (visibility-gated),
	// so a stale overlay duplicate is dropped, not double-ranked.
	merged := make([]mergedHit, 0, fanout)
	seen := make(map[string]struct{}, len(corpusHits)+fanout)
	for _, c := range corpusHits {
		seen[c.doc.ID] = struct{}{}
		merged = append(merged, mergedHit{
			result: chromem.Result{ID: c.doc.ID, Metadata: c.doc.Metadata, Content: c.doc.Content},
			score:  c.score,
		})
	}

	if lex != nil {
		hits, err := lex.Query(ctx, query, fanout)
		if err != nil {
			return nil, fmt.Errorf("lexical search: %w", err)
		}
		for _, h := range hits {
			doc, getErr := col.GetByID(ctx, h.ID)
			if getErr != nil {
				// Drift between lexical and vector; reconciliation will
				// repair on next project open.
				s.logger.Debug("lexical hit missing in chromem", "id", h.ID, "error", getErr)
				continue
			}
			r := chromem.Result{ID: doc.ID, Metadata: doc.Metadata, Content: doc.Content}
			if !passesFilters(r, filePattern, mustMatch, s.current.workspaceRoot, resolver, s.logger) {
				continue
			}
			if _, dup := seen[doc.ID]; dup {
				continue
			}
			seen[doc.ID] = struct{}{}
			merged = append(merged, mergedHit{result: r, score: h.Score})
		}
	}

	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].score > merged[j].score
	})
	if len(merged) > topK {
		merged = merged[:topK]
	}

	out := make([]SearchResult, 0, len(merged))
	for i, m := range merged {
		sr := resultToSearchResult(m.result, s.current.workspaceRoot)
		sr.Score = m.score
		sr.LexicalScore = m.score
		sr.LexicalRank = i + 1
		out = append(out, sr)
	}
	hydrateSearchContent(out, resolver)
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

// hybridSearchRRF runs vector and lexical queries in parallel, fuses their
// ranked lists with Reciprocal Rank Fusion (k=60), and returns the top-K
// results after pre-fusion filters and score gates. See specs/domains/workspace.md.
func (s *Service) hybridSearchRRF(
	ctx context.Context,
	col *chromem.Collection,
	lex lexical.Index,
	query string,
	topK int,
	filePattern string,
	mustMatch []string,
	resolver *contentResolver,
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
		corpusVec  []chromem.Result
		corpusLex  []corpusLexicalHit
		vecErr     error
		lexErr     error
		cVecErr    error
		cLexErr    error
		wg         sync.WaitGroup
	)

	if vecFanout > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vecResults, vecErr = col.Query(ctx, query, vecFanout, nil, nil)
		}()
	}
	if lex != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lexHits, lexErr = lex.Query(ctx, query, fanout)
		}()
	}
	// The corpus legs (ADR-104 stage 2): the shared read-only handle serves
	// both sides; candidates arrive visibility-filtered per root, so the
	// fusion below only ever sees this root's view.
	if cv := s.current.corpus; cv != nil {
		wg.Add(2)
		go func() {
			defer wg.Done()
			corpusVec, cVecErr = s.corpusVectorCandidates(ctx, query, fanout, filePattern, mustMatch, resolver)
		}()
		go func() {
			defer wg.Done()
			corpusLex, cLexErr = s.corpusLexicalCandidates(ctx, query, fanout, filePattern, mustMatch, resolver)
		}()
	}
	wg.Wait()

	if vecErr != nil {
		return nil, fmt.Errorf("vector search: %w", vecErr)
	}
	if lexErr != nil {
		return nil, fmt.Errorf("lexical search: %w", lexErr)
	}
	if cVecErr != nil {
		return nil, cVecErr
	}
	if cLexErr != nil {
		return nil, cLexErr
	}

	// Per-side filters apply BEFORE fusing so the rank spaces are comparable
	// (see specs/domains/workspace.md). The vector candidates of both stores
	// merge into ONE similarity-ordered list (cosine scores share one
	// embedding space), so RRF ranks reflect the view, not the store split.
	// Same-ID dedup across the two legs (they share the DocumentID space),
	// mirroring vectorOnlySearch: the corpus entry carries the root's CURRENT
	// bytes (its visibility gate matched the sidecar hash), so a stale
	// overlay duplicate — e.g. a retirement that outlived a transient
	// failure — is dropped, not double-ranked (a second occurrence would
	// earn a second vector-side RRF contribution and corrupt the fusion).
	vecAll := make([]chromem.Result, 0, len(vecResults)+len(corpusVec))
	seenVec := make(map[string]struct{}, len(corpusVec)+len(vecResults))
	for _, r := range corpusVec {
		seenVec[r.ID] = struct{}{}
		vecAll = append(vecAll, r)
	}
	for _, r := range vecResults {
		if _, dup := seenVec[r.ID]; dup {
			continue
		}
		seenVec[r.ID] = struct{}{}
		vecAll = append(vecAll, r)
	}
	sort.SliceStable(vecAll, func(i, j int) bool {
		return vecAll[i].Similarity > vecAll[j].Similarity
	})

	agg := make(map[string]*fusedEntry, len(vecAll)+len(lexHits)+len(corpusLex))

	aggregateVectorHits(agg, vecAll, filePattern, mustMatch, s.current.workspaceRoot, s.hybridConfig, s.logger, resolver)
	aggregateLexicalHits(ctx, col, agg, lexHits, filePattern, mustMatch, s.current.workspaceRoot, s.hybridConfig, s.logger, resolver)
	aggregateCorpusLexicalHits(agg, corpusLex, s.hybridConfig)

	out := make([]SearchResult, 0, len(agg))
	for _, e := range agg {
		sr := resultToSearchResult(e.result, s.current.workspaceRoot)
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
	hydrateSearchContent(out, resolver)
	return out, nil
}

// aggregateVectorHits adds vector results into the RRF aggregation map in
// two passes: filter (collecting the top similarity among survivors), then
// the score gate, 1-based ranks, and the RRF contribution 1/(RRFK+rank).
// Gate failures never enter agg, so weak tail hits cannot earn a double RRF
// boost by also appearing lexically. See specs/domains/workspace.md.
func aggregateVectorHits(
	agg map[string]*fusedEntry,
	vecResults []chromem.Result,
	filePattern string,
	mustMatch []string,
	root string,
	hc HybridConfig,
	logger *slog.Logger,
	resolver *contentResolver,
) {
	// First pass: path/mustmatch filter, collect survivors + top sim.
	survivors := make([]chromem.Result, 0, len(vecResults))
	var maxSim float32
	for _, r := range vecResults {
		if !passesFilters(r, filePattern, mustMatch, root, resolver, logger) {
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

// aggregateLexicalHits adds lexical results into the RRF aggregation map,
// mirroring aggregateVectorHits: resolve via chromem.GetByID (entries created
// here are removed again when the gate drops them), filter, collect
// survivors, apply the relative BM25 gate, rank, contribute. Hits missing
// from chromem or failing filters are skipped; a zero threshold disables the
// gate. See specs/domains/workspace.md.
func aggregateLexicalHits(
	ctx context.Context,
	col *chromem.Collection,
	agg map[string]*fusedEntry,
	lexHits []lexical.Hit,
	filePattern string,
	mustMatch []string,
	root string,
	hc HybridConfig,
	logger *slog.Logger,
	resolver *contentResolver,
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
			if !passesFilters(r, filePattern, mustMatch, root, resolver, logger) {
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

// aggregateCorpusLexicalHits adds the corpus lexical leg into the RRF
// aggregation. The candidates are already visibility-filtered and
// passesFilters-screened (corpusLexicalCandidates) and carry their
// pre-resolved corpus documents, so this mirrors aggregateLexicalHits minus
// the chromem lookup. The relative BM25 gate spans the corpus list's own
// maximum: BM25 statistics are corpus-shape-dependent (ADR-104 §3) and the
// overlay's maximum is not a meaningful threshold for the corpus's scores.
// An ID the OVERLAY lexical leg already fused (lexicalRank != 0) is
// skipped: the two stores share the DocumentID space, and a stale overlay
// duplicate of a corpus document must not earn a second lexical-side RRF
// contribution (the overlay's rank stays authoritative for that ID).
func aggregateCorpusLexicalHits(
	agg map[string]*fusedEntry,
	hits []corpusLexicalHit,
	hc HybridConfig,
) {
	var maxScore float32
	for _, h := range hits {
		if h.score > maxScore {
			maxScore = h.score
		}
	}
	lexRank := 0
	for _, h := range hits {
		if !passesLexicalScoreGate(h.score, maxScore, hc) {
			continue
		}
		entry := agg[h.doc.ID]
		if entry != nil && entry.lexicalRank != 0 {
			continue // the overlay lexical leg already fused this ID
		}
		if entry == nil {
			r := chromem.Result{ID: h.doc.ID, Metadata: h.doc.Metadata, Content: h.doc.Content}
			entry = &fusedEntry{result: r}
			agg[h.doc.ID] = entry
		}
		lexRank++
		entry.lexicalScore = h.score
		entry.lexicalRank = lexRank
		entry.score += 1.0 / float64(hc.RRFK+lexRank)
	}
}

// hybridFanout returns the per-side RRF fanout max(topK×FanoutMultiplier,
// FanoutMin); a larger candidate pool than topK materially improves recall.
func hybridFanout(topK int, hc HybridConfig) int {
	fanout := topK * hc.FanoutMultiplier
	if fanout < hc.FanoutMin {
		fanout = hc.FanoutMin
	}
	return fanout
}

// matchFilePathPattern tests whether a file path matches a glob pattern with
// three strategies — direct doublestar match, "**/" prefix for relative
// patterns, basename match — because file_path is stored absolute while
// users type relative patterns like "*.md" or "src/**".
//
// It mirrors the frontend pathFilter logic (picomatch with contains:true plus
// basename testing) so the sidebar filter behaves like the file-tree filter;
// this FE↔Go parity contract is specified in specs/domains/workspace.md
// (file-pattern matching parity).
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

// passesFilters returns true if the chromem result satisfies the file-path
// glob filter (if any) and all must-match tokens. Must-match checks stored
// content first; content-less documents (see strippedForCommit) are
// reconstructed via resolver — lazily, with the per-call path cache keeping
// multi-token checks at one file read, resolving the occurrence coordinate
// against root (per-request hydration, ADR-104 L4). A file that cannot be
// reconstructed is a filter MISS — never matched against the diagnostic
// placeholder, whose embedded path and words would otherwise admit a
// substring token — so must_match excludes deleted files from filtered
// results (ADR-049). The glob filter matches the root-relative coordinate
// first (users type relative patterns like "src/**") and falls back to the
// stored absolute path.
func passesFilters(r chromem.Result, filePattern string, mustMatch []string, root string, resolver *contentResolver, logger *slog.Logger) bool {
	if filePattern != "" {
		if !matchFilePathPattern(filePattern, r.Metadata[relPathMetaKey]) &&
			!matchFilePathPattern(filePattern, r.Metadata["file_path"]) {
			return false
		}
	}
	for _, tok := range mustMatch {
		if tok == "" {
			continue
		}
		if strings.Contains(r.Content, tok) {
			continue
		}
		content := r.Content
		if content == "" && resolver != nil {
			startLine, _ := strconv.Atoi(r.Metadata["start_line"])
			endLine, _ := strconv.Atoi(r.Metadata["end_line"])
			reconstructed, ok := resolver.chunkContentOK(sourcePath(r.Metadata, root), startLine, endLine)
			if !ok {
				return false
			}
			content = reconstructed
		}
		if !strings.Contains(content, tok) {
			return false
		}
	}
	return true
}

// passesVectorScoreGate reports whether a hit survives the pre-fusion vector
// gate: below the absolute floor (VectorScoreFloor) OR below the relative
// cutoff (VectorScoreRatio × maxSim) rejects. Zero thresholds disable the
// gate; a zero maxSim disables the relative cutoff (a degenerate vector list
// rejects nothing).
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

// passesLexicalScoreGate reports whether a hit survives the pre-fusion
// lexical gate: below LexicalScoreRatio × maxScore rejects. A zero ratio or
// a zero maxScore disables the gate (a degenerate lexical list rejects
// nothing).
func passesLexicalScoreGate(score, maxScore float32, hc HybridConfig) bool {
	if hc.LexicalScoreRatio == 0 {
		return true
	}
	if maxScore <= 0 {
		return true
	}
	return float64(score) >= hc.LexicalScoreRatio*float64(maxScore)
}
