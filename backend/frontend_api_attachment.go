package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/v0lka/c0wrk/backend/session"
)

// maxAttachContentBytes bounds the decoded payload AttachFileContent accepts,
// mirroring the session package's raw attachment buffer cap; anything larger
// is rejected before any temp file or conversion work. The web transport's
// per-method RPC body limit is sized against this value (base64 inflates by
// ~4/3, see internal/webaccess).
const maxAttachContentBytes = 32 << 20 // 32 MiB

// imageMimeExtensions maps the image MIME types the attachment pipeline
// supports to file extensions, giving an extensionless web-supplied name a
// routing extension.
var imageMimeExtensions = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// windowsReservedBaseNames are the MS-DOS device names Windows resolves even
// with an extension ("nul.png" targets the NUL device — bytes never reach a
// regular file).
var windowsReservedBaseNames = func() map[string]struct{} {
	m := make(map[string]struct{}, 16)
	for _, n := range []string{"con", "prn", "aux", "nul"} {
		m[n] = struct{}{}
	}
	for i := 1; i <= 9; i++ {
		m["com"+strconv.Itoa(i)] = struct{}{}
		m["lpt"+strconv.Itoa(i)] = struct{}{}
	}
	return m
}()

// sanitizeAttachmentName reduces an untrusted web-supplied file name to a
// bare base name safe to create inside the caller's throwaway temp directory:
// directory components are stripped, an extensionless image gains a MIME-
// derived extension, and a Windows reserved device stem is underscore-escaped
// ("nul.png" → "nul_.png"). The reserved check covers the FIRST-dot segment:
// Windows device resolution keys on the stem before the first dot, so
// "nul.tar.gz" would sail through a last-extension check and still open the
// NUL device. Returns "" when nothing usable remains.
func sanitizeAttachmentName(fileName, mimeType string) string {
	base := fileName
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == ".." {
		return ""
	}
	if filepath.Ext(base) == "" {
		if ext, ok := imageMimeExtensions[strings.ToLower(strings.TrimSpace(mimeType))]; ok {
			base += ext
		}
	}
	stem := base
	if i := strings.IndexByte(base, '.'); i >= 0 {
		stem = base[:i]
	}
	if _, reserved := windowsReservedBaseNames[strings.ToLower(stem)]; reserved {
		base = stem + "_" + base[len(stem):]
	}
	return base
}

// AttachFiles converts the files at the given paths and stages them as
// pending attachments on the session; see session.Manager.AttachFiles and
// specs/contracts/desktop-frontend.md (Attachments) for the full semantics.
func (f *FrontendAPI) AttachFiles(sessionID string, paths []string) ([]session.AttachmentInfo, error) {
	if f.appCell() == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized")
	}
	infos, err := f.app.Manager().AttachFiles(context.Background(), sessionID, paths)
	if err != nil {
		return infos, fmt.Errorf("attach files: %w", err)
	}
	return infos, nil
}

// PasteFromClipboard stages the clipboard's highest-priority content
// (image → files → text), respecting the active model's vision capability;
// see session.Manager.PasteFromClipboard for the per-kind semantics.
func (f *FrontendAPI) PasteFromClipboard(sessionID string, supportsVision bool) (session.PasteResult, error) {
	if f.appCell() == nil || f.app.Manager() == nil {
		return session.PasteResult{}, errors.New("session manager not initialized")
	}
	res, err := f.app.Manager().PasteFromClipboard(context.Background(), sessionID, supportsVision)
	if err != nil {
		return res, fmt.Errorf("paste from clipboard: %w", err)
	}
	return res, nil
}

// RemoveAttachment removes a staged (pending) attachment from the session by ID.
func (f *FrontendAPI) RemoveAttachment(sessionID, attachmentID string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
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
	if f.appCell() == nil || f.app.Manager() == nil {
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
	if f.appCell() == nil || f.app.Manager() == nil {
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

// AttachFileContent stages an in-memory file (the web transport's upload path:
// a browser pick has no absolute path to hand to AttachFiles) as a pending
// attachment. The bytes are written under the sanitized name into a throwaway
// directory and the ENTIRE existing AttachFiles pipeline applies unchanged,
// so the returned AttachmentInfo is identical to a same-content file on disk.
// data travels base64-encoded over JSON; fileName is untrusted input reduced
// by sanitizeAttachmentName; the payload is capped at maxAttachContentBytes
// before any work happens.
func (f *FrontendAPI) AttachFileContent(sessionID, fileName, mimeType string, data []byte) ([]session.AttachmentInfo, error) {
	if f.appCell() == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized")
	}
	base := sanitizeAttachmentName(fileName, mimeType)
	if base == "" {
		return nil, fmt.Errorf("attach file content: file name %q is not usable", fileName)
	}
	if len(data) > maxAttachContentBytes {
		return nil, fmt.Errorf("attach file content: payload of %d bytes exceeds the %d byte attachment limit",
			len(data), maxAttachContentBytes)
	}

	// The temp directory keeps the staged file exactly as a picked file would
	// exist on disk, so AttachFiles' OriginalName/Format/SizeBytes semantics
	// hold without forking the pipeline; the whole tree is removed on return.
	dir, err := os.MkdirTemp("", "c0wrk-web-attach-")
	if err != nil {
		return nil, fmt.Errorf("attach file content: create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, base)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("attach file content: write temp file: %w", err)
	}

	infos, err := f.app.Manager().AttachFiles(f.ctx(), sessionID, []string{path})
	if err != nil {
		return infos, fmt.Errorf("attach file content: %w", err)
	}
	return infos, nil
}
