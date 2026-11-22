package core

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/v0lka/sp4rk/agent/router"
)

// Heuristic continuation fast-path: c0wrk orchestration policy layered on top
// of the sp4rk router (the engine itself is untouched). A short
// continuation-style message ("continue", "ok", "yes", a bare acknowledgment)
// skips the routing LLM call and reuses the routing verdict the session last
// ran with. The cached verdict is invalidated on a session switch, a model
// switch, and by every substantive message (the real routing stage clears the
// slot before it runs), so a stale-verdict misroute window is bounded to at
// most one message. See specs/domains/orchestration/router.md,
// "Continuation Fast-Path".

// continuationMaxRunes caps the candidate message length. Every phrase in the
// accepted set is far shorter; the cap exists so a long message can never
// reach the set lookup through later normalization changes.
const continuationMaxRunes = 32

// continuationRejectRunes are characters that mark a message as substantive
// even when short: questions, slash commands and paths, code spans, file
// references, shell/expansion syntax, quoting (ASCII and Cyrillic guillemets
// alike), and any line structure. A message containing any of them always
// routes through the real router. (A trailing "!" is NOT here: "ok!", "yes!"
// are plain excited acknowledgments; the exact-phrase match still rejects
// everything substantive.)
const continuationRejectRunes = "?`/@\\#:;=\"<>*_~|[](){}$\n\r\t«»„“”"

// continuationPhrases is the closed set of continuation-style messages the
// fast-path accepts, bilingual English + Russian (translations of the English
// entries plus common colloquial synonyms), matched after normalization
// (lowercase, single-spaced, sentence punctuation and internal commas
// stripped, ’→' and ё→е folded). Exact match only — prefixes, extra words,
// and anything not listed here stay substantive and re-route. Auditable by
// design: widening the set is the only way to widen the fast-path.
var continuationPhrases = map[string]struct{}{
	// Acknowledgments.
	"ok":           {},
	"okay":         {},
	"yes":          {},
	"yep":          {},
	"yeah":         {},
	"yup":          {},
	"sure":         {},
	"aye":          {},
	"roger":        {},
	"confirmed":    {},
	"approved":     {},
	"agree":        {},
	"agreed":       {},
	"fine":         {},
	"great":        {},
	"perfect":      {},
	"awesome":      {},
	"sounds good":  {},
	"sounds great": {},
	"sounds fine":  {},
	"that's fine":  {},
	"thats fine":   {},
	"looks good":   {},
	"lgtm":         {},

	// Continuation nudges.
	"continue":   {},
	"go":         {},
	"go on":      {},
	"go ahead":   {},
	"proceed":    {},
	"carry on":   {},
	"keep going": {},
	"next":       {},
	"do it":      {},
	"do that":    {},
	"please do":  {},

	// "please"-qualified forms of the nudges.
	"please continue": {},
	"continue please": {},
	"please proceed":  {},
	"please go on":    {},
	"please go ahead": {},

	// Combined acknowledgment + nudge.
	"ok continue": {},
	"ok go ahead": {},

	// Gratitude acknowledgments (the previous task is done; its verdict rules).
	"thanks":       {},
	"thank you":    {},
	"thx":          {},
	"ok thanks":    {},
	"ok thank you": {},

	// Russian phrases: translations of the sets above plus common colloquial
	// synonyms. The normalizer folds ё→е ("вперёд" → "вперед") and strips
	// internal commas, so the written comma'd politeness forms ("продолжай,
	// пожалуйста") match without spelling variants in the set.

	// Acknowledgments.
	"да":               {},
	"ага":              {},
	"угу":              {},
	"ок":               {},
	"окей":             {},
	"конечно":          {},
	"есть":             {},
	"принято":          {},
	"вас понял":        {},
	"понял":            {},
	"поняла":           {},
	"подтверждаю":      {},
	"одобряю":          {},
	"одобрено":         {},
	"согласен":         {},
	"согласна":         {},
	"договорились":     {},
	"ладно":            {},
	"хорошо":           {},
	"норм":             {},
	"нормально":        {},
	"отлично":          {},
	"супер":            {},
	"круто":            {},
	"идеально":         {},
	"годится":          {},
	"подойдет":         {},
	"звучит хорошо":    {},
	"звучит отлично":   {},
	"звучит нормально": {},
	"выглядит хорошо":  {},
	"выглядит отлично": {},

	// Continuation nudges.
	"продолжай":         {},
	"продолжаем":        {},
	"продолжить":        {},
	"давай":             {},
	"го":                {},
	"погнали":           {},
	"поехали":           {},
	"давай дальше":      {},
	"действуй":          {},
	"вперед":            {},
	"не останавливайся": {},
	"дальше":            {},
	"следующий":         {},
	"следующее":         {},
	"сделай":            {},
	"сделай это":        {},
	"делай":             {},
	"так и сделай":      {},

	// "пожалуйста"-qualified forms (both word orders).
	"пожалуйста продолжай":  {},
	"продолжай пожалуйста":  {},
	"пожалуйста продолжаем": {},
	"пожалуйста действуй":   {},
	"действуй пожалуйста":   {},
	"пожалуйста давай":      {},
	"пожалуйста сделай":     {},
	"сделай пожалуйста":     {},

	// Combined acknowledgment + nudge.
	"да продолжай":     {},
	"хорошо продолжай": {},
	"ок продолжай":     {},
	"окей продолжай":   {},
	"да давай":         {},
	"хорошо давай":     {},
	"ок давай":         {},
	"окей давай":       {},

	// Gratitude acknowledgments.
	"спасибо":          {},
	"благодарю":        {},
	"спасибо большое":  {},
	"спс":              {},
	"спсб":             {},
	"спасиб":           {},
	"пасиб":            {},
	"ок спасибо":       {},
	"хорошо спасибо":   {},
	"ок благодарю":     {},
	"хорошо благодарю": {},
}

// isContinuationStyleMessage reports whether message is a short
// continuation-style utterance eligible for the routing fast-path. The trigger
// is deliberately conservative: a single line, at most continuationMaxRunes
// runes, no question/tool/code/quoting markers, and an exact match against
// continuationPhrases after normalization (lowercase, sentence punctuation and
// internal commas stripped, ’→' and ё→е folded, whitespace collapsed).
// Everything else is substantive.
func isContinuationStyleMessage(message string) bool {
	s := strings.ToLower(strings.TrimSpace(message))
	if s == "" || utf8.RuneCountInString(s) > continuationMaxRunes {
		return false
	}
	if strings.ContainsAny(s, continuationRejectRunes) {
		return false
	}
	// Trailing sentence punctuation is conversational noise ("ok!",
	// "continue."). Internal commas are the same noise — Russian politeness
	// forms REQUIRE them ("продолжай, пожалуйста"), so stripping them keeps
	// those forms on the fast-path; the residue still has to match a listed
	// phrase exactly, which the closed set alone guarantees. Typographic
	// apostrophes fold to ASCII ("that’s fine") and ё folds to е ("вперёд"),
	// the spelling pairs chat typing swaps freely.
	s = strings.Trim(s, "!.,…'\"")
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "’", "'")
	s = strings.ReplaceAll(s, "ё", "е")
	s = strings.Join(strings.Fields(s), " ")
	_, ok := continuationPhrases[s]
	return ok
}

// routingVerdictEntry is one cached routing verdict together with the
// identity it was captured under.
type routingVerdictEntry struct {
	sessionID string
	model     string
	decision  router.RoutingDecision
}

// routingVerdictCache is the orchestrator's single-slot cache of the routing
// verdict the session last ran with. The zero value is ready to use. One
// Orchestrator serves one session, so the slot is session-scoped by
// construction; the sessionID/model identity kept beside the decision makes
// the scope explicit and lets lookup() fail closed on any mismatch instead of
// trusting that invariant.
type routingVerdictCache struct {
	mu    sync.Mutex
	entry routingVerdictEntry
	valid bool
}

// note records the verdict the session last ran with, under its current
// identity (sessionID + active model).
func (c *routingVerdictCache) note(sessionID, model string, decision router.RoutingDecision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entry = routingVerdictEntry{sessionID: sessionID, model: model, decision: decision}
	c.valid = true
}

// lookup returns the cached verdict only when both the session and the active
// model match the capture-time identity. Any mismatch (session switch or
// model switch) invalidates the slot and reports a miss — a stale verdict is
// never served.
func (c *routingVerdictCache) lookup(sessionID, model string) (router.RoutingDecision, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid || c.entry.sessionID != sessionID || c.entry.model != model {
		c.valid = false
		return router.RoutingDecision{}, false
	}
	return c.entry.decision, true
}

// invalidate clears the slot. Every real routing attempt calls this before
// the router runs, so a routing failure cannot leave a pre-failure verdict
// consultable.
func (c *routingVerdictCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.valid = false
}

// noteRoutingVerdict records the routing verdict the session last ran with:
// a fresh router decision, the model-profiles unparseable-JSON fallback
// default, or a restored decision re-applied by the existing continuation
// fast-path. Nil-safe no-op.
func (o *Orchestrator) noteRoutingVerdict(sessionID string, decision *router.RoutingDecision) {
	if decision == nil {
		return
	}
	o.routingCache.note(sessionID, o.currentModel(), *decision)
}

// cachedRoutingVerdict returns the session's cached routing verdict when it
// is still valid for this session and the currently active model; any session
// or model switch invalidates it (reported as a miss).
func (o *Orchestrator) cachedRoutingVerdict(sessionID string) (*router.RoutingDecision, bool) {
	decision, ok := o.routingCache.lookup(sessionID, o.currentModel())
	if !ok {
		return nil, false
	}
	return &decision, true
}

// invalidateRoutingVerdict clears the cached verdict. Called by the routing
// stage (routeAndActivateSkills) on every real router run: the slot is
// repopulated on success, and stays empty after a routing failure so a
// follow-up continuation-style message cannot reuse a pre-failure verdict.
func (o *Orchestrator) invalidateRoutingVerdict() {
	o.routingCache.invalidate()
}
