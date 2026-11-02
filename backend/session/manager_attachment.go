package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/markitdown"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/safeio"
)

// attachmentConvertTimeout is the per-file budget for a single markitdown
// conversion when the Manager lazily initializes its converter.
const attachmentConvertTimeout = 2 * time.Minute

// AttachmentInfo is a metadata-only view of a pending attachment; the
// converted markdown is excluded so it never reaches the UI (only the
// orchestrator reads it via the blackboard).
type AttachmentInfo struct {
	ID           string `json:"id"`
	OriginalName string `json:"original_name"`
	Format       string `json:"format"`
	SizeBytes    int64  `json:"size_bytes"`
	IsImage      bool   `json:"is_image"`
	Thumbnail    string `json:"thumbnail,omitempty"` // JPEG data URI for image attachments
	// Path is the on-disk location of a staged image (absent for documents);
	// the UI mirrors it into the optimistic user-message metadata so
	// thumbnails render immediately.
	Path string `json:"path,omitempty"`
	// MediaType is the staged image's MIME type; with Path it lets the UI
	// build a complete StoredImageMetadata record optimistically.
	MediaType string `json:"media_type,omitempty"`
}

// AttachmentFailure describes a single file that could not be converted or
// staged, surfaced to the UI so the user knows which picks were rejected.
type AttachmentFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// AttachmentsChangedData is the payload of "attachments:changed". Attachments
// is the full current pending list (the UI replaces its store); Failed carries
// the most recent attach operation's per-file failures.
type AttachmentsChangedData struct {
	Attachments []AttachmentInfo    `json:"attachments"`
	Failed      []AttachmentFailure `json:"failed,omitempty"`
}

// isImageFormat reports whether the given normalized format (lowercase, no dot)
// is one of the image formats supported by processImage.
func isImageFormat(format string) bool {
	switch format {
	case "png", "jpg", "jpeg", "gif", "webp":
		return true
	default:
		return false
	}
}

// toAttachmentInfos returns a defensive metadata-only copy of the given
// attachments, suitable for the event payload or RPC return.
func toAttachmentInfos(attachments []orchestration.Attachment) []AttachmentInfo {
	out := make([]AttachmentInfo, 0, len(attachments))
	for _, a := range attachments {
		out = append(out, AttachmentInfo{
			ID:           a.ID,
			OriginalName: a.OriginalName,
			Format:       a.Format,
			SizeBytes:    a.SizeBytes,
			IsImage:      isImageFormat(a.Format),
		})
	}
	return out
}

// combinedAttachmentInfos builds the unified metadata-only list from document
// and image attachments; the caller must pass freshly-snapshotted slices (or
// hold session.mu).
func combinedAttachmentInfos(docs []orchestration.Attachment, images []ImageAttachment) []AttachmentInfo {
	out := toAttachmentInfos(docs)
	for _, img := range images {
		out = append(out, AttachmentInfo{
			ID:           img.ID,
			OriginalName: img.OriginalName,
			Format:       strings.TrimPrefix(img.MediaType, "image/"),
			SizeBytes:    img.SizeBytes,
			IsImage:      true,
			Thumbnail:    img.ThumbnailB64,
			Path:         img.FilePath,
			MediaType:    img.MediaType,
		})
	}
	return out
}

// imageAttachmentsToContentBlocks converts staged images into LLM image
// content blocks; nil when there are no images so HandleOptions.PendingImages
// stays zero-valued.
func imageAttachmentsToContentBlocks(images []ImageAttachment) []llm.ContentBlock {
	if len(images) == 0 {
		return nil
	}
	blocks := make([]llm.ContentBlock, 0, len(images))
	for _, img := range images {
		blocks = append(blocks, llm.ContentBlock{
			Type:      "image",
			ImageB64:  img.Base64Data,
			MediaType: img.MediaType,
		})
	}
	return blocks
}

// StoredImageMetadata is the per-image record persisted in ChatMessage.Metadata
// so images survive a restart: only the thumbnail and the on-disk path — never
// the full base64 data; restore re-reads and re-encodes the file at Path.
type StoredImageMetadata struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Thumbnail string `json:"thumbnail"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
}

// StoredImagesMetadata is the legacy image-only metadata blob; richer messages
// use UserMessageMetadata.
type StoredImagesMetadata struct {
	Images []StoredImageMetadata `json:"images"`
}

// StoredAttachmentMeta is the per-document record persisted in
// ChatMessage.Metadata — enough (name/format/size) to render a chip on reload;
// the converted markdown lives only in the blackboard for the active task.
type StoredAttachmentMeta struct {
	OriginalName string `json:"original_name"`
	Format       string `json:"format"`
	SizeBytes    int64  `json:"size_bytes"`
}

// UserMessageMetadata is the unified metadata blob for a user message (goal
// flag + images + document summaries). Every field is omitempty so an
// image-only message keeps the legacy {"images":[...]} shape.
type UserMessageMetadata struct {
	Goal        bool                   `json:"goal,omitempty"`
	Images      []StoredImageMetadata  `json:"images,omitempty"`
	Attachments []StoredAttachmentMeta `json:"attachments,omitempty"`
}

// imageAttachmentsToStoredImages maps staged images to their persisted form;
// shared by the image-only blob and the merged user-message metadata.
func imageAttachmentsToStoredImages(images []ImageAttachment) []StoredImageMetadata {
	out := make([]StoredImageMetadata, 0, len(images))
	for _, img := range images {
		out = append(out, StoredImageMetadata{
			ID:        img.ID,
			Name:      img.OriginalName,
			Thumbnail: img.ThumbnailB64,
			Path:      img.FilePath,
			MediaType: img.MediaType,
		})
	}
	return out
}

// imageAttachmentsToMetadata builds the legacy {"images":[...]} blob for an
// image-only message; nil when there are no images.
func imageAttachmentsToMetadata(images []ImageAttachment) json.RawMessage {
	if len(images) == 0 {
		return nil
	}
	data, err := json.Marshal(StoredImagesMetadata{Images: imageAttachmentsToStoredImages(images)})
	if err != nil {
		return nil
	}
	return data
}

// PendingMessageMetadata returns the persistable metadata blob for a user
// message about to be sent (goal flag OR the staged attachments); nil when
// none are present. Read before SendMessage snapshots and clears the lists.
// Image-only messages delegate to imageAttachmentsToMetadata so the blob is
// exactly {"images":[...]}.
func (m *Manager) PendingMessageMetadata(sessionID string, goal bool) (json.RawMessage, error) {
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("get message metadata: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("get message metadata: session %q not found", sessionID)
	}

	// Snapshot both pending lists under one lock (SendMessage clears them
	// right after).
	session.mu.Lock()
	images := make([]ImageAttachment, len(session.pendingImageAttachments))
	copy(images, session.pendingImageAttachments)
	docs := make([]orchestration.Attachment, len(session.pendingAttachments))
	copy(docs, session.pendingAttachments)
	session.mu.Unlock()

	// No signal at all — nothing to persist.
	if !goal && len(images) == 0 && len(docs) == 0 {
		return nil, nil
	}

	// Image-only fast path: keep the legacy {"images":[...]} shape byte-for-byte
	// and reuse imageAttachmentsToMetadata.
	if !goal && len(docs) == 0 {
		return imageAttachmentsToMetadata(images), nil
	}

	md := UserMessageMetadata{Goal: goal}
	if len(images) > 0 {
		md.Images = imageAttachmentsToStoredImages(images)
	}
	if len(docs) > 0 {
		md.Attachments = make([]StoredAttachmentMeta, 0, len(docs))
		for _, d := range docs {
			md.Attachments = append(md.Attachments, StoredAttachmentMeta{
				OriginalName: d.OriginalName,
				Format:       d.Format,
				SizeBytes:    d.SizeBytes,
			})
		}
	}

	data, err := json.Marshal(md)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// HasPendingAttachments backs the live-send gate: a message sent while a task
// is running cannot carry attachments (they flush only at task start), so the
// frontend API rejects the send. Memory-only — no lazy restore; false when the
// session is not resident or has nothing staged.
func (m *Manager) HasPendingAttachments(sessionID string) bool {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return len(sess.pendingAttachments) > 0 || len(sess.pendingImageAttachments) > 0
}

// PendingAttachmentSnapshot carries a session's staged attachments across an
// in-memory eviction (the pending lists are memory-only). The only production
// producer is the capture inside Manager.EvictSessionCarryingAttachments,
// taken under the eviction's own critical section — never build a separate
// snapshot-then-evict pair: anything staged between the two calls would be
// silently lost.
type PendingAttachmentSnapshot struct {
	Docs   []orchestration.Attachment
	Images []ImageAttachment
}

// Empty reports whether the snapshot carries nothing worth restoring.
func (s *PendingAttachmentSnapshot) Empty() bool {
	return s == nil || (len(s.Docs) == 0 && len(s.Images) == 0)
}

// SnapshotPendingAttachments is a test/inspection helper: no production flow
// calls it, and it must never substitute the capture-carrying eviction (a
// snapshot outside the eviction's critical section misses the race window).
// Memory-only; nil when the session is not resident.
func (m *Manager) SnapshotPendingAttachments(sessionID string) *PendingAttachmentSnapshot {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	snap := &PendingAttachmentSnapshot{
		Docs:   make([]orchestration.Attachment, len(sess.pendingAttachments)),
		Images: make([]ImageAttachment, len(sess.pendingImageAttachments)),
	}
	copy(snap.Docs, sess.pendingAttachments)
	copy(snap.Images, sess.pendingImageAttachments)
	return snap
}

// RestorePendingAttachments re-stages a pre-eviction snapshot on the lazily
// restored session. Both sides re-validate manager membership after each
// append: a second eviction racing the restore (the retrying send's bind)
// would otherwise strand the snapshot items on the orphaned object — its
// capture ran before the items landed. Call only when the restore is wanted
// (right after the eviction whose flow keeps the attachments alive).
func (m *Manager) RestorePendingAttachments(sessionID string, snap *PendingAttachmentSnapshot) error {
	if snap.Empty() {
		return nil
	}
	sess, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return fmt.Errorf("restore staged attachments: %w", err)
	}
	if sess == nil {
		return fmt.Errorf("restore staged attachments: session %q not found", sessionID)
	}
	return m.restorePendingAttachments(sessionID, sess, snap)
}

// restorePendingAttachments appends under sess.mu, then re-checks manager
// membership AFTER sess.mu is released (never under it: eviction takes
// m.mu → sess.mu — inverting the order could deadlock); on a mismatch the
// lazily restored session gets the snapshot (the held-IDs dedupe prevents
// double-staging in either settling order). The separated session parameter
// mirrors appendPendingAttachments so tests can drive the orphaned-handle
// race deterministically.
func (m *Manager) restorePendingAttachments(sessionID string, sess *Session, snap *PendingAttachmentSnapshot) error {
	for attempt := 0; ; attempt++ {
		sess.mu.Lock()
		held := pendingAttachmentIDs(sess)
		for _, doc := range snap.Docs {
			if !held[doc.ID] {
				sess.pendingAttachments = append(sess.pendingAttachments, doc)
				held[doc.ID] = true
			}
		}
		for _, img := range snap.Images {
			if !held[img.ID] {
				sess.pendingImageAttachments = append(sess.pendingImageAttachments, img)
				held[img.ID] = true
			}
		}
		sess.mu.Unlock()

		m.mu.RLock()
		live := m.sessions[sessionID] == sess
		m.mu.RUnlock()
		if live {
			return nil
		}
		if attempt > 0 {
			return errors.New("restore staged attachments: the session's workspace switched twice while restoring — the staged files were lost; re-attach them")
		}
		restored, err := m.getOrRestoreSession(sessionID)
		if err != nil {
			return fmt.Errorf("restore staged attachments across the workspace switch: %w", err)
		}
		if restored == nil {
			return fmt.Errorf("restore staged attachments: session %q not found", sessionID)
		}
		sess = restored
	}
}

// pendingAttachmentIDs returns the set of IDs currently staged on the
// session's pending lists (documents and images alike). Caller holds sess.mu.
func pendingAttachmentIDs(sess *Session) map[string]bool {
	ids := make(map[string]bool, len(sess.pendingAttachments)+len(sess.pendingImageAttachments))
	for _, doc := range sess.pendingAttachments {
		ids[doc.ID] = true
	}
	for _, img := range sess.pendingImageAttachments {
		ids[img.ID] = true
	}
	return ids
}

// appendPendingAttachments stages items on the session CURRENTLY registered
// under sessionID — never an orphaned in-memory object: the send-time
// workspace bind evicts the in-memory session mid-flow, and an append racing
// into that window would die with the evicted object. Membership is
// re-checked AFTER sess.mu is released (see restorePendingAttachments for
// the lock order) and the append re-staged on the lazily restored session on
// a mismatch. Returns the infos captured under the append's critical section
// plus the LIVE session, so callers derive UI feedback from what actually
// holds the items.
func (m *Manager) appendPendingAttachments(sessionID string, sess *Session, docs []orchestration.Attachment, images []ImageAttachment) ([]AttachmentInfo, *Session, error) {
	for attempt := 0; ; attempt++ {
		var infos []AttachmentInfo
		sess.mu.Lock()
		// Dedupe against what the session already holds: when the re-stage
		// path runs, the bind's snapshot may have carried these very items
		// onto the restored session first (RestorePendingAttachments skips
		// them symmetrically) — either settling order must stage once.
		held := pendingAttachmentIDs(sess)
		for _, doc := range docs {
			if !held[doc.ID] {
				sess.pendingAttachments = append(sess.pendingAttachments, doc)
				held[doc.ID] = true
			}
		}
		for _, img := range images {
			if !held[img.ID] {
				sess.pendingImageAttachments = append(sess.pendingImageAttachments, img)
				held[img.ID] = true
			}
		}
		infos = combinedAttachmentInfos(sess.pendingAttachments, sess.pendingImageAttachments)
		sess.mu.Unlock()

		// Re-validate after releasing sess.mu: if the session was evicted
		// after our append, the bind's snapshot either captured it (taken
		// under sess.mu after our append) or missed it — the latter case
		// re-stages below.
		m.mu.RLock()
		live := m.sessions[sessionID] == sess
		m.mu.RUnlock()
		if live {
			return infos, sess, nil
		}
		if attempt > 0 {
			return nil, nil, errors.New("stage attachments: the session's workspace switched twice while attaching — retry the attach")
		}
		restored, err := m.getOrRestoreSession(sessionID)
		if err != nil {
			return nil, nil, fmt.Errorf("stage attachments across the workspace switch: %w", err)
		}
		if restored == nil {
			return nil, nil, fmt.Errorf("stage attachments: session %q not found", sessionID)
		}
		sess = restored
	}
}

// EmitStagedAttachmentsLost reports a failed RestorePendingAttachments: the
// staged files did not survive the workspace switch, so the event carries the
// current (typically empty) pending list plus a per-file failure naming every
// snapshot item, letting the UI drop stale chips and the user re-attach.
func (m *Manager) EmitStagedAttachmentsLost(sessionID string, snap *PendingAttachmentSnapshot) {
	if snap.Empty() {
		return
	}
	infos, _ := m.GetSessionAttachments(sessionID)
	failures := make([]AttachmentFailure, 0, len(snap.Docs)+len(snap.Images))
	const reason = "could not be carried across the workspace switch — attach it again"
	for _, doc := range snap.Docs {
		failures = append(failures, AttachmentFailure{Path: doc.OriginalName, Error: reason})
	}
	for _, img := range snap.Images {
		failures = append(failures, AttachmentFailure{Path: img.OriginalName, Error: reason})
	}
	m.emitAttachmentsChanged(sessionID, infos, failures)
}

// emitAttachmentsChanged is the single emit point for "attachments:changed"
// so the payload shape stays consistent across attach/remove/send-clear.
func (m *Manager) emitAttachmentsChanged(sessionID string, attachments []AttachmentInfo, failures []AttachmentFailure) {
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "attachments:changed",
		Data: AttachmentsChangedData{
			Attachments: attachments,
			Failed:      failures,
		},
	})
}

// converterOrInit lazily creates the markitdown converter (manager logger +
// attachmentConvertTimeout); a missing binary yields a wrapped error.
func (m *Manager) converterOrInit() (*markitdown.Converter, error) {
	m.converterMu.Lock()
	defer m.converterMu.Unlock()
	if m.converter != nil {
		return m.converter, nil
	}
	// The venv interpreter is re-resolved on every init attempt: the
	// tool-manager may not have finished installing markitdown yet.
	c, err := markitdown.NewConverter(markitdown.Options{
		Logger:     m.log(),
		Timeout:    attachmentConvertTimeout,
		PythonPath: m.markitdownPythonPath(),
	})
	if err != nil {
		return nil, fmt.Errorf("init markitdown converter: %w", err)
	}
	m.converter = c
	return c, nil
}

// AttachFiles converts each supported file via markitdown and stages the
// result on the session (flushed into the blackboard at the next SendMessage).
// An "attachments:changed" event is emitted after each success and once at
// the end carrying per-file failures; file-level failures ride the event
// payload, NOT the error, so partial success keeps the attached files.
func (m *Manager) AttachFiles(ctx context.Context, sessionID string, paths []string) ([]AttachmentInfo, error) {
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("attach files: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("attach files: session %q not found", sessionID)
	}

	imagesDir := config.SessionImagesDir(m.agentDir, session.ProjectID, sessionID)
	var failures []AttachmentFailure

	for _, path := range paths {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))

		// Image path: stage as an ImageAttachment — images go to the LLM as
		// image content blocks, not markdown context.
		if isImageFormat(ext) {
			if err := m.attachImage(session, sessionID, path, imagesDir, ""); err != nil {
				failures = append(failures, AttachmentFailure{
					Path:  filepath.Base(path),
					Error: err.Error(),
				})
			}
			continue
		}

		// Document path: convert via markitdown, stage as orchestration.Attachment.
		if !markitdown.IsSupported(path) {
			failures = append(failures, AttachmentFailure{
				Path:  filepath.Base(path),
				Error: "unsupported file format",
			})
			continue
		}

		info, statErr := os.Stat(path)
		if statErr != nil {
			failures = append(failures, AttachmentFailure{
				Path:  filepath.Base(path),
				Error: fmt.Sprintf("cannot access: %v", statErr),
			})
			continue
		}

		converter, convInitErr := m.converterOrInit()
		if convInitErr != nil {
			failures = append(failures, AttachmentFailure{
				Path:  filepath.Base(path),
				Error: fmt.Sprintf("init converter: %v", convInitErr),
			})
			continue
		}

		// Per-document vision resolution against the session's CURRENTLY active
		// model (a mid-session switch applies to the next document).
		vision := m.resolveSessionVision(session)
		markdown, convErr := converter.ConvertWithVision(ctx, path, vision)
		if convErr != nil {
			failures = append(failures, AttachmentFailure{
				Path:  filepath.Base(path),
				Error: fmt.Sprintf("convert: %v", convErr),
			})
			continue
		}

		att := orchestration.Attachment{
			ID:              uuid.NewString(),
			OriginalName:    filepath.Base(path),
			OriginalPath:    path,
			Format:          ext,
			SizeBytes:       info.Size(),
			MarkdownContent: markdown,
			AttachedAt:      time.Now(),
		}

		infos, live, stageErr := m.appendPendingAttachments(sessionID, session, []orchestration.Attachment{att}, nil)
		if stageErr != nil {
			// The append lost the workspace-switch race twice (or could not
			// re-stage): per-file failure, not a silent drop.
			failures = append(failures, AttachmentFailure{
				Path:  filepath.Base(path),
				Error: stageErr.Error(),
			})
			continue
		}
		session = live

		// Emit after each conversion so chips appear one-by-one, not all at
		// once after a long batch.
		m.emitAttachmentsChanged(sessionID, infos, nil)
	}

	// Final event carries the full current pending list (authoritative
	// memory-only read shared with the return: the loop may have re-staged a
	// racing append onto a restored session, so the local reference may be
	// stale).
	result, err := m.GetSessionAttachments(sessionID)
	if err != nil {
		return nil, fmt.Errorf("list staged attachments: %w", err)
	}
	if len(failures) > 0 {
		m.emitAttachmentsChanged(sessionID, result, failures)
	}
	return result, nil
}

// attachImage processes a single image (decode, optional resize, JPEG
// re-encode, thumbnail), saves it to the session's images directory as
// {uuid}.jpg, and stages it as an ImageAttachment. The error is surfaced by
// the caller as a per-file failure, not a method-level error, so partial
// success is preserved.
//
// displayName overrides OriginalName when non-empty (the clipboard paste
// path's "c0wrk-clip-*" temp file); empty falls back to filepath.Base(path).
func (m *Manager) attachImage(session *Session, sessionID, path, imagesDir, displayName string) error {
	base64Data, mediaType, thumbURI, sizeBytes, err := processImage(path)
	if err != nil {
		return err
	}

	// The images directory is a fixed, prompt-disclosed path under the session
	// dir, and both writes stay inside the agent tree: MkdirAllRealWithin (the
	// agent dir is the boundary) only creates real directories and REFUSES a
	// pre-existing link resolving outside ~/.c0wrk (review finding #48) —
	// links inside the tree and a symlinked agent dir stay operator intent;
	// safeio.WriteFile opens the final component O_NOFOLLOW (os.WriteFile
	// would follow a symlink at imgPath and truncate its target; on Windows
	// the final symlink is still resolved, tempered by symlink creation
	// requiring elevated/dev-mode rights).
	if err := safeio.MkdirAllRealWithin(m.agentDir, imagesDir, 0o755); err != nil {
		return fmt.Errorf("create images dir: %w", err)
	}

	imgID := uuid.NewString()
	imgPath := filepath.Join(imagesDir, imgID+imageFileExtension(mediaType))

	// Decode the processed base64 for on-disk persistence: the file is the
	// source of truth for restart reconstruction; in-memory Base64Data lives
	// only until the next SendMessage snapshots it.
	imgBytes, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return fmt.Errorf("decode processed image: %w", err)
	}
	if err := safeio.WriteFile(imgPath, imgBytes, 0o644); err != nil {
		return fmt.Errorf("write image: %w", err)
	}

	name := displayName
	if name == "" {
		name = filepath.Base(path)
	}

	imgAtt := ImageAttachment{
		ID:           imgID,
		OriginalName: name,
		MediaType:    mediaType,
		Base64Data:   base64Data,
		ThumbnailB64: thumbURI,
		FilePath:     imgPath,
		SizeBytes:    sizeBytes,
	}

	infos, _, stageErr := m.appendPendingAttachments(sessionID, session, nil, []ImageAttachment{imgAtt})
	if stageErr != nil {
		// The file was already written; with staging failed nothing
		// references it — remove it or it orphans until session deletion.
		if rmErr := os.Remove(imgPath); rmErr != nil && !os.IsNotExist(rmErr) {
			m.log().Debug("failed to remove unstageable image file", "path", imgPath, "error", rmErr)
		}
		return stageErr
	}

	m.emitAttachmentsChanged(sessionID, infos, nil)
	return nil
}

// RemovePendingAttachment removes a staged attachment by ID (already-flushed
// attachments are untouched); nil when the ID is not staged. Emits
// "attachments:changed" with the remaining list.
func (m *Manager) RemovePendingAttachment(sessionID, attachmentID string) error {
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return fmt.Errorf("remove attachment: %w", err)
	}
	if session == nil {
		return fmt.Errorf("remove attachment: session %q not found", sessionID)
	}

	var removedImagePath string

	session.mu.Lock()
	found := false
	for i, a := range session.pendingAttachments {
		if a.ID == attachmentID {
			session.pendingAttachments = append(session.pendingAttachments[:i], session.pendingAttachments[i+1:]...)
			found = true
			break
		}
	}
	// Removing a pending image also deletes its on-disk copy (only pending
	// images have one; already-sent images are reconstructed from DB metadata).
	if !found {
		for i, img := range session.pendingImageAttachments {
			if img.ID == attachmentID {
				session.pendingImageAttachments = append(session.pendingImageAttachments[:i], session.pendingImageAttachments[i+1:]...)
				removedImagePath = img.FilePath
				found = true
				break
			}
		}
	}
	remaining := combinedAttachmentInfos(session.pendingAttachments, session.pendingImageAttachments)
	session.mu.Unlock()

	if !found {
		return nil
	}

	if removedImagePath != "" {
		if rmErr := os.Remove(removedImagePath); rmErr != nil && !os.IsNotExist(rmErr) {
			m.log().Debug("failed to remove staged image file", "path", removedImagePath, "error", rmErr)
		}
	}

	m.emitAttachmentsChanged(sessionID, remaining, nil)
	return nil
}

// GetSessionAttachments returns a defensive metadata-only copy of the staged
// attachments for UI chips. MEMORY-ONLY lookup (sessionByID, no lazy
// restore): the chips effect re-fires on EVERY session switch, and restoring
// there would build an orchestrator and open handles nobody closes for a
// session that by definition has nothing staged (staging is in-memory).
func (m *Manager) GetSessionAttachments(sessionID string) ([]AttachmentInfo, error) {
	session, ok := m.sessionByID(sessionID)
	if !ok || session == nil {
		return []AttachmentInfo{}, nil
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	return combinedAttachmentInfos(session.pendingAttachments, session.pendingImageAttachments), nil
}

// PendingImageAttachments returns a defensive copy of the staged images with
// the full data (FilePath/MediaType) needed to persist thumbnail + path in
// ChatMessage.Metadata; read before SendMessage clears the list.
func (m *Manager) PendingImageAttachments(sessionID string) ([]ImageAttachment, error) {
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("get image attachments: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("get image attachments: session %q not found", sessionID)
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	out := make([]ImageAttachment, len(session.pendingImageAttachments))
	copy(out, session.pendingImageAttachments)
	return out, nil
}
