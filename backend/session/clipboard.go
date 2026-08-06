package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
)

// PasteKind discriminates the kind of content that was found on the system
// clipboard by PasteFromClipboard, in priority order image → files → text.
type PasteKind string

const (
	// PasteKindImage: raw image data (a screenshot or "copy image") was found.
	// Staged as an image attachment via processImage when the active model
	// supports vision; otherwise Rejected is filled and nothing is staged.
	PasteKindImage PasteKind = "image"
	// PasteKindFiles: one or more file URLs were found (Finder/Explorer "Copy").
	// Staged via AttachFiles. Image-extension files are skipped when the active
	// model does not support vision; documents are always attached.
	PasteKindFiles PasteKind = "files"
	// PasteKindText: only plain text was found. The text is returned in Text for
	// the caller to insert into the chat input (no attachment is staged).
	PasteKindText PasteKind = "text"
	// PasteKindEmpty: the clipboard held no supported content.
	PasteKindEmpty PasteKind = "empty"
)

// PasteResult is the discriminated result of a clipboard paste, returned to the
// frontend so it can react per-kind (render an image/file chip, insert text, or
// surface a rejection). Kind determines which fields are populated:
//
//   - image: Files holds the staged image AttachmentInfo (with thumbnail) when
//     accepted; Rejected holds the reason when the image was not staged.
//   - files: Files holds the staged AttachmentInfos from AttachFiles.
//     SkippedImages counts image-extension files that were dropped because the
//     active model lacks vision (so the UI can surface a rejection banner even
//     though some documents may still have been attached).
//   - text:  Text holds the clipboard string.
//   - empty: no fields are populated.
type PasteResult struct {
	Kind          PasteKind        `json:"kind"`
	Text          string           `json:"text,omitempty"`
	Files         []AttachmentInfo `json:"files,omitempty"`
	Rejected      string           `json:"rejected,omitempty"`
	SkippedImages int              `json:"skipped_images,omitempty"`
}

// pasteImageVisionRejected is the sentinel Rejected value the backend returns
// when a raw clipboard image could not be staged because the active model lacks
// vision capability. It is NOT a human-readable message — the frontend maps it
// to a banner so copy lives in exactly one place. A real
// processing error is reported as the error's own text instead.
const pasteImageVisionRejected = "vision_unsupported"

// writeTempImage writes clipboard image bytes to a temp file with an extension
// matching the media type (so processImage decodes the right format), returning
// the path. The caller owns the file and must remove it.
func writeTempImage(data []byte, mediaType string) (string, error) {
	ext := imageFileExtension(mediaType) // image/png -> .png
	f, err := os.CreateTemp("", "c0wrk-clip-*"+ext)
	if err != nil {
		return "", fmt.Errorf("create temp image: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write temp image: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("close temp image: %w", err)
	}
	return f.Name(), nil
}

// Clipboard probe seam. These package-level vars point at the platform-specific
// (build-tagged) readers so PasteFromClipboard can be unit-tested deterministically
// by swapping in stubs without touching the real system clipboard. Each returns
// ok=false (and a nil error) when that content type is simply absent.
var (
	clipboardImageFn func(ctx context.Context) (data []byte, mediaType string, ok bool, err error) = clipboardImage
	clipboardFilesFn func(ctx context.Context) (paths []string, ok bool, err error)                = clipboardFiles
	clipboardTextFn  func(ctx context.Context) (text string, ok bool, err error)                   = clipboardText
)

// PasteFromClipboard probes the system clipboard in priority order
// (image → files → text) and stages the highest-priority content found as a
// pending attachment on the session. supportsVision reflects whether the active
// model can consume image input and gates both the raw-image and image-file
// paths:
//
//   - Image found + supportsVision: bytes are written to a temp file and staged
//     via attachImage (decode, optional resize, thumbnail, session/images/).
//   - Image found + !supportsVision: Kind is image but Rejected is filled and
//     nothing is staged.
//   - File URLs found: attached via AttachFiles (image-extension files are
//     skipped when !supportsVision; documents are always attached).
//   - Text only: returned as a string for the chat input.
//
// A non-nil error is returned only for system-level failures (session not
// found, AttachFiles converter init). Clipboard-probe errors are logged and
// treated as "not present" so a flaky platform helper never blocks a paste.
func (m *Manager) PasteFromClipboard(ctx context.Context, sessionID string, supportsVision bool) (PasteResult, error) {
	// 1. Image (highest priority).
	imgData, mediaType, imgOK, imgErr := clipboardImageFn(ctx)
	if imgErr != nil {
		m.log().Debug("clipboard image probe failed", "error", imgErr)
	}
	if imgOK {
		return m.pasteImage(sessionID, imgData, mediaType, supportsVision)
	}

	// 2. File URLs (Finder/Explorer "Copy").
	paths, filesOK, filesErr := clipboardFilesFn(ctx)
	if filesErr != nil {
		m.log().Debug("clipboard files probe failed", "error", filesErr)
	}
	if filesOK {
		return m.pasteFiles(ctx, sessionID, paths, supportsVision)
	}

	// 3. Plain text (lowest priority).
	text, textOK, textErr := clipboardTextFn(ctx)
	if textErr != nil {
		m.log().Debug("clipboard text probe failed", "error", textErr)
	}
	if textOK {
		return PasteResult{Kind: PasteKindText, Text: text}, nil
	}

	return PasteResult{Kind: PasteKindEmpty}, nil
}

// pasteImage stages clipboard image bytes as an image attachment when the active
// model supports vision; otherwise returns a rejected image result.
func (m *Manager) pasteImage(sessionID string, imgData []byte, mediaType string, supportsVision bool) (PasteResult, error) {
	if !supportsVision {
		return PasteResult{Kind: PasteKindImage, Rejected: pasteImageVisionRejected}, nil
	}

	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return PasteResult{Kind: PasteKindImage}, fmt.Errorf("paste image: %w", err)
	}
	if session == nil {
		return PasteResult{Kind: PasteKindImage}, fmt.Errorf("paste image: session %q not found", sessionID)
	}

	tmpPath, err := writeTempImage(imgData, mediaType)
	if err != nil {
		return PasteResult{Kind: PasteKindImage, Rejected: err.Error()}, nil
	}
	defer func() { _ = os.Remove(tmpPath) }()

	imagesDir := config.SessionImagesDir(m.agentDir, session.ProjectID, sessionID)
	// A friendly name for the staged chip instead of the temp file's ugly
	// "c0wrk-clip-*" basename. The picker/drop paths pass "" so they keep the
	// real on-disk filename.
	displayName := "pasted-image" + imageFileExtension(mediaType)
	if err := m.attachImage(session, sessionID, tmpPath, imagesDir, displayName); err != nil {
		return PasteResult{Kind: PasteKindImage, Rejected: err.Error()}, nil
	}

	infos, _ := m.GetSessionAttachments(sessionID)
	return PasteResult{Kind: PasteKindImage, Files: infos}, nil
}

// pasteFiles attaches clipboard file URLs via AttachFiles, honoring the active
// model's vision capability for image-extension files. Image-ext files skipped
// due to !supportsVision are counted in SkippedImages so the UI can surface a
// rejection banner (consistent with the picker/drop path).
func (m *Manager) pasteFiles(ctx context.Context, sessionID string, paths []string, supportsVision bool) (PasteResult, error) {
	var toAttach []string
	var skippedImages int
	for _, p := range paths {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		// Skip image-extension files when the model can't consume images;
		// documents are always attached.
		if isImageFormat(ext) && !supportsVision {
			skippedImages++
			continue
		}
		toAttach = append(toAttach, p)
	}

	infos, err := m.AttachFiles(ctx, sessionID, toAttach)
	return PasteResult{Kind: PasteKindFiles, Files: infos, SkippedImages: skippedImages}, err
}
