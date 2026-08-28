package backend

import (
	"context"
	"errors"

	"github.com/v0lka/c0wrk/core/vectorindex"
)

const defaultVectorBrowseTopK = 50

// SearchVectorStore searches the vector store for the given request.
//
// When req.Query is empty it browses arbitrary chunks (no semantic
// ordering) through BrowseWithFilter. Otherwise it dispatches to
// Service.HybridSearch with the requested mode (hybrid | vector |
// lexical; empty defaults to hybrid with auto-fallback to vector when
// the lexical index is empty).
//
// req.TopK defaults to 50 when <= 0.
func (f *FrontendAPI) SearchVectorStore(req SearchRequest) ([]VectorStoreEntry, error) {
	// No Project (CHAT mode): the vector index is disabled. Return empty
	// results (not an error) so the frontend renders an empty state rather
	// than attempting a search against a dormant subsystem.
	if f.isNoProject() {
		return []VectorStoreEntry{}, nil
	}

	vm := f.getVectorManager()
	if vm == nil {
		return nil, errors.New("vector search not available")
	}

	topK := req.TopK
	if topK <= 0 {
		topK = defaultVectorBrowseTopK
	}

	vectorSvc := vm.Service()

	var results []vectorindex.SearchResult
	var err error

	// Defense-in-depth: bound the readiness wait inside HybridSearch /
	// BrowseWithFilter with the same knob that bounds the semantic_search
	// tool (vector_index.search_wait_timeout_ms), so the RPC can never block
	// unboundedly while a full index is stuck. The fail-fast sentinel (0)
	// skips waiting entirely: it dispatches to the NoWait variants, so a
	// not-ready index errors immediately AND an incremental pass starting
	// between the readiness state and the call cannot block the RPC until
	// the pass finishes.
	ctx := f.ctx()
	failFast := false
	if wait := vm.SearchWaitTimeout(); wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	} else {
		failFast = true
	}

	if req.Query == "" {
		if failFast {
			results, err = vectorSvc.BrowseWithFilterNoWait(ctx, topK, req.FilePattern)
		} else {
			results, err = vectorSvc.BrowseWithFilter(ctx, topK, req.FilePattern)
		}
	} else {
		opts := vectorindex.SearchOptions{
			Query:       req.Query,
			TopK:        topK,
			Mode:        vectorindex.ParseMode(req.Mode),
			FilePattern: req.FilePattern,
			MustMatch:   req.MustMatch,
		}
		if failFast {
			results, err = vectorSvc.HybridSearchNoWait(ctx, opts)
		} else {
			results, err = vectorSvc.HybridSearch(ctx, opts)
		}
	}
	if err != nil {
		// Fail-fast dispatch observed a not-ready index (including the
		// pass-started-after-gate race): surface the actionable index
		// status (progress, current file) instead of a bare error.
		if errors.Is(err, vectorindex.ErrNotReady) {
			return nil, vm.NotReadyError()
		}
		// The bound expired while waiting for readiness: surface the
		// actionable index status (progress, current file) instead of a
		// bare deadline error.
		if !vectorSvc.IsReady() {
			return nil, vm.NotReadyError()
		}
		return nil, err
	}

	out := make([]VectorStoreEntry, len(results))
	for i, r := range results {
		out[i] = VectorStoreEntry{
			FilePath:     r.FilePath,
			FileName:     r.FileName,
			Content:      r.Content,
			Score:        r.Score,
			StartLine:    r.StartLine,
			EndLine:      r.EndLine,
			Language:     r.Language,
			VectorScore:  r.VectorScore,
			LexicalScore: r.LexicalScore,
			VectorRank:   r.VectorRank,
			LexicalRank:  r.LexicalRank,
		}
	}

	return out, nil
}

// GetVectorIndexStatus returns the current state and progress of the
// vector index for the active project.
func (f *FrontendAPI) GetVectorIndexStatus() VectorIndexStatus {
	// No Project (CHAT mode): the vector index is disabled. Report an
	// unavailable state so the frontend UI reflects the dormant subsystem
	// (neither "building" nor "ready").
	if f.isNoProject() {
		return VectorIndexStatus{State: "unavailable", Indices: []string{}}
	}

	result := VectorIndexStatus{}

	vm := f.getVectorManager()
	if vm == nil {
		result.State = "unavailable"
		return result
	}

	st := vm.GetIndexStatus()
	result.State = string(st.State)
	result.Phase = string(st.Phase)
	result.FilesIndexed = st.FilesIndexed
	result.TotalFiles = st.TotalFiles
	result.CurrentFile = st.CurrentFile
	result.Branch = st.Branch

	// Compute progress as a fraction.
	if st.TotalFiles > 0 {
		result.Progress = float64(st.FilesIndexed) / float64(st.TotalFiles)
	}

	// Determine which indices are active.
	svc := vm.Service()
	indices := make([]string, 0, 2)
	if svc == nil {
		result.Indices = indices
		return result
	}
	if svc.GetCollection() != nil {
		indices = append(indices, "vector")
	}
	if svc.GetLexical() != nil {
		indices = append(indices, "lexical")
	}
	result.Indices = indices

	return result
}
