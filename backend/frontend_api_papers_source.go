package backend

import (
	"context"
	"errors"
	"strings"

	"github.com/v0lka/c0wrk/core/papers"
)

// Paper original-source RPC: fetches a library paper's original arXiv HTML
// rendition (arxiv.org/html, ar5iv fallback) via core/papers.FetchOriginalHTML
// and persists <paper-dir>/paper.html atomically. Mirrors RunPaperLiterature's
// explicit-outcome contract — every non-success is a STATUS, not an error —
// and emits no event: the write lands inside the watched library, so the
// watcher's papers:changed is the refresh signal.
// See specs/domains/papers.md.

// PaperOriginalDTO.Status values, mirroring papers.FetchStatus one-to-one;
// anything unrecognized folds to origStatusError so a future core status can
// never surface as success.
const (
	origStatusOK       = "ok"        // rendition fetched and written
	origStatusOffline  = "offline"   // no endpoint reachable at the network level
	origStatusNoArxiv  = "no_arxiv"  // the card carries no usable arXiv id
	origStatusNotFound = "not_found" // every reachable endpoint answered 404/410
	origStatusError    = "error"     // anything else (oversize, containment, fs)
)

// PaperOriginalDTO is the FetchPaperOriginal response: Status is an
// origStatus* value; URL is the post-redirect arXiv URL, empty unless the
// fetch succeeded — nothing is written for any status other than ok.
// See specs/domains/papers.md.
type PaperOriginalDTO struct {
	Status string `json:"status"`
	URL    string `json:"url"`
}

// FetchPaperOriginal fetches a paper's original HTML rendition and writes
// <paper-dir>/paper.html (+ assets/) via core/papers.FetchOriginalHTML. Every
// non-success outcome is an explicit status; a Go error is reserved for an
// unknown paper, a containment violation, or a research-root change observed
// while waiting. Concurrency mirrors RunPaperLiterature: the paper resolves
// under a SHORT hold of the per-research-root mutation mutex (a concurrent
// root move is rejected), released before the network-bound fetch — holding
// it across the fetch would head-of-line-block every project mutation — and
// same-paper fetches serialize on the shared per-paper-directory guard.
// See specs/domains/papers.md.
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

	// Same-paper runs serialize on the shared per-directory guard (distinct
	// mutex key, shared with RunPaperLiterature) so two clicks cannot
	// interleave their atomic writes to the one paper directory.
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

// paperOriginalDTO maps a core fetch outcome to the wire DTO, folding an
// unrecognized status to origStatusError so the explicit-status contract
// cannot regress into a silent success. See specs/domains/papers.md.
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
