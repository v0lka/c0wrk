//go:build linux

package session

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// clipboardHelper returns the first available clipboard front-end ("wl-paste"
// for Wayland, "xclip" for X11); "" means every probe reports "not present".
func clipboardHelper() string {
	if _, err := exec.LookPath("wl-paste"); err == nil {
		return "wl-paste"
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return "xclip"
	}
	return ""
}

// runHelper runs a clipboard front-end and returns its stdout; a non-zero exit
// (e.g. the requested MIME type is not on the clipboard) becomes
// errNoClipboardType — "not present", not a hard failure.
func runHelper(ctx context.Context, tool string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, tool, args...).Output()
	if err != nil {
		return nil, errNoClipboardType
	}
	return out, nil
}

var errNoClipboardType = errors.New("clipboard type not available")

// clipboardImage reads a PNG image from the clipboard ("image/png" via either
// front-end); ok is true only when image bytes are present.
func clipboardImage(ctx context.Context) (data []byte, mediaType string, ok bool, err error) {
	tool := clipboardHelper()
	if tool == "" {
		return nil, "", false, nil
	}
	var out []byte
	var rerr error
	if tool == "wl-paste" {
		out, rerr = runHelper(ctx, tool, "-t", "image/png")
	} else {
		out, rerr = runHelper(ctx, tool, "-selection", "clipboard", "-t", "image/png", "-o")
	}
	if rerr != nil {
		return nil, "", false, nil //nolint:nilerr // type unavailable: "not present", not a hard failure
	}
	if len(out) == 0 {
		return nil, "", false, nil
	}
	return out, "image/png", true, nil
}

// clipboardFiles reads file URIs from the clipboard (text/uri-list, or GNOME's
// x-special/gnome-copied-files format); ok is true only with a file:// URI.
func clipboardFiles(ctx context.Context) (paths []string, ok bool, err error) {
	tool := clipboardHelper()
	if tool == "" {
		return nil, false, nil
	}

	var raw string
	if tool == "wl-paste" {
		out, rerr := runHelper(ctx, tool, "-t", "text/uri-list")
		if rerr == nil {
			raw = string(out)
		}
	} else {
		// xclip: prefer text/uri-list, fall back to the GNOME file-copy format.
		out, rerr := runHelper(ctx, tool, "-selection", "clipboard", "-t", "text/uri-list", "-o")
		if rerr == nil {
			raw = string(out)
		} else {
			out2, rerr2 := runHelper(ctx, tool, "-selection", "clipboard", "-t", "x-special/gnome-copied-files", "-o")
			if rerr2 == nil {
				raw = string(out2)
			}
		}
	}
	raw = strings.TrimSpace(raw)
	paths = parseFileURIList(raw)
	return paths, len(paths) > 0, nil
}

// clipboardText reads plain text from the clipboard. ok is true only when
// non-empty text is present.
func clipboardText(ctx context.Context) (text string, ok bool, err error) {
	tool := clipboardHelper()
	if tool == "" {
		return "", false, nil
	}
	var out []byte
	var rerr error
	if tool == "wl-paste" {
		out, rerr = runHelper(ctx, tool)
	} else {
		out, rerr = runHelper(ctx, tool, "-selection", "clipboard", "-o")
	}
	if rerr != nil {
		return "", false, nil //nolint:nilerr // type unavailable: "not present", not a hard failure
	}
	text = string(out)
	if strings.TrimSpace(text) == "" {
		return "", false, nil
	}
	return text, true, nil
}
