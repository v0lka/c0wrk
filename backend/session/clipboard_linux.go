//go:build linux

package session

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// clipboardHelper returns the first available Wayland/X11 clipboard front-end:
// "wl-paste" (Wayland) or "xclip" (X11). Returns "" when neither is installed,
// in which case clipboard probes report "not present".
func clipboardHelper() string {
	if _, err := exec.LookPath("wl-paste"); err == nil {
		return "wl-paste"
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return "xclip"
	}
	return ""
}

// runHelper runs a clipboard front-end with the given arguments and returns its
// stdout. A non-zero exit (e.g. the requested MIME type is not on the
// clipboard) is reported as errNoClipboardType so the caller treats it as
// "not present" rather than a hard failure.
var errNoClipboardType = errors.New("clipboard type not available")

func runHelper(ctx context.Context, tool string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, tool, args...).Output()
	if err != nil {
		return nil, errNoClipboardType
	}
	return out, nil
}

// clipboardImage reads a PNG image from the clipboard. On Wayland it requests
// "image/png"; on X11 it requests the same target via xclip. ok is true only
// when image bytes are present.
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

// clipboardFiles reads file URIs from the clipboard (text/uri-list, or the
// x-special/gnome-copied-files format used by GNOME/X11 file managers). ok is
// true only when at least one file:// URI is present.
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
