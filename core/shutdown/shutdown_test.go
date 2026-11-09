package shutdown

import (
	"testing"
	"time"
)

// TestEffectiveShutdownDeadline pins the single configured-deadline mapping
// both the shutdown watchdog and the GPU-relaunch handover budget consume: a
// positive config value passes through, an unset (0) or nonsense value
// resolves to the built-in cumulative default. The GPU-relaunch handover waits
// exactly this long (plus margin) for the predecessor — sized against
// anything shorter, a legitimate slow teardown (the ~46 s embedded-LLM stop)
// would outlast the wait and lose the replacement window (review finding
// #155.1).
func TestEffectiveShutdownDeadline(t *testing.T) {
	cases := []struct {
		name              string
		configuredSeconds int
		want              time.Duration
	}{
		{"unset falls back to the default", 0, defaultShutdownHardDeadline},
		{"negative falls back to the default", -5, defaultShutdownHardDeadline},
		{"configured passes through", 120, 120 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveShutdownDeadline(tc.configuredSeconds); got != tc.want {
				t.Fatalf("EffectiveShutdownDeadline(%d) = %v, want %v", tc.configuredSeconds, got, tc.want)
			}
		})
	}
}

// TestDefaultHardDeadline_CoversCumulativeCeiling pins the floor: the default
// deadline must never size below the cumulative teardown ceiling (the 60 s
// historical floor applies), so a legitimate slow teardown is never
// force-exited mid-stop.
func TestDefaultHardDeadline_CoversCumulativeCeiling(t *testing.T) {
	if got := DefaultHardDeadline(); got < cumulativeTeardownCeiling {
		t.Fatalf("default hard deadline %v sizes below the cumulative teardown ceiling %v", got, cumulativeTeardownCeiling)
	}
}
