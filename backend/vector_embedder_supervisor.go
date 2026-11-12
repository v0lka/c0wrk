// vector_embedder_supervisor.go — the embedder supervisor behind the
// restartable vector-index launcher. Every batch embed call is bounded by a
// constant deadline; an expiry returns a TRANSIENT error and consults the
// authoritative liveness probe (sp4rk embedding.Embedder.Wedged over the
// ONNX worker's per-job heartbeat) to decide whether the expiry was a
// genuine wedge — an ONNX inference stuck inside a C call that no ctx can
// interrupt. A confirmed wedge restarts the embedder on a FRESH instance
// (new instance = new inference mutex, so new calls never queue behind the
// wedged one's Embedder.mu, which its stranded caller holds forever); after
// embedCPUFallbackAfterRestarts consecutive FAILED restart attempts the next
// restart uses the CPU execution provider (the operator decision cpu_after_n).
//
// The wedged verdict is never inferred from consecutive expired waits
// (step_10): the timeout bound stays a pure backstop, and a nil/absent probe
// degrades the whole mechanism to plain timeout semantics.
package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/embedding"
)

const (
	// embedCallDeadline bounds one batch embed call. The ONNX inference
	// itself is uninterruptible, so this is NOT a cancellation: expiry means
	// the caller gets a transient error while the underlying job keeps
	// flowing on the instance it captured. ~3× sp4rk's slow-job evidence
	// threshold, comfortably above the largest realistic batch on the CPU
	// provider, and tight enough that a wedged thread surfaces within
	// minutes instead of stalling an indexing pass forever.
	embedCallDeadline = 90 * time.Second

	// embedCPUFallbackAfterRestarts is the operator decision cpu_after_n:
	// after this many CONSECUTIVE failed restart attempts the next restart
	// stops retrying the configured execution provider and comes back on
	// the CPU provider instead. Any successful restart resets the counter
	// (a later wedge on the fresh instance starts the ladder over).
	embedCPUFallbackAfterRestarts = 2
)

var (
	// errEmbedCallTimeout marks the bounded call's deadline expiry. It is
	// deliberately distinct from ctx cancellation: the pass that receives it
	// may retry on the (possibly restarted) embedder, and sp4rk's heartbeat
	// — not this error — decides whether a restart actually happens.
	errEmbedCallTimeout = errors.New("embed call deadline exceeded (transient; the embedder is restarted when the ONNX thread is confirmed wedged)")

	// errEmbedderStopped is the post-close surface: the supervisor's close
	// (the registry ShutdownAll path) nil-ed the live instance.
	errEmbedderStopped = errors.New("vector embedder is unavailable (stopped or restarting)")
)

// liveEmbedder is the instance surface the supervisor drives. *embedding.
// Embedder satisfies it directly; the interface exists so tests can supply
// fakes without loading the ONNX runtime.
type liveEmbedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	Wedged(bound time.Duration) bool
	ExecutionProvider() string
	Close() error
}

// Compile-time proof of the two seams the launcher wires: the sp4rk embedder
// is a valid supervised instance, and the supervisor is a valid
// ManagerConfig.BatchEmbedder.
var (
	_ liveEmbedder              = (*embedding.Embedder)(nil)
	_ vectorindex.BatchEmbedder = (*embedderSupervisor)(nil)
)

// embedderSupervisor owns the LIVE embedding.Embedder instance for one
// launcher init and bounds every batch embed call routed through it.
//
// Lock discipline (the mechanism's core invariant): mu guards ONLY the
// pointer-level fields below and is never held across an embed wait or an
// embedder build. A bounded call snapshots the live instance under mu,
// releases mu, and selects on the result/timer channels — so a restart
// running concurrently never deadlocks behind it, and a restart in flight
// never blocks new calls (they snapshot whatever instance is current,
// including the fresh one the moment it lands).
type embedderSupervisor struct {
	log *slog.Logger

	// newEmbedder builds a supervised instance with the given execution
	// provider (restart path). Production default derives from the init's
	// EmbedderConfig with only the provider overridden; tests replace it.
	newEmbedder func(provider string) (liveEmbedder, error)

	// wedgedProbe is the authoritative liveness probe consulted after a
	// deadline expiry, with the embed deadline as its bound. Production
	// default reads the LIVE instance's sp4rk heartbeat (Embedder.Wedged);
	// nil degrades to plain timeout semantics (transient error, no
	// restart) — the same nil-probe contract as ManagerConfig.EmbedderWedged.
	wedgedProbe func(bound time.Duration) bool

	// shuttingDown reports "the GUI host is going away"; a restart is
	// pointless teardown work then and is skipped. Nil-safe.
	shuttingDown func() bool

	// onSwapped observes every instance swap (restart success) so the
	// launcher keeps the ADR-045 requested/effective provider facts honest.
	// The fresh instance rides along: per-instance probe verdicts
	// (CUDAVerified, OVDevice) belong to the instance they ran on and must
	// be re-derived for — or dropped from — the successor. Nil-safe; called
	// outside mu.
	onSwapped func(provider string, emb liveEmbedder)

	mu              sync.Mutex // guards the fields below; NEVER held across a wait
	cur             liveEmbedder
	stopped         bool
	restarting      bool
	restartFailures int // consecutive failed restart attempts; reset on success
	// requestedProvider is the first-choice provider for restart attempts
	// (the config-resolved provider of this init).
	requestedProvider string

	// deadline is the per-call bound (embedCallDeadline in production);
	// a field so tests can shrink it without virtual-clock plumbing.
	deadline time.Duration
	// cpuAfter is the failed-restart threshold for the CPU fallback
	// (embedCPUFallbackAfterRestarts in production).
	cpuAfter int
	// newTimer is the virtual-time seam behind the bounded select.
	newTimer func(d time.Duration) (<-chan time.Time, stopFunc)

	// probeWG counts the fire-and-forget probe goroutines launched by
	// timed-out calls. Production never waits on it; virtual-time tests
	// join it to observe a wedge's consequences deterministically.
	probeWG sync.WaitGroup
}

// stopFunc stops a pending deadline timer so a completed call does not leave
// a live timer behind per embed.
type stopFunc func()

// embedderSupervisorConfig carries the constructor seams; every func field is
// nil-safe.
type embedderSupervisorConfig struct {
	Log               *slog.Logger
	Initial           liveEmbedder
	EmbedderCfg       embedding.EmbedderConfig // restart template; provider overridden per attempt
	RequestedProvider string                   // first-choice provider for restart attempts
	ShuttingDown      func() bool
	OnSwapped         func(provider string, emb liveEmbedder)
}

// newEmbedderSupervisor wraps the init's freshly built embedder. The instance
// starts as Initial; restarts build fresh instances through the config
// template.
func newEmbedderSupervisor(cfg embedderSupervisorConfig) *embedderSupervisor {
	sup := &embedderSupervisor{
		log:               cfg.Log,
		cur:               cfg.Initial,
		requestedProvider: cfg.RequestedProvider,
		shuttingDown:      cfg.ShuttingDown,
		onSwapped:         cfg.OnSwapped,
		deadline:          embedCallDeadline,
		cpuAfter:          embedCPUFallbackAfterRestarts,
		newTimer:          defaultNewTimer,
	}
	sup.newEmbedder = func(provider string) (liveEmbedder, error) {
		c := cfg.EmbedderCfg
		c.ExecutionProvider = provider
		return embedding.NewEmbedder(c)
	}
	sup.wedgedProbe = func(bound time.Duration) bool {
		sup.mu.Lock()
		cur := sup.cur
		sup.mu.Unlock()
		if cur == nil {
			return false
		}
		return cur.Wedged(bound)
	}
	return sup
}

// defaultNewTimer is the production timer seam: a real timer, stopped
// promptly when the call completes so per-call timers never accumulate.
func defaultNewTimer(d time.Duration) (<-chan time.Time, stopFunc) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// embedResult is what the detached call goroutine delivers.
type embedResult struct {
	vecs [][]float32
	err  error
}

// EmbedDocuments is the ManagerConfig.BatchEmbedder surface: the bounded
// batch embed call. The live instance is snapshotted under mu and the actual
// call runs detached — a wedged inference (which ignores ctx and holds the
// instance's mutex forever) can therefore never stall THIS caller past the
// deadline, and the detached goroutine keeps flowing on the instance it
// captured even after a restart swaps the live pointer ("the old wedged call
// flows on the old instance; new instance = new mu").
func (sup *embedderSupervisor) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	sup.mu.Lock()
	cur := sup.cur
	sup.mu.Unlock()
	if cur == nil {
		return nil, errEmbedderStopped
	}

	resCh := make(chan embedResult, 1) // buffered: the detached call always delivers, even abandoned
	go func() {
		vecs, err := cur.EmbedDocuments(ctx, texts)
		resCh <- embedResult{vecs: vecs, err: err}
	}()

	timerCh, stopTimer := sup.newTimer(sup.deadline)
	select {
	case res := <-resCh:
		stopTimer()
		return res.vecs, res.err
	case <-ctx.Done():
		stopTimer()
		return nil, fmt.Errorf("batch embed call cancelled: %w", ctx.Err())
	case <-timerCh:
		// Transient surface: the detached goroutine keeps the deadline's
		// context, so a merely-slow job still stops at its next chunk
		// boundary when the caller cancels; a genuinely wedged one flows
		// until the ONNX thread dies with the process.
		stopTimer()
		sup.onEmbedTimeout(len(texts))
		return nil, fmt.Errorf("batch embed call exceeded %s (texts=%d): %w", sup.deadline, len(texts), errEmbedCallTimeout)
	}
}

// onEmbedTimeout is the post-expiry diagnosis path, detached exactly like
// step_10's consumer contract prescribes: consult the authoritative Wedged
// probe (with the embed deadline as the bound) and restart at most once per
// wedge — the in-flight guard collapses every concurrent expiry during one
// wedge into a single restart, and the fresh instance's idle heartbeat can
// never re-trigger the old wedge. Nothing is held across any of it.
func (sup *embedderSupervisor) onEmbedTimeout(texts int) {
	sup.log.Warn("batch embed call exceeded its deadline; returning a transient error",
		"deadline", sup.deadline, "texts", texts,
		"hint", "the embedder restarts on a fresh instance if the ONNX thread is confirmed wedged")
	sup.probeWG.Add(1)
	go func() {
		defer sup.probeWG.Done()
		sup.probeAndRestart()
	}()
}

// probeAndRestart consults the wedge probe and conditionally restarts. A nil
// probe (or a not-wedged verdict — a slow-but-alive batch) leaves plain
// timeout semantics: the transient error stands, no restart is attempted.
func (sup *embedderSupervisor) probeAndRestart() {
	if sup.wedgedProbe == nil {
		sup.log.Debug("no embedder liveness probe wired; the embed timeout keeps plain timeout semantics (no restart)")
		return
	}
	if !sup.wedgedProbe(sup.deadline) {
		sup.log.Debug("embed call timed out but the ONNX thread is not wedged; no restart",
			"bound", sup.deadline)
		return
	}
	sup.tryRestart()
}

// tryRestart swaps in a fresh embedder instance. mu is taken only around the
// bookkeeping: the embedder build itself — the slow part — runs unlocked, so
// bounded calls and concurrent restart attempts never queue behind it. The
// provider ladder is the operator decision cpu_after_n: the requested
// provider until embedCPUFallbackAfterRestarts consecutive failures, then the
// CPU provider.
func (sup *embedderSupervisor) tryRestart() {
	if sup.shuttingDown != nil && sup.shuttingDown() {
		sup.log.Debug("embedder restart skipped: the app is shutting down")
		return
	}

	sup.mu.Lock()
	if sup.stopped || sup.restarting {
		// stopped: the registry already closed the supervisor. restarting:
		// another expiry of the SAME wedge already triggered the one
		// restart this wedge gets.
		sup.mu.Unlock()
		return
	}
	sup.restarting = true
	failures := sup.restartFailures
	useCPU := failures >= sup.cpuAfter
	provider := sup.requestedProvider
	if useCPU {
		provider = embedding.ExecutionProviderCPU
	}
	sup.mu.Unlock()

	if useCPU {
		sup.log.Warn("vector embedder restart falling back to the CPU execution provider",
			"failedRestarts", failures, "threshold", sup.cpuAfter)
	}
	sup.log.Info("restarting the vector embedder after a wedged batch embed call",
		"provider", provider, "attempt", failures+1)

	// Lock-free section: an embedder build takes seconds and must not block
	// embed-call snapshots or a concurrent close.
	emb, err := sup.newEmbedder(provider)

	sup.mu.Lock()
	sup.restarting = false
	if sup.stopped {
		// Close raced the restart: the fresh instance has no owner —
		// release it outside mu rather than swapping it into a dead
		// supervisor (which would leak it past ShutdownAll).
		sup.mu.Unlock()
		if err == nil {
			_ = emb.Close()
		}
		sup.log.Debug("embedder restart landed after close; fresh instance released", "provider", provider)
		return
	}
	if err != nil {
		sup.restartFailures++
		failed := sup.restartFailures
		sup.mu.Unlock()
		sup.log.Warn("vector embedder restart failed; the wedged instance stays live",
			"error", err, "provider", provider, "failedRestarts", failed)
		return
	}
	effective := emb.ExecutionProvider()
	sup.cur = emb
	sup.restartFailures = 0 // counters reset on success: a later wedge starts the ladder over
	sup.mu.Unlock()
	sup.log.Info("vector embedder restarted on a fresh instance",
		"provider", effective, "failedRestartsReset", true)
	if sup.onSwapped != nil {
		sup.onSwapped(effective, emb)
	}
}

// wedged is the ManagerConfig.EmbedderWedged surface (the Shutdown drain's
// diagnosis seam): the authoritative verdict of the LIVE instance, probed
// with the embed call deadline as the bound — the same healthy-inference
// envelope probeAndRestart uses, and deliberately NOT the drain-grace wait
// the caller just expired: healthy inference can legitimately run tens of
// seconds past a short shutdown grace, so a job that merely outlived the
// grace must not be diagnosed as a wedge. After a restart it reports the
// fresh instance — the drain then diagnoses the wedge that actually
// matters, not the stranded one.
func (sup *embedderSupervisor) wedged() bool {
	sup.mu.Lock()
	cur := sup.cur
	sup.mu.Unlock()
	if cur == nil {
		return false
	}
	return cur.Wedged(sup.deadline)
}

// queryEmbedResult is what the detached query-embed goroutine delivers.
type queryEmbedResult struct {
	vec []float32
	err error
}

// queryEmbeddingFunc is the ManagerConfig.EmbeddingFunc surface: the chromem
// query-side embedding function resolving the LIVE instance per call, so a
// post-restart query embeds on the fresh instance instead of queuing behind
// the wedged one's mutex. The call itself runs detached and races the
// caller's ctx: sp4rk's EmbedQuery→EmbedDocuments parks on the instance
// mutex and honors ctx only once the lock is held, so a query that resolved
// the wedged instance would otherwise park past any deadline — an unbounded
// RPC. The race makes the bound every real caller already carries effective
// (the vector RPC's SearchWaitTimeout covers the whole search including this
// embed; the RAG hint path spends one deadline on both its stages). A
// raced-out call strands one goroutine on the wedged instance until the
// process exits — the same stranded-work stance as the restart ladder — and
// drives no probe/restart of its own: restarts stay batch-driven (a query
// parking behind a wedge is collateral of a wedged batch, whose expiry runs
// the restart).
func (sup *embedderSupervisor) queryEmbeddingFunc() func(ctx context.Context, text string) ([]float32, error) {
	return func(ctx context.Context, text string) ([]float32, error) {
		sup.mu.Lock()
		cur := sup.cur
		sup.mu.Unlock()
		if cur == nil {
			return nil, errEmbedderStopped
		}
		resCh := make(chan queryEmbedResult, 1) // buffered: the detached call always delivers, even abandoned
		go func() {
			vec, err := cur.EmbedQuery(ctx, text)
			resCh <- queryEmbedResult{vec: vec, err: err}
		}()
		select {
		case res := <-resCh:
			return res.vec, res.err
		case <-ctx.Done():
			return nil, fmt.Errorf("query embed abandoned: %w", ctx.Err())
		}
	}
}

// close is the registry's closeEmbedder seam (ShutdownAll, after every
// manager is down): it marks the supervisor stopped (refusing further
// restarts) and closes the LIVE instance — outside mu, so even a teardown
// wedging on the ONNX thread never blocks the supervisor's other paths.
// Instances stranded by earlier restarts are intentionally NOT closed: their
// inference thread is wedged inside a C call (Close would deadlock behind it
// — sp4rk dispatches teardown through the same worker), and that thread dies
// only with the process.
//
// The LIVE instance gets the same protection, because it can be wedged too
// (the wedge that has not triggered a restart yet, or one on the fresh
// instance itself): sp4rk's Close parks on the instance's e.mu, which a
// wedged inference holds forever, so a synchronous Close would freeze
// ShutdownAll — and with it app quit. Two bounds therefore apply: a wedged
// verdict (the authoritative "this Close would never return") skips the
// Close entirely, and the remaining call races the embed deadline — a job
// younger than the wedge bound that still never returns must not freeze the
// quit either; the losing Close goroutine is stranded exactly like the
// restart ladder's abandoned work and dies with the process.
func (sup *embedderSupervisor) close() error {
	sup.mu.Lock()
	sup.stopped = true
	cur := sup.cur
	sup.cur = nil
	sup.mu.Unlock()
	if cur == nil {
		return nil
	}
	if cur.Wedged(sup.deadline) {
		sup.log.Warn("vector embedder close skipped: the ONNX inference thread is wedged and its Close would never return; the thread dies with the process",
			"deadline", sup.deadline)
		return nil
	}
	done := make(chan error, 1) // buffered: the abandoned Close always delivers, even after the race is lost
	go func() {
		done <- cur.Close()
	}()
	timerCh, stopTimer := sup.newTimer(sup.deadline)
	defer stopTimer()
	select {
	case err := <-done:
		return err
	case <-timerCh:
		sup.log.Warn("vector embedder close timed out; a wedged or long-running inference still holds the instance — the embedder dies with the process",
			"deadline", sup.deadline)
		return nil
	}
}
