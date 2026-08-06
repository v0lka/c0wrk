//go:build darwin

package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// osa runs a JavaScript-for-Automation (JXA) program via osascript, passing the
// trailing args as argv to the program's run(argv) function. It returns the
// program's stdout with trailing newlines trimmed. osascript is cgo-free
// (spawned via os/exec) and available on every stock macOS install.
func osa(ctx context.Context, script string, args ...string) (string, error) {
	cmdArgs := append([]string{"-l", "JavaScript", "-e", script}, args...)
	out, err := exec.CommandContext(ctx, "osascript", cmdArgs...).Output()
	if err != nil {
		return "", fmt.Errorf("osascript: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// clipboardImage reads image data from the macOS pasteboard. It prefers
// public.png and falls back to public.tiff (the format used by screenshots),
// converting TIFF to PNG via NSBitmapImageRep. ok is true only when image data
// is present. The returned bytes are always PNG so processImage decodes a
// format it supports (png) regardless of the pasteboard's native encoding.
func clipboardImage(ctx context.Context) (data []byte, mediaType string, ok bool, err error) {
	// Create a temp file that the JXA program writes the PNG into; Go reads it
	// back. This avoids round-tripping binary data through osascript's stdout.
	f, ferr := os.CreateTemp("", "c0wrk-cbimg-*.png")
	if ferr != nil {
		return nil, "", false, fmt.Errorf("create temp: %w", ferr)
	}
	tmpPath := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	const script = `function run(argv){
		ObjC.import('AppKit');
		var pb=$.NSPasteboard.generalPasteboard;
		var png=pb.dataForType('public.png');
		if(!png.isNil()){ png.writeToFileAtomically(argv[0],true); return 'png'; }
		var tiff=pb.dataForType('public.tiff');
		if(!tiff.isNil()){
			var rep=$.NSBitmapImageRep.imageRepWithData(tiff);
			if(rep.isNil()){ return ''; }
			var enc=rep.representationUsingTypeProperties($.NSPNGFileType,$.NSDictionary.dictionary);
			if(enc.isNil()){ return ''; }
			enc.writeToFileAtomically(argv[0],true);
			return 'png';
		}
		return '';
	}`
	res, rerr := osa(ctx, script, tmpPath)
	if rerr != nil {
		return nil, "", false, rerr
	}
	if res == "" {
		return nil, "", false, nil
	}
	b, readErr := os.ReadFile(tmpPath)
	if readErr != nil {
		return nil, "", false, fmt.Errorf("read clipboard image: %w", readErr)
	}
	return b, "image/png", true, nil
}

// clipboardFiles reads file URLs (public.file-url) from the pasteboard — the
// content placed by Finder's "Copy" / "Copy <file>" commands. ok is true only
// when at least one file URL is present. Non-file URLs (e.g. a copied https
// link from a browser, exposed as a public.url NSURL) are skipped so they fall
// through to the text path instead of being treated as non-existent file paths.
func clipboardFiles(ctx context.Context) (paths []string, ok bool, err error) {
	const script = `function run(){
		ObjC.import('AppKit');
		var pb=$.NSPasteboard.generalPasteboard;
		var urls=pb.readObjectsForClassesOptions($.NSArray.arrayWithObject($.NSURL),$.NSDictionary.dictionary);
		if(urls.isNil()){ return ''; }
		var out=[];
		for(var i=0;i<urls.count;i++){
			var u=urls.objectAtIndex(i);
			// Skip non-file URLs (e.g. a copied web link). A web URL's .path is
			// non-nil but points at a non-existent file, which would yield a
			// confusing failed-attachment and lose the URL text the user wanted.
			// NOTE: isFileURL is a Obj-C BOOL exposed as a JS boolean property
			// (not a method), so it is read WITHOUT parentheses.
			if(!u.isFileURL){ continue; }
			var p=u.path;
			if(!p.isNil()){ out.push(p.js); }
		}
		return out.join('\n');
	}`
	res, rerr := osa(ctx, script)
	if rerr != nil {
		return nil, false, rerr
	}
	if res == "" {
		return nil, false, nil
	}
	for _, p := range strings.Split(res, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, len(paths) > 0, nil
}

// clipboardText reads plain text from the pasteboard. ok is true only when
// non-empty text is present.
func clipboardText(ctx context.Context) (text string, ok bool, err error) {
	const script = `function run(){
		ObjC.import('AppKit');
		var pb=$.NSPasteboard.generalPasteboard;
		var s=pb.stringForType('public.utf8-plain-text');
		return s.isNil()?'':s.js;
	}`
	res, rerr := osa(ctx, script)
	if rerr != nil {
		return "", false, rerr
	}
	if res == "" {
		return "", false, nil
	}
	return res, true, nil
}
