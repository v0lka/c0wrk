package backend

import (
	"errors"
	"fmt"
	"os"

	"github.com/shirou/gopsutil/v4/process"
)

// readProcessRSS returns the resident set size (RSS), in bytes, of the
// current process. It backs the GetProcessMemory RPC (frontend_api_system.go).
//
// gopsutil is the implementation of the seam because x/sys cannot provide RSS
// on darwin: the Go structures expose no p_rssize field and Vmspace is
// stubbed out, while gopsutil reads the same resident-memory fact on every
// desktop platform (Linux /proc, darwin proc_pidinfo, Windows process status)
// in pure Go without cgo — one call for all three build targets.
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
