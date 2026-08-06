//go:build darwin

package session

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// osaSet sets the macOS clipboard by running a JXA program via osascript. Used
// only by the darwin real-clipboard tests to stage known pasteboard content.
func osaSet(t *testing.T, script string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-l", "JavaScript", "-e", script}, args...)
	if out, err := exec.CommandContext(context.Background(), "osascript", cmdArgs...).CombinedOutput(); err != nil {
		t.Fatalf("set clipboard: %v: %s", err, out)
	}
}

func osaSetText(t *testing.T, text string) {
	osaSet(t, `function run(argv){ObjC.import('AppKit'); var pb=$.NSPasteboard.generalPasteboard; pb.clearContents; pb.setStringForType(argv[0],'public.utf8-plain-text'); return 'ok';}`, text)
}

func osaSetPNG(t *testing.T, path string) {
	osaSet(t, `function run(argv){ObjC.import('AppKit'); var data=$.NSData.dataWithContentsOfFile(argv[0]); var pb=$.NSPasteboard.generalPasteboard; pb.clearContents; pb.setDataForType(data,'public.png'); return 'ok';}`, path)
}

func osaSetFileURL(t *testing.T, path string) {
	osaSet(t, `function run(argv){ObjC.import('AppKit'); var url=$.NSURL.fileURLWithPath(argv[0]); var pb=$.NSPasteboard.generalPasteboard; pb.clearContents; pb.writeObjects($.NSArray.arrayWithObject(url)); return 'ok';}`, path)
}

// TestDarwinClipboard_TextProbe: text on the clipboard is read back verbatim,
// and neither the image nor file probe reports present.
func TestDarwinClipboard_TextProbe(t *testing.T) {
	osaSetText(t, "c0wrk paste unit test")
	text, ok, err := clipboardText(context.Background())
	if err != nil {
		t.Fatalf("clipboardText: %v", err)
	}
	if !ok || text != "c0wrk paste unit test" {
		t.Fatalf("clipboardText = (%q,%v), want (text,true)", text, ok)
	}
	if _, _, imgOK, _ := clipboardImage(context.Background()); imgOK {
		t.Error("clipboardImage reported present for a text-only clipboard")
	}
	if _, filesOK, _ := clipboardFiles(context.Background()); filesOK {
		t.Error("clipboardFiles reported present for a text-only clipboard")
	}
}

// TestDarwinClipboard_ImageProbe: a PNG placed on the clipboard is read back as
// PNG bytes (matching the staged content).
func TestDarwinClipboard_ImageProbe(t *testing.T) {
	want := pngBytes(t)
	path := t.TempDir() + "/probe.png"
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	osaSetPNG(t, path)

	data, mediaType, ok, err := clipboardImage(context.Background())
	if err != nil {
		t.Fatalf("clipboardImage: %v", err)
	}
	if !ok {
		t.Fatal("clipboardImage reported not present for an image clipboard")
	}
	if mediaType != "image/png" {
		t.Errorf("mediaType = %q, want image/png", mediaType)
	}
	// The clipboard round-trips the exact PNG bytes we staged.
	if !bytes.Equal(data, want) {
		t.Errorf("clipboard image bytes differ from staged PNG (got %d bytes, want %d)", len(data), len(want))
	}
}

// TestDarwinClipboard_FilesProbe: a file URL placed on the clipboard is read
// back as the matching filesystem path.
func TestDarwinClipboard_FilesProbe(t *testing.T) {
	target := strings.TrimRight(t.TempDir(), "/") + "/copied.txt"
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	osaSetFileURL(t, target)

	paths, ok, err := clipboardFiles(context.Background())
	if err != nil {
		t.Fatalf("clipboardFiles: %v", err)
	}
	if !ok || len(paths) != 1 {
		t.Fatalf("clipboardFiles = (%v,%v), want one path", paths, ok)
	}
	if paths[0] != target {
		t.Errorf("path = %q, want %q", paths[0], target)
	}
}

// TestDarwinClipboard_WebURLNotTreatedAsFile: a copied web URL (the content a
// browser places when you "Copy link") must NOT be returned by clipboardFiles.
// A web URL's .path is non-nil (e.g. "/article.html") but isFileURL is false, so
// it is skipped here and falls through to the text path (the URL the user
// wanted) instead of producing a confusing failed-attachment.
func TestDarwinClipboard_WebURLNotTreatedAsFile(t *testing.T) {
	osaSet(t, `function run(){ObjC.import('AppKit'); var url=$.NSURL.URLWithString('https://example.com/article.html'); var pb=$.NSPasteboard.generalPasteboard; pb.clearContents; pb.writeObjects($.NSArray.arrayWithObject(url)); return 'ok';}`)

	paths, ok, err := clipboardFiles(context.Background())
	if err != nil {
		t.Fatalf("clipboardFiles: %v", err)
	}
	if ok {
		t.Errorf("clipboardFiles reported present for a web URL: paths=%v", paths)
	}
}
