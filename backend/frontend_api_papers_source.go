package backend

import (
	"context"
	"errors"
	"strings"

	"github.com/v0lka/c0wrk/core/papers"
)

// ---------------------------------------------------------------------------
// Paper original-source RPC
//
// Fetches a library paper's original arXiv HTML rendition (native
// arxiv.org/html, falling back to the ar5iv mirror), sanitizes and localizes
// it (scripts stripped, images rewritten to a local assets/ dir), and persists
// <paper-dir>/paper.html atomically — core/papers.FetchOriginalHTML owns the
// whole pipeline (host allowlist, byte caps, containment, staging+rename).
//
// The fetch is OPTIONAL and network-bound, so this RPC mirrors
// RunPaperLiterature's explicit-outcome contract: it always resolves with a
// STATUS rather than an opaque failure, so the UI can render offline /
// no-arXiv-id / no-rendition as messages instead of a silent empty viewer.
// Only a transport-level problem (unknown paper, containment violation, or a
// research-root change observed while waiting) is a Go error.
//
// No event is emitted synchronously: the write lands inside the watched
// library, so the file watcher emits `papers:changed` and the caller's
// resolved promise is its refresh signal.
// ---------------------------------------------------------------------------

// PaperOriginalStatus values returned in PaperOriginalDTO.Status. They mirror
// papers.FetchStatus one-to-one; paperOriginalDTO folds anything else to
// origStatusError so a future core status can never surface as success.
const (
	origStatusOK       = "ok"
	origStatusOffline  = "offline"
	origStatusNoArxiv  = "no_arxiv"
	origStatusNotFound = "not_found"
	origStatusError    = "error"
)

// PaperOriginalDTO is the response for FetchPaperOriginal. Status is one of
// the origStatus* values; URL is the arXiv URL that actually produced the
// document (after redirects) and is empty unless the fetch succeeded (see
// papers.FetchResult.ResolvedURL). Nothing was written for any status other
// than ok.
type PaperOriginalDTO struct {
	Status string `json:"status"`
	URL    string `json:"url"`
}

// FetchPaperOriginal fetches a paper's original HTML rendition and writes
// <paper-dir>/paper.html (+ assets/) through core/papers.FetchOriginalHTML.
// It returns an explicit status for every non-success outcome; an error is
// reserved for an unknown paper, a containment violation, or a research-root
// change observed while waiting. See the block comment above for the
// rationale.
//
// Concurrency: mirroring RunPaperLiterature, the paper is resolved and
// containment-checked under a SHORT hold of the per-effective-research-root
// mutation mutex (re-loading the row so a concurrent root move is rejected
// with errResearchRootChanged), and the mutex
// is RELEASED before the network-bound fetch runs — holding the row-mutation
// mutex across a fetch (bounded per-request timeouts of 60s per document and
// 20s per image, up to 200 images) would head-of-line-block every
// pin/flashcard/research mutation on the project. The single shared resource
// is the paper's own paper.html/assets output, so concurrent fetches of the
// SAME paper are serialized on the shared per-paper-directory guard instead —
// fetches of other papers, and the projects-row writers, stay concurrent.
func (f *FrontendAPI) FetchPaperOriginal(projectID, paperID string) (*PaperOriginalDTO, error) {
	if strings.TrimSpace(paperID) == "" {
		return nil, errors.New("paper id or slug is required")
	}
	rctx, err := f.papersReadContextFor(projectID)
	if err != nil {
		return nil, err
	}

	rec, err := f.resolvePaperForRun(projectID, paperID, rctx)
	if err != nil {
		return nil, err
	}

	// Serialize concurrent runs targeting the SAME paper on the shared
	// per-directory guard (a distinct mutex key, shared with
	// RunPaperLiterature) so two clicks cannot interleave their atomic
	// writes to the one paper directory — without holding the projects-row
	// mutex for the fetch. See the method doc for the rationale.
	guard := f.researchMutationMu(paperRunMuKey(rec.Dir))
	guard.Lock()
	defer guard.Unlock()

	fetch := papers.FetchOriginalHTML
	if f.fetchPaperOriginalFn != nil {
		fetch = f.fetchPaperOriginalFn
	}
	res := fetch(context.Background(), nil, rctx.libraryRoot, *rec)
	if res.Err != nil {
		f.log().Warn("paper original fetch did not succeed",
			"paper", rec.ID, "status", string(res.Status), "error", res.Err)
	}
	return paperOriginalDTO(res), nil
}

// paperOriginalDTO maps a core fetch outcome to the wire DTO. It is total and
// side-effect free: an unrecognized status folds to origStatusError so the
// explicit-status contract can never regress into a silent success.
func paperOriginalDTO(res papers.FetchResult) *PaperOriginalDTO {
	status := origStatusError
	switch res.Status {
	case papers.FetchStatusOK:
		status = origStatusOK
	case papers.FetchStatusOffline:
		status = origStatusOffline
	case papers.FetchStatusNoArxiv:
		status = origStatusNoArxiv
	case papers.FetchStatusNotFound:
		status = origStatusNotFound
	}
	return &PaperOriginalDTO{Status: status, URL: res.ResolvedURL}
}
