package embedding

import (
	"fmt"
	"math"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// onnxInitOnce ensures initONNXRuntime performs its work at most once per process.
// onnxInitErr caches the outcome of that single initialization attempt.
var (
	onnxInitOnce sync.Once
	onnxInitErr  error
)

// initONNXRuntime initializes the global ONNX Runtime environment.
// Must be called once before creating any sessions. libraryPath is the path to
// the ONNX Runtime shared library.
//
// Initialization is guarded by sync.Once, so only the first call performs the
// actual work; all subsequent calls return the cached result. Consequences:
//   - Once initialized, the ONNX Runtime cannot be reinitialized in the same
//     process, even after destroyONNXRuntime has been called. The first
//     libraryPath supplied is final.
//   - If the first initialization fails, every later call returns the same
//     error without retrying.
func initONNXRuntime(libraryPath string) error {
	onnxInitOnce.Do(func() {
		ort.SetSharedLibraryPath(libraryPath)
		if err := ort.InitializeEnvironment(); err != nil {
			onnxInitErr = fmt.Errorf("initializing ONNX Runtime environment: %w", err)
			return
		}
	})
	return onnxInitErr
}

// destroyONNXRuntime cleans up the global ONNX Runtime environment.
func destroyONNXRuntime() error {
	return ort.DestroyEnvironment()
}

// onnxSession holds a reusable ONNX Runtime session with pre-allocated tensors
// for batchSize=1 inference. Creating an ONNX session is expensive (~2s for model
// loading + graph optimization), while inference is fast (~50ms). Reusing the
// session eliminates per-call overhead when embedding one text at a time.
type onnxSession struct {
	session    *ort.AdvancedSession
	inputIDs   *ort.Tensor[int64]
	attMask    *ort.Tensor[int64]
	tokenTypes *ort.Tensor[int64]
	output     *ort.Tensor[float32]
	seqLen     int
	hiddenDim  int
}

// newONNXSession creates a persistent ONNX session for batchSize=1 inference.
// The session and its tensors are kept alive for reuse across multiple calls.
func newONNXSession(modelPath string, seqLen, hiddenDim int) (*onnxSession, error) {
	inputShape := ort.NewShape(1, int64(seqLen))

	inputIDsData := make([]int64, seqLen)
	inputIDsTensor, err := ort.NewTensor(inputShape, inputIDsData)
	if err != nil {
		return nil, fmt.Errorf("creating input_ids tensor: %w", err)
	}

	attMaskData := make([]int64, seqLen)
	attMaskTensor, err := ort.NewTensor(inputShape, attMaskData)
	if err != nil {
		_ = inputIDsTensor.Destroy()
		return nil, fmt.Errorf("creating attention_mask tensor: %w", err)
	}

	tokenTypesData := make([]int64, seqLen)
	tokenTypesTensor, err := ort.NewTensor(inputShape, tokenTypesData)
	if err != nil {
		_ = inputIDsTensor.Destroy()
		_ = attMaskTensor.Destroy()
		return nil, fmt.Errorf("creating token_type_ids tensor: %w", err)
	}

	outputShape := ort.NewShape(1, int64(seqLen), int64(hiddenDim))
	outputTensor, err := ort.NewEmptyTensor[float32](outputShape)
	if err != nil {
		_ = inputIDsTensor.Destroy()
		_ = attMaskTensor.Destroy()
		_ = tokenTypesTensor.Destroy()
		return nil, fmt.Errorf("creating output tensor: %w", err)
	}

	session, err := ort.NewAdvancedSession(
		modelPath,
		[]string{"input_ids", "attention_mask", "token_type_ids"},
		[]string{"last_hidden_state"},
		[]ort.Value{inputIDsTensor, attMaskTensor, tokenTypesTensor},
		[]ort.Value{outputTensor},
		nil,
	)
	if err != nil {
		_ = inputIDsTensor.Destroy()
		_ = attMaskTensor.Destroy()
		_ = tokenTypesTensor.Destroy()
		_ = outputTensor.Destroy()
		return nil, fmt.Errorf("creating ONNX session: %w", err)
	}

	return &onnxSession{
		session:    session,
		inputIDs:   inputIDsTensor,
		attMask:    attMaskTensor,
		tokenTypes: tokenTypesTensor,
		output:     outputTensor,
		seqLen:     seqLen,
		hiddenDim:  hiddenDim,
	}, nil
}

// run executes inference using the persistent session with the given input data.
// The caller must ensure len(inputIDs) == len(attMask) == len(tokenTypes) == seqLen.
// Returns a single pooled, L2-normalized embedding vector.
func (s *onnxSession) run(inputIDs, attMask, tokenTypes []int64) ([]float32, error) {
	// Validate input lengths to prevent stale data from prior inferences.
	if len(inputIDs) != s.seqLen || len(attMask) != s.seqLen || len(tokenTypes) != s.seqLen {
		return nil, fmt.Errorf("input length mismatch: got (%d,%d,%d), want seqLen=%d",
			len(inputIDs), len(attMask), len(tokenTypes), s.seqLen)
	}
	copy(s.inputIDs.GetData(), inputIDs)
	copy(s.attMask.GetData(), attMask)
	copy(s.tokenTypes.GetData(), tokenTypes)

	if err := s.session.Run(); err != nil {
		return nil, fmt.Errorf("running ONNX inference: %w", err)
	}

	results := meanPoolAndNormalize(s.output.GetData(), attMask, 1, s.seqLen, s.hiddenDim)
	return results[0], nil
}

// destroy releases the session and all associated tensors.
func (s *onnxSession) destroy() {
	if s.session != nil {
		_ = s.session.Destroy()
	}
	if s.inputIDs != nil {
		_ = s.inputIDs.Destroy()
	}
	if s.attMask != nil {
		_ = s.attMask.Destroy()
	}
	if s.tokenTypes != nil {
		_ = s.tokenTypes.Destroy()
	}
	if s.output != nil {
		_ = s.output.Destroy()
	}
}

// runInferenceBatch runs the ONNX model on a batch of tokenized inputs and returns
// pooled, normalized embeddings. Creates a temporary session for the batch.
// The inputs are flat int64 slices of shape [batchSize, seqLen].
//
// For jina-embeddings-v2-small-en:
//   - Inputs: input_ids, attention_mask, token_type_ids (all int64, shape [batch, seq])
//   - Output: last_hidden_state (float32, shape [batch, seq, hiddenDim])
//   - Post-processing: mean pooling + L2 normalization
func runInferenceBatch(modelPath string, batchSize, seqLen, hiddenDim int, inputIDs, attentionMask, tokenTypeIDs []int64) ([][]float32, error) {
	inputShape := ort.NewShape(int64(batchSize), int64(seqLen))

	inputIDsTensor, err := ort.NewTensor(inputShape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("creating input_ids tensor: %w", err)
	}
	defer func() { _ = inputIDsTensor.Destroy() }()

	attMaskTensor, err := ort.NewTensor(inputShape, attentionMask)
	if err != nil {
		return nil, fmt.Errorf("creating attention_mask tensor: %w", err)
	}
	defer func() { _ = attMaskTensor.Destroy() }()

	tokenTypeTensor, err := ort.NewTensor(inputShape, tokenTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("creating token_type_ids tensor: %w", err)
	}
	defer func() { _ = tokenTypeTensor.Destroy() }()

	outputShape := ort.NewShape(int64(batchSize), int64(seqLen), int64(hiddenDim))
	outputTensor, err := ort.NewEmptyTensor[float32](outputShape)
	if err != nil {
		return nil, fmt.Errorf("creating output tensor: %w", err)
	}
	defer func() { _ = outputTensor.Destroy() }()

	session, err := ort.NewAdvancedSession(
		modelPath,
		[]string{"input_ids", "attention_mask", "token_type_ids"},
		[]string{"last_hidden_state"},
		[]ort.Value{inputIDsTensor, attMaskTensor, tokenTypeTensor},
		[]ort.Value{outputTensor},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("creating ONNX session: %w", err)
	}
	defer func() { _ = session.Destroy() }()

	if err := session.Run(); err != nil {
		return nil, fmt.Errorf("running ONNX inference: %w", err)
	}

	rawOutput := outputTensor.GetData()

	// Mean pooling with attention mask, then L2 normalization.
	embeddings := meanPoolAndNormalize(rawOutput, attentionMask, batchSize, seqLen, hiddenDim)
	return embeddings, nil
}

// meanPoolAndNormalize performs masked mean pooling across the sequence dimension
// and L2-normalizes the resulting vectors.
func meanPoolAndNormalize(hiddenStates []float32, attentionMask []int64, batchSize, seqLen, hiddenDim int) [][]float32 {
	embeddings := make([][]float32, batchSize)

	for b := range batchSize {
		embedding := make([]float32, hiddenDim)
		var maskSum float32

		for s := range seqLen {
			mask := float32(attentionMask[b*seqLen+s])
			if mask == 0 {
				continue
			}
			maskSum += mask
			baseIdx := (b*seqLen + s) * hiddenDim
			for d := range hiddenDim {
				embedding[d] += hiddenStates[baseIdx+d] * mask
			}
		}

		// Average by mask sum.
		if maskSum > 0 {
			for d := range hiddenDim {
				embedding[d] /= maskSum
			}
		}

		// L2 normalization.
		var norm float64
		for d := range hiddenDim {
			norm += float64(embedding[d]) * float64(embedding[d])
		}
		norm = math.Sqrt(norm)
		if norm > 0 {
			invNorm := float32(1.0 / norm)
			for d := range hiddenDim {
				embedding[d] *= invNorm
			}
		}

		embeddings[b] = embedding
	}

	return embeddings
}
