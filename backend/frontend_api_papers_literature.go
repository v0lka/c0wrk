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

// Study-paper skill coordinates: the helper is seeded under the global agent
// skills directory as <SkillsDir>/study-paper/scripts/literature.py.
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
// non-success outcome; an error is reserved for an unknown paper or a
// containment violation. See the block comment above for the rationale.
func (f *FrontendAPI) RunPaperLiterature(projectID, paperID string) (*PaperLiteratureDTO, error) {
	if strings.TrimSpace(paperID) == "" {
		return nil, errors.New("paper id or slug is required")
	}
	rctx, err := f.papersReadContextFor(projectID)
	if err != nil {
		return nil, err
	}
	rec := f.parsePaperLibrary(rctx.libraryRoot).Get(paperID)
	if rec == nil {
		return nil, fmt.Errorf("paper %q not found in the library", paperID)
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
			Message: "the seeded study-paper literature.py helper was not found",
		}, nil
	}

	// Serialize the write against the same per-research-root mutation mutex that
	// guards SetPaperPinned / RecordFlashcardReview (and the research pin RPCs /
	// Enable/DisableResearch): the helper rewrites literature.json inside the
	// paper directory, so a lookup must not interleave with a pin or flashcard
	// write that re-resolves the same library. The lock is held for the whole
	// helper run (it owns the atomic write), so a concurrent toggle waits for it.
	mu := f.researchMutationMu(rctx.researchRoot)
	mu.Lock()
	defer mu.Unlock()

	return runLiteratureHelper(pythonPath, scriptPath, seed, rec.Dir), nil
}

// literatureScriptPath resolves the seeded study-paper helper script, or "" when
// it is not present (the global skill-pack is seeded at startup; a missing file
// is an explicit, honest degradation rather than a crash).
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
// seed --format json --out <paperDir>/literature.json` and maps the process
// outcome to an explicit DTO. It never returns an error — a failed run is data.
func runLiteratureHelper(pythonPath, scriptPath, seed, paperDir string) *PaperLiteratureDTO {
	outPath := filepath.Join(paperDir, papers.LiteratureFileName)

	ctx, cancel := context.WithTimeout(context.Background(), literatureRunTimeout)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(
		ctx,
		pythonPath,
		scriptPath,
		seed,
		"--format", "json",
		"--timeout", strconv.Itoa(literatureHTTPTimeout),
		"--out", outPath,
	)
	cmd.Stdout = io.Discard // --out writes the payload to the file
	cmd.Stderr = &stderr

	err := cmd.Run()
	message := tailMessage(stderr.String())

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
			switch exitErr.ExitCode() {
			case 2:
				status = litStatusOffline
			case 1:
				status = litStatusUnresolved
			case 3:
				status = litStatusRateLimited
			}
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
