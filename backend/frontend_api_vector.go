package backend

import (
	"errors"

	"github.com/user/agent/backend/vectorindex"
)

const defaultVectorBrowseTopK = 50

// SearchVectorStore searches the vector store for the given query.
// When query is empty it browses arbitrary chunks (no semantic ordering).
// topK defaults to 50 when <= 0.
// An optional filePattern glob filter (e.g. "*.go", "src/**") can be applied.
func (f *FrontendAPI) SearchVectorStore(query string, topK int, filePattern string) ([]VectorStoreEntry, error) {
	if f.vectorManager == nil {
		return nil, errors.New("vector search not available")
	}

	if topK <= 0 {
		topK = defaultVectorBrowseTopK
	}

	vectorSvc := f.vectorManager.Service()

	var results []vectorindex.SearchResult
	var err error

	ctx := f.ctx()

	if query == "" {
		results, err = vectorSvc.BrowseWithFilter(ctx, topK, filePattern)
	} else {
		results, err = vectorSvc.SearchWithFilter(ctx, query, topK, filePattern)
	}
	if err != nil {
		return nil, err
	}

	out := make([]VectorStoreEntry, len(results))
	for i, r := range results {
		out[i] = VectorStoreEntry{
			FilePath:  r.FilePath,
			FileName:  r.FileName,
			Content:   r.Content,
			Score:     r.Score,
			StartLine: r.StartLine,
			EndLine:   r.EndLine,
			Language:  r.Language,
		}
	}

	return out, nil
}
