package builtins

import (
	"fmt"
	"time"

	"github.com/v0lka/sp4rk/tools"
)

// formatWriteConflict formats a coherence conflict as an error message for write operations.
func formatWriteConflict(c *tools.CoherenceConflict) string {
	age := time.Since(c.ModifiedAt).Truncate(time.Second)
	return fmt.Sprintf(
		"File conflict: %s was modified since your last read.\n"+
			"Modified by: session %q (%s ago).\n"+
			"Your snapshot: mtime=%s, size=%d. Current: mtime=%s, size=%d.\n"+
			"Action required: re-read the file with read_file before editing.",
		c.Path,
		c.ModifiedBy,
		age,
		c.LastReadSig.ModTime.Format(time.RFC3339),
		c.LastReadSig.Size,
		c.CurrentSig.ModTime.Format(time.RFC3339),
		c.CurrentSig.Size,
	)
}

// formatReadConflict formats a coherence conflict as a warning annotation for read operations.
func formatReadConflict(c *tools.CoherenceConflict) string {
	return fmt.Sprintf(
		"[!] This file was modified by session %q since your last read. Review carefully.\n",
		c.ModifiedBy,
	)
}
