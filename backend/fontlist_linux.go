//go:build linux

package backend

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// fontListTimeout bounds the fc-list child process behind listFontFamilies:
// a wedged fontconfig cache rebuild must never hang an RPC. It is more
// generous than the gsettings probe (gnomeFontTimeout) because fc-list may
// rebuild its font cache on first run, which reads every installed font
// file; the RPC is still a one-shot per settings open, not a hot path.
const fontListTimeout = 5 * time.Second

// listFontFamilies returns the deduplicated, case-insensitively sorted list
// of installed font family names, parsed from `fc-list --format
// '%{family}\n'` by parseFontFamilies (fontlist.go). With monospace=true the
// fontconfig pattern `:mono` narrows the listing to the families fontconfig
// tags as monospace (spacing=mono) — the monospace picker's data source;
// dual-width (spacing=dual) and proportional families are excluded by
// fontconfig itself, and the caller's current choice stays selectable in the
// UI regardless (the picker prepends it as a free-text value). An error
// means "the enumeration mechanism is not usable" — fc-list missing from
// PATH (a minimal container, a system without fontconfig) or the probe
// failing — never "zero fonts installed": an empty-but-successful listing
// comes back as an empty slice with a nil error. The RPC
// (frontend_api_system.go) reports every error as Available=false, never as
// a failure.
//
// fc-list is exec'd rather than linked via fontconfig cgo: it is the
// established pattern for desktop-integration probes in this codebase (see
// systemfont_linux.go, config/shell_env.go), keeps the build cgo-neutral,
// and the listing is read once per settings surface open, so the spawn cost
// is irrelevant. The command and arguments are compile-time constants —
// nothing user- or model-controlled ever reaches the argv.
func listFontFamilies(monospace bool) ([]string, error) {
	if _, err := exec.LookPath("fc-list"); err != nil {
		return nil, fmt.Errorf("fc-list not found in PATH: %w", err)
	}
	// The `:mono` fontconfig pattern is the same filter `fc-match :mono`
	// uses; both argv variants below are compile-time constants.
	args := []string{"--format", "%{family}\n"}
	if monospace {
		args = []string{":mono", "--format", "%{family}\n"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), fontListTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "fc-list", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("fc-list %s: %w", strings.Join(args, " "), err)
	}
	return parseFontFamilies(string(out)), nil
}
