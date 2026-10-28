package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
)

// Clipboard paste content kind, in PasteFromClipboard's priority order
// image → files → text.
type PasteKind string

const (
	// PasteKindImage: raw image data; staged via processImage when the
	// active model supports vision, else Rejected is filled, nothing staged.
	PasteKindImage PasteKind = "image"
	// PasteKindFiles: file URLs (Finder/Explorer "Copy"); staged via
	// AttachFiles — image extensions skipped without vision, docs always.
	PasteKindFiles PasteKind = "files"
	// PasteKindText: plain text only, returned in Text for the input (no
	// attachment staged).
	PasteKindText PasteKind = "text"
	// PasteKindEmpty: the clipboard held no supported content.
	PasteKindEmpty PasteKind = "empty"
)

// The discriminated clipboard-paste result; Kind determines which fields
// are populated: image — Files (staged image), or Rejected; files — Files
// plus SkippedImages (image files dropped for a non-vision model, UI
// banner); text — Text; empty — nothing.
type PasteResult struct {
	Kind          PasteKind        `json:"kind"`
	Text          string           `json:"text,omitempty"`
	Files         []AttachmentInfo `json:"files,omitempty"`
	Rejected      string           `json:"rejected,omitempty"`
	SkippedImages int              `json:"skipped_images,omitempty"`
}

// Sentinel Rejected value for a raw clipboard image the active model cannot
// consume (no vision). NOT human-readable — the frontend maps it to a
// banner; real errors carry their own text.
// See specs/contracts/desktop-frontend.md (PasteResult.Rejected sentinel).
const pasteImageVisionRejected = "vision_unsupported"

// Writes clipboard image bytes to a temp file with a media-type-matching
// extension (so processImage decodes the right format). The caller owns the
// file and must remove it.
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

// Clipboard probe seam: the platform-specific (build-tagged) readers,
// swapped for stubs in tests; ok=false (nil error) = simply absent.
var (
	clipboardImageFn func(ctx context.Context) (data []byte, mediaType string, ok bool, err error) = clipboardImage
	clipboardFilesFn func(ctx context.Context) (paths []string, ok bool, err error)                = clipboardFiles
	clipboardTextFn  func(ctx context.Context) (text string, ok bool, err error)                   = clipboardText
)

// Probes the clipboard in priority order (image → files → text), staging
// the highest-priority content; supportsVision gates the image paths.
// Non-nil error = system-level failure only; probe errors are treated as
// "not present" (see specs/domains/session-lifecycle.md).
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

// pasteImage stages clipboard image bytes as an image attachment
// (vision-gated); rejection fills Rejected.
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
	// Friendly chip name instead of the temp file's "c0wrk-clip-*" basename;
	// the picker/drop paths pass "" and keep the real filename.
	displayName := "pasted-image" + imageFileExtension(mediaType)
	if err := m.attachImage(session, sessionID, tmpPath, imagesDir, displayName); err != nil {
		return PasteResult{Kind: PasteKindImage, Rejected: err.Error()}, nil
	}

	infos, _ := m.GetSessionAttachments(sessionID)
	return PasteResult{Kind: PasteKindImage, Files: infos}, nil
}

// Attaches clipboard file URLs via AttachFiles; image-extension files
// skipped for a non-vision model are counted in SkippedImages (UI banner).
func (m *Manager) pasteFiles(ctx context.Context, sessionID string, paths []string, supportsVision bool) (PasteResult, error) {
	var toAttach []string
	var skippedImages int
	for _, p := range paths {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		if isImageFormat(ext) && !supportsVision {
			skippedImages++
			continue
		}
		toAttach = append(toAttach, p)
	}

	infos, err := m.AttachFiles(ctx, sessionID, toAttach)
	return PasteResult{Kind: PasteKindFiles, Files: infos, SkippedImages: skippedImages}, err
}
