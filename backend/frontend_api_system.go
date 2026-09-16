package backend

import (
	"fmt"
	"math"
)

// GetProcessMemory returns the resident set size (RSS) of the c0wrk desktop
// process, in bytes. The frontend status bar polls it every few seconds to
// render a live memory indicator; the value is informational only (no policy
// or security decisions hang off it).
//
// The read goes through the readProcessRSS seam (processmem.go); tests stub
// it via the readProcessRSSFn field on FrontendAPI.
func (f *FrontendAPI) GetProcessMemory() (int64, error) {
	readRSS := f.readProcessRSSFn
	if readRSS == nil {
		readRSS = readProcessRSS
	}
	rss, err := readRSS()
	if err != nil {
		return 0, fmt.Errorf("failed to read process memory: %w", err)
	}
	if rss > math.MaxInt64 {
		return 0, fmt.Errorf("process memory %d bytes overflows int64", rss)
	}
	return int64(rss), nil
}
