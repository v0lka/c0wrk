// Package shutdown holds the hard-teardown-deadline policy shared by the
// desktop shutdown watchdog and the GPU-relaunch handover budget: the
// cumulative component ceilings the default must cover and the mapping from
// the configured shutdown.hardDeadline onto the effective deadline, so the
// handover can never be sized against a deadline the predecessor is not
// actually bounded by. See specs/domains/crash-logging.md and
// specs/domains/webview-gpu-policy.md.
package shutdown

import "time"

// Component ceilings the default deadline must cover CUMULATIVELY — shutdown
// runs sequentially, so the worst case is the SUM of the bounded steps, not
// the largest one:
//
//   - embeddedLLMStopCeiling: stopEmbeddedLLM is bounded at ~46 s when a quit
//     races an in-flight cold load holding the supervisor gate — a 30 s wait
//     for the single-instance gate plus the stop's own 15/16 s force budget
//     (core/embeddedllm) — after which the child is terminated.
//   - sessionManagerStopBudget: a.app.Shutdown joins the manager's background
//     goroutines and drains both blackboards under ONE shared budget
//     (backend/session/manager.go).
//   - teardownSlack: every remaining step is individually bounded and fast
//     (batcher stop, pending-action drains, judgeWG.Wait, FrontendAPI
//     cleanup, db/sessionLogger close); the slack covers their sum on slow
//     storage.
const (
	embeddedLLMStopCeiling   = 30*time.Second + 16*time.Second
	sessionManagerStopBudget = 10 * time.Second
	teardownSlack            = 10 * time.Second
)

// defaultShutdownHardDeadline bounds the whole Shutdown teardown when
// shutdown.hardDeadline is unset (0): the cumulative worst case of the
// sequential teardown steps (above), with the historical 60 s as a floor —
// normal teardown completes in well under a second, so the deadline only
// fires on a genuinely wedged cancellation path. Sizing it below the
// cumulative worst case would force-exit a legitimate quit mid-stop, leaving
// the detached inference server running — exactly what the deadline prevents.
const cumulativeTeardownCeiling = embeddedLLMStopCeiling + sessionManagerStopBudget + teardownSlack

var defaultShutdownHardDeadline = max(cumulativeTeardownCeiling, 60*time.Second)

// EffectiveShutdownDeadline maps the configured shutdown.hardDeadline
// (seconds; 0 = unset) onto the effective teardown deadline — the single
// source shared by the shutdown watchdog and the GPU-relaunch handover
// budget, so the handover can never be sized against a deadline the
// predecessor is not actually bounded by. See specs/domains/webview-gpu-policy.md.
func EffectiveShutdownDeadline(configuredSeconds int) time.Duration {
	if configuredSeconds > 0 {
		return time.Duration(configuredSeconds) * time.Second
	}
	return defaultShutdownHardDeadline
}

// DefaultHardDeadline returns the built-in default teardown deadline used
// when shutdown.hardDeadline is unset; hosts without a configured value fall
// back to it.
func DefaultHardDeadline() time.Duration {
	return defaultShutdownHardDeadline
}
