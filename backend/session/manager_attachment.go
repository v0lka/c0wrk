package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
)

// attachmentConvertTimeout is the per-file budget for a single markitdown
// conversion when the Manager lazily initializes its converter.
const attachmentConvertTimeout = 2 * time.Minute

// AttachmentInfo is a JSON-friendly, metadata-only view of a pending
// attachment. The converted markdown content is intentionally excluded so it is
// never leaked to the UI (only the orchestrator reads it via the blackboard).
type AttachmentInfo struct {
	ID           string `json:"id"`
	OriginalName string `json:"original_name"`
	Format       string `json:"format"`
	SizeBytes    int64  `json:"size_bytes"`
	IsImage      bool   `json:"is_image"`
	Thumbnail    string `json:"thumbnail,omitempty"` // JPEG data URI for image attachments
	// Path is the on-disk location of a staged image attachment (absent for
	// documents). The UI mirrors it into the optimistic user-message metadata
	// so image thumbnails render immediately instead of after a reload.
	Path string `json:"path,omitempty"`
	// MediaType is the staged image's MIME type (e.g. "image/png"; absent for
	// documents). Together with Path it lets the UI build a complete
	// StoredImageMetadata record optimistically.
	MediaType string `json:"media_type,omitempty"`
}

// AttachmentFailure describes a single file that could not be converted or
// staged, surfaced to the UI so the user knows which picks were rejected.
type AttachmentFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// AttachmentsChangedData is the payload of the "attachments:changed" session
// event. Attachments is the full current pending list — the UI replaces its
// store on every event. Failed carries per-file failures from the most recent
// attach operation (absent on remove/send-clear events).
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

// combinedAttachmentInfos builds a unified, metadata-only AttachmentInfo list
// from both document attachments (markitdown-converted) and image attachments
// (processed images). Image entries carry IsImage=true and a JPEG thumbnail
// data URI so the UI can render image chips. The caller must pass
// freshly-snapshotted slices (or hold session.mu).
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

// imageAttachmentsToContentBlocks converts staged ImageAttachments into LLM
// image content blocks for the context window. Each block carries the base64
// image data and its media type. Returns nil when there are no images so the
// HandleOptions.PendingImages field stays zero-valued (no image content).
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
// so image attachments survive a backend restart. Only the thumbnail (data URI)
// and the on-disk file path are stored — never the full base64 image data —
// keeping the DB row small. The file at Path is read and re-encoded on restore.
type StoredImageMetadata struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Thumbnail string `json:"thumbnail"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
}

// StoredImagesMetadata is the top-level metadata blob persisted in
// ChatMessage.Metadata for user messages carrying image attachments. It is the
// legacy image-only shape; richer messages use UserMessageMetadata.
type StoredImagesMetadata struct {
	Images []StoredImageMetadata `json:"images"`
}

// StoredAttachmentMeta is the per-document record persisted in
// ChatMessage.Metadata: just enough (name/format/size) to render a chip for an
// already-sent document attachment on reload. The converted markdown content is
// never persisted here — it lives only in the blackboard for the active task.
type StoredAttachmentMeta struct {
	OriginalName string `json:"original_name"`
	Format       string `json:"format"`
	SizeBytes    int64  `json:"size_bytes"`
}

// UserMessageMetadata is the unified metadata blob persisted in
// ChatMessage.Metadata for a user message. It combines the goal flag, image
// attachments (thumbnail + on-disk path), and document attachment summaries.
// Every field is omitempty so a message carrying a single signal serializes to
// just that field — e.g. an image-only message stays {"images":[...]} (matching
// StoredImagesMetadata) for backward compatibility.
type UserMessageMetadata struct {
	Goal        bool                   `json:"goal,omitempty"`
	Images      []StoredImageMetadata  `json:"images,omitempty"`
	Attachments []StoredAttachmentMeta `json:"attachments,omitempty"`
}

// imageAttachmentsToStoredImages maps staged image attachments to their
// persisted StoredImageMetadata form (thumbnail + on-disk path, never the full
// base64 data). Shared by the standalone image-only blob and the merged
// user-message metadata so the per-image mapping lives in one place.
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

// imageAttachmentsToMetadata builds the persistable metadata blob (JSON) for a
// set of staged image attachments. Returns nil when there are no images so the
// ChatMessage.Metadata field stays empty for image-free messages. Produces the
// legacy {"images":[...]} shape used when a message carries images only.
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

// PendingMessageMetadata returns the unified persistable metadata blob (JSON)
// for a user message about to be sent: the goal flag OR-ed with the session's
// staged image and document attachments. Returns nil when none of the three
// signals are present (no goal, no images, no docs) so ChatMessage.Metadata
// stays empty. Read before SendMessage snapshots and clears the pending lists.
//
// Backward compatibility: a message carrying images only (no goal, no docs)
// delegates to imageAttachmentsToMetadata so the blob is exactly {"images":[...]}.
func (m *Manager) PendingMessageMetadata(sessionID string, goal bool) (json.RawMessage, error) {
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("get message metadata: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("get message metadata: session %q not found", sessionID)
	}

	// Snapshot both pending lists under a single lock so the blob reflects one
	// consistent point in time (the lists are cleared by SendMessage right after).
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

// HasPendingAttachments reports whether the session has staged (not yet
// flushed) document or image attachments. Used by the live-send gate: a
// message sent while a task is running cannot carry attachments (they are
// flushed into the blackboard/context only at task start), so the frontend
// API rejects the send and asks the user to wait for pause/completion.
//
// This is a memory-only lookup — it never restores a session as a side effect
// (mirroring GetSessionRuntimeStatus) and returns false when the session is
// not in memory or has no staged attachments.
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

// emitAttachmentsChanged emits an "attachments:changed" event for the given
// session, carrying the current pending list (and optional failures). This is
// the single emit point so the payload shape stays consistent across
// attach/remove/send-clear operations.
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

// converterOrInit returns the lazily-initialized markitdown converter, creating
// it on first use with the manager's logger and attachmentConvertTimeout. The
// markitdown binary must be resolvable on PATH; a missing binary yields a
// wrapped error the caller can surface.
func (m *Manager) converterOrInit() (*markitdown.Converter, error) {
	m.converterMu.Lock()
	defer m.converterMu.Unlock()
	if m.converter != nil {
		return m.converter, nil
	}
	c, err := markitdown.NewConverter(m.log(), attachmentConvertTimeout)
	if err != nil {
		return nil, fmt.Errorf("init markitdown converter: %w", err)
	}
	m.converter = c
	return c, nil
}

// AttachFiles converts each supported file at the given paths to markdown via
// markitdown, stages the resulting attachments on the session (to be flushed
// into the blackboard on the next SendMessage), and returns metadata for the
// successfully attached files.
//
// An "attachments:changed" event is emitted after each successful conversion
// (incremental UI feedback) and once more at the end carrying any per-file
// failures in the Failed field. The method returns a non-nil error only for
// system-level failures (session not found, converter init). File-level
// failures (unsupported format, conversion error) are reported via the event
// payload — not as an error — so partial success does not discard the attached
// files or trigger a generic error toast.
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

		// Image path: process, save to session/images/, stage as an
		// ImageAttachment (kept separate from document attachments — images
		// are passed to the LLM as image content blocks, not markdown context).
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

		markdown, convErr := converter.Convert(ctx, path)
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

		session.mu.Lock()
		session.pendingAttachments = append(session.pendingAttachments, att)
		infos := combinedAttachmentInfos(session.pendingAttachments, session.pendingImageAttachments)
		session.mu.Unlock()

		// Incremental UI feedback: emit after each successful conversion so
		// chips appear one-by-one rather than all-at-once after a long batch.
		m.emitAttachmentsChanged(sessionID, infos, nil)
	}

	// Surface any per-file failures in a final event. The attachments field is
	// the full current pending list so the UI store stays consistent.
	if len(failures) > 0 {
		session.mu.Lock()
		infos := combinedAttachmentInfos(session.pendingAttachments, session.pendingImageAttachments)
		session.mu.Unlock()
		m.emitAttachmentsChanged(sessionID, infos, failures)
	}

	session.mu.Lock()
	result := combinedAttachmentInfos(session.pendingAttachments, session.pendingImageAttachments)
	session.mu.Unlock()
	return result, nil
}

// attachImage processes a single image file (decode, optional resize, JPEG
// re-encode, thumbnail), saves the processed copy to the session's images
// directory as {uuid}.jpg, and stages it as an ImageAttachment on the session.
// It emits an incremental "attachments:changed" event on success. The returned
// error describes why the image could not be attached and is surfaced by the
// caller as a per-file failure (not a method-level error) so partial success
// is preserved.
//
// displayName overrides OriginalName when non-empty (used by the clipboard
// paste path, whose temp file has an ugly "c0wrk-clip-*" name). When empty,
// OriginalName falls back to filepath.Base(path) — the real filename for the
// picker and drag-and-drop paths.
func (m *Manager) attachImage(session *Session, sessionID, path, imagesDir, displayName string) error {
	base64Data, mediaType, thumbURI, sizeBytes, err := processImage(path)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return fmt.Errorf("create images dir: %w", err)
	}

	imgID := uuid.NewString()
	imgPath := filepath.Join(imagesDir, imgID+imageFileExtension(mediaType))

	// Decode the processed base64 back to bytes for on-disk persistence. The
	// file is the source of truth for restart reconstruction; the in-memory
	// Base64Data is held only until the next SendMessage snapshots it.
	imgBytes, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return fmt.Errorf("decode processed image: %w", err)
	}
	if err := os.WriteFile(imgPath, imgBytes, 0o644); err != nil {
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

	session.mu.Lock()
	session.pendingImageAttachments = append(session.pendingImageAttachments, imgAtt)
	infos := combinedAttachmentInfos(session.pendingAttachments, session.pendingImageAttachments)
	session.mu.Unlock()

	m.emitAttachmentsChanged(sessionID, infos, nil)
	return nil
}

// RemovePendingAttachment removes a staged attachment from the session by ID.
// It does not touch attachments already flushed into the blackboard. Returns
// nil if the attachment was not found among the pending ones. Emits an
// "attachments:changed" event with the remaining pending attachments.
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
	// Not a document attachment — check the staged image attachments. Removing
	// a pending image also deletes its on-disk copy (only pending images have
	// one; already-sent images are reconstructed from DB metadata on restart).
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

// GetSessionAttachments returns a defensive copy of the session's staged
// (pending) attachments as metadata-only AttachmentInfo values, for UI chips.
// Includes both document attachments and image attachments.
func (m *Manager) GetSessionAttachments(sessionID string) ([]AttachmentInfo, error) {
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("get attachments: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("get attachments: session %q not found", sessionID)
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	return combinedAttachmentInfos(session.pendingAttachments, session.pendingImageAttachments), nil
}

// PendingImageAttachments returns a defensive copy of the session's staged
// (pending) image attachments, carrying the full ImageAttachment data
// (including FilePath and MediaType) needed to persist thumbnail + path in
// ChatMessage.Metadata. Used by the frontend API before SendMessage snapshots
// and clears the pending list.
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
