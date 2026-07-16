package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/v0lka/c0wrk/core/markitdown"
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
		})
	}
	return out
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

	converter, err := m.converterOrInit()
	if err != nil {
		return nil, err
	}

	var failures []AttachmentFailure

	for _, path := range paths {
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
			Format:          strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")),
			SizeBytes:       info.Size(),
			MarkdownContent: markdown,
			AttachedAt:      time.Now(),
		}

		session.mu.Lock()
		session.pendingAttachments = append(session.pendingAttachments, att)
		infos := toAttachmentInfos(session.pendingAttachments)
		session.mu.Unlock()

		// Incremental UI feedback: emit after each successful conversion so
		// chips appear one-by-one rather than all-at-once after a long batch.
		m.emitAttachmentsChanged(sessionID, infos, nil)
	}

	// Surface any per-file failures in a final event. The attachments field is
	// the full current pending list so the UI store stays consistent.
	if len(failures) > 0 {
		session.mu.Lock()
		infos := toAttachmentInfos(session.pendingAttachments)
		session.mu.Unlock()
		m.emitAttachmentsChanged(sessionID, infos, failures)
	}

	session.mu.Lock()
	result := toAttachmentInfos(session.pendingAttachments)
	session.mu.Unlock()
	return result, nil
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

	session.mu.Lock()
	found := false
	for i, a := range session.pendingAttachments {
		if a.ID == attachmentID {
			session.pendingAttachments = append(session.pendingAttachments[:i], session.pendingAttachments[i+1:]...)
			found = true
			break
		}
	}
	remaining := toAttachmentInfos(session.pendingAttachments)
	session.mu.Unlock()

	if !found {
		return nil
	}

	m.emitAttachmentsChanged(sessionID, remaining, nil)
	return nil
}

// GetSessionAttachments returns a defensive copy of the session's staged
// (pending) attachments as metadata-only AttachmentInfo values, for UI chips.
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
	return toAttachmentInfos(session.pendingAttachments), nil
}
