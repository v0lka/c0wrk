//go:build linux

package desktop

import (
	"strconv"
	"strings"

	"github.com/v0lka/sp4rk/safeio"
)

// physicalMemoryBytes reads total physical RAM from /proc/meminfo MemTotal —
// the AUTO soft-limit input (specs/decisions/049-vector-index-memory-reduction.md).
func physicalMemoryBytes() (int64, bool) {
	data, err := safeio.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil || kb <= 0 {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
