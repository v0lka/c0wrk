package backend

import (
	"context"
	"errors"
	"fmt"

	"github.com/v0lka/c0wrk/backend/session"
)

// AttachFiles converts the files at the given paths to markdown and stages them
// as pending attachments on the session. Returns metadata for the successfully
// attached files. See session.Manager.AttachFiles for full semantics.
func (f *FrontendAPI) AttachFiles(sessionID string, paths []string) ([]session.AttachmentInfo, error) {
	if f.app == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized")
	}
	infos, err := f.app.Manager().AttachFiles(context.Background(), sessionID, paths)
	if err != nil {
		return infos, fmt.Errorf("attach files: %w", err)
	}
	return infos, nil
}

// RemoveAttachment removes a staged (pending) attachment from the session by ID.
func (f *FrontendAPI) RemoveAttachment(sessionID, attachmentID string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	if err := f.app.Manager().RemovePendingAttachment(sessionID, attachmentID); err != nil {
		return fmt.Errorf("remove attachment: %w", err)
	}
	return nil
}

// GetAttachments returns the staged (pending) attachments for a session as
// metadata-only values for UI display.
func (f *FrontendAPI) GetAttachments(sessionID string) ([]session.AttachmentInfo, error) {
	if f.app == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized")
	}
	return f.app.Manager().GetSessionAttachments(sessionID)
}

// GetBlackboardAttachmentMarkdown returns the converted markdown content of a
// committed blackboard attachment. The markdown is fetched on demand (e.g. when
// the user opens an attachment in the file viewer) rather than embedded in the
// blackboard state response, keeping that response free of potentially large
// payloads.
func (f *FrontendAPI) GetBlackboardAttachmentMarkdown(sessionID, attachmentID string) (string, error) {
	if f.app == nil || f.app.Manager() == nil {
		return "", errors.New("session manager not initialized")
	}
	bbState, err := f.app.Manager().GetBlackboardState(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get blackboard state: %w", err)
	}
	if bbState == nil || bbState.TaskState == nil {
		return "", fmt.Errorf("no blackboard state for session %q", sessionID)
	}
	for _, att := range bbState.TaskState.Attachments {
		if att.ID == attachmentID {
			return att.MarkdownContent, nil
		}
	}
	return "", fmt.Errorf("attachment %q not found in blackboard for session %q", attachmentID, sessionID)
}
