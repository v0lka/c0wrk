//go:build darwin

package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/v0lka/sp4rk/safeio"
)

// osa runs a JavaScript-for-Automation (JXA) program via osascript, passing
// the trailing args as argv to run(argv) and returning stdout with trailing
// newlines trimmed.
func osa(ctx context.Context, script string, args ...string) (string, error) {
	cmdArgs := append([]string{"-l", "JavaScript", "-e", script}, args...)
	out, err := exec.CommandContext(ctx, "osascript", cmdArgs...).Output()
	if err != nil {
		return "", fmt.Errorf("osascript: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// pasteboardExpr resolves the NSPasteboard the helpers below read from and
// write to — the general (system) pasteboard in production. Darwin tests
// override it to a private uniquely-named pasteboard so they exercise the
// real osascript/AppKit path without ever touching the user's clipboard.
var pasteboardExpr = "$.NSPasteboard.generalPasteboard"

// clipboardImage reads image data from the macOS pasteboard: public.png
// preferred, public.tiff (screenshots) converted via NSBitmapImageRep. The
// bytes are always PNG so processImage decodes a format it supports; ok is
// true only when image data is present.
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

	script := `function run(argv){
		ObjC.import('AppKit');
		var pb=` + pasteboardExpr + `;
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
	b, readErr := safeio.ReadFile(tmpPath)
	if readErr != nil {
		return nil, "", false, fmt.Errorf("read clipboard image: %w", readErr)
	}
	return b, "image/png", true, nil
}

// clipboardFiles reads file URLs (public.file-url) from the pasteboard — the
// content placed by Finder's "Copy" commands. Non-file URLs (a copied browser
// link) are skipped so they fall through to the text path instead of becoming
// non-existent file paths; ok is true only when at least one file URL is present.
func clipboardFiles(ctx context.Context) (paths []string, ok bool, err error) {
	script := `function run(){
		ObjC.import('AppKit');
		var pb=` + pasteboardExpr + `;
		var urls=pb.readObjectsForClassesOptions($.NSArray.arrayWithObject($.NSURL),$.NSDictionary.dictionary);
		if(urls.isNil()){ return ''; }
		var out=[];
		for(var i=0;i<urls.count;i++){
			var u=urls.objectAtIndex(i);
			// A web URL's .path is non-nil but points nowhere — skipping keeps the URL
			// text on the text path. isFileURL is an Obj-C BOOL exposed as a JS
			// property (not a method): read WITHOUT parentheses.
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
	script := `function run(){
		ObjC.import('AppKit');
		var pb=` + pasteboardExpr + `;
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
