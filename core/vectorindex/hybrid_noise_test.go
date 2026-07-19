package vectorindex

import (
	"context"
	"sort"
	"strings"
	"testing"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
)

// scriptedEmbeddingFunc returns an embedding function that produces
// deterministic, controllable cosine similarities for a noise-corpus
// hybrid search test.
//
// The fakeEmbeddingFunc used by other tests sums character codes into a
// fixed-size vector, which yields near-identical (~0.95) similarities
// for all documents. That makes it impossible to model the noise-tail
// scenario that the pre-fusion score thresholds are designed to fix:
// relevant documents with high similarity and noise documents with low
// similarity that still appear in both retriever lists.
//
// scriptedEmbeddingFunc instead encodes a target similarity directly
// into the embedding vector. A document whose content starts with the
// marker prefix "SIM:<value>:" is embedded at a cosine similarity of
// exactly <value> (in [0,1]) to the query embedding. Text without the
// marker (i.e. the query itself) is embedded as the reference unit
// vector e0 = (1, 0, 0, …). A document with target similarity s is
// embedded as (s, sqrt(1-s²), 0, …), whose cosine similarity to e0 is
// exactly s.
func scriptedEmbeddingFunc(dim int) chromem.EmbeddingFunc {
	return func(_ context.Context, text string) ([]float32, error) {
		vec := make([]float32, dim)
		if !strings.HasPrefix(text, "SIM:") {
			// Query / unmarked text → reference unit vector e0.
			vec[0] = 1
			return vec, nil
		}
		target := 0.05 // default: noise-level similarity
		rest := strings.TrimPrefix(text, "SIM:")
		if idx := strings.Index(rest, ":"); idx > 0 {
			if v, ok := parseFloat(rest[:idx]); ok {
				target = v
			}
		}
		vec[0] = float32(target)
		// Orthogonal component so the vector has unit norm and its
		// cosine similarity to e0 is exactly `target`.
		perp := sqrtf(1.0 - target*target)
		if dim > 1 {
			vec[1] = float32(perp)
		}
		return vec, nil
	}
}

// parseFloat is a minimal float parser to avoid pulling strconv into the
// test helper signature; it handles the simple "0.9" / "0.05" forms we
// emit.
func parseFloat(s string) (float64, bool) {
	var whole, frac float64
	var div float64 = 1
	neg := false
	i := 0
	if i < len(s) && s[i] == '-' {
		neg = true
		i++
	}
	hasDot := false
	for ; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			hasDot = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		d := float64(c - '0')
		if hasDot {
			div *= 10
			frac += d / div
		} else {
			whole = whole*10 + d
		}
	}
	v := whole + frac
	if neg {
		v = -v
	}
	return v, true
}

// sqrtf is a float64 square root helper.
func sqrtf(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// Newton's method, 6 iterations — more than enough for test precision.
	z := x
	for i := 0; i < 6; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

// seedNoiseCorpus builds a service with a scripted embedding function
// and a corpus that models the noise-tail RRF promotion scenario:
//
//   - REL_BOTH docs: high vector similarity AND contain the query term
//     (appear at the top of both retriever lists).
//   - REL_VEC docs: high vector similarity but do NOT contain the query
//     term (appear only in the vector list — one-sided relevant hits).
//   - NOISE docs: low vector similarity but DO contain the query term
//     (appear in the vector tail AND the lexical list — noise that earns
//     a double RRF contribution without thresholds).
//
// The query term "blackboard" is common enough to populate the lexical
// list with many hits, while the REL_VEC docs use a synonym that does
// not match lexically.
func seedNoiseCorpus(t *testing.T, hc HybridConfig) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: scriptedEmbeddingFunc(8),
		HybridConfig:  hc,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.SetProject("noise-proj", t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	// Close the service so its on-disk lexical store (.bolt/.zap) handles are
	// released before TempDir cleanup — Windows refuses to delete open files.
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("seedNoiseCorpus Close: %v", err)
		}
	})

	mkDoc := func(id, path, name, content string, sim float64) (chromem.Document, lexical.Doc) {
		// Prepend the SIM marker so the scripted embedding function
		// places this document at the target similarity. The marker is
		// stripped from the lexical content so it does not affect BM25.
		embedded := "SIM:" + formatSim(sim) + ":" + content
		return chromem.Document{
				ID:      id,
				Content: embedded,
				Metadata: map[string]string{
					"file_path":    path,
					"file_name":    name,
					"content_hash": id + "-hash",
					"start_line":   "1",
					"end_line":     "5",
					"language":     "go",
				},
			},
			lexical.Doc{
				ID:       id,
				FilePath: path,
				Language: "go",
				Content:  content,
			}
	}

	var vecDocs []chromem.Document
	var lexDocs []lexical.Doc

	// 2 relevant docs in both lists (top of vector + lexical). Rich
	// content with multiple occurrences of the query term yields a high
	// BM25 score, placing them at the top of the lexical list.
	add := func(id, path, name, content string, sim float64) {
		v, l := mkDoc(id, path, name, content, sim)
		vecDocs = append(vecDocs, v)
		lexDocs = append(lexDocs, l)
	}

	add("rel_both:0", "/proj/blackboard.go", "blackboard.go",
		"blackboard blackboard blackboard stores facts for the agent orchestration pipeline blackboard", 0.9)
	add("rel_both:1", "/proj/blackboard_api.go", "blackboard_api.go",
		"blackboard blackboard API exposes read write and clear operations on the blackboard", 0.88)

	// 3 relevant docs only in vector (high sim, no query term).
	add("rel_vec:0", "/proj/notebook.go", "notebook.go",
		"shared memory notebook persists intermediate reasoning across steps", 0.85)
	add("rel_vec:1", "/proj/memory.go", "memory.go",
		"agent memory compaction summarizes conversation context window", 0.82)
	add("rel_vec:2", "/proj/facts.go", "facts.go",
		"fact store retains key value pairs for later retrieval", 0.80)

	// 40 noise docs: low vector sim but contain the query term once →
	// appear in the vector tail AND the lexical list. Very long padding
	// dilutes the term frequency so BM25 drops below 10% of the
	// rel_both max, placing noise firmly in the lexical tail where the
	// relative score threshold discards it before fusion.
	for i := 0; i < 40; i++ {
		id := "noise:" + itoa(i)
		path := "/proj/pkg/noise_" + itoa(i) + ".json"
		name := "noise_" + itoa(i) + ".json"
		// Single occurrence of the query term buried in very long
		// padding so BM25 is well below the relative cutoff.
		padding := strings.Repeat("padding token ", 200+i%10)
		content := padding + " blackboard " + itoa(i)
		add(id, path, name, content, 0.05)
	}

	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), vecDocs, lexDocs); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()
	svc.SetReady(true)
	return svc
}

// formatSim formats a similarity value as a trimmed decimal string.
func formatSim(v float64) string {
	// Simple formatting: trim trailing zeros from a 2-decimal representation.
	s := ""
	whole := int(v)
	frac := int((v-float64(whole))*100 + 0.5)
	s += itoa(whole) + "."
	if frac < 10 {
		s += "0"
	}
	s += itoa(frac)
	return s
}

// itoa converts a non-negative int to its decimal string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestHybridSearch_NoiseCorpusRRF verifies that pre-fusion score
// thresholds suppress noise-tail promotion in Reciprocal Rank Fusion.
//
// With thresholds enabled (the default HybridConfig), one-sided
// relevant documents (REL_VEC) that appear only in the vector list
// remain in the hybrid top-K, and noise documents that appear in both
// tails are suppressed. With thresholds disabled, noise documents
// dominate the top-K because their double RRF contribution outweighs
// the single contribution of one-sided relevant hits.
func TestHybridSearch_NoiseCorpusRRF(t *testing.T) {
	const topK = 10

	t.Run("thresholds suppress noise and keep one-sided relevant hits", func(t *testing.T) {
		svc := seedNoiseCorpus(t, DefaultHybridConfig())
		results, err := svc.HybridSearch(context.Background(), SearchOptions{
			Query: "blackboard", TopK: topK, Mode: ModeHybrid,
		})
		if err != nil {
			t.Fatalf("HybridSearch: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected non-empty results")
		}

		// Every result in top-K must be a relevant doc (rel_both or rel_vec),
		// not a noise doc.
		for _, r := range results {
			if strings.HasPrefix(r.FileName, "noise_") {
				t.Errorf("noise doc %q leaked into hybrid top-K with thresholds enabled; "+
					"score=%.5f vecRank=%d lexRank=%d", r.FileName, r.Score, r.VectorRank, r.LexicalRank)
			}
		}

		// At least one one-sided relevant (rel_vec) doc must survive in top-K.
		// This is the core assertion: without thresholds, one-sided relevant
		// hits are displaced by noise from the intersection of tails.
		foundOneSided := false
		for _, r := range results {
			if strings.HasPrefix(r.FileName, "notebook.go") ||
				strings.HasPrefix(r.FileName, "memory.go") ||
				strings.HasPrefix(r.FileName, "facts.go") {
				foundOneSided = true
				break
			}
		}
		if !foundOneSided {
			names := make([]string, 0, len(results))
			for _, r := range results {
				names = append(names, r.FileName)
			}
			t.Errorf("expected at least one one-sided relevant (rel_vec) doc in hybrid top-K "+
				"with thresholds enabled, got: %v", names)
		}
	})

	t.Run("thresholds disabled lets noise dominate", func(t *testing.T) {
		// Zero HybridConfig: thresholds off, k/fanout resolved to defaults.
		svc := seedNoiseCorpus(t, HybridConfig{})
		results, err := svc.HybridSearch(context.Background(), SearchOptions{
			Query: "blackboard", TopK: topK, Mode: ModeHybrid,
		})
		if err != nil {
			t.Fatalf("HybridSearch: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected non-empty results")
		}

		// Without thresholds, noise docs (which appear in both tails) earn a
		// double RRF contribution and crowd out one-sided relevant hits.
		// We assert that at least one noise doc appears in the top-K — this
		// confirms the bug exists without thresholds and that the test is
		// meaningfully exercising the noise-tail scenario.
		noiseInTop := 0
		for _, r := range results {
			if strings.HasPrefix(r.FileName, "noise_") {
				noiseInTop++
			}
		}
		if noiseInTop == 0 {
			names := make([]string, 0, len(results))
			for _, r := range results {
				names = append(names, r.FileName)
			}
			t.Fatalf("noise did not dominate top-K with thresholds disabled (results: %v); "+
				"corpus may need recalibration for this embedding model", names)
		}

		// Sort by score descending for diagnostics.
		sorted := make([]SearchResult, len(results))
		copy(sorted, results)
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].Score > sorted[j].Score
		})
		t.Logf("top-K with thresholds disabled (noise in top-K=%d):", noiseInTop)
		for i, r := range sorted {
			t.Logf("  %2d. %-20s score=%.5f vecRank=%d lexRank=%d vecScore=%.4f lexScore=%.4f",
				i+1, r.FileName, r.Score, r.VectorRank, r.LexicalRank, r.VectorScore, r.LexicalScore)
		}
	})
}

// TestPassesVectorScoreGate verifies the pre-fusion vector score gate
// semantics: a hit survives when it is above both the absolute floor
// and the relative (ratio × top) cutoff. Zero thresholds disable the
// gate entirely.
func TestPassesVectorScoreGate(t *testing.T) {
	tests := []struct {
		name   string
		sim    float32
		maxSim float32
		hc     HybridConfig
		want   bool
	}{
		{
			name: "above both thresholds passes",
			sim:  0.5, maxSim: 1.0,
			hc:   HybridConfig{VectorScoreFloor: 0.15, VectorScoreRatio: 0.25},
			want: true,
		},
		{
			name: "below absolute floor rejected",
			sim:  0.1, maxSim: 1.0,
			hc:   HybridConfig{VectorScoreFloor: 0.15, VectorScoreRatio: 0.25},
			want: false,
		},
		{
			name: "below relative ratio rejected",
			sim:  0.2, maxSim: 1.0,
			hc:   HybridConfig{VectorScoreFloor: 0.0, VectorScoreRatio: 0.25},
			want: false,
		},
		{
			name: "zero thresholds disable gate",
			sim:  0.01, maxSim: 1.0,
			hc:   HybridConfig{},
			want: true,
		},
		{
			name: "floor only, above passes",
			sim:  0.3, maxSim: 1.0,
			hc:   HybridConfig{VectorScoreFloor: 0.15},
			want: true,
		},
		{
			name: "ratio only, exactly at boundary passes",
			sim:  0.25, maxSim: 1.0,
			hc:   HybridConfig{VectorScoreRatio: 0.25},
			want: true,
		},
		{
			name: "maxSim zero with ratio disabled (no division)",
			sim:  0.5, maxSim: 0.0,
			hc:   HybridConfig{VectorScoreRatio: 0.25},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := passesVectorScoreGate(tt.sim, tt.maxSim, tt.hc)
			if got != tt.want {
				t.Errorf("passesVectorScoreGate(sim=%v, maxSim=%v, hc=%+v) = %v, want %v",
					tt.sim, tt.maxSim, tt.hc, got, tt.want)
			}
		})
	}
}

// TestPassesLexicalScoreGate verifies the pre-fusion lexical score gate
// semantics: a hit survives when its BM25 is at least ratio × top. Zero
// threshold disables the gate; zero maxScore disables the gate (avoids
// rejecting everything when the lexical list is degenerate).
func TestPassesLexicalScoreGate(t *testing.T) {
	tests := []struct {
		name     string
		score    float32
		maxScore float32
		hc       HybridConfig
		want     bool
	}{
		{
			name:  "above ratio passes",
			score: 0.5, maxScore: 1.0,
			hc:   HybridConfig{LexicalScoreRatio: 0.1},
			want: true,
		},
		{
			name:  "below ratio rejected",
			score: 0.05, maxScore: 1.0,
			hc:   HybridConfig{LexicalScoreRatio: 0.1},
			want: false,
		},
		{
			name:  "zero ratio disables gate",
			score: 0.001, maxScore: 1.0,
			hc:   HybridConfig{},
			want: true,
		},
		{
			name:  "zero maxScore disables gate",
			score: 0.001, maxScore: 0.0,
			hc:   HybridConfig{LexicalScoreRatio: 0.1},
			want: true,
		},
		{
			name:  "exactly at boundary passes",
			score: 0.1, maxScore: 1.0,
			hc:   HybridConfig{LexicalScoreRatio: 0.1},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := passesLexicalScoreGate(tt.score, tt.maxScore, tt.hc)
			if got != tt.want {
				t.Errorf("passesLexicalScoreGate(score=%v, maxScore=%v, hc=%+v) = %v, want %v",
					tt.score, tt.maxScore, tt.hc, got, tt.want)
			}
		})
	}
}
