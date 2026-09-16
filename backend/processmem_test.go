package backend

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestGetProcessMemory_StubbedSeam(t *testing.T) {
	f := &FrontendAPI{readProcessRSSFn: func() (uint64, error) {
		return 1_234_567_896, nil
	}}

	got, err := f.GetProcessMemory()
	if err != nil {
		t.Fatalf("GetProcessMemory() error = %v", err)
	}
	if got != 1_234_567_896 {
		t.Fatalf("GetProcessMemory() = %d, want 1234567896", got)
	}
}

func TestGetProcessMemory_NilSeamUsesRealRead(t *testing.T) {
	// No readProcessRSSFn wired — the production path through readProcessRSS
	// must serve the RPC. The read is environment-coupled (a sandbox may deny
	// proc_pidinfo / procfs / WMI), so skip when the host read is unavailable
	// and only assert a positive RSS when it succeeds.
	if _, err := readProcessRSS(); err != nil {
		t.Skipf("readProcessRSS() unavailable in this environment: %v", err)
	}
	f := &FrontendAPI{}

	got, err := f.GetProcessMemory()
	if err != nil {
		t.Fatalf("GetProcessMemory() error = %v", err)
	}
	if got <= 0 {
		t.Fatalf("GetProcessMemory() = %d, want > 0", got)
	}
}

func TestGetProcessMemory_SeamError(t *testing.T) {
	errSeam := errors.New("seam failure")
	f := &FrontendAPI{readProcessRSSFn: func() (uint64, error) {
		return 0, errSeam
	}}

	got, err := f.GetProcessMemory()
	if err == nil {
		t.Fatal("GetProcessMemory() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "failed to read process memory") {
		t.Errorf("error %q missing structured prefix", err)
	}
	if !errors.Is(err, errSeam) {
		t.Errorf("error %q does not wrap the seam error", err)
	}
	if got != 0 {
		t.Errorf("GetProcessMemory() = %d, want 0 on error", got)
	}
}

func TestGetProcessMemory_Overflow(t *testing.T) {
	f := &FrontendAPI{readProcessRSSFn: func() (uint64, error) {
		return math.MaxUint64, nil
	}}

	got, err := f.GetProcessMemory()
	if err == nil {
		t.Fatal("GetProcessMemory() error = nil, want overflow error")
	}
	if !strings.Contains(err.Error(), "overflows int64") {
		t.Errorf("error %q missing overflow explanation", err)
	}
	if got != 0 {
		t.Errorf("GetProcessMemory() = %d, want 0 on overflow", got)
	}
}

func TestReadProcessRSS(t *testing.T) {
	rss, err := readProcessRSS()
	if err != nil {
		// Environment-coupled: a restricted sandbox may not expose the OS
		// process-memory API. The production path degrades gracefully, so the
		// seam contract (a non-nil error, no panic) is all this asserts here.
		t.Skipf("readProcessRSS() unavailable in this environment: %v", err)
	}
	if rss == 0 {
		t.Fatal("readProcessRSS() = 0, want > 0 for a live test binary")
	}
}
