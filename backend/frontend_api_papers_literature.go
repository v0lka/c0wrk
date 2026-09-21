package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/papers"
	"github.com/v0lka/c0wrk/core/toolmanager"
)

// ---------------------------------------------------------------------------
// Paper literature-neighbourhood RPC
//
// Runs the study-paper `literature.py` helper (stdlib-only Python 3, needs the
// network) for a library paper through c0wrk's MANAGED Python interpreter and
// writes the result to <paper-dir>/literature.json. The paper workspace then
// renders that file as a DAG.
//
// The helper is OPTIONAL and network-bound, so this RPC is designed to always
// resolve with an EXPLICIT outcome rather than an opaque failure: a run that
// cannot reach the network, cannot resolve the seed, or lacks a managed Python
// returns a status the UI renders as a message (never an empty graph). Only a
// transport-level problem (unknown paper, containment violation) is an error.
// ---------------------------------------------------------------------------

// PaperLiteratureStatus values returned in PaperLiteratureDTO.Status.
const (
	litStatusOK          = "ok"
	litStatusOffline     = "offline"
	litStatusUnresolved  = "unresolved"
	litStatusRateLimited = "rate_limited"
	litStatusNoPython    = "no_python"
	litStatusNoScript    = "no_script"
	litStatusNoSeed      = "no_seed"
	litStatusError       = "error"
)

// Study-paper skill coordinates: the helper is seeded into c0wrk's GLOBAL
// agent skills directory by seedGlobalPacks as
// <agentDir>/.agents/skills/study-paper/scripts/literature.py.
const (
	studyPaperSkillName     = "study-paper"
	literatureScriptRelPath = "scripts/literature.py"
)

// literatureRunTimeout bounds the whole helper invocation (it makes several
// HTTP requests); the per-request timeout is passed to the helper itself.
const (
	literatureRunTimeout  = 90 * time.Second
	literatureHTTPTimeout = 20
	literatureMsgMaxRunes = 600
)

// PaperLiteratureDTO is the response for RunPaperLiterature. Status is one of
// the litStatus* values; Message carries the helper's stderr tail (or a
// resolver note); Path/Content describe the written literature.json (empty when
// nothing was written).
type PaperLiteratureDTO struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// RunPaperLiterature runs the study-paper literature helper for a paper and
// writes <paper-dir>/literature.json. It returns an explicit status for every
// non-success outcome; an error is reserved for an unknown paper, a containment
// violation, or a research-root change observed while waiting (see below). See
// the block comment above for the rationale.
//
// Concurrency: the paper is resolved and containment-checked under a SHORT hold
// of the per-effective-research-root mutation mutex (re-loading the row so a
// concurrent root-affecting change is rejected with errResearchRootChanged,
// mirroring SetPaperPinned/RecordFlashcardReview). The
// mutex is RELEASED before the network-bound helper runs: the helper writes no
// projects row, so holding the row-mutation mutex across its whole run (up to
// literatureRunTimeout) would head-of-line-block every pin/flashcard/research
// mutation on the project. The single shared resource is the paper's own
// literature.json, so concurrent lookups of the SAME paper are serialized on a
// per-paper-directory guard instead — lookups of other papers, and the
// projects-row writers, stay concurrent.
func (f *FrontendAPI) RunPaperLiterature(projectID, paperID string) (*PaperLiteratureDTO, error) {
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

	seed := literatureSeed(rec)
	if seed == "" {
		return &PaperLiteratureDTO{
			Status:  litStatusNoSeed,
			Message: "the paper card carries no DOI, arXiv id, or title to seed the lookup",
		}, nil
	}

	pythonPath := toolmanager.VenvPythonPath(config.ToolsDir(f.agentDir))
	if pythonPath == "" {
		return &PaperLiteratureDTO{
			Status:  litStatusNoPython,
			Message: "c0wrk's managed Python interpreter is not installed yet",
		}, nil
	}

	scriptPath := f.literatureScriptPath()
	if scriptPath == "" {
		return &PaperLiteratureDTO{
			Status:  litStatusNoScript,
			Message: "the study-paper literature.py helper was not found in c0wrk's global skills directory (~/.c0wrk/.agents/skills/study-paper)",
		}, nil
	}

	// Serialize concurrent lookups of the SAME paper on a per-directory guard
	// (a distinct mutex key) so two "Refresh" clicks cannot interleave their
	// atomic writes to the one shared literature.json — without holding the
	// projects-row mutex for the run. See the method doc for the rationale.
	guard := f.researchMutationMu(paperRunMuKey(rec.Dir))
	guard.Lock()
	defer guard.Unlock()

	return runLiteratureHelper(pythonPath, scriptPath, seed, rec.Dir), nil
}

// paperRunMuKey namespaces the per-paper lookup guard in the shared
// researchMutationMu map. The prefix cannot collide with a research root key
// (roots are absolute paths).
func paperRunMuKey(paperDir string) string {
	return "paper-run\x00" + paperDir
}

// resolvePaperForRun resolves and containment-checks the paper record for a
// network-bound per-paper run (RunPaperLiterature, FetchPaperOriginal) under
// a SHORT hold of the per-effective-research-root row-mutation mutex. It
// re-loads the project row so a concurrent root-affecting change is rejected
// with errResearchRootChanged (mirroring
// SetPaperPinned/RecordFlashcardReview), then re-resolves the record from the
// pre-lock, containment-checked library root. The mutex is released before the
// network-bound run proceeds.
func (f *FrontendAPI) resolvePaperForRun(projectID, paperID string, rctx *papersReadContext) (*papers.PaperRecord, error) {
	mu := f.researchMutationMu(rctx.researchRoot)
	mu.Lock()
	defer mu.Unlock()

	fresh, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return nil, err
	}
	if effectiveResearchRoot(fresh) != rctx.researchRoot {
		return nil, errResearchRootChanged
	}

	lib, err := f.parsePaperLibrary(rctx.libraryRoot)
	if err != nil {
		return nil, err
	}
	rec := lib.Get(paperID)
	if rec == nil {
		return nil, fmt.Errorf("paper %q not found in the library", paperID)
	}
	return rec, nil
}

// literatureScriptPath resolves the study-paper helper script from c0wrk's
// GLOBAL agent skills directory — the copy seeded once per launch by
// seedGlobalPacks into config.SkillsDir(agentDir) — or "" when it is not
// present (an explicit, honest degradation rather than a crash). A missing
// agentDir (or an unseeded pack) yields "".
func (f *FrontendAPI) literatureScriptPath() string {
	if f.agentDir == "" {
		return ""
	}
	path := filepath.Join(config.SkillsDir(f.agentDir), studyPaperSkillName, literatureScriptRelPath)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	return path
}

// literatureSeed picks the most precise reference for the helper: a DOI first,
// then an arXiv id, then any declared identifier, and finally the title. Returns
// "" when the card carries none of these.
func literatureSeed(rec *papers.PaperRecord) string {
	if rec == nil {
		return ""
	}
	byScheme := func(want string) string {
		for _, id := range rec.Identifiers {
			if strings.EqualFold(id.Scheme, want) && strings.TrimSpace(id.Value) != "" {
				return strings.TrimSpace(id.Value)
			}
		}
		return ""
	}
	if doi := byScheme("doi"); doi != "" {
		return doi
	}
	if arxiv := byScheme("arxiv"); arxiv != "" {
		return "arXiv:" + arxiv
	}
	for _, id := range rec.Identifiers {
		if strings.TrimSpace(id.Value) != "" {
			return strings.TrimSpace(id.Value)
		}
	}
	return strings.TrimSpace(rec.Title)
}

// runLiteratureHelper is the testable seam: it invokes `pythonPath scriptPath
// --format json --timeout <n> --out <paperDir>/literature.json -- <seed>` and
// maps the process outcome to an explicit DTO. It never returns an error — a
// failed run is data.
//
// The seed is passed after a `--` terminator so a seed that begins with `-`
// (a title/identifier) is never parsed by argparse as an option, and the exit
// code is only trusted together with its stderr marker (see
// classifyLiteratureExit): CPython exits 1 on an uncaught exception and argparse
// exits 2 on a usage error, so the bare codes are ambiguous.
func runLiteratureHelper(pythonPath, scriptPath, seed, paperDir string) *PaperLiteratureDTO {
	outPath := filepath.Join(paperDir, papers.LiteratureFileName)

	ctx, cancel := context.WithTimeout(context.Background(), literatureRunTimeout)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(
		ctx,
		pythonPath,
		scriptPath,
		"--format", "json",
		"--timeout", strconv.Itoa(literatureHTTPTimeout),
		"--out", outPath,
		"--",
		seed,
	)
	cmd.Stdout = io.Discard // --out writes the payload to the file
	cmd.Stderr = &stderr

	err := cmd.Run()
	rawStderr := stderr.String()
	message := tailMessage(rawStderr)

	if ctx.Err() == context.DeadlineExceeded {
		return &PaperLiteratureDTO{
			Status:  litStatusError,
			Message: "the literature lookup timed out",
		}
	}
	if err != nil {
		status := litStatusError
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			status = classifyLiteratureExit(exitErr.ExitCode(), rawStderr)
		}
		if message == "" {
			message = err.Error()
		}
		return &PaperLiteratureDTO{Status: status, Message: message}
	}

	content, readErr := os.ReadFile(outPath)
	if readErr != nil {
		return &PaperLiteratureDTO{
			Status:  litStatusError,
			Message: fmt.Sprintf("the helper ran but produced no output file: %v", readErr),
		}
	}
	return &PaperLiteratureDTO{
		Status:  litStatusOK,
		Message: message,
		Path:    outPath,
		Content: string(content),
	}
}

// classifyLiteratureExit maps the helper's process exit code to a status,
// keyed on the diagnostic marker the helper writes to stderr. The bare exit
// codes are ambiguous — literature.py returns 1/2/3 for "could not resolve the
// seed" / network failure / rate-limit-or-write-failure, but CPython also exits
// 1 on an uncaught exception and argparse exits 2 on a usage error — so a code
// is only accepted as the documented one when its stderr marker is present;
// anything else is a generic error.
func classifyLiteratureExit(code int, stderr string) string {
	switch code {
	case 1:
		if strings.Contains(stderr, "could not resolve the seed:") {
			return litStatusUnresolved
		}
	case 2:
		if strings.Contains(stderr, "network unavailable:") {
			return litStatusOffline
		}
	case 3:
		if strings.Contains(stderr, "rate limited:") {
			return litStatusRateLimited
		}
	}
	return litStatusError
}

// tailMessage trims a helper stderr dump and caps it to the last
// literatureMsgMaxRunes runes so a chatty failure never floods the UI.
func tailMessage(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= literatureMsgMaxRunes {
		return s
	}
	return "…" + string(runes[len(runes)-literatureMsgMaxRunes:])
}
