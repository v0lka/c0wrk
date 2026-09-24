//go:build !windows

package embeddedllm

import (
	"context"
	"fmt"
	"os"
	"runtime"
)

// platformTotalRAMBytes reads total physical RAM in bytes.
//
// darwin: "sysctl -n hw.memsize" — the same source the fork's demo script
// reads in bonsai_ctx_default. There is no portable syscall for it that also
// compiles on Linux, so the probe shells out under the bounded probe budget.
//
// linux: MemTotal from /proc/meminfo, which the kernel reports in kilobytes.
//
// Any other GOOS is unsupported: c0wrk ships darwin, linux and windows only,
// and an unreadable RAM size must fail closed (see ErrRAMUnknown) rather than
// let the 16 GiB gate pass on a guess.
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

	content, err := os.ReadFile(meminfoPath)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", meminfoPath, err)
	}
	total, ok := parseMeminfoMemTotal(string(content))
	if !ok {
		return 0, fmt.Errorf("%s: MemTotal not found or unparsable", meminfoPath)
	}
	return total, nil
}
