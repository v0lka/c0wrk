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
// ChatMessage.Metadata for user messages carrying image attachments.
type StoredImagesMetadata struct {
	Images []StoredImageMetadata `json:"images"`
}

// imageAttachmentsToMetadata builds the persistable metadata blob (JSON) for a
// set of staged image attachments. Returns nil when there are no images so the
// ChatMessage.Metadata field stays empty for image-free messages.
func imageAttachmentsToMetadata(images []ImageAttachment) json.RawMessage {
	if len(images) == 0 {
		return nil
	}
	md := StoredImagesMetadata{Images: make([]StoredImageMetadata, 0, len(images))}
	for _, img := range images {
		md.Images = append(md.Images, StoredImageMetadata{
			ID:        img.ID,
			Name:      img.OriginalName,
			Thumbnail: img.ThumbnailB64,
			Path:      img.FilePath,
			MediaType: img.MediaType,
		})
	}
	data, err := json.Marshal(md)
	if err != nil {
		return nil
	}
	return data
}

// PendingImageMetadata returns the persistable metadata blob (JSON) for the
// session's staged image attachments, suitable for storing in
// ChatMessage.Metadata. Returns nil when there are no pending images. Used by
// the frontend API before SendMessage snapshots and clears the pending list.
func (m *Manager) PendingImageMetadata(sessionID string) (json.RawMessage, error) {
	images, err := m.PendingImageAttachments(sessionID)
	if err != nil {
		return nil, err
	}
	return imageAttachmentsToMetadata(images), nil
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
			if err := m.attachImage(session, sessionID, path, imagesDir); err != nil {
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
func (m *Manager) attachImage(session *Session, sessionID, path, imagesDir string) error {
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

	imgAtt := ImageAttachment{
		ID:           imgID,
		OriginalName: filepath.Base(path),
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
