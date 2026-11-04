package vectorindex

import (
	"context"
	"log/slog"
	"os/exec"

	"github.com/v0lka/sp4rk/embedding"
)

// VerifyEmbedderOvDevice performs the one-shot external device verification
// after a successful OpenVINO embedder init and returns the device class the
// provider actually drives: "gpu", "cpu", or "" (unverified). The probe
// mechanics and verdict contract — the 30 s whole-probe budget, sustained
// inference during sampling, the bounded readings with a 25% threshold, and
// the rule that every failure shape is unverified, never "cpu" — are owned by
// sp4rk's embedding.VerifyOpenVINOExecution. See
// specs/decisions/089-openvino-embedding-flavor.md and
// specs/domains/workspace.md (OpenVINO flavor).
//
// This wrapper carries the one distinction the (verified, err) boolean cannot
// express: a machine without xpu-smi reports (false, nil) — the same shape as
// a concluded below-threshold window — so the LookPath guard pins that case
// to "". Diagnostics only: the probe never blocks or fails startup, and no
// separate warmup is needed (the probe's sustained-inference loop
// materializes the lazily-created OpenVINO context on its first call).
func VerifyEmbedderOvDevice(ctx context.Context, emb *embedding.Embedder, log *slog.Logger) string {
	if _, err := exec.LookPath("xpu-smi"); err != nil {
		// Missing tool ≠ "not on GPU": unverified — the WARN keeps the
		// missing verdict visible, never silent.
		log.Warn("OpenVINO device verification unavailable: xpu-smi not found",
			"hint", "treated as unverified, not as a CPU-device verdict")
		return ""
	}
	verified, err := embedding.VerifyOpenVINOExecution(ctx, emb)
	return ovDeviceVerdict(verified, err, log)
}

// ovDeviceVerdict maps the (verified, err) outcome of
// embedding.VerifyOpenVINOExecution onto the embedder-info tri-state and
// logs the verdict either way: any error is unverified "", never "cpu"; a
// concluded window below threshold is "cpu"; a threshold crossing is "gpu".
// The raw utilization is deliberately not logged (the core probe does not
// expose it). Factored out pure for tests.
func ovDeviceVerdict(verified bool, err error, log *slog.Logger) string {
	switch {
	case err != nil:
		// Probe failure ≠ "on a CPU device": unverified, and visible.
		log.Warn("OpenVINO device verification probe failed",
			"error", err,
			"hint", "treated as unverified, not as a CPU-device verdict")
		return ""
	case verified:
		log.Info("OpenVINO GPU device verified via xpu-smi")
		return "gpu"
	default:
		log.Warn("no GPU compute utilization during inference — OpenVINO picked a CPU device",
			"hint", "the plugin registered both its GPU and CPU devices; inference ran on the CPU one")
		return "cpu"
	}
}
