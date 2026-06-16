package backend

import (
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

	ctx := f.ctx()

	if req.Query == "" {
		results, err = vectorSvc.BrowseWithFilter(ctx, topK, req.FilePattern)
	} else {
		mode := vectorindex.ParseMode(req.Mode)
		results, err = vectorSvc.HybridSearch(ctx, vectorindex.SearchOptions{
			Query:       req.Query,
			TopK:        topK,
			Mode:        mode,
			FilePattern: req.FilePattern,
			MustMatch:   req.MustMatch,
		})
	}
	if err != nil {
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
