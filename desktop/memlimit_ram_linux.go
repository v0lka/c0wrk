//go:build linux

package desktop

import (
	"os"
	"strconv"
	"strings"
)

// physicalMemoryBytes returns the machine's total physical RAM from
// /proc/meminfo's MemTotal line (reported in kB by the kernel). Used by the
// AUTO memory-soft-limit mode (see memlimit.go).
func physicalMemoryBytes() (int64, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		// Format: "MemTotal:       16384256 kB"
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
