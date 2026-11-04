package vectorindex

// ADR-104 store-protocol benchmark harness — the measurement instrument for
// BENCHMARKS.md's "Multi-branch / multi-root store protocol" section. The
// protocol (corpus shape, topology grid, scenarios, TTFS metric, latency
// budgets, correctness assertions) is fixed there; this file implements the
// harness and must satisfy that contract, not redefine it.
//
// Everything runs on the deterministic host embedder (fakeEmbeddingFunc
// wrapped with counters) and the REAL chunker (benchmarkChunker), so every
// case runs on any developer machine without ONNX assets. The primary
// metric is time-to-first-correct-search (TTFS): from the operation's start
// to a ground-truth batch returning every expected file at rank <= 3 —
// readiness events never stop the clock, correctness does.
//
// Scenario matrix (b.Run names):
//
//	cold_full          — v2 store, fresh storage, attach + full build
//	cold_full_legacy   — v1 per-branch store (registry unwired): the
//	                     "before" arm of the before/after claim
//	warm_attach        — fresh registry+service (process-restart shape)
//	                     against a published corpus; zero embeddings
//	branch_fork_reuse  — new branch at an indexed commit: pointer write +
//	                     attach; ZERO corpus writes (hard assert)
//	branch_switch      — A -> B -> A round trip on warm corpora (+ v1 arm)
//	incremental        — <= 10-file edit pass
//	migration          — v1 per-branch store promoted to a corpus (rename)
//	eviction           — residency eviction + honest re-derive re-attach
//	concurrent_roots   — searchers on R-1 warm roots while one root cold-builds
//
// Knobs (environment, per the script's per-process grid):
//
//	PROTOCOL_CORPUS=small|large   500 (default) | 5000 files
//	PROTOCOL_BRANCHES=N           branch count for warm_attach topology (default 4)
//	PROTOCOL_ROOTS=N              root count for concurrent_roots (default 2)
//
// Run (one process per case, peak-RSS discipline):
//
//	go test ./core/vectorindex -run '^$' -bench 'BenchmarkStoreProtocol/sub=warm_attach' \
//	  -benchtime=1x -benchmem
//
// The whole suite: scripts/benchmark-vector-index.sh (store-protocol section).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Deterministic corpus generator
// ---------------------------------------------------------------------------

const (
	protocolSmallCorpus = 500
	protocolLargeCorpus = 5000
	// protocolQueryCount is the ground-truth batch size (protocol: >= 20).
	protocolQueryCount = 24
)

func protocolCorpusFiles() int {
	if strings.EqualFold(os.Getenv("PROTOCOL_CORPUS"), "large") {
		return protocolLargeCorpus
	}
	return protocolSmallCorpus
}

func protocolEnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// protocolMarker builds a unique lexical marker for file i under a given
// family prefix: the 4 base-26 letters vary the token's rune classes widely,
// so the bag-of-runes harness embedder separates files on the vector leg as
// well (a digit suffix would leave near-identical vectors across the
// family). Different prefixes never share a token.
func protocolMarker(family string, i int) string {
	s := make([]byte, 4)
	v := i
	for k := 3; k >= 0; k-- {
		s[k] = byte('a' + v%26)
		v /= 26
	}
	return family + string(s)
}

// protocolToken is the unique lexical marker of file i: it appears verbatim
// ONLY in file i (and its duplicate path), so the lexical leg answers
// ground-truth queries exactly.
func protocolToken(i int) string { return protocolMarker("unimark", i) }

// protocolRevToken is the unique revision marker of file i (the incremental
// scenario's edit): a separate family so an edited file's new chunks are
// dominated by a token no other file shares — a shared edit marker would
// cross-match every edited file in vector space and let multi-chunk files
// win the RRF aggregation over the lexical-rank-1 truth.
func protocolRevToken(i int) string { return protocolMarker("revmark", i) }

// protocolFileRel is the occurrence path of generated file i: mixed Go and
// Markdown with real sections, minified noise every 40th file so the content
// filter sees genuine work, and a verbatim duplicate of every 10th file at a
// second path (the protocol's 10% duplicate-content fraction).
func protocolFileRel(i int) string {
	switch {
	case i%40 == 39:
		return fmt.Sprintf("gen/noise%04d.min.js", i)
	case i%3 == 1:
		return fmt.Sprintf("docs/unit%04d.md", i)
	default:
		return fmt.Sprintf("src/unit%04d.go", i)
	}
}

func protocolDupRel(i int) string {
	if i%10 != 0 {
		return ""
	}
	return fmt.Sprintf("dup/copy%04d.go", i)
}

// protocolNoiseBody is a single-line minified blob (content-filter target).
// It deliberately does NOT reuse the unimark marker family: the noise must
// compete for neither leg of a ground-truth query (different term family,
// different rune distribution), while still being real minified work the
// content filter has to classify.
func protocolNoiseBody(i int) string {
	var sb strings.Builder
	sb.WriteString("/*! bundle ")
	for k := 0; k < 120; k++ {
		sb.WriteString("vendmin")
		sb.WriteString(strconv.Itoa(i % 7))
		sb.WriteString("=function(){return")
		sb.WriteString(strconv.Itoa(k))
		sb.WriteString("};")
	}
	sb.WriteString("*/")
	return sb.String()
}

// protocolContent generates file i's body: a short header, marker lines
// carrying the unique token, long sections (real chunking work), and a
// closing section — deterministic in i, never reading repository files.
func protocolContent(i int) string {
	token := protocolToken(i)
	if i%40 == 39 {
		return protocolNoiseBody(i)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "package unit%04d\n\n// %s module header\n", i, token)
	for k := 0; k < 12; k++ {
		fmt.Fprintf(&sb, "// %s section %d validates bounded shared-store telemetry.\n", token, k)
	}
	if i%3 == 1 {
		sb.WriteString("\n# Design notes\n\n")
		for k := 0; k < 6; k++ {
			sb.WriteString(strings.Repeat(fmt.Sprintf("The %s design note %d covers occurrence identity and per-root visibility. ", token, k), 6))
			sb.WriteString("\n\n")
		}
	} else {
		sb.WriteString("\nfunc Handle")
		sb.WriteString(token)
		sb.WriteString("(in int) int {\n")
		for k := 0; k < 8; k++ {
			fmt.Fprintf(&sb, "\tout := in*%d + %d // %s fold %d\n", k+2, k, token, k)
		}
		sb.WriteString("\treturn out\n}\n")
	}
	return sb.String()
}

// protocolGroundTruth is one manifest-derived query: Primary must appear at
// rank <= 3; Dup (a verbatim duplicate at a second path) must be findable in
// the top 10 (both occurrences stay searchable).
type protocolGroundTruth struct {
	Query   string
	Primary string
	Dup     string
}

// protocolQueries derives the ground-truth batch: an even sample across the
// corpus (protocolQueryCount queries, at least two hitting duplicate pairs).
func protocolQueries(total int) []protocolGroundTruth {
	qs := make([]protocolGroundTruth, 0, protocolQueryCount)
	step := total / protocolQueryCount
	if step < 1 {
		step = 1
	}
	dups := 0
	for i := 0; i < total && len(qs) < protocolQueryCount; i += step {
		dup := protocolDupRel(i)
		if dup != "" {
			dups++
		}
		qs = append(qs, protocolGroundTruth{Query: protocolToken(i), Primary: protocolFileRel(i), Dup: dup})
	}
	// Guarantee the protocol's two duplicate-path queries even when the even
	// sample missed them (replacing non-duplicate entries keeps the batch
	// budget fixed).
	for i := 0; i < total && dups < 2; i += 10 {
		if protocolDupRel(i) == "" {
			continue
		}
		for j := range qs {
			if qs[j].Dup == "" {
				qs[j] = protocolGroundTruth{Query: protocolToken(i), Primary: protocolFileRel(i), Dup: protocolDupRel(i)}
				dups++
				break
			}
		}
	}
	return qs
}

// protocolManifestCount is the expected on-disk file count: the generated
// files plus their verbatim duplicate paths (10%).
func protocolManifestCount(total int) int {
	dups := 0
	for i := 0; i < total; i += 10 {
		dups++
	}
	return total + dups
}

// verifyGroundTruth runs the batch and enforces the correctness gates on
// EVERY repetition: every Primary at rank <= 3, every Dup findable in top 10.
func verifyGroundTruth(b testing.TB, svc *Service, qs []protocolGroundTruth) {
	b.Helper()
	for _, q := range qs {
		res, err := svc.HybridSearch(context.Background(), SearchOptions{Query: q.Query, TopK: 10, Mode: ModeHybrid})
		if err != nil {
			b.Fatalf("ground-truth query %q: %v", q.Query, err)
		}
		if len(res) < 3 {
			b.Fatalf("ground-truth query %q returned %d results; want >= 3", q.Query, len(res))
		}
		rank := -1
		for i, r := range res[:3] {
			p := filepath.ToSlash(r.FilePath)
			// A verbatim duplicate carries the SAME bytes: either occurrence
			// is a correct answer for the rank gate (the protocol's "both
			// must be findable" is asserted separately below).
			if strings.HasSuffix(p, q.Primary) || (q.Dup != "" && strings.HasSuffix(p, q.Dup)) {
				rank = i
				break
			}
		}
		if rank < 0 {
			b.Fatalf("ground-truth query %q: %q not in top 3 (top: %s)", q.Query, q.Primary, resultPaths(res, 3))
		}
		if q.Dup != "" {
			found := false
			for _, r := range res {
				if strings.HasSuffix(filepath.ToSlash(r.FilePath), q.Dup) {
					found = true
					break
				}
			}
			if !found {
				b.Fatalf("ground-truth query %q: duplicate occurrence %q not findable in top 10 (top: %s)", q.Query, q.Dup, resultPaths(res, 10))
			}
		}
	}
}

func resultPaths(res []SearchResult, n int) string {
	if len(res) < n {
		n = len(res)
	}
	parts := make([]string, 0, n)
	for _, r := range res[:n] {
		parts = append(parts, filepath.ToSlash(r.FilePath))
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// Git fixture (benchmark-local: gittest targets *testing.T; the benchmark
// needs the same canonical-root discipline against *testing.B)
// ---------------------------------------------------------------------------

func benchGit(b testing.TB, dir string, args ...string) string {
	b.Helper()
	full := append([]string{"-C", dir, "-c", "commit.gpgsign=false", "-c", "user.email=proto-bench@example.invalid", "-c", "user.name=proto bench"}, args...)
	cmd := exec.CommandContext(context.Background(), "git", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		b.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// protocolRepo is one deterministic fixture repository: generated corpus
// committed on main, N branches at DISTINCT commits (storage-cost shape),
// and R roots (the main checkout plus worktrees at main's commit — the
// sharing-best shape; their branches carry the same snapshot key).
type protocolRepo struct {
	b            testing.TB
	root         string
	storage      string
	branches     []string
	roots        []string
	rootBranches []string // the branch each root sits on
	fileCount    int
	queries      []protocolGroundTruth
}

func newProtocolRepo(b testing.TB, branches, roots int) *protocolRepo {
	b.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("git binary not available")
	}
	total := protocolCorpusFiles()
	base := b.TempDir()
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		b.Fatalf("mkdir repo: %v", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		b.Fatalf("resolve repo root: %v", err)
	}
	root = resolvedRoot
	benchGit(b, root, "init", "-b", "main", "-q")
	for i := 0; i < total; i++ {
		rel := protocolFileRel(i)
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(protocolContent(i)), 0o644); err != nil {
			b.Fatalf("write %s: %v", rel, err)
		}
		if dup := protocolDupRel(i); dup != "" {
			dupPath := filepath.Join(root, filepath.FromSlash(dup))
			if err := os.MkdirAll(filepath.Dir(dupPath), 0o755); err != nil {
				b.Fatalf("mkdir %s: %v", dup, err)
			}
			if err := os.WriteFile(dupPath, []byte(protocolContent(i)), 0o644); err != nil {
				b.Fatalf("write %s: %v", dup, err)
			}
		}
	}
	benchGit(b, root, "add", ".")
	benchGit(b, root, "commit", "-q", "-m", "protocol corpus")

	names := make([]string, 0, branches)
	names = append(names, "main")
	for k := 1; k < branches; k++ {
		name := fmt.Sprintf("br%02d", k)
		benchGit(b, root, "checkout", "-q", "-b", name, "main")
		marker := filepath.Join(root, fmt.Sprintf("BRANCHMARK%02d", k))
		if err := os.WriteFile(marker, []byte(fmt.Sprintf("branch diversity marker %d\n", k)), 0o644); err != nil {
			b.Fatalf("write branch marker: %v", err)
		}
		benchGit(b, root, "add", ".")
		benchGit(b, root, "commit", "-q", "-m", fmt.Sprintf("branch %d diversity", k))
		names = append(names, name)
	}
	benchGit(b, root, "checkout", "-q", "main")

	rootDirs := make([]string, 0, roots)
	rootDirs = append(rootDirs, root)
	rootBrs := make([]string, 0, roots)
	rootBrs = append(rootBrs, "main")
	for k := 1; k < roots; k++ {
		wt := filepath.Join(base, fmt.Sprintf("wt%d", k))
		brName := fmt.Sprintf("wtdir%d", k)
		benchGit(b, root, "worktree", "add", "-q", "-b", brName, wt, "main")
		resolved, rerr := filepath.EvalSymlinks(wt)
		if rerr != nil {
			b.Fatalf("resolve worktree %s: %v", wt, rerr)
		}
		rootDirs = append(rootDirs, resolved)
		rootBrs = append(rootBrs, brName)
	}

	storage := filepath.Join(b.TempDir(), "vector_index")
	protocolRSSBaseline.Store(benchmarkPeakRSSBytes())
	return &protocolRepo{
		b: b, root: root, storage: storage, branches: names,
		roots: rootDirs, rootBranches: rootBrs,
		fileCount: total, queries: protocolQueries(total),
	}
}

// addForkBranch creates a NEW branch at main's commit (a pointer write) and
// checks the fixture root out onto it — the fork-reuse shape of §1 L3.
func (p *protocolRepo) addForkBranch(name string) {
	p.b.Helper()
	benchGit(p.b, p.root, "branch", name, "main")
	benchGit(p.b, p.root, "checkout", "-q", name)
}

func (p *protocolRepo) checkout(branch string) {
	p.b.Helper()
	benchGit(p.b, p.root, "checkout", "-q", branch)
}

// ---------------------------------------------------------------------------
// Counting pipeline
// ---------------------------------------------------------------------------

// protocolEmbedding is the harness's deterministic embedder: an order-free
// bag-of-runes direction over 16 rune classes. Unlike the positional test
// fake, a repeated token dominates its chunk's direction proportionally to
// its counts, so ground-truth queries rank their own file on the vector leg
// as well — the harness then measures the store, not embedder degeneracy.
func protocolEmbedding(text string) []float32 {
	v := make([]float32, 16)
	for _, r := range text {
		v[int(r)%16] += float32(r%29 + 1)
	}
	return v
}

// protocolCounters are the deterministic structural counters the protocol
// asserts on every repetition. embedDocs counts DOCUMENT texts through the
// batch embedder only — the per-text EmbeddingFunc also serves search
// queries, which must never pollute the corpus-write counters.
type protocolCounters struct {
	embedDocs atomic.Int64
	batches   atomic.Int64
	telemetry *Telemetry
}

// protocolBatchEmbedder is the counting document embedder (batch rows are
// the protocol's embedding-call counter).
type protocolBatchEmbedder struct {
	counters *protocolCounters
}

func (e *protocolBatchEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	e.counters.embedDocs.Add(int64(len(texts)))
	e.counters.batches.Add(1)
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = protocolEmbedding(text)
	}
	return out, nil
}

func newProtocolService(b testing.TB, registry *CorpusRegistry, storage, workspaceRoot string, counters *protocolCounters) (*Service, *Indexer) {
	b.Helper()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: func(_ context.Context, text string) ([]float32, error) {
			return protocolEmbedding(text), nil
		},
		BatchEmbedder:             &protocolBatchEmbedder{counters: counters},
		EmbeddingBatchSize:        32,
		EmbeddingCacheFingerprint: "fp-proto",
		Corpus:                    registry,
		Telemetry:                 counters.telemetry,
	})
	if err != nil {
		b.Fatalf("NewService: %v", err)
	}
	if err := svc.SetProject("proto-bench", storage); err != nil {
		b.Fatalf("SetProject: %v", err)
	}
	svc.SetWorkspaceRoot(workspaceRoot)
	idx := NewIndexer(IndexerConfig{
		Service:     svc,
		ChunkFn:     benchmarkChunker,
		Telemetry:   counters.telemetry,
		PrepWorkers: 2,
	})
	return svc, idx
}

// attachCorpus drives the full view attach of one root: snapshot identity,
// corpus ensure, view bind, overlay reconcile, ground-truth-correct search.
// The TTFS clock spans the whole operation (the caller brackets the timer);
// the ATTACH-ONLY split (before the ground-truth queries) is reported as
// ms/attach — the budget table's attach bound is judged on this metric,
// TTFS on the reported ms/ttfs.
func attachCorpus(b testing.TB, svc *Service, idx *Indexer, root, legacyBranch string, qs []protocolGroundTruth) time.Duration {
	b.Helper()
	started := time.Now()
	svc.SetWorkspaceRoot(root)
	if err := idx.AttachSnapshot(context.Background(), root, legacyBranch); err != nil {
		b.Fatalf("AttachSnapshot: %v", err)
	}
	if err := idx.IndexIncremental(context.Background(), root); err != nil {
		b.Fatalf("IndexIncremental: %v", err)
	}
	attached := time.Since(started)
	if bb, ok := b.(*testing.B); ok {
		bb.ReportMetric(float64(attached.Microseconds())/1000, "ms/attach")
	}
	verifyGroundTruth(b, svc, qs)
	return time.Since(started)
}

// registryHandleCount reports the number of decoded corpus handles the
// registry holds (attached + retained): the shared-decode structural
// counter.
func registryHandleCount(registry *CorpusRegistry, storage string) int {
	cs := registry.storeFor(storage)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.handles)
}

// registryDecodeCount sums the per-key decode ledger (corpus table opens).
func registryDecodeCount(registry *CorpusRegistry, storage string) int {
	cs := registry.storeFor(storage)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	total := 0
	for _, n := range cs.decodes {
		total += n
	}
	return total
}

// retainedHandleCount reports the residency LRU length (zero-ref handles
// kept warm; the eviction candidates).
func retainedHandleCount(registry *CorpusRegistry, storage string) int {
	cs := registry.storeFor(storage)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.lru)
}

func storageBytes(storage string) int64 { return dirBytes(storage) }

func reportProtocolCommon(b *testing.B, ttfs time.Duration, files int, counters *protocolCounters, disk int64) {
	b.Helper()
	s := counters.telemetry.Snapshot()
	b.ReportMetric(float64(ttfs)/float64(time.Millisecond), "ms/ttfs")
	b.ReportMetric(float64(files)/ttfs.Seconds(), "files/sec")
	b.ReportMetric(float64(counters.embedDocs.Load()), "embed-texts")
	b.ReportMetric(float64(s.Stages[StageChromemCommit].Items), "chunks-written")
	b.ReportMetric(float64(s.Stages[StageWalkValidation].Items), "stat-validations")
	b.ReportMetric(float64(disk)/float64(1<<20), "storage-MiB")
	b.ReportMetric(float64(benchmarkPeakRSSBytes())/(1<<20), "peak-RSS-MiB")
	b.ReportMetric(peakRSSDeltaMiB(), "peak-RSS-delta-MiB")
}

// ---------------------------------------------------------------------------
// Scenarios
// ---------------------------------------------------------------------------

// BenchmarkStoreProtocol implements the BENCHMARKS.md store-protocol suite.
// Every scenario asserts its correctness gates on every repetition.
func BenchmarkStoreProtocol(b *testing.B) {
	b.Run("sub=cold_full", func(b *testing.B) {
		p := newProtocolRepo(b, 1, 1)
		for i := 0; i < b.N; i++ {
			// A fresh storage root per iteration keeps the store COLD while
			// reusing the fixture repo (git setup is never measured).
			storage := filepath.Join(b.TempDir(), "vector_index")
			counters := &protocolCounters{telemetry: &Telemetry{}}
			registry := NewCorpusRegistry(nil)
			svc, idx := newProtocolService(b, registry, storage, p.root, counters)
			b.StartTimer()
			ttfs := attachCorpus(b, svc, idx, p.root, "main", p.queries)
			b.StopTimer()
			reportProtocolCommon(b, ttfs, p.fileCount, counters, storageBytes(storage))
			if n := registryHandleCount(registry, storage); n != 1 {
				b.Fatalf("cold attach retained %d corpus handles; want 1", n)
			}
			if err := svc.Close(); err != nil {
				b.Fatal(err)
			}
			registry.Close()
		}
	})

	b.Run("sub=cold_full_legacy", func(b *testing.B) {
		p := newProtocolRepo(b, 1, 1)
		for i := 0; i < b.N; i++ {
			storage := filepath.Join(b.TempDir(), "vector_index")
			counters := &protocolCounters{telemetry: &Telemetry{}}
			svc, idx := newProtocolService(b, nil, storage, p.root, counters) // registry nil = the v1 model
			b.StartTimer()
			started := time.Now()
			if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
				b.Fatalf("SwitchBranch: %v", err)
			}
			if err := idx.IndexFull(context.Background(), p.root); err != nil {
				b.Fatalf("IndexFull: %v", err)
			}
			verifyGroundTruth(b, svc, p.queries)
			ttfs := time.Since(started)
			b.StopTimer()
			reportProtocolCommon(b, ttfs, p.fileCount, counters, storageBytes(storage))
			if err := svc.Close(); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("sub=warm_attach", func(b *testing.B) {
		branches := protocolEnvInt("PROTOCOL_BRANCHES", 4)
		p := newProtocolRepo(b, branches, 1)
		// Setup (unmeasured): build the corpora of EVERY branch once, then
		// drop the whole pipeline — the per-iteration attach faces a fully
		// indexed store with N branches' history behind it. HandleBranchSwitch
		// (corpus path) runs attach + reconcile itself.
		buildCounters := &protocolCounters{telemetry: &Telemetry{}}
		buildRegistry := NewCorpusRegistry(nil)
		svc, idx := newProtocolService(b, buildRegistry, p.storage, p.root, buildCounters)
		for _, br := range p.branches {
			p.checkout(br)
			if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, br); hbsErr != nil {
				b.Fatalf("setup switch %s: %v", br, hbsErr)
			}
		}
		p.checkout("main")
		if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, "main"); hbsErr != nil {
			b.Fatalf("setup switch main: %v", hbsErr)
		}
		if err := svc.Close(); err != nil {
			b.Fatal(err)
		}
		buildRegistry.Close()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			counters := &protocolCounters{telemetry: &Telemetry{}}
			// Fresh registry + service: the process-restart shape. Opening
			// must be O(one table) regardless of the branch count behind it.
			registry := NewCorpusRegistry(nil)
			svc, idx := newProtocolService(b, registry, p.storage, p.root, counters)
			b.StartTimer()
			ttfs := attachCorpus(b, svc, idx, p.root, "main", p.queries)
			b.StopTimer()
			reportProtocolCommon(b, ttfs, p.fileCount, counters, storageBytes(p.storage))
			b.ReportMetric(float64(branches), "branches")
			b.ReportMetric(float64(registryDecodeCount(registry, p.storage)), "corpus-decodes")
			if n := counters.embedDocs.Load(); n != 0 {
				b.Fatalf("warm attach embedded %d texts; want 0 (published corpus)", n)
			}
			if n := registryHandleCount(registry, p.storage); n != 1 {
				b.Fatalf("warm attach retained %d corpus handles; want exactly 1 decoded table", n)
			}
			if err := svc.Close(); err != nil {
				b.Fatal(err)
			}
			registry.Close()
		}
	})

	b.Run("sub=branch_fork_reuse", func(b *testing.B) {
		p := newProtocolRepo(b, 1, 1)
		// Setup (unmeasured): index main once.
		setupCounters := &protocolCounters{telemetry: &Telemetry{}}
		setupRegistry := NewCorpusRegistry(nil)
		svc, idx := newProtocolService(b, setupRegistry, p.storage, p.root, setupCounters)
		if d := attachCorpus(b, svc, idx, p.root, "main", p.queries); d <= 0 {
			b.Fatal("setup attach did not run")
		}
		if err := svc.Close(); err != nil {
			b.Fatal(err)
		}
		setupRegistry.Close()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			branch := fmt.Sprintf("fork%03d", i)
			counters := &protocolCounters{telemetry: &Telemetry{}}
			registry := NewCorpusRegistry(nil)
			svc, idx := newProtocolService(b, registry, p.storage, p.root, counters)
			b.StartTimer()
			started := time.Now()
			p.addForkBranch(branch) // pointer write at main's commit + checkout
			if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, branch); hbsErr != nil {
				b.Fatalf("switch to fork: %v", hbsErr)
			}
			verifyGroundTruth(b, svc, p.queries)
			ttfs := time.Since(started)
			b.StopTimer()
			reportProtocolCommon(b, ttfs, p.fileCount, counters, storageBytes(p.storage))
			// HARD assertion (protocol): a fork from an indexed commit is a
			// pointer write — ZERO corpus bytes written, ZERO embeddings.
			if n := counters.embedDocs.Load(); n != 0 {
				b.Fatalf("branch fork re-embedded %d texts; want 0 (pointer write)", n)
			}
			if chunks := counters.telemetry.Snapshot().Stages[StageChromemCommit].Items; chunks != 0 {
				b.Fatalf("branch fork wrote %d chunk documents; want 0", chunks)
			}
			if err := svc.Close(); err != nil {
				b.Fatal(err)
			}
			registry.Close()
		}
	})

	b.Run("sub=branch_switch", func(b *testing.B) {
		p := newProtocolRepo(b, 2, 1)
		warmSwitch := func(b *testing.B, corpus bool) {
			storage := p.storage
			if !corpus {
				storage = filepath.Join(b.TempDir(), "vector_index")
			}
			counters := &protocolCounters{telemetry: &Telemetry{}}
			var registry *CorpusRegistry
			if corpus {
				registry = NewCorpusRegistry(nil)
			}
			svc, idx := newProtocolService(b, registry, storage, p.root, counters)
			// Setup: index main and br01 once (unmeasured), land on main.
			// HandleBranchSwitch reconciles internally on both models.
			if corpus {
				if d := attachCorpus(b, svc, idx, p.root, "main", p.queries); d <= 0 {
					b.Fatal("setup attach failed")
				}
				p.checkout("br01")
				if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, "br01"); hbsErr != nil {
					b.Fatalf("setup switch br01: %v", hbsErr)
				}
				p.checkout("main")
				if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, "main"); hbsErr != nil {
					b.Fatalf("setup switch main: %v", hbsErr)
				}
			} else {
				if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
					b.Fatal(err)
				}
				if err := idx.IndexFull(context.Background(), p.root); err != nil {
					b.Fatal(err)
				}
				p.checkout("br01")
				if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, "br01"); hbsErr != nil {
					b.Fatalf("setup switch br01: %v", hbsErr)
				}
				p.checkout("main")
				if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, "main"); hbsErr != nil {
					b.Fatalf("setup switch main: %v", hbsErr)
				}
			}
			b.ResetTimer()
			var ab, ba time.Duration
			for i := 0; i < b.N; i++ {
				b.StartTimer()
				started := time.Now()
				p.checkout("br01")
				if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, "br01"); hbsErr != nil {
					b.Fatalf("switch A->B: %v", hbsErr)
				}
				verifyGroundTruth(b, svc, p.queries)
				ab += time.Since(started)
				started = time.Now()
				p.checkout("main")
				if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, "main"); hbsErr != nil {
					b.Fatalf("switch B->A: %v", hbsErr)
				}
				verifyGroundTruth(b, svc, p.queries)
				ba += time.Since(started)
				b.StopTimer()
			}
			b.StopTimer()
			b.ReportMetric(float64(ab)/float64(time.Millisecond)/float64(b.N), "ms/switch_ab")
			b.ReportMetric(float64(ba)/float64(time.Millisecond)/float64(b.N), "ms/switch_ba")
			if err := svc.Close(); err != nil {
				b.Fatal(err)
			}
			if registry != nil {
				registry.Close()
			}
		}
		b.Run("corpus=v2", func(b *testing.B) { warmSwitch(b, true) })
		b.Run("corpus=v1", func(b *testing.B) { warmSwitch(b, false) })
	})

	b.Run("sub=incremental", func(b *testing.B) {
		p := newProtocolRepo(b, 1, 1)
		counters := &protocolCounters{telemetry: &Telemetry{}}
		registry := NewCorpusRegistry(nil)
		svc, idx := newProtocolService(b, registry, p.storage, p.root, counters)
		if d := attachCorpus(b, svc, idx, p.root, "main", p.queries); d <= 0 {
			b.Fatal("setup attach failed")
		}
		// Revision targets: an even sample of <= 10 single-path files (no
		// duplicate-path files — the duplicates stay corpus-current). Each
		// edit appends lines dominated by the file's UNIQUE revision token
		// (protocolRevToken), and the ground-truth query is that token
		// alone: it matches exactly one edited file on both legs.
		type revision struct {
			target int
			query  string
			rel    string
		}
		revisions := make([]revision, 0, 10)
		for i := 0; i < len(p.queries) && len(revisions) < 10; i++ {
			ti := (i * p.fileCount) / len(p.queries)
			if protocolDupRel(ti) != "" {
				continue
			}
			revisions = append(revisions, revision{
				target: ti, rel: protocolFileRel(ti),
				query: protocolRevToken(ti),
			})
		}
		// The per-iteration ground truth (iterQueries) is derived inside the
		// loop: every pass writes a fresh, iteration-suffixed revision token.
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			counters.telemetry.Reset()
			counters.embedDocs.Store(0)
			// Fold the iteration into every revision token: a byte-identical
			// rewrite makes iterations >= 2 validation-only no-ops (the
			// incremental pass finds nothing changed) and the reported
			// metrics describe an idle pass. The per-iteration token is
			// unique to its one edited file, so the ground truth re-derived
			// below tracks it exactly.
			iterQueries := make([]protocolGroundTruth, 0, len(revisions))
			for _, r := range revisions {
				token := fmt.Sprintf("%s_i%03d", r.query, i)
				var sb strings.Builder
				sb.WriteString(protocolContent(r.target))
				for k := 0; k < 8; k++ {
					fmt.Fprintf(&sb, "// %s revision checkpoint %d\n", token, k)
				}
				if err := os.WriteFile(filepath.Join(p.root, filepath.FromSlash(r.rel)), []byte(sb.String()), 0o644); err != nil {
					b.Fatalf("edit %s: %v", r.rel, err)
				}
				iterQueries = append(iterQueries, protocolGroundTruth{Query: token, Primary: r.rel})
			}
			b.StartTimer()
			started := time.Now()
			if err := idx.IndexIncremental(context.Background(), p.root); err != nil {
				b.Fatalf("incremental pass: %v", err)
			}
			verifyGroundTruth(b, svc, iterQueries)
			ttfs := time.Since(started)
			b.StopTimer()
			reportProtocolCommon(b, ttfs, len(revisions), counters, storageBytes(p.storage))
		}
		b.StopTimer()
		if err := svc.Close(); err != nil {
			b.Fatal(err)
		}
		registry.Close()
	})

	b.Run("sub=migration", func(b *testing.B) {
		p := newProtocolRepo(b, 1, 1)
		// Setup: a v1 per-branch store for main (the pre-ADR-104 shape).
		v1Storage := filepath.Join(b.TempDir(), "vector_index")
		v1Counters := &protocolCounters{telemetry: &Telemetry{}}
		v1Svc, v1Idx := newProtocolService(b, nil, v1Storage, p.root, v1Counters)
		if err := v1Svc.SwitchBranch(context.Background(), "main"); err != nil {
			b.Fatal(err)
		}
		if err := v1Idx.IndexFull(context.Background(), p.root); err != nil {
			b.Fatal(err)
		}
		if err := v1Svc.Close(); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			storage := filepath.Join(b.TempDir(), "vector_index")
			if err := copyTree(v1Storage, storage); err != nil {
				b.Fatalf("clone v1 store: %v", err)
			}
			before := storageBytes(storage)
			counters := &protocolCounters{telemetry: &Telemetry{}}
			registry := NewCorpusRegistry(nil)
			svc, idx := newProtocolService(b, registry, storage, p.root, counters)
			b.StartTimer()
			// Attach promotes the legacy branch store into the corpus by
			// rename (vectors move without re-embedding).
			started := time.Now()
			if err := idx.AttachSnapshot(context.Background(), p.root, "main"); err != nil {
				b.Fatalf("migration attach: %v", err)
			}
			if err := idx.IndexIncremental(context.Background(), p.root); err != nil {
				b.Fatalf("post-migration reconcile: %v", err)
			}
			verifyGroundTruth(b, svc, p.queries)
			ttfs := time.Since(started)
			b.StopTimer()
			after := storageBytes(storage)
			reportProtocolCommon(b, ttfs, p.fileCount, counters, after)
			b.ReportMetric(float64(after-before)/float64(1<<20), "storage-delta-MiB")
			// The promotion must not re-embed: vectors moved, not rebuilt.
			if n := counters.embedDocs.Load(); n != 0 {
				b.Fatalf("migration re-embedded %d texts; want 0 (rename promotion)", n)
			}
			legacy := filepath.Join(storage, branchRootsDirName, collectionName("main"))
			if _, err := os.Stat(legacy); !os.IsNotExist(err) {
				b.Fatalf("legacy branch store still present after promotion: %s (stat err: %v)", legacy, err)
			}
			if err := svc.Close(); err != nil {
				b.Fatal(err)
			}
			registry.Close()
		}
	})

	b.Run("sub=eviction", func(b *testing.B) {
		p := newProtocolRepo(b, 2, 1)
		counters := &protocolCounters{telemetry: &Telemetry{}}
		registry := NewCorpusRegistry(nil)
		registry.SetResidency(4, 1<<40)
		svc, idx := newProtocolService(b, registry, p.storage, p.root, counters)
		// Setup: corpora for main (K_A) and br01 (K_B) published; the
		// service lands on br01 so K_A's handle is RETAINED (zero-ref).
		if d := attachCorpus(b, svc, idx, p.root, "main", p.queries); d <= 0 {
			b.Fatal("setup attach failed")
		}
		p.checkout("br01")
		if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, "br01"); hbsErr != nil {
			b.Fatalf("setup switch br01: %v", hbsErr)
		}
		if n := retainedHandleCount(registry, p.storage); n == 0 {
			b.Fatal("fixture sanity: K_A must be retained after switching to br01")
		}
		decodesBefore := registryDecodeCount(registry, p.storage)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			// Shrink residency to nothing: every RETAINED handle is evicted
			// while the attached one (K_B) stays pinned.
			registry.SetResidency(1, 1)
			if n := retainedHandleCount(registry, p.storage); n != 0 {
				b.Fatalf("eviction left %d retained handles; want 0", n)
			}
			counters.telemetry.Reset()
			counters.embedDocs.Store(0)
			b.StartTimer()
			started := time.Now()
			// Honest re-derive: switching back re-opens the published K_A
			// (decode only, never a re-embed).
			p.checkout("main")
			if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, "main"); hbsErr != nil {
				b.Fatalf("re-attach after eviction: %v", hbsErr)
			}
			verifyGroundTruth(b, svc, p.queries)
			ttfs := time.Since(started)
			b.StopTimer()
			reportProtocolCommon(b, ttfs, p.fileCount, counters, storageBytes(p.storage))
			if n := counters.embedDocs.Load(); n != 0 {
				b.Fatalf("post-eviction re-attach re-embedded %d texts; want 0 (decode-only)", n)
			}
			decodesNow := registryDecodeCount(registry, p.storage)
			if decodesNow <= decodesBefore {
				b.Fatalf("post-eviction re-attach did not re-decode the corpus (decodes %d -> %d)", decodesBefore, decodesNow)
			}
			decodesBefore = decodesNow
			// Leave the fixture on br01 with retention re-enabled for the
			// next repetition.
			registry.SetResidency(4, 1<<40)
			p.checkout("br01")
			if _, hbsErr := idx.HandleBranchSwitch(context.Background(), p.root, "br01"); hbsErr != nil {
				b.Fatalf("reset to br01: %v", hbsErr)
			}
		}
		b.StopTimer()
		if err := svc.Close(); err != nil {
			b.Fatal(err)
		}
		registry.Close()
	})

	b.Run("sub=concurrent_roots", func(b *testing.B) {
		roots := protocolEnvInt("PROTOCOL_ROOTS", 2)
		if roots < 2 {
			b.Fatal("concurrent_roots needs at least 2 roots: one cold indexer + one searcher")
		}
		p := newProtocolRepo(b, 1, roots)
		registry := NewCorpusRegistry(nil)
		// Root 0 (the checkout) is the COLD INDEXER — it gets NO setup
		// attach, so its overlay's lexical bolt is free for the timed
		// section's service (bleve's bolt flock is exclusive per store; a
		// second service on the same root would block on it forever —
		// exactly the one-manager-per-root rule of ADR-081). The SEARCHERS
		// are the worktree roots (1..N-1): each attaches main's corpus
		// through its own service (the first worktree's attach builds it;
		// the rest reuse the published corpus with zero embeddings).
		type searcher struct {
			svc *Service
		}
		searchers := make([]searcher, 0, roots-1)
		for ri := 1; ri < roots; ri++ {
			root := p.roots[ri]
			c := &protocolCounters{telemetry: &Telemetry{}}
			svc, idx := newProtocolService(b, registry, p.storage, root, c)
			if d := attachCorpus(b, svc, idx, root, p.rootBranches[ri], p.queries); d <= 0 {
				b.Fatal("setup attach failed")
			}
			searchers = append(searchers, searcher{svc: svc})
		}
		for si := range searchers {
			if n := searchers[si].svc.CorpusFileCount(); n != protocolManifestCount(p.fileCount) {
				b.Fatalf("searcher root %d sees %d corpus files; want %d", si+1, n, protocolManifestCount(p.fileCount))
			}
		}
		b.ResetTimer()
		for iter := 0; iter < b.N; iter++ {
			b.StopTimer()
			// The indexing root: main advanced by a load commit, branched —
			// a COLD corpus key while every searcher keeps serving the old
			// one from its own worktree.
			branch := fmt.Sprintf("loadgen%03d", iter)
			p.checkout("main")
			editToken := fmt.Sprintf("loadgen%03dmarker", iter)
			for k := 0; k < 10; k++ {
				rel := fmt.Sprintf("src/loadgen%03d.go", k)
				content := fmt.Sprintf("package main\n\n// %s cold-build load generator %d\n", editToken, k)
				if err := os.WriteFile(filepath.Join(p.root, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
					b.Fatalf("write load file: %v", err)
				}
			}
			benchGit(b, p.root, "add", ".")
			benchGit(b, p.root, "commit", "-q", "-m", "load "+branch)
			benchGit(b, p.root, "branch", branch)
			benchGit(b, p.root, "checkout", "-q", branch)

			coldCounters := &protocolCounters{telemetry: &Telemetry{}}
			coldSvc, coldIdx := newProtocolService(b, registry, p.storage, p.root, coldCounters)

			stop := make(chan struct{})
			var mu sync.Mutex
			latencies := make([]time.Duration, 0, 1024)
			var wg sync.WaitGroup
			var searchFailures atomic.Int64
			for si := 0; si < len(searchers); si++ {
				s := searchers[si]
				query := p.queries[si%len(p.queries)].Query
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-stop:
							return
						default:
						}
						started := time.Now()
						res, err := s.svc.HybridSearch(context.Background(), SearchOptions{Query: query, TopK: 10, Mode: ModeHybrid})
						d := time.Since(started)
						if err != nil {
							searchFailures.Add(1)
							continue // fail-soft sampling; the p95 gates the load
						}
						mu.Lock()
						latencies = append(latencies, d)
						mu.Unlock()
						if len(res) == 0 {
							b.Errorf("searcher got zero results under load for %q", query)
						}
					}
				}()
			}
			b.StartTimer()
			started := time.Now()
			if err := coldIdx.AttachSnapshot(context.Background(), p.root, branch); err != nil {
				b.Fatalf("cold attach under load: %v", err)
			}
			if err := coldIdx.IndexIncremental(context.Background(), p.root); err != nil {
				b.Fatalf("cold reconcile under load: %v", err)
			}
			verifyGroundTruth(b, coldSvc, p.queries)
			coldElapsed := time.Since(started)
			b.StopTimer()
			close(stop)
			wg.Wait()

			// Gating (finding #48 shape): a persistent searcher failure never
			// fails the loop above — it only skips appending a latency — so
			// an all-failing run would report pct()=0 for every percentile
			// and silently PASS with search-p50-ms=0. Fail when every
			// sample errored; a partial error fraction is gated below.
			if fails := searchFailures.Load(); fails > 0 && len(latencies) == 0 {
				b.Fatalf("concurrent-roots: all %d searches failed under load; no latency samples to report", fails)
			}

			mu.Lock()
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			pct := func(q float64) float64 {
				if len(latencies) == 0 {
					return 0
				}
				idx := int(float64(len(latencies)-1) * q)
				return float64(latencies[idx]) / float64(time.Millisecond)
			}
			p50, p95, samples := pct(0.50), pct(0.95), len(latencies)
			if fails := searchFailures.Load(); fails > 0 {
				if attempts := fails + int64(samples); attempts > 0 && fails*100 > attempts {
					b.Errorf("concurrent-roots: %d of %d searches failed under load (>1%%)", fails, attempts)
				}
			}
			mu.Unlock()
			b.ReportMetric(float64(coldElapsed)/float64(time.Millisecond), "ms/cold-under-load")
			b.ReportMetric(float64(p.fileCount)/coldElapsed.Seconds(), "files/sec-under-load")
			b.ReportMetric(p50, "search-p50-ms")
			b.ReportMetric(p95, "search-p95-ms")
			b.ReportMetric(float64(samples), "search-samples")
			b.ReportMetric(float64(searchFailures.Load()), "search-errors")
			b.ReportMetric(float64(coldCounters.embedDocs.Load()), "embed-texts")
			if err := coldSvc.Close(); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		for _, s := range searchers {
			if err := s.svc.Close(); err != nil {
				b.Fatal(err)
			}
		}
		registry.Close()
	})
}

// copyTree recursively copies src to dst (the migration scenario's per-
// iteration store clone).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// ---------------------------------------------------------------------------
// Peak RSS (process high-water; Darwin/Linux)
// ---------------------------------------------------------------------------

var protocolRSSBaseline atomic.Uint64

// peakRSSDeltaMiB reports the process high-water rise since the fixture was
// built (zero on unsupported platforms or when the OS reports no RSS).
func peakRSSDeltaMiB() float64 {
	now := benchmarkPeakRSSBytes()
	base := protocolRSSBaseline.Load()
	if base == 0 || now < base {
		return 0
	}
	return float64(now-base) / (1 << 20)
}
