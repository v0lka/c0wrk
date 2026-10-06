package vectorindex

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/v0lka/sp4rk/safeio"
)

const (
	embeddingCacheFormatVersion = uint16(1)
	// EmbeddingNormalizationAlgorithm identifies canonical chunk preprocessing
	// and is included in every production fingerprint.
	EmbeddingNormalizationAlgorithm = "line-endings-v1"
	embeddingCacheHeaderSize        = 10 // magic (4) + version (2) + dimension (4)
	embeddingCacheChecksumSize      = sha256.Size

	// Prune hysteresis and accounting-refresh cadence. While tracked bytes
	// stay below embeddingCachePruneAtPercent of the cap, prune takes a fast
	// path and skips the directory walk entirely. A walk that does run evicts
	// oldest entries down to embeddingCacheEvictToPercent of the cap; the
	// headroom between the two thresholds keeps a cache running near the cap
	// from walking — and evicting — on every resolved embedding batch.
	embeddingCachePruneAtPercent = int64(95)
	embeddingCacheEvictToPercent = int64(90)
	// embeddingCacheReconcileEveryPuts forces a full walk (which reconciles
	// the tracked-bytes counter against the filesystem) after this many
	// successful puts even while far below the cap, bounding accounting drift
	// from external filesystem changes. Analogous to
	// fullHashRevalidationEvery.
	embeddingCacheReconcileEveryPuts = 4096
)

var embeddingCacheMagic = [4]byte{'C', '0', 'E', 'C'}

// EmbeddingFingerprintParams are the model settings that affect generated vectors.
type EmbeddingFingerprintParams struct {
	MaxSeqLength           int
	Dimension              int
	NormalizationAlgorithm string
}

// EmbeddingFingerprint hashes model and tokenizer bytes together with every
// vector-producing parameter. Paths are deliberately excluded so moving the
// same artifacts does not invalidate reusable embeddings.
func EmbeddingFingerprint(modelPath, tokenizerPath string, params EmbeddingFingerprintParams) (string, error) {
	h := sha256.New()
	if params.NormalizationAlgorithm == "" {
		params.NormalizationAlgorithm = EmbeddingNormalizationAlgorithm
	}
	for _, value := range []string{
		"embedding-fingerprint-v1\x00",
		fmt.Sprintf("max-seq=%d\x00dimension=%d\x00normalization=%s\x00", params.MaxSeqLength, params.Dimension, params.NormalizationAlgorithm),
	} {
		_, _ = io.WriteString(h, value)
	}
	for _, path := range []string{modelPath, tokenizerPath} {
		f, err := safeio.Open(path)
		if err != nil {
			return "", fmt.Errorf("opening embedding artifact for fingerprint: %w", err)
		}
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return "", fmt.Errorf("hashing embedding artifact: %w", copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("closing embedding artifact after fingerprint: %w", closeErr)
		}
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func normalizeEmbeddingChunk(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

type embeddingCache struct {
	mu          sync.Mutex
	root        string
	fingerprint string
	dimension   int
	maxBytes    int64
	logger      *slog.Logger

	// trackedBytes mirrors the on-disk size of every .vec entry, maintained
	// incrementally by put/get mutations so prune can decide whether a full
	// directory walk is needed without performing one. Every walk reconciles
	// it back to the walked truth, so bounded drift (external deletions,
	// failed removals) cannot accumulate indefinitely. Guarded by mu.
	trackedBytes int64
	// accountingSeeded reports whether trackedBytes is known to reflect the
	// actual tree. A freshly constructed cache has never walked its root, so
	// the first prune must run the walk once to seed the counter — SetProject
	// performs exactly that walk on project switch.
	accountingSeeded bool
	// putsSinceReconcile counts successful puts since the last walk; a walk
	// is forced every reconcileEveryPuts puts as a drift backstop. Guarded
	// by mu.
	putsSinceReconcile int
	// seedInFlight reports whether the one-shot background accounting seed
	// (seedAccountingAsync) is currently walking the tree. While it is set,
	// synchronous prunes defer to that walk instead of stacking a second one
	// — the embed path calls prune while holding the service write lock and
	// must never block behind a full directory walk (prune therefore uses
	// TryLock, not Lock). Unlike mu-guarded accounting state, this flag is
	// atomic because the seed goroutine sets it before taking mu, while
	// pruneLocked reads it under mu.
	seedInFlight atomic.Bool
	// reconcileEveryPuts is the forced-walk cadence; a separate field (set
	// from embeddingCacheReconcileEveryPuts) so tests can tighten it.
	reconcileEveryPuts int
	// pruneWalks counts full directory walks (scan + reconcile) performed by
	// pruneLocked and by the async seed; it exists so tests can assert the
	// fast path performs none.
	pruneWalks int
}

func newEmbeddingCache(root, fingerprint string, dimension int, maxBytes int64, logger *slog.Logger) *embeddingCache {
	if root == "" || fingerprint == "" || dimension <= 0 || maxBytes <= 0 {
		return nil
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &embeddingCache{
		root:               root,
		fingerprint:        fingerprint,
		dimension:          dimension,
		maxBytes:           maxBytes,
		logger:             logger,
		reconcileEveryPuts: embeddingCacheReconcileEveryPuts,
	}
}

func (c *embeddingCache) key(text string) string {
	chunkHash := sha256.Sum256([]byte(text))
	keyHash := sha256.Sum256([]byte(fmt.Sprintf("format=%d\x00fingerprint=%s\x00chunk=%x", embeddingCacheFormatVersion, c.fingerprint, chunkHash)))
	return hex.EncodeToString(keyHash[:])
}

func (c *embeddingCache) path(key string) string {
	return filepath.Join(c.root, key[:2], key+".vec")
}

// entrySize is the deterministic on-disk size of one entry written by put:
// fixed header + one float32 per dimension + trailing checksum. put uses it
// to grow trackedBytes without stating the file it just wrote.
func (c *embeddingCache) entrySize() int64 {
	return int64(embeddingCacheHeaderSize + c.dimension*4 + embeddingCacheChecksumSize)
}

func (c *embeddingCache) get(text string) ([]float32, bool) {
	if c == nil {
		return nil, false
	}
	key := c.key(text)
	path := c.path(key)
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := safeio.ReadFile(path)
	if err != nil {
		return nil, false
	}
	vec, err := decodeEmbeddingCacheEntry(data, c.dimension)
	if err != nil {
		c.logger.Warn("ignoring corrupt embedding cache entry", "error", err)
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			c.logger.Debug("failed to remove corrupt embedding cache entry", "error", removeErr)
		} else if removeErr == nil {
			// data holds the whole entry, so its length is the exact number
			// of bytes leaving the tree.
			c.trackedBytes -= int64(len(data))
		}
		return nil, false
	}
	return vec, true
}

func (c *embeddingCache) put(text string, vec []float32) {
	if c == nil || len(vec) != c.dimension {
		return
	}
	key := c.key(text)
	path := c.path(key)
	data := encodeEmbeddingCacheEntry(vec)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		c.logger.Debug("embedding cache directory unavailable", "error", err)
		return
	}
	if _, err := os.Stat(path); err == nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".embedding-*.tmp")
	if err != nil {
		c.logger.Debug("creating embedding cache temp file failed", "error", err)
		return
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	writeErr := func() error {
		if err := tmp.Chmod(0o600); err != nil {
			return err
		}
		if _, err := tmp.Write(data); err != nil {
			return err
		}
		if err := tmp.Sync(); err != nil {
			return err
		}
		return tmp.Close()
	}()
	if writeErr != nil {
		_ = tmp.Close()
		c.logger.Debug("writing embedding cache entry failed", "error", writeErr)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		c.logger.Debug("committing embedding cache entry failed", "error", err)
		return
	}
	// The committed entry has the deterministic entrySize and the temp file
	// was never counted, so the accounting grows by exactly one entry.
	c.trackedBytes += c.entrySize()
	c.putsSinceReconcile++
}

func (c *embeddingCache) prune() {
	if c == nil {
		return
	}
	// Never block: the embed path calls prune while holding the service write
	// lock, so blocking here would stall every queued search behind it. TryLock
	// keeps the call non-blocking. If the lock is momentarily held by a get/put
	// mutation, skipping is safe — the next batch's prune retries; if it is
	// held by a concurrent prune or the brief reconcile step of the seed walk
	// (which scans the tree WITHOUT the lock — see seedAccountingAsync), that
	// holder performs the same evict/reconcile. pruneLocked's seedInFlight
	// check still covers the window between the seed flag being set and the
	// seed goroutine acquiring mu.
	if !c.mu.TryLock() {
		return
	}
	defer c.mu.Unlock()
	c.pruneLocked()
}

// seedAccountingAsync starts the cache's one-shot accounting seed walk on a
// background goroutine. SetProject calls this instead of a synchronous prune
// so the first walk after a fresh construction — potentially hundreds of
// thousands of stat calls against a warm 512 MiB cache — never runs under
// the service write lock (which would stall every search and every
// SetProject/SwitchBranch behind it) nor under c.mu (which the embed path's
// get/put take while holding the service write lock, so holding it across the
// traversal would stall searches behind indexing just the same). The tree is
// therefore scanned WITHOUT the lock and only the cheap reconcile/evict is
// applied under it. At most one seed walk is in flight at a time; a failed
// walk (unreadable root) leaves the accounting unseeded so a later seed or
// prune retries it, preserving pruneLocked's retry semantics.
func (c *embeddingCache) seedAccountingAsync() {
	if c == nil {
		return
	}
	if !c.seedInFlight.CompareAndSwap(false, true) {
		return // a seed walk is already running
	}
	go func() {
		defer c.seedInFlight.Store(false)
		files, total, err := scanCacheTree(c.root)
		if err != nil {
			return // unreadable root: leave unseeded so a later walk retries
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.accountingSeeded {
			return
		}
		// The scan ran without c.mu, so a put that landed during it may not be
		// reflected in total; trackedBytes is reconciled to the walked truth
		// here, the same bounded-drift model the periodic reconcile relies on —
		// the entry is still on disk, so the next walk counts it, and the fast
		// path only ever delays eviction by a few entries. The in-flight flag
		// stays set for the whole function so synchronous prunes keep deferring.
		c.applyWalkLocked(files, total)
	}()
}

func encodeEmbeddingCacheEntry(vec []float32) []byte {
	payloadLen := len(vec) * 4
	data := make([]byte, embeddingCacheHeaderSize+payloadLen+embeddingCacheChecksumSize)
	copy(data[:4], embeddingCacheMagic[:])
	binary.LittleEndian.PutUint16(data[4:6], embeddingCacheFormatVersion)
	binary.LittleEndian.PutUint32(data[6:10], uint32(len(vec)))
	for i, value := range vec {
		binary.LittleEndian.PutUint32(data[embeddingCacheHeaderSize+i*4:], math.Float32bits(value))
	}
	checksum := sha256.Sum256(data[:embeddingCacheHeaderSize+payloadLen])
	copy(data[embeddingCacheHeaderSize+payloadLen:], checksum[:])
	return data
}

func decodeEmbeddingCacheEntry(data []byte, expectedDimension int) ([]float32, error) {
	if len(data) < embeddingCacheHeaderSize+embeddingCacheChecksumSize {
		return nil, errors.New("embedding cache entry is truncated")
	}
	if !bytes.Equal(data[:4], embeddingCacheMagic[:]) {
		return nil, errors.New("embedding cache magic mismatch")
	}
	if version := binary.LittleEndian.Uint16(data[4:6]); version != embeddingCacheFormatVersion {
		return nil, fmt.Errorf("embedding cache version %d is unsupported", version)
	}
	dimension := int(binary.LittleEndian.Uint32(data[6:10]))
	if dimension != expectedDimension || dimension <= 0 {
		return nil, fmt.Errorf("embedding cache dimension %d, want %d", dimension, expectedDimension)
	}
	wantLen := embeddingCacheHeaderSize + dimension*4 + embeddingCacheChecksumSize
	if len(data) != wantLen {
		return nil, fmt.Errorf("embedding cache entry size %d, want %d", len(data), wantLen)
	}
	payloadEnd := embeddingCacheHeaderSize + dimension*4
	checksum := sha256.Sum256(data[:payloadEnd])
	if !bytes.Equal(checksum[:], data[payloadEnd:]) {
		return nil, errors.New("embedding cache checksum mismatch")
	}
	vec := make([]float32, dimension)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[embeddingCacheHeaderSize+i*4:]))
		if math.IsNaN(float64(vec[i])) || math.IsInf(float64(vec[i]), 0) {
			return nil, errors.New("embedding cache vector contains a non-finite value")
		}
	}
	return vec, nil
}

type embeddingCacheFile struct {
	path    string
	size    int64
	modTime int64
}

func (c *embeddingCache) pruneLocked() {
	// Defer to an in-flight background seed walk: the embed path calls prune
	// while holding the service write lock, and blocking behind the walk (via
	// mu) would reintroduce exactly the stall seedAccountingAsync exists to
	// remove. The seed walk performs the same walk/evict/reconcile this
	// prune would.
	if c.seedInFlight.Load() {
		return
	}
	// Fast path: while the tracked tree size stays below the prune-at
	// threshold and the accounting is seeded and fresh enough, skip the
	// directory walk entirely. This is what keeps cold indexing — one prune
	// per resolved batch — from walking a growing tree once per batch.
	if c.accountingSeeded &&
		c.trackedBytes < c.maxBytes*embeddingCachePruneAtPercent/100 &&
		c.putsSinceReconcile < c.reconcileEveryPuts {
		return
	}
	c.walkLocked()
}

// walkLocked performs the full directory walk: it evicts oldest entries down
// to the low-water mark and reconciles the incremental accounting with the
// walked truth. The caller must hold c.mu (and must not be deferring to an
// in-flight seed — see pruneLocked).
func (c *embeddingCache) walkLocked() {
	files, total, err := scanCacheTree(c.root)
	if err != nil {
		// Walk failed: leave the accounting untouched so the next prune
		// retries the walk rather than trusting a stale counter.
		return
	}
	c.applyWalkLocked(files, total)
}

// scanCacheTree walks root and returns every ".vec" entry with its size and
// modification time, plus their total size. It performs no locking — the
// traversal is read-only (the expensive part: potentially hundreds of
// thousands of stat calls), and the result is reconciled under c.mu by
// applyWalkLocked.
func scanCacheTree(root string) (files []embeddingCacheFile, total int64, err error) {
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() && filepath.Ext(path) == ".vec" {
			if info, infoErr := entry.Info(); infoErr == nil {
				total += info.Size()
				files = append(files, embeddingCacheFile{path: path, size: info.Size(), modTime: info.ModTime().UnixNano()})
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return files, total, nil
}

// applyWalkLocked evicts oldest-first down to the low-water mark and
// reconciles the accounting with a tree scan. The caller must hold c.mu.
func (c *embeddingCache) applyWalkLocked(files []embeddingCacheFile, total int64) {
	c.pruneWalks++
	// Evict oldest-first down to the low-water mark rather than merely under
	// the cap: the headroom up to the prune-at threshold is the hysteresis
	// that amortizes near-cap prunes.
	targetBytes := c.maxBytes * embeddingCacheEvictToPercent / 100
	sort.Slice(files, func(i, j int) bool { return files[i].modTime < files[j].modTime })
	for _, file := range files {
		if total <= targetBytes {
			break
		}
		if err := os.Remove(file.path); err == nil {
			total -= file.size
		}
	}
	// Reconcile the incremental accounting with the walked truth, covering
	// drift from external additions/deletions and removals that failed.
	c.trackedBytes = total
	c.accountingSeeded = true
	c.putsSinceReconcile = 0
}
