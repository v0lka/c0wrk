//go:build windows

package workspace

import (
	"fmt"
	"os"
)

// openRegularFile is the Windows counterpart of the unix O_NONBLOCK open
// (review [14]): Windows named pipes live in the \\.\pipe\ namespace and
// cannot appear at a filesystem path like .git/config, so the blocking-open
// race is not reachable here. The handle-based regularity check still
// closes the TOCTOU the Stat→Open pair had: what is opened is what is
// checked, and only regular files are ever read.
func openRegularFile(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return f, nil
}
