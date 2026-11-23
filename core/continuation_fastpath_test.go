package core

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// ---------------------------------------------------------------------------
// Heuristic continuation fast-path tests (specs/domains/orchestration/router.md,
// "Continuation Fast-Path"): a continuation-style message reuses the session's
// previous routing verdict instead of paying the routing LLM round-trip.
// ---------------------------------------------------------------------------

const fastPathRoutingJSON = `{"domain":"code","complexity":3}`
const fastPathReroutingJSON = `{"domain":"research","complexity":4}`
const fastPathUnparseableJSON = `definitely not json <<<`

// countingRouterCaller answers every router request with the same fixed JSON
// content and counts the calls — the router-skip assertions key on it.
type countingRouterCaller struct {
	mu      sync.Mutex
	calls   int
	content string
}

func (c *countingRouterCaller) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return &llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: c.content}}, nil
}

func (c *countingRouterCaller) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// newFastPathOrchestrator builds an orchestrator whose router always answers
// with the given JSON content, plus its call counter and event spy.
func newFastPathOrchestrator(content string) (*Orchestrator, *countingRouterCaller, *spyEmitter) {
	caller := &countingRouterCaller{content: content}
	spy := &spyEmitter{}
	o := &Orchestrator{
		router: router.New(caller, router.Config{
			SystemPrompt:  "Tools: {{AVAILABLE-TOOLS}}\nMatching: {{TOOL-MATCHING}}",
			HistoryWindow: 5,
		}),
		emitter: spy,
	}
	return o, caller, spy
}

// TestIsContinuationStyleMessage pins the conservative trigger: exact matches
// from the closed phrase set (after normalization) pass; everything with a
// question, tool, code, or structure marker — and every longer or off-list
// message — stays substantive and must route.
func TestIsContinuationStyleMessage(t *testing.T) {
	accepted := []string{
		"continue", "Continue.", "CONTINUE", "ok", "OK!", "yes", "Yep.",
		"go ahead", "go on", "proceed!", "keep going", "carry on",
		"sounds good", "sounds great", "that's fine", "thats fine",
		"looks good", "lgtm", "next!", "do it", "please continue",
		"continue please", "please proceed", "ok thanks", "ok thank you",
		"thanks", "thank you", "  continue  ", "confirmed.", "agreed!",
		// Internal commas and typographic apostrophes normalize onto listed
		// phrases ("continue, please" → "continue please", "that’s" → "that's").
		"continue, please", "ok, continue", "ok, go ahead", "that’s fine",
		// Russian closed set: translations + colloquial synonyms; comma'd
		// politeness forms, both ё/е spellings, and case folding must match.
		"продолжай", "ПРОДОЛЖАЙ", "Продолжай.", "продолжаем", "продолжить",
		"да", "Да!", "ок", "окей", "ага", "угу", "конечно", "есть",
		"принято", "вас понял", "понял", "поняла", "подтверждаю",
		"одобряю", "одобрено", "согласен", "согласна", "договорились",
		"ладно", "хорошо", "норм", "нормально", "отлично", "супер",
		"круто", "идеально", "годится", "подойдет", "звучит хорошо",
		"выглядит хорошо", "давай", "го", "погнали", "поехали",
		"дальше", "вперед", "вперёд", "действуй", "сделай это", "делай",
		"так и сделай", "не останавливайся",
		"пожалуйста продолжай", "продолжай, пожалуйста",
		"продолжай пожалуйста", "пожалуйста действуй", "действуй, пожалуйста",
		"хорошо, продолжай", "да, давай", "ок, продолжай", "ок,  продолжай",
		"хорошо давай", "окей давай",
		"спасибо", "Спасибо!", "благодарю", "спасибо большое", "спс",
		"спсб", "спасиб", "пасиб", "ок спасибо", "хорошо спасибо",
		"ок, спасибо", "ок благодарю", "хорошо благодарю",
	}
	for _, m := range accepted {
		if !isContinuationStyleMessage(m) {
			t.Errorf("isContinuationStyleMessage(%q) = false, want true", m)
		}
	}

	rejected := []string{
		"", "   ",
		// Substantive work requests.
		"fix the failing test", "continue step 10", "continue with the plan",
		"now also fix the parser", "ok: run the tests",
		"great work on the refactor so far", "please fix the parser bug",
		// Question marker.
		"ok?", "what about the tests?", "continue?",
		// Tool / code / path / file-reference markers.
		"run /usr/bin/make", "ok `check`", "@bot continue", "ok #2",
		"see src/main.go", "ok *emphasis*",
		// Multi-line and structure.
		"continue\nwith step 2", "ok\tthanks",
		// Too long.
		"please continue with the implementation of the parser module",
		// Russian substantive work requests, and exact-match pins: prefixes,
		// extra words, quoting, and comma'd multi-phrase messages stay
		// substantive even after comma stripping.
		"исправь падающий тест", "продолжай с шага 10",
		"продолжить работу над парсером", "а что с тестами?",
		"запусти make", "спасибо, исправь парсер",
		"хорошо, но сначала исправь парсер", "продолжа", "да?",
		"«продолжай»", "that's fine, thanks",
	}
	for _, m := range rejected {
		if isContinuationStyleMessage(m) {
			t.Errorf("isContinuationStyleMessage(%q) = true, want false", m)
		}
	}
}

// TestRoutingVerdictCacheIdentityMismatchInvalidates pins the cache
// contract: a verdict is served only under the exact session+model identity
// it was noted with, and any mismatch or explicit invalidation clears the
// slot instead of serving a stale verdict.
func TestRoutingVerdictCacheIdentityMismatchInvalidates(t *testing.T) {
	var c routingVerdictCache

	// Empty cache: miss.
	if _, ok := c.lookup("s1", "m1"); ok {
		t.Fatal("empty cache must miss")
	}

	c.note("s1", "m1", router.RoutingDecision{Domain: "code", Complexity: 2})
	d, ok := c.lookup("s1", "m1")
	if !ok || d.Domain != "code" || d.Complexity != 2 {
		t.Fatalf("lookup(s1, m1) = (%+v, %v), want code/2, true", d, ok)
	}

	// Model switch: miss + invalidated.
	if _, ok := c.lookup("s1", "m2"); ok {
		t.Fatal("model switch must miss")
	}
	if _, ok := c.lookup("s1", "m1"); ok {
		t.Fatal("slot must stay invalidated after a model mismatch")
	}

	// Session switch: miss + invalidated.
	c.note("s1", "m1", router.RoutingDecision{Domain: "code", Complexity: 2})
	if _, ok := c.lookup("s2", "m1"); ok {
		t.Fatal("session switch must miss")
	}
	if _, ok := c.lookup("s1", "m1"); ok {
		t.Fatal("slot must stay invalidated after a session mismatch")
	}

	// Explicit invalidation.
	c.note("s1", "m1", router.RoutingDecision{Domain: "code", Complexity: 2})
	c.invalidate()
	if _, ok := c.lookup("s1", "m1"); ok {
		t.Fatal("invalidate must clear the slot")
	}
}

// TestRouteOrContinue_HeuristicFastPathReusesCachedVerdict covers the core
// acceptance criterion: after one real routing, a continuation-style message
// skips the router entirely and returns the cached verdict.
func TestRouteOrContinue_HeuristicFastPathReusesCachedVerdict(t *testing.T) {
	o, caller, spy := newFastPathOrchestrator(fastPathRoutingJSON)

	// First message: substantive, the real router runs and notes the verdict.
	_, first, _, _, err := o.routeOrContinue(
		context.Background(), "fix the failing test", "sess-1", HandleOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("initial routing failed: %v", err)
	}
	if first.Domain != "code" || first.Complexity != 3 {
		t.Fatalf("initial routing = %+v, want code/3", first)
	}
	if got := caller.callCount(); got != 1 {
		t.Fatalf("router calls after first message = %d, want 1", got)
	}

	// Continuation-style message: fast-path, no router call.
	_, second, _, _, err := o.routeOrContinue(
		context.Background(), "ok", "sess-1", HandleOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("fast-path routing failed: %v", err)
	}
	if got := caller.callCount(); got != 1 {
		t.Fatalf("router calls after continuation message = %d, want 1 (router must be skipped)", got)
	}
	if second.Domain != "code" || second.Complexity != 3 {
		t.Fatalf("reused routing = %+v, want the cached code/3", second)
	}

	// The reused verdict is announced exactly like a routing decision.
	mode, domain, complexity, ok := routingCall(spy)
	if !ok {
		t.Fatal("no Routing event emitted on the fast-path")
	}
	if mode != "conductor" || domain != "code" || complexity != "3" {
		t.Errorf("Routing event = (%q, %q, %q), want (conductor, code, 3)", mode, domain, complexity)
	}
	var fastPathNotice bool
	for _, c := range spy.calls {
		if c.method == "ServiceWithMeta" && len(c.args) >= 1 {
			if msg, _ := c.args[0].(string); msg == "Continuation (routing skipped — reused previous routing decision)" {
				fastPathNotice = true
			}
		}
	}
	if !fastPathNotice {
		t.Error("no fast-path service notice emitted")
	}
}

// TestRouteOrContinue_SubstantiveMessageReroutesAndRefreshesCache pins the
// bounded-misroute window: a substantive message always routes and the cache
// is refreshed, so the following continuation-style message reuses the NEW
// verdict.
func TestRouteOrContinue_SubstantiveMessageReroutesAndRefreshesCache(t *testing.T) {
	caller := &countingRouterCaller{content: fastPathRoutingJSON}
	o := &Orchestrator{
		router: router.New(caller, router.Config{
			SystemPrompt:  "Tools: {{AVAILABLE-TOOLS}}\nMatching: {{TOOL-MATCHING}}",
			HistoryWindow: 5,
		}),
		emitter: &spyEmitter{},
	}

	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "fix the failing test", "sess-1", HandleOptions{}, nil, nil); err != nil {
		t.Fatalf("first routing failed: %v", err)
	}

	// Substantive follow-up: the router runs again and the verdict changes.
	caller.content = fastPathReroutingJSON
	_, second, _, _, err := o.routeOrContinue(
		context.Background(), "now also fix the parser", "sess-1", HandleOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("second routing failed: %v", err)
	}
	if second.Domain != "research" || second.Complexity != 4 {
		t.Fatalf("re-routing = %+v, want research/4", second)
	}
	if got := caller.callCount(); got != 2 {
		t.Fatalf("router calls after substantive message = %d, want 2", got)
	}

	// The next continuation-style message reuses the refreshed verdict.
	_, third, _, _, err := o.routeOrContinue(
		context.Background(), "ok", "sess-1", HandleOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("fast-path routing failed: %v", err)
	}
	if got := caller.callCount(); got != 2 {
		t.Fatalf("router calls after follow-up continuation = %d, want 2", got)
	}
	if third.Domain != "research" || third.Complexity != 4 {
		t.Fatalf("reused routing = %+v, want the refreshed research/4", third)
	}
}

// TestRouteOrContinue_CachedVerdictInvalidatedOnModelSwitch: a model switch
// between messages invalidates the cached verdict — the next
// continuation-style message routes fully under the new model.
func TestRouteOrContinue_CachedVerdictInvalidatedOnModelSwitch(t *testing.T) {
	o, caller, _ := newFastPathOrchestrator(fastPathRoutingJSON)
	o.setCurrentModel("model-a")

	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "fix the failing test", "sess-1", HandleOptions{}, nil, nil); err != nil {
		t.Fatalf("first routing failed: %v", err)
	}

	// Model switch (as ApplyRequestOverrides performs it).
	o.setCurrentModel("model-b")

	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "ok", "sess-1", HandleOptions{}, nil, nil); err != nil {
		t.Fatalf("post-switch routing failed: %v", err)
	}
	if got := caller.callCount(); got != 2 {
		t.Fatalf("router calls after model switch = %d, want 2 (cache must not serve)", got)
	}

	// The re-noted verdict serves again under the stable model.
	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "continue", "sess-1", HandleOptions{}, nil, nil); err != nil {
		t.Fatalf("fast-path routing failed: %v", err)
	}
	if got := caller.callCount(); got != 2 {
		t.Fatalf("router calls after re-note = %d, want 2", got)
	}
}

// TestRouteOrContinue_CachedVerdictInvalidatedOnSessionSwitch: a verdict noted
// for one session never serves another; the new session re-routes and then
// builds its own cache.
func TestRouteOrContinue_CachedVerdictInvalidatedOnSessionSwitch(t *testing.T) {
	o, caller, _ := newFastPathOrchestrator(fastPathRoutingJSON)

	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "fix the failing test", "sess-1", HandleOptions{}, nil, nil); err != nil {
		t.Fatalf("first routing failed: %v", err)
	}

	// Session switch: "ok" belongs to a different session — full route.
	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "ok", "sess-2", HandleOptions{}, nil, nil); err != nil {
		t.Fatalf("routing after session switch failed: %v", err)
	}
	if got := caller.callCount(); got != 2 {
		t.Fatalf("router calls after session switch = %d, want 2", got)
	}

	// Same session again: fast-path.
	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "ok", "sess-2", HandleOptions{}, nil, nil); err != nil {
		t.Fatalf("fast-path routing failed: %v", err)
	}
	if got := caller.callCount(); got != 2 {
		t.Fatalf("router calls on same-session follow-up = %d, want 2", got)
	}

	// Switching back: the stale verdict must not serve — full route.
	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "ok", "sess-1", HandleOptions{}, nil, nil); err != nil {
		t.Fatalf("routing after switch-back failed: %v", err)
	}
	if got := caller.callCount(); got != 3 {
		t.Fatalf("router calls after switch-back = %d, want 3", got)
	}
}

// TestRouteOrContinue_GoalModeNeverTakesHeuristicFastPath: goal mode owns its
// routing — even a continuation-style message routes fully when Goal is set.
func TestRouteOrContinue_GoalModeNeverTakesHeuristicFastPath(t *testing.T) {
	o, caller, _ := newFastPathOrchestrator(fastPathRoutingJSON)

	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "plan the refactor", "sess-1", HandleOptions{}, nil, nil); err != nil {
		t.Fatalf("first routing failed: %v", err)
	}

	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "ok", "sess-1", HandleOptions{Goal: true}, nil, nil); err != nil {
		t.Fatalf("goal-mode routing failed: %v", err)
	}
	if got := caller.callCount(); got != 2 {
		t.Fatalf("router calls in goal mode = %d, want 2 (heuristic fast-path must not apply)", got)
	}
}

// TestRouteOrContinue_RoutingFailureLeavesNoCachedVerdict: after a failed
// routing stage the pre-failure verdict is invalidated — a follow-up
// continuation-style message cannot silently reuse it.
func TestRouteOrContinue_RoutingFailureLeavesNoCachedVerdict(t *testing.T) {
	o, caller, _ := newFastPathOrchestrator(fastPathUnparseableJSON)

	_, _, _, _, err := o.routeOrContinue(
		context.Background(), "fix the failing test", "sess-1", HandleOptions{}, nil, nil)
	if !errors.Is(err, router.ErrRoutingParse) {
		t.Fatalf("routing failure = %v, want router.ErrRoutingParse", err)
	}
	// One full routing attempt costs 3 caller calls (the engine's repair
	// cycle: two repair retries, three attempts total).
	const routingCycleCalls = 3
	if got := caller.callCount(); got != routingCycleCalls {
		t.Fatalf("router calls after failure = %d, want %d", got, routingCycleCalls)
	}

	// The failed routing invalidated the slot: "ok" routes (and fails) again
	// instead of reusing the pre-failure verdict.
	_, _, _, _, err = o.routeOrContinue(
		context.Background(), "ok", "sess-1", HandleOptions{}, nil, nil)
	if !errors.Is(err, router.ErrRoutingParse) {
		t.Fatalf("post-failure routing = %v, want a fresh router.ErrRoutingParse", err)
	}
	if got := caller.callCount(); got != 2*routingCycleCalls {
		t.Fatalf("router calls after failure + continuation = %d, want %d (no stale reuse)",
			got, 2*routingCycleCalls)
	}
}

// TestRouteOrContinue_HeuristicFastPathOnContinuationTask: a restored task
// without a persisted plan/routing (the task-continuation fast-path declines)
// still takes the heuristic fast-path on a continuation-style message.
func TestRouteOrContinue_HeuristicFastPathOnContinuationTask(t *testing.T) {
	o, caller, _ := newFastPathOrchestrator(fastPathRoutingJSON)

	if _, _, _, _, err := o.routeOrContinue(
		context.Background(), "fix the failing test", "sess-1", HandleOptions{}, nil, nil); err != nil {
		t.Fatalf("first routing failed: %v", err)
	}

	opts := HandleOptions{TaskID: "task-1"}
	_, second, _, _, err := o.routeOrContinue(
		context.Background(), "ok", "sess-1", opts, nil, nil)
	if err != nil {
		t.Fatalf("continuation-task routing failed: %v", err)
	}
	if got := caller.callCount(); got != 1 {
		t.Fatalf("router calls on continuation-task follow-up = %d, want 1", got)
	}
	if second.Domain != "code" || second.Complexity != 3 {
		t.Fatalf("reused routing = %+v, want the cached code/3", second)
	}
}

// routedPersistableTestBB overrides testPersistableBlackboard's fixed
// Routing() so the task-continuation fast-path (restored plan + decision)
// can be exercised.
type routedPersistableTestBB struct {
	testPersistableBlackboard
	routing *router.RoutingDecision
}

func (t *routedPersistableTestBB) Routing() *router.RoutingDecision { return t.routing }

var _ PersistableBlackboard = (*routedPersistableTestBB)(nil)

// TestRouteOrContinue_RestoredFastPathPrecedesAndNotes: the restored-task
// fast-path keeps precedence over the heuristic one (zero router calls), and
// the restored verdict it re-applies is noted — so a subsequent plain
// continuation-style message takes the heuristic fast-path without routing.
func TestRouteOrContinue_RestoredFastPathPrecedesAndNotes(t *testing.T) {
	o, caller, _ := newFastPathOrchestrator(fastPathRoutingJSON)

	mb := orchestration.NewMapBlackboard()
	mb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1", Summary: "step one"}}})
	bb := &routedPersistableTestBB{
		testPersistableBlackboard: testPersistableBlackboard{MapBlackboard: mb, taskID: "task-1"},
		routing:                   &router.RoutingDecision{Domain: "code", Complexity: 3},
	}

	opts := HandleOptions{TaskID: "task-1"}
	_, restored, _, _, err := o.routeOrContinue(
		context.Background(), "continue step 1", "sess-1", opts, bb, nil)
	if err != nil {
		t.Fatalf("restored fast-path failed: %v", err)
	}
	if got := caller.callCount(); got != 0 {
		t.Fatalf("router calls on restored fast-path = %d, want 0", got)
	}
	if restored.Domain != "code" || restored.Complexity != 3 {
		t.Fatalf("restored routing = %+v, want code/3", restored)
	}

	// The restored verdict was noted: a fresh plain follow-up reuses it.
	_, followUp, _, _, err := o.routeOrContinue(
		context.Background(), "ok", "sess-1", HandleOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("follow-up routing failed: %v", err)
	}
	if got := caller.callCount(); got != 0 {
		t.Fatalf("router calls after restored fast-path follow-up = %d, want 0", got)
	}
	if followUp.Domain != "code" || followUp.Complexity != 3 {
		t.Fatalf("follow-up routing = %+v, want the noted code/3", followUp)
	}
}
