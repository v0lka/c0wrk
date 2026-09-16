//go:build darwin

package session

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These tests exercise the real osascript/AppKit clipboard path, which in
// production reads and writes the general (system) pasteboard. They must never
// touch the user's ACTUAL clipboard: staging fixtures on the general pasteboard
// silently overwrote (and could race) whatever the user had copied — a leftover
// fixture such as "https://example.com/article.html" would then surface when the
// user pasted. To prevent that, every test runs against a private,
// uniquely-named NSPasteboard owned by a keeper osascript process, and
// pasteboardExpr (see clipboard_darwin.go) is pointed at it for the test's
// duration. The keeper exits — releasing the pasteboard — when its stdin closes
// during cleanup.

// usePrivatePasteboard starts a keeper osascript that owns a private,
// uniquely-named pasteboard and repoints pasteboardExpr at it for this test.
// The keeper blocks reading its stdin; the registered cleanup closes that stdin
// so the keeper exits at test end. Because the pasteboard is private, the
// general system clipboard is never read or written.
func usePrivatePasteboard(t *testing.T) {
	t.Helper()

	name := fmt.Sprintf("c0wrk-clipboard-test-%d-%d", os.Getpid(), time.Now().UnixNano())

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("private pasteboard stdin pipe: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("private pasteboard stdout pipe: %v", err)
	}

	const keeper = `function run(argv){
		ObjC.import('AppKit');
		var pb=$.NSPasteboard.pasteboardWithName(argv[0]);
		pb.clearContents;
		var out=$.NSFileHandle.fileHandleWithStandardOutput;
		out.writeData($.NSString.stringWithString('ready\n').dataUsingEncoding($.NSUTF8StringEncoding));
		var fh=$.NSFileHandle.fileHandleWithStandardInput;
		fh.readDataToEndOfFile;
		return 'ok';
	}`
	cmd := exec.CommandContext(context.Background(), "osascript", "-l", "JavaScript", "-e", keeper, name)
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	if err := cmd.Start(); err != nil {
		t.Fatalf("start private pasteboard keeper: %v", err)
	}
	// The child has its own copies of the pipe ends; close ours so the only live
	// references are the keeper's stdin read end and our stdinW write end.
	_ = stdinR.Close()
	_ = stdoutW.Close()

	ready := make(chan error, 1)
	go func() {
		line, rerr := bufio.NewReader(stdoutR).ReadString('\n')
		if rerr == nil && strings.TrimSpace(line) != "ready" {
			rerr = fmt.Errorf("unexpected keeper output %q", strings.TrimSpace(line))
		}
		ready <- rerr
	}()
	select {
	case rerr := <-ready:
		if rerr != nil {
			t.Fatalf("private pasteboard keeper failed to start: %v", rerr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("private pasteboard keeper did not become ready")
	}

	prev := pasteboardExpr
	pasteboardExpr = "$.NSPasteboard.pasteboardWithName('" + name + "')"
	t.Cleanup(func() {
		_ = stdinW.Close() // EOF tells the keeper to exit, releasing the pasteboard
		_ = cmd.Wait()
		pasteboardExpr = prev
	})
}

// osaSet runs a JXA program via osascript, passing the trailing args as argv to
// the program's run(argv) function. Used only by the darwin real-clipboard tests
// to stage known pasteboard content on the private pasteboard.
func osaSet(t *testing.T, script string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-l", "JavaScript", "-e", script}, args...)
	if out, err := exec.CommandContext(context.Background(), "osascript", cmdArgs...).CombinedOutput(); err != nil {
		t.Fatalf("set clipboard: %v: %s", err, out)
	}
}

func osaSetText(t *testing.T, text string) {
	osaSet(t, `function run(argv){ObjC.import('AppKit'); var pb=`+pasteboardExpr+`; pb.clearContents; pb.setStringForType(argv[0],'public.utf8-plain-text'); return 'ok';}`, text)
}

func osaSetPNG(t *testing.T, path string) {
	osaSet(t, `function run(argv){ObjC.import('AppKit'); var data=$.NSData.dataWithContentsOfFile(argv[0]); var pb=`+pasteboardExpr+`; pb.clearContents; pb.setDataForType(data,'public.png'); return 'ok';}`, path)
}

func osaSetFileURL(t *testing.T, path string) {
	osaSet(t, `function run(argv){ObjC.import('AppKit'); var url=$.NSURL.fileURLWithPath(argv[0]); var pb=`+pasteboardExpr+`; pb.clearContents; pb.writeObjects($.NSArray.arrayWithObject(url)); return 'ok';}`, path)
}

// TestDarwinClipboard_TextProbe: text on the clipboard is read back verbatim,
// and neither the image nor file probe reports present.
func TestDarwinClipboard_TextProbe(t *testing.T) {
	usePrivatePasteboard(t)
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
	usePrivatePasteboard(t)
	want := pngBytes(t)
	path := runtimeTempDir(t) + "/probe.png"
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
	usePrivatePasteboard(t)
	target := strings.TrimRight(runtimeTempDir(t), "/") + "/copied.txt"
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
	usePrivatePasteboard(t)
	osaSet(t, `function run(){ObjC.import('AppKit'); var url=$.NSURL.URLWithString('https://example.com/article.html'); var pb=`+pasteboardExpr+`; pb.clearContents; pb.writeObjects($.NSArray.arrayWithObject(url)); return 'ok';}`)

	paths, ok, err := clipboardFiles(context.Background())
	if err != nil {
		t.Fatalf("clipboardFiles: %v", err)
	}
	if ok {
		t.Errorf("clipboardFiles reported present for a web URL: paths=%v", paths)
	}
}
