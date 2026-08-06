package session

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// pngBytes encodes a tiny 2×2 RGBA PNG for staging tests (processImage decodes
// png natively, so no extra format registration is needed).
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255})
	img.Set(1, 1, color.RGBA{G: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// probeImgFunc / probeFilesFunc / probeTextFunc match the clipboard probe seam
// signatures so tests can install deterministic stubs.
type (
	probeImgFunc   func(context.Context) (data []byte, mediaType string, ok bool, err error)
	probeFilesFunc func(context.Context) (paths []string, ok bool, err error)
	probeTextFunc  func(context.Context) (text string, ok bool, err error)
)

// swapProbes installs deterministic clipboard probes and restores the originals
// (the platform readers) on test completion.
func swapProbes(t *testing.T, img probeImgFunc, files probeFilesFunc, text probeTextFunc) {
	t.Helper()
	origImg, origFiles, origText := clipboardImageFn, clipboardFilesFn, clipboardTextFn
	clipboardImageFn, clipboardFilesFn, clipboardTextFn = img, files, text
	t.Cleanup(func() {
		clipboardImageFn, clipboardFilesFn, clipboardTextFn = origImg, origFiles, origText
	})
}

// newPasteSession creates a test manager + session suitable for PasteFromClipboard.
func newPasteSession(t *testing.T) (manager *Manager, sessionID string) {
	t.Helper()
	manager, _, _ = testManager(t)
	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return manager, info.ID
}

// TestPasteFromClipboard_ImageStagedWhenVisionSupported: raw image on the
// clipboard with supportsVision=true stages it via processImage (thumbnail +
// session/images/), returning Kind=image with the staged attachment.
func TestPasteFromClipboard_ImageStagedWhenVisionSupported(t *testing.T) {
	manager, sessionID := newPasteSession(t)
	pngData := pngBytes(t)
	swapProbes(t,
		func(context.Context) ([]byte, string, bool, error) { return pngData, "image/png", true, nil },
		func(context.Context) ([]string, bool, error) { return nil, false, nil },
		func(context.Context) (string, bool, error) { return "", false, nil },
	)

	res, err := manager.PasteFromClipboard(context.Background(), sessionID, true)
	if err != nil {
		t.Fatalf("PasteFromClipboard: %v", err)
	}
	if res.Kind != PasteKindImage {
		t.Fatalf("kind = %q, want image", res.Kind)
	}
	if res.Rejected != "" {
		t.Errorf("Rejected = %q, want empty", res.Rejected)
	}
	if len(res.Files) == 0 {
		t.Fatal("expected one staged image, got none")
	}
	if !res.Files[0].IsImage {
		t.Errorf("staged attachment is not an image: %+v", res.Files[0])
	}
	if res.Files[0].Thumbnail == "" {
		t.Error("expected a generated thumbnail on the staged image")
	}
	// A clipboard paste gets a friendly chip name, not the temp file's
	// "c0wrk-clip-*" basename.
	if res.Files[0].OriginalName != "pasted-image.png" {
		t.Errorf("OriginalName = %q, want %q", res.Files[0].OriginalName, "pasted-image.png")
	}
	// A processed copy must have been written to the session's images dir.
	infos, _ := manager.GetSessionAttachments(sessionID)
	if len(infos) != 1 || !infos[0].IsImage {
		t.Errorf("session has no staged image after paste: %+v", infos)
	}
}

// TestPasteFromClipboard_ImageRejectedWithoutVision: raw image with
// supportsVision=false returns Kind=image with Rejected filled and stages
// nothing.
func TestPasteFromClipboard_ImageRejectedWithoutVision(t *testing.T) {
	manager, sessionID := newPasteSession(t)
	swapProbes(t,
		func(context.Context) ([]byte, string, bool, error) { return pngBytes(t), "image/png", true, nil },
		func(context.Context) ([]string, bool, error) { return nil, false, nil },
		func(context.Context) (string, bool, error) { return "", false, nil },
	)

	res, err := manager.PasteFromClipboard(context.Background(), sessionID, false)
	if err != nil {
		t.Fatalf("PasteFromClipboard: %v", err)
	}
	if res.Kind != PasteKindImage {
		t.Fatalf("kind = %q, want image", res.Kind)
	}
	if res.Rejected != pasteImageVisionRejected {
		t.Errorf("Rejected = %q, want sentinel %q", res.Rejected, pasteImageVisionRejected)
	}
	if len(res.Files) != 0 {
		t.Errorf("expected nothing staged, got %d attachment(s)", len(res.Files))
	}
	infos, _ := manager.GetSessionAttachments(sessionID)
	if len(infos) != 0 {
		t.Errorf("expected no staged attachments, got %d", len(infos))
	}
}

// TestPasteFromClipboard_FilesRouteThroughAttachFiles: file URLs (image-ext)
// are attached via AttachFiles when vision is supported, and skipped when not.
func TestPasteFromClipboard_FilesRouteThroughAttachFiles(t *testing.T) {
	// A real image file on disk so AttachFiles can process it (no markitdown).
	imgPath := filepath.Join(t.TempDir(), "clip.png")
	if err := os.WriteFile(imgPath, pngBytes(t), 0o644); err != nil {
		t.Fatalf("write img: %v", err)
	}

	t.Run("vision_supported_stages_image_file", func(t *testing.T) {
		manager, sessionID := newPasteSession(t)
		swapProbes(t,
			func(context.Context) ([]byte, string, bool, error) { return nil, "", false, nil },
			func(context.Context) ([]string, bool, error) { return []string{imgPath}, true, nil },
			func(context.Context) (string, bool, error) { return "", false, nil },
		)
		res, err := manager.PasteFromClipboard(context.Background(), sessionID, true)
		if err != nil {
			t.Fatalf("PasteFromClipboard: %v", err)
		}
		if res.Kind != PasteKindFiles {
			t.Fatalf("kind = %q, want files", res.Kind)
		}
		if len(res.Files) != 1 || !res.Files[0].IsImage {
			t.Errorf("expected one staged image file, got %+v", res.Files)
		}
	})

	t.Run("vision_unsupported_skips_image_file", func(t *testing.T) {
		manager, sessionID := newPasteSession(t)
		swapProbes(t,
			func(context.Context) ([]byte, string, bool, error) { return nil, "", false, nil },
			func(context.Context) ([]string, bool, error) { return []string{imgPath}, true, nil },
			func(context.Context) (string, bool, error) { return "", false, nil },
		)
		res, err := manager.PasteFromClipboard(context.Background(), sessionID, false)
		if err != nil {
			t.Fatalf("PasteFromClipboard: %v", err)
		}
		if res.Kind != PasteKindFiles {
			t.Fatalf("kind = %q, want files (clipboard had files)", res.Kind)
		}
		// Image-ext file skipped because vision is unsupported; nothing staged.
		if len(res.Files) != 0 {
			t.Errorf("expected image-ext file skipped, got %d staged", len(res.Files))
		}
		// The skip must be surfaced so the UI can show a rejection banner.
		if res.SkippedImages != 1 {
			t.Errorf("SkippedImages = %d, want 1", res.SkippedImages)
		}
	})
}

// TestPasteFromClipboard_Text: text-only clipboard returns Kind=text with the
// string, staging nothing.
func TestPasteFromClipboard_Text(t *testing.T) {
	manager, sessionID := newPasteSession(t)
	swapProbes(t,
		func(context.Context) ([]byte, string, bool, error) { return nil, "", false, nil },
		func(context.Context) ([]string, bool, error) { return nil, false, nil },
		func(context.Context) (string, bool, error) { return "hello from clipboard", true, nil },
	)

	res, err := manager.PasteFromClipboard(context.Background(), sessionID, true)
	if err != nil {
		t.Fatalf("PasteFromClipboard: %v", err)
	}
	if res.Kind != PasteKindText {
		t.Fatalf("kind = %q, want text", res.Kind)
	}
	if res.Text != "hello from clipboard" {
		t.Errorf("Text = %q, want %q", res.Text, "hello from clipboard")
	}
}

// TestPasteFromClipboard_Precedence verifies the image → files → text priority.
func TestPasteFromClipboard_Precedence(t *testing.T) {
	// All three present → image wins (vision disabled so no processing occurs).
	t.Run("image_beats_files_and_text", func(t *testing.T) {
		manager, sessionID := newPasteSession(t)
		swapProbes(t,
			func(context.Context) ([]byte, string, bool, error) { return []byte("x"), "image/png", true, nil },
			func(context.Context) ([]string, bool, error) { return []string{"/a.png"}, true, nil },
			func(context.Context) (string, bool, error) { return "t", true, nil },
		)
		res, _ := manager.PasteFromClipboard(context.Background(), sessionID, false)
		if res.Kind != PasteKindImage {
			t.Fatalf("kind = %q, want image", res.Kind)
		}
	})

	// Files present (image-ext), no image, text present → files wins.
	t.Run("files_beats_text", func(t *testing.T) {
		manager, sessionID := newPasteSession(t)
		swapProbes(t,
			func(context.Context) ([]byte, string, bool, error) { return nil, "", false, nil },
			func(context.Context) ([]string, bool, error) { return []string{"/a.png"}, true, nil },
			func(context.Context) (string, bool, error) { return "t", true, nil },
		)
		res, _ := manager.PasteFromClipboard(context.Background(), sessionID, false)
		if res.Kind != PasteKindFiles {
			t.Fatalf("kind = %q, want files", res.Kind)
		}
	})

	// Nothing present → empty.
	t.Run("empty", func(t *testing.T) {
		manager, sessionID := newPasteSession(t)
		swapProbes(t,
			func(context.Context) ([]byte, string, bool, error) { return nil, "", false, nil },
			func(context.Context) ([]string, bool, error) { return nil, false, nil },
			func(context.Context) (string, bool, error) { return "", false, nil },
		)
		res, _ := manager.PasteFromClipboard(context.Background(), sessionID, true)
		if res.Kind != PasteKindEmpty {
			t.Fatalf("kind = %q, want empty", res.Kind)
		}
	})
}
