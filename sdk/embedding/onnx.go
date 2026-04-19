package embedding

import (
	"fmt"
	"math"

	ort "github.com/yalue/onnxruntime_go"
)

// initONNXRuntime initializes the global ONNX Runtime environment.
// Must be called once before creating any sessions.
// libraryPath is the path to the ONNX Runtime shared library.
func initONNXRuntime(libraryPath string) error {
	ort.SetSharedLibraryPath(libraryPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return fmt.Errorf("initializing ONNX Runtime environment: %w", err)
	}
	return nil
}

// destroyONNXRuntime cleans up the global ONNX Runtime environment.
func destroyONNXRuntime() error {
	return ort.DestroyEnvironment()
}

// runInference runs the ONNX model on a batch of tokenized inputs and returns
// pooled, normalized embeddings. The inputs are flat int64 slices of shape
// [batchSize, seqLen].
//
// For jina-embeddings-v2-small-en:
//   - Inputs: input_ids, attention_mask, token_type_ids (all int64, shape [batch, seq])
//   - Output: last_hidden_state (float32, shape [batch, seq, hiddenDim])
//   - Post-processing: mean pooling + L2 normalization
func runInference(modelPath string, batchSize, seqLen, hiddenDim int, inputIDs, attentionMask, tokenTypeIDs []int64) ([][]float32, error) {
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
