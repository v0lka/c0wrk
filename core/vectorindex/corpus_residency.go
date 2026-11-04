package vectorindex

// Corpus residency — ADR-104 §5 over the stage-2 owner/view shape: the
// shared owner RETAINS zero-reference corpus handles in an LRU bounded by
// COUNT (handles) and BYTES (estimated resident footprint), so a root
// returning to a corpus it recently viewed (A→B→A) re-attaches the SAME
// handle object — no chromem re-decode, no lexical reopen — while the
// process's retained-corpus memory stays bounded. Retention is OFF by
// default (0/0): the pre-residency behavior (a zero-ref handle closes at
// the last release) is exactly what a default config produces.
//
// Accounting (the ADR-104 §5 ledger rules, stage-2 form):
//
//   - Shared data is counted ONCE. A handle's estimate is a property of the
//     HANDLE, not of its references: N roots viewing one corpus, a parked
//     service that just released it, and the LRU slot it occupies all see
//     the same single entry in the handle map — there is nothing to
//     double-count between roots, parking, and view slots.
//   - Root-local deltas (each root's overlay working store) are NOT part of
//     this ledger: they stay bounded by the park budget (estimateStateBytes
//     sizes a parked state from its OVERLAY collection; the corpus handle
//     rides outside it) and by the per-root manager LRU.
//   - The bounds govern RETENTION, not admission: an attached view is
//     pinned by its refcount (the read lease) and is never evicted, so an
//     active working set that outgrows the budget stays resident. The
//     budget is an honest retention bound, NOT a hard RSS cap — no surface
//     may present a budget-compliant report as one.
//
// Eviction reuses the park mechanics (ADR-049): the oldest ELIGIBLE
// (zero-ref) handle is closed (lexical reader released, decode dropped),
// enforcement runs at the state change that overflowed the bound, and the
// freed memory is nudged back to the OS through the freeOSMemory seam
// asynchronously — never under the store lock.

import "runtime/debug"

// estimateCorpusHandleBytes approximates one retained corpus handle's
// resident memory: one embedding vector (manifest dims float32s) plus a
// coarse ~1 KiB per document (decoded chromem document + read-only lexical
// entry) — the same shape as estimateStateBytes, applied to the SHARED
// corpus (counted once per handle, never per attached root). Package-variable
// test seam mirroring estimateStateBytes — tests simulate multi-hundred-MiB
// corpora without materializing them; production must never reassign it.
var estimateCorpusHandleBytes = func(h *CorpusHandle) int64 {
	if h == nil {
		return 0
	}
	count := 0
	switch {
	case h.v2 != nil:
		count = h.v2.count() // §3 form: chunk occurrences are the documents
	case h.collection != nil:
		count = h.collection.Count()
	default:
		return 0
	}
	dims := 0
	if h.manifest != nil {
		dims = h.manifest.Dims
	}
	return int64(count) * (int64(dims)*4 + 1024)
}

// retainedBytesLocked sums the estimated bytes of the RETAINED handles
// (each key once — the shared-data-counted-once rule). Attached handles are
// not part of the retention ledger. The caller must hold cs.mu.
func (cs *corpusStore) retainedBytesLocked() int64 {
	var total int64
	for _, key := range cs.lru {
		if h, ok := cs.handles[key]; ok {
			total += estimateCorpusHandleBytes(h)
		}
	}
	return total
}

// enforceResidencyLocked brings the retained set inside the configured
// bounds, closing the OLDEST eligible (zero-ref) handles first — including
// the just-retained one when it alone exceeds the byte budget (the park
// LRU's oversized semantics). Attached handles are never candidates: a
// working set above the bound stays resident (honest over-budget state, not
// a hard cap). The caller must hold cs.mu.
func (cs *corpusStore) enforceResidencyLocked() {
	if len(cs.lru) == 0 {
		return
	}
	evicted := false
	for len(cs.lru) > 0 {
		over := false
		if cs.residencyEnabled() {
			switch {
			case cs.resCapacity > 0 && len(cs.lru) > cs.resCapacity:
				over = true // count bound
			case cs.resBudget > 0 && cs.retainedBytesLocked() > cs.resBudget:
				over = true // byte bound
			}
		}
		if !over {
			break
		}
		key := cs.lru[0]
		cs.lru = cs.lru[1:]
		if h, ok := cs.handles[key]; ok {
			delete(cs.handles, key)
			cs.closeHandleLocked(h)
			evicted = true
		}
	}
	if evicted {
		cs.scheduleFreeLocked()
	}
}

// scheduleFreeLocked nudges the OS to reclaim the evicted corpora's memory
// through the registry's freeOSMemory seam. The call is dispatched to its
// own goroutine so the seam itself never runs under cs.mu (a full GC cycle
// is stop-the-world; the lock must not wait for it) — the same posture as
// Service.parkCurrentLocked. The caller must hold cs.mu.
func (cs *corpusStore) scheduleFreeLocked() {
	free := debug.FreeOSMemory
	if cs.reg != nil {
		free = cs.reg.freeOSMemory
	}
	if free == nil {
		return
	}
	go free()
}

// applyResidency installs new bounds and immediately enforces them (a
// shrunk budget evicts on the spot; a disabled policy closes every retained
// handle).
func (cs *corpusStore) applyResidency(capacity int, budgetBytes int64) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.resCapacity = capacity
	cs.resBudget = budgetBytes
	if !cs.residencyEnabled() {
		// Retention off: close everything retained (the disabled policy is
		// not just "stop retaining new ones" — the LRU must not outlive its
		// budget).
		for len(cs.lru) > 0 {
			key := cs.lru[0]
			cs.lru = cs.lru[1:]
			if h, ok := cs.handles[key]; ok {
				delete(cs.handles, key)
				cs.closeHandleLocked(h)
			}
		}
		return
	}
	cs.enforceResidencyLocked()
}

// teardown closes every retained handle and retires the still-attached
// ones: their holders keep reading (the refcount pin is honored to the
// end), but the LAST release now closes instead of retaining — a torn-down
// store's corpora never re-enter residency. Used by DeleteStore and Close.
func (cs *corpusStore) teardown() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for len(cs.lru) > 0 {
		key := cs.lru[0]
		cs.lru = cs.lru[1:]
		if h, ok := cs.handles[key]; ok {
			delete(cs.handles, key)
			cs.closeHandleLocked(h)
		}
	}
	for _, h := range cs.handles {
		h.retired = true
	}
}

// SetResidency installs the process-wide corpus-retention policy: at most
// `capacity` zero-reference corpus handles stay resident, their summed
// estimated bytes at most `budgetBytes`. Both zero (the default) disables
// retention — zero-ref handles close immediately. A single non-zero bound
// applies alone (count-only or byte-only); both apply together when set.
// Applying is idempotent and immediately enforced against what is already
// retained. The factory re-applies the live config on every manager build,
// so a saved settings change lands with the next build (UpdateIndexingSettings
// rebuilds the focus root) — no restart needed.
func (r *CorpusRegistry) SetResidency(capacity int, budgetBytes int64) {
	if r == nil {
		return
	}
	if capacity < 0 {
		capacity = 0
	}
	if budgetBytes < 0 {
		budgetBytes = 0
	}
	r.mu.Lock()
	r.resCapacity = capacity
	r.resBudget = budgetBytes
	stores := make([]*corpusStore, 0, len(r.stores))
	for _, cs := range r.stores {
		stores = append(stores, cs)
	}
	r.mu.Unlock()
	for _, cs := range stores {
		cs.applyResidency(capacity, budgetBytes)
	}
}

// ResidencySnapshot reports the installed policy (test/status surface).
func (r *CorpusRegistry) ResidencySnapshot() (capacity int, budgetBytes int64) {
	if r == nil {
		return 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resCapacity, r.resBudget
}

// DeleteStore removes every residency slot of one physical storage root:
// retained handles close now; still-attached handles (a live root of the
// deleted project) are retired — they close at their last release instead
// of being retained. The store coordinator itself leaves the registry, so a
// later open of the same root starts from a clean slate. Called by
// Service.DeleteProjectData before the directory removal.
func (r *CorpusRegistry) DeleteStore(storageRoot string) {
	if r == nil || storageRoot == "" {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	cs, ok := r.stores[storageRoot]
	if ok {
		delete(r.stores, storageRoot)
	}
	r.mu.Unlock()
	if cs != nil {
		cs.teardown()
	}
}

// Close tears the whole registry down: every store's retained handles close
// and attached ones are retired (their managers should already have drained
// — the caller's bounded shutdown; a straggler simply closes at its last
// release). Idempotent and final: after Close, storeFor/Open fail closed.
// Wired into VectorRoots.ShutdownAll after the managers' drain.
func (r *CorpusRegistry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	stores := make([]*corpusStore, 0, len(r.stores))
	for _, cs := range r.stores {
		stores = append(stores, cs)
	}
	r.stores = nil
	r.mu.Unlock()
	for _, cs := range stores {
		cs.teardown()
	}
}
