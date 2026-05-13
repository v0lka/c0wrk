package backend

import (
	"errors"

	"github.com/user/agent/backend/vectorindex"
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
	if f.vectorManager == nil {
		return nil, errors.New("vector search not available")
	}

	topK := req.TopK
	if topK <= 0 {
		topK = defaultVectorBrowseTopK
	}

	vectorSvc := f.vectorManager.Service()

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
