package embedding

import (
	"fmt"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
)

// Tokenizer wraps a HuggingFace-compatible WordPiece tokenizer loaded from
// a tokenizer.json file. It produces input_ids, attention_mask, and
// token_type_ids suitable for BERT-family models like jina-embeddings-v2-small-en.
type Tokenizer struct {
	inner *tokenizer.Tokenizer
}

// NewTokenizer loads a tokenizer from a HuggingFace tokenizer.json file.
func NewTokenizer(path string) (*Tokenizer, error) {
	tk, err := pretrained.FromFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading tokenizer from %s: %w", path, err)
	}
	return &Tokenizer{inner: tk}, nil
}

// Encode tokenizes a single text string and returns padded/truncated tensors
// ready for ONNX inference. maxLen controls the maximum sequence length
// (including [CLS] and [SEP] special tokens).
func (t *Tokenizer) Encode(text string, maxLen int) (inputIDs, attentionMask, tokenTypeIDs []int64) {
	// Guard against zero or too-small maxLen which would cause index-out-of-range.
	if maxLen < 2 {
		// Return empty slices; caller must provide a valid maxLen >= 2.
		return nil, nil, nil
	}

	en, err := t.inner.EncodeSingle(text, true)
	if err != nil {
		// Return a minimal valid encoding on error (just [CLS][SEP]).
		// This is a best-effort fallback; the caller receives a valid but empty embedding.
		inputIDs = make([]int64, maxLen)
		attentionMask = make([]int64, maxLen)
		tokenTypeIDs = make([]int64, maxLen)
		inputIDs[0] = 101 // [CLS]
		inputIDs[1] = 102 // [SEP]
		attentionMask[0] = 1
		attentionMask[1] = 1
		return inputIDs, attentionMask, tokenTypeIDs
	}

	ids := en.GetIds()
	mask := en.GetAttentionMask()
	typeIDs := en.GetTypeIds()

	// Truncate if needed, with bounds safety for mask/typeIDs slices.
	seqLen := len(ids)
	if seqLen > maxLen {
		seqLen = maxLen
	}
	// Ensure mask and typeIDs are at least seqLen to avoid panics.
	if len(mask) < seqLen {
		seqLen = len(mask)
	}
	if len(typeIDs) < seqLen {
		seqLen = len(typeIDs)
	}
	ids = ids[:seqLen]
	mask = mask[:seqLen]
	typeIDs = typeIDs[:seqLen]

	// Convert to int64 and pad to maxLen.
	inputIDs = make([]int64, maxLen)
	attentionMask = make([]int64, maxLen)
	tokenTypeIDs = make([]int64, maxLen)

	for i := 0; i < seqLen; i++ {
		inputIDs[i] = int64(ids[i])
		attentionMask[i] = int64(mask[i])
		tokenTypeIDs[i] = int64(typeIDs[i])
	}

	return inputIDs, attentionMask, tokenTypeIDs
}

// EncodeBatch tokenizes multiple texts and returns batched tensors.
// Each returned slice is flattened in row-major order: [batch_size * maxLen].
func (t *Tokenizer) EncodeBatch(texts []string, maxLen int) (inputIDs, attentionMask, tokenTypeIDs []int64) {
	batchSize := len(texts)
	totalLen := batchSize * maxLen

	inputIDs = make([]int64, totalLen)
	attentionMask = make([]int64, totalLen)
	tokenTypeIDs = make([]int64, totalLen)

	for i, text := range texts {
		ids, mask, types := t.Encode(text, maxLen)
		offset := i * maxLen
		copy(inputIDs[offset:offset+maxLen], ids)
		copy(attentionMask[offset:offset+maxLen], mask)
		copy(tokenTypeIDs[offset:offset+maxLen], types)
	}

	return inputIDs, attentionMask, tokenTypeIDs
}
