//go:build !windows

package embeddedllm

import (
	"context"
	"fmt"
	"runtime"

	"github.com/v0lka/sp4rk/safeio"
)

// platformTotalRAMBytes reads total physical RAM in bytes: darwin shells out
// to "sysctl -n hw.memsize" (no portable syscall), linux reads MemTotal from
// /proc/meminfo (kilobytes). Any other GOOS is unsupported: an unreadable RAM
// size must fail closed (ErrRAMUnknown), never let the memory gate derive a
// host budget from nothing. See specs/domains/embedded-llm.md (Hardware
// probe → resolution).
func platformTotalRAMBytes(ctx context.Context) (uint64, error) {
	if runtime.GOOS == "darwin" {
		return darwinTotalRAMBytes(ctx)
	}
	return linuxTotalRAMBytes()
}

// darwinTotalRAMBytes shells out to "sysctl -n hw.memsize".
func darwinTotalRAMBytes(ctx context.Context) (uint64, error) {
	out, err := runProbeCommand(ctx, "sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	total, ok := parseDecimalBytes(out)
	if !ok {
		return 0, fmt.Errorf("sysctl hw.memsize: unparsable output %q", out)
	}
	return total, nil
}

// linuxTotalRAMBytes reads MemTotal from /proc/meminfo.
func linuxTotalRAMBytes() (uint64, error) {
	const meminfoPath = "/proc/meminfo"

	content, err := safeio.ReadFile(meminfoPath)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", meminfoPath, err)
	}
	total, ok := parseMeminfoMemTotal(string(content))
	if !ok {
		return 0, fmt.Errorf("%s: MemTotal not found or unparsable", meminfoPath)
	}
	return total, nil
}
