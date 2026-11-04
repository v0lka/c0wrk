package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/orchestration"
)

// Backend half of the prompt-suggestion trigger (ADR-088): on full/partial
// finish, one fail-open pass per kind, persisted and announced via the
// "prompt_suggestions" event. See specs/domains/prompt-suggestions.md.

// next_task project window: the last 10 sessions (ADR-088 D3); core
// enforces the same cap on the assembled input.
const promptSuggestWindowSessions = 10

// One-shot suggestion generation; *core.OrchestratorBuilder implements it.
// Mirrors LLMTitleCaller so the trigger path is testable without a builder.
type LLMPromptSuggestCaller interface {
	GeneratePromptSuggestions(ctx context.Context, kind core.SuggestionKind, in core.PromptSuggestInput) ([]string, error)
}

// Manager-facing shell around the suggestion caller, mirroring TitleGenerator.
// The caller is fixed at construction; logging belongs to the manager.
type PromptSuggestGenerator struct {
	caller LLMPromptSuggestCaller
}

// A nil caller makes the generator inert (Generate errors; the pass treats
// that as "no suggestions").
func NewPromptSuggestGenerator(caller LLMPromptSuggestCaller) *PromptSuggestGenerator {
	return &PromptSuggestGenerator{caller: caller}
}

// Errors are the caller's to fail open on.
func (g *PromptSuggestGenerator) Generate(ctx context.Context, kind core.SuggestionKind, in core.PromptSuggestInput) ([]string, error) {
	if g.caller == nil {
		return nil, errPromptSuggestNoCaller
	}
	return g.caller.GeneratePromptSuggestions(ctx, kind, in)
}

// errPromptSuggestNoCaller reports an inert generator (nil caller).
var errPromptSuggestNoCaller = errPromptSuggestInert{}

type errPromptSuggestInert struct{}

func (errPromptSuggestInert) Error() string { return "prompt suggest: no caller installed" }

// Installs the suggestion generator (ADR-088); nil (the default) disables
// the feature — the outcome gate never fires a pass.
func (m *Manager) SetPromptSuggestionsGenerator(gen *PromptSuggestGenerator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promptSuggestGen = gen
}

// Applies the prompt_suggestions.enabled gate: disabled costs zero LLM
// calls, writes, and events. See specs/domains/prompt-suggestions.md.
func (m *Manager) SetPromptSuggestionsEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promptSuggestEnabled = enabled
}

// Diagnostic mirror of the gate; hot paths read their own snapshot under
// the manager lock.
func (m *Manager) PromptSuggestionsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.promptSuggestEnabled
}

// Outcome gate (ADR-088 D2): full/partial finishes only, from the TYPED
// status — Cancelled/Paused read as "partial" yet must never trigger
// (pause/resume continues the same task). A nil result is a full success.
// See specs/domains/prompt-suggestions.md (§ Triggers (D2)).
func promptSuggestOutcomeTriggers(result *core.HandleResult) bool {
	if result == nil {
		return true
	}
	switch result.Status {
	case orchestration.ExecutionStatusSuccess, orchestration.ExecutionStatusPartial:
		return true
	default:
		return false
	}
}

// Spawns the tracked background pass after a successful emitTaskComplete;
// guards (flag, generator, store) check synchronously, readiness and the
// LLM call run on the goroutine. See specs/domains/prompt-suggestions.md.
func (m *Manager) maybeSpawnPromptSuggestions(sessionID string, result *core.HandleResult) {
	m.mu.RLock()
	gen := m.promptSuggestGen
	enabled := m.promptSuggestEnabled
	store := m.sessionStore
	serviceLLMTimeout := m.serviceLLMTimeout
	serviceLLMGate := m.serviceLLMGate
	projectID := ""
	if session := m.sessions[sessionID]; session != nil {
		// ProjectID is immutable after creation (no lock needed for reads).
		projectID = session.ProjectID
	}
	m.mu.RUnlock()
	if !enabled || gen == nil || store == nil {
		return
	}

	output, taskID := promptSuggestResultFacts(result)
	m.spawnBackgroundNamed("prompt-suggestions session="+sessionID, func() {
		m.runPromptSuggestionPasses(sessionID, projectID, taskID, output, gen, store, serviceLLMGate, serviceLLMTimeout)
	})
}

// Extracts the final answer and the triggering task id (rides the persistent
// blackboard) from a completed task's result.
func promptSuggestResultFacts(result *core.HandleResult) (output, taskID string) {
	if result == nil {
		return "", ""
	}
	if pbb, ok := result.Blackboard.(core.PersistableBlackboard); ok {
		taskID = pbb.TaskID()
	}
	return result.Output, taskID
}

// Runs both passes: goal_advance (the session's transcript) and next_task
// (the 10-session window, real projects only); readiness BEFORE the budget
// (ADR-066 D13).
func (m *Manager) runPromptSuggestionPasses(
	sessionID, projectID, taskID, output string,
	gen *PromptSuggestGenerator,
	store SessionStore,
	serviceLLMGate func(context.Context) error,
	serviceLLMTimeout time.Duration,
) {
	// The gate sits outside the serviceLLMTimeout envelope: the embedded
	// model's cold load exceeds the service timeout, so arming it first
	// would spend the budget on the load.
	if serviceLLMGate != nil {
		if err := serviceLLMGate(m.shutdownCtx); err != nil {
			m.log().Debug("skipping prompt suggestions: the model is not ready",
				"session", sessionID, "error", err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(m.shutdownCtx, serviceLLMTimeout)
	defer cancel()
	logger := m.log()

	// Continuation pass (goal_advance): the persisted transcript plus the
	// final answer — task_complete races the event persister's final write.
	m.runPromptSuggestPass(ctx, logger, sessionID, taskID, gen, store, core.SuggestionKindGoalAdvance,
		func() ([]core.PromptSuggestSession, error) {
			history, err := store.LoadSessionHistory(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			return []core.PromptSuggestSession{{
				ID:       sessionID,
				Messages: promptSuggestMessagesFromHistory(history, output),
			}}, nil
		})

	// New-session pass (next_task); the No-Project sentinel (CHAT mode) and
	// an empty id are both "no project".
	if projectID == "" || projectID == project.NoProjectID {
		return
	}
	m.runPromptSuggestPass(ctx, logger, sessionID, taskID, gen, store, core.SuggestionKindNextTask,
		func() ([]core.PromptSuggestSession, error) {
			return promptSuggestProjectWindow(ctx, store, projectID, sessionID, output, logger)
		})
}

// Drives one pass end to end: window → generate → persist → announce.
// Every failure is a Debug log and "no suggestions" (D9); an empty result
// never reaches the store.
func (m *Manager) runPromptSuggestPass(
	ctx context.Context,
	logger *slog.Logger,
	sessionID, taskID string,
	gen *PromptSuggestGenerator,
	store SessionStore,
	kind core.SuggestionKind,
	window func() ([]core.PromptSuggestSession, error),
) {
	sessions, err := window()
	if err != nil {
		logger.Debug("prompt suggestion window unavailable",
			"session", sessionID, "kind", string(kind), "error", err)
		return
	}
	texts, err := gen.Generate(ctx, kind, core.PromptSuggestInput{Sessions: sessions})
	if err != nil {
		logger.Debug("prompt suggestion generation failed",
			"session", sessionID, "kind", string(kind), "error", err)
		return
	}
	if len(texts) == 0 {
		return
	}
	rows := make([]core.PromptSuggestion, 0, len(texts))
	for _, text := range texts {
		rows = append(rows, core.PromptSuggestion{
			SessionID: sessionID,
			TaskID:    taskID,
			Kind:      kind,
			Text:      text,
		})
	}
	saved, err := store.SavePromptSuggestions(ctx, rows)
	if err != nil {
		logger.Debug("failed to persist prompt suggestions",
			"session", sessionID, "kind", string(kind), "error", err)
		return
	}
	m.emitPromptSuggestionsEvent(ctx, sessionID, saved)
}

// Pure effective-activity recency (ADR-088 D3): the sessionListOrderSQL
// order without its pinned DESC term — a long-idle pinned session must not
// displace recent ones; tie-breaks mirror the SQL.
func PromptSuggestSessionsNewestFirst(infos []SessionInfo) []SessionInfo {
	out := make([]SessionInfo, len(infos))
	copy(out, infos)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.LastActiveAt != b.LastActiveAt {
			return a.LastActiveAt > b.LastActiveAt
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt > b.CreatedAt
		}
		return a.ID < b.ID
	})
	return out
}

// The next_task window: the last 10 sessions (D3), chronological,
// triggering session last. NewestFirst undoes pinned-first ordering before
// the head is taken and reversed; unreadable transcripts skip (fail-open).
func promptSuggestProjectWindow(
	ctx context.Context,
	store SessionStore,
	projectID, triggeringSessionID, finalAnswer string,
	logger *slog.Logger,
) ([]core.PromptSuggestSession, error) {
	infos, err := store.ListSessionsByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	infos = PromptSuggestSessionsNewestFirst(infos)
	if len(infos) > promptSuggestWindowSessions {
		infos = infos[:promptSuggestWindowSessions]
	}
	sessions := make([]core.PromptSuggestSession, 0, len(infos))
	for i := len(infos) - 1; i >= 0; i-- {
		history, err := store.LoadSessionHistory(ctx, infos[i].ID)
		if err != nil {
			logger.Debug("prompt suggestions: skipping unreadable session transcript",
				"session", infos[i].ID, "project", projectID, "error", err)
			continue
		}
		answer := ""
		if infos[i].ID == triggeringSessionID {
			answer = finalAnswer
		}
		sessions = append(sessions, core.PromptSuggestSession{
			ID:       infos[i].ID,
			Messages: promptSuggestMessagesFromHistory(history, answer),
		})
	}
	return sessions, nil
}

// Core generation input: user/assistant rows only, is_nudge dropped (D5).
// finalAnswer supplements a transcript whose final write is still in flight
// (the pass races the event persister).
func promptSuggestMessagesFromHistory(history []ChatMessage, finalAnswer string) []core.PromptSuggestMessage {
	msgs := make([]core.PromptSuggestMessage, 0, len(history)+1)
	for _, msg := range history {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		if msg.Role == "user" && chatMessageIsNudge(msg) {
			continue
		}
		msgs = append(msgs, core.PromptSuggestMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	answer := strings.TrimSpace(finalAnswer)
	if answer == "" {
		return msgs
	}
	supplemented := false
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		// The LAST assistant row decides: if it already carries the answer
		// the history is complete; otherwise the final write has not landed.
		if strings.TrimSpace(msgs[i].Content) != answer {
			msgs = append(msgs, core.PromptSuggestMessage{Role: "assistant", Content: answer})
		}
		supplemented = true
		break
	}
	if !supplemented {
		// No assistant row: the answer must still land or the transcript's
		// only prompt→answer pair is lost to the persister race. A dangling
		// tail is harmless — core drops an answer with no open prompt.
		msgs = append(msgs, core.PromptSuggestMessage{Role: "assistant", Content: answer})
	}
	return msgs
}

// Reports the is_nudge metadata stamp (ADR-088 D5): metadata-only, user
// messages, consulted exclusively by the suggestion input builder.
func chatMessageIsNudge(msg ChatMessage) bool {
	if len(msg.Metadata) == 0 {
		return false
	}
	var meta map[string]any
	if err := json.Unmarshal(msg.Metadata, &meta); err != nil {
		return false
	}
	flag, ok := meta["is_nudge"].(bool)
	return ok && flag
}

// Session-scoped "prompt_suggestions" event: the FULL active set (both
// kinds) — the frontend replaces its set; a failed re-read degrades to the
// just-saved pass.
func (m *Manager) emitPromptSuggestionsEvent(ctx context.Context, sessionID string, justSaved []core.PromptSuggestion) {
	active := justSaved
	m.mu.RLock()
	store := m.sessionStore
	m.mu.RUnlock()
	if store != nil {
		if rows, err := store.ListPromptSuggestions(ctx, sessionID); err == nil {
			active = rows
		}
	}
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "prompt_suggestions",
		Data: PromptSuggestionsData{
			SessionID:   sessionID,
			Suggestions: active,
		},
	})
}
