//go:build linux

package backend

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// fontListTimeout bounds the fc-list child process; more generous than the gsettings
// probe because a first run may rebuild the fontconfig cache. See specs/domains/fonts.md.
const fontListTimeout = 5 * time.Second

// listFontFamilies returns the deduplicated, case-insensitively sorted installed family
// names; monospace=true narrows to fontconfig-tagged monospace families via the `:mono`
// pattern. An error means the mechanism is unusable, never "zero fonts installed" — the
// RPC reports every error as Available=false. See specs/domains/fonts.md.
func listFontFamilies(monospace bool) ([]string, error) {
	if _, err := exec.LookPath("fc-list"); err != nil {
		return nil, fmt.Errorf("fc-list not found in PATH: %w", err)
	}
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
