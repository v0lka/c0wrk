# Embedding

## Purpose

ONNX-based local text embedding for semantic search, plus a document chunker and a built-in vector search tool. Everything runs in-process — no external API calls are required for embedding. The package provides the embedder, tokenizer, and chunker primitives; the persistent vector index/store itself is a host-application concern (the SDK supplies the `semantic_search` tool that delegates to a host-provided search function).

## Key Files

- `github.com/v0lka/sp4rk/embedding` — `Embedder`, `EmbedderConfig`, `NewEmbedder`, `EmbedDocuments`/`EmbedQuery`/`EmbeddingFunc`/`Close`
- `github.com/v0lka/sp4rk/embedding` (runtime) — ONNX Runtime lifecycle (`initONNXRuntime`, `destroyONNXRuntime`, reusable session with pre-allocated tensors)
- `github.com/v0lka/sp4rk/embedding` (tokenizer) — `Tokenizer`, `NewTokenizer`, `Encode`/`EncodeBatch`
- `github.com/v0lka/sp4rk/embedding` (chunker) — `Chunk`, `ChunkerConfig`, `ChunkFile`, `ComputeFileHash`
- `github.com/v0lka/sp4rk/tools/builtins` — `VectorSearchTool` (the `semantic_search` tool), `VectorSearchFunc`, `VectorSearchResult`

## Core Types

```go
type EmbedderConfig struct {
    ModelPath     string // .onnx model file
    TokenizerPath string // HuggingFace tokenizer.json
    LibraryPath   string // ONNX Runtime shared library
    MaxSeqLength  int    // default 512
    HiddenDim     int    // default 512
    Logger        *slog.Logger
}

type Chunk struct {
    Content   string
    FilePath  string // absolute path to source file
    FileName  string // basename
    StartLine int    // 1-based
    EndLine   int    // 1-based
    Language  string // detected type ("go", "typescript", "markdown", …)
}

type ChunkerConfig struct {
    MaxChunkSize int // default 1500
    Overlap      int // default 200 (reduced to MaxChunkSize/5 if >= MaxChunkSize)
}
```

## Flow

```
Indexing (host):
  ChunkFile(path, content, cfg) → []Chunk  (with location metadata)
       │
       ├─ ComputeFileHash(content) → SHA-256 (change detection)
       └─ Embedder.EmbedDocuments(chunks) → [][]float32  (mean-pooled, L2-normalized)
            stored in the host's vector index keyed by file hash

Querying:
  Embedder.EmbedQuery(text) → []float32
       └─ host index performs hybrid (vector + BM25) search
       └─ VectorSearchTool surfaces results to the agent
```

## Embedder

`NewEmbedder(cfg)` loads the tokenizer and initializes the ONNX Runtime environment. `ModelPath`, `TokenizerPath`, and `LibraryPath` are required; `MaxSeqLength`/`HiddenDim` default to `512`. The init sequence is `initONNXRuntime(libraryPath)` → `NewTokenizer(tokenizerPath)` → `newONNXSession(modelPath, maxSeqLen, hiddenDim)`; on any failure the ONNX environment is cleaned up before returning the error.

`EmbedDocuments` uses a **fast path** for single-text embedding: a persistent ONNX session with pre-allocated tensors is reused (session creation is ~2s; inference ~50ms). Larger batches create a temporary session. All embeddings are **mean-pooled** (attention mask) and **L2-normalized**. `EmbeddingFunc()` returns a chromem-go-compatible embedding function.

### Process-global singleton limitation

The ONNX Runtime is a **process-global singleton** — only one `Embedder` can exist at a time, and it lives for the process lifetime. There is no reference counting; the single owner must call `Close()` at shutdown. Sufficient for a single-process application; a known limitation for library-reuse scenarios.

For `jina-embeddings-v2-small-en`: inputs are `input_ids`/`attention_mask`/`token_type_ids` (`int64`, `[batch, seq]`); output is `last_hidden_state` (`float32`, `[batch, seq, hiddenDim]`); post-processing is mean pooling + L2 normalization.

## Tokenizer

`Tokenizer` wraps a HuggingFace-compatible WordPiece tokenizer loaded from `tokenizer.json`. `Encode(text, maxLen)`/`EncodeBatch(texts, maxLen)` produce `input_ids`/`attention_mask`/`token_type_ids` suitable for BERT-family models, padded/truncated to `maxLen` (including `[CLS]`/`[SEP]` special tokens). `EncodeBatch` returns flattened row-major tensors of shape `[batch_size * maxLen]`.

## Chunker

`ChunkFile(filePath, content, cfg)` splits a file's content into semantically meaningful chunks using a strategy chosen by file type:

| File type | Strategy |
| --------- | -------- |
| Code (`.go`, `.ts`, `.py`, `.rs`, `.java`, …) | Split by blank lines, then fixed-size if a section is still oversized. |
| Markdown (`.md`, `.mdx`) | Split by `## ` (H2) headers, then blank lines, then fixed-size. |
| Config (`.json`, `.yaml`, `.yml`, `.toml`, …) | Split by top-level keys; fall back to fixed-size. |
| Other | Fixed-size split with overlap. |

Files with null bytes in the first 512 bytes are treated as binary and return `nil` (no chunks). `ComputeFileHash(content)` returns the SHA-256 hex digest for change detection (re-embed only when a file's hash changes).

## VectorSearchTool

`semantic_search` is a built-in tool (in `tools/builtins`) that searches the codebase using hybrid (vector + BM25) similarity matching. It is constructed with a host-provided search function and an optional wait function (the embedder loads asynchronously; searches return empty results until ready).

```go
func NewVectorSearchTool(searchFunc VectorSearchFunc, waitFunc VectorSearchWaitFunc) *VectorSearchTool
```

### Search modes

| Mode | Description |
| ---- | ----------- |
| `hybrid` (default) | Fuses vector and BM25 results via Reciprocal Rank Fusion; auto-falls-back to `vector` when the lexical index is empty. |
| `vector` | Embedding similarity only. |
| `lexical` | BM25 only. |

Parameters: `query` (natural-language description; tokens prefixed with `+` are must-match substrings), `top_k` (default 10, max 50), `file_pattern` (optional glob), `must_match` (literal substrings that must all appear), `mode`. Results carry file path/name, content, and line range.

## Invariants

- Only one `Embedder` exists per process (ONNX Runtime singleton); `Close()` releases it.
- All embeddings are mean-pooled and L2-normalized before being returned.
- `ChunkFile` returns `nil` for binary files; chunks always carry location metadata (file path + 1-based line range).
- The persistent vector index/store is host-side; the SDK provides only the embedder, tokenizer, chunker, and the search tool that delegates to a host-provided function.
- `semantic_search` returns empty results until the embedder/index is ready (the wait function gates it).

## Configuration

`EmbedderConfig` and `ChunkerConfig` are the configuration surfaces. Defaults: `MaxSeqLength`/`HiddenDim` = `512`; `MaxChunkSize` = `1500`; `Overlap` = `200` (reduced to `MaxChunkSize/5` if it would exceed `MaxChunkSize`). Model/tokenizer/runtime library paths are host-resolved at wiring time. Because the embedder loads asynchronously, the host gates `semantic_search` with a wait function and surfaces readiness separately.

## Extension Points

- **Custom embedding model**: provide a different ONNX model + tokenizer; adjust `MaxSeqLength`/`HiddenDim` to match. The singleton limitation means swapping models requires `Close()` first.
- **Custom chunking**: implement an alternative splitter producing `[]Chunk` (each with location metadata) and feed chunks to `EmbedDocuments`.
- **Vector index backend**: the host owns the index/store; `EmbeddingFunc()` returns a chromem-go-compatible function for `chromem.NewCollection`, but any store consuming `[][]float32` works.
- **Custom search**: supply a `VectorSearchFunc` to `NewVectorSearchTool` implementing the host's retrieval (hybrid/vector/lexical).

## Related Specs

- [../tool-system/builtins.md](tool-system/builtins.md) — `semantic_search` tool catalog entry
- [../tool-system/README.md](tool-system/README.md) — tool registration and execution pipeline
- [llm-providers.md](llm-providers.md) — embeddings run fully in-process (no LLM provider required)
