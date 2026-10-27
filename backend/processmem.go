package backend

import (
	"errors"
	"fmt"
	"os"

	"github.com/shirou/gopsutil/v4/process"
)

// readProcessRSS returns the resident set size (RSS), in bytes, of the
// current process; it backs the GetProcessMemory RPC (see
// specs/domains/workspace.md). gopsutil because x/sys cannot provide RSS on
// darwin; one pure-Go call on every desktop target.
func readProcessRSS() (uint64, error) {
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return 0, fmt.Errorf("cannot open process handle: %w", err)
	}
	info, err := proc.MemoryInfo()
	if err != nil {
		return 0, fmt.Errorf("cannot read process memory info: %w", err)
	}
	if info == nil {
		return 0, errors.New("process memory info is unavailable")
	}
	return info.RSS, nil
}
