// Backend compatibility guards: the machine-class failures the pinned runtime
// documents about ITSELF, turned into decisions c0wrk makes before it spends a
// multi-gigabyte download on a plan that is known to crash, hang, abort — or
// quietly produce garbage. A guard is never invented from a hunch: every one
// cites its upstream document of record (KNOWN_ISSUES.md and the model card in
// the pinned weights repository), this file must be re-read at every pin bump,
// and no degradation is ever silent — every decision a plan acts on, or cannot
// act on, is recorded. Sources, per-guard rows and the design rules are in
// specs/domains/embedded-llm.md (Backend compatibility guards).
package embeddedllm

import (
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// GPU generation vocabulary
// ---------------------------------------------------------------------------

// GPUFamily is the accelerator GENERATION a device description identifies —
// the axis the packing decision, the guards and the memory planner's split
// allowance all decide on. Classification reads the DESCRIPTION string the
// runtime prints for a device, never the backend: the backend says which
// archive was downloaded, the description says which silicon answered.
// GPUFamilyUnknown means "nothing recognized": no GPU-specific guard, the
// default packing, the largest memory allowance — not "no GPU". See
// specs/domains/embedded-llm.md (Backend compatibility guards).
type GPUFamily string

const (
	// GPUFamilyUnknown is an accelerator no rule recognizes, or no accelerator
	// at all.
	GPUFamilyUnknown GPUFamily = ""

	// GPUFamilyAppleSilicon is an Apple M-series part (Metal) — the family
	// every memory.go figure was measured on; it keeps the default packing
	// (the model card's only Apple row is measured on PQ2_0).
	GPUFamilyAppleSilicon GPUFamily = "apple-silicon"

	// GPUFamilyNVIDIAAda is Ada Lovelace: RTX 40-series, the RTX x000 Ada
	// workstation cards, L4/L40/L40S. The one family the model card measures
	// as PTQ1_0-faster for decode (every Ada row agrees).
	GPUFamilyNVIDIAAda GPUFamily = "nvidia-ada"

	// GPUFamilyNVIDIABlackwell is Blackwell (RTX 50-series, RTX PRO Blackwell,
	// B100/B200/GB200); PQ2_0 decodes faster (5090: 129.9 vs 120.5 TG128).
	GPUFamilyNVIDIABlackwell GPUFamily = "nvidia-blackwell"

	// GPUFamilyNVIDIAHopper is H100/H200/GH200; PQ2_0 decodes faster (H100 SXM
	// 113.9 vs 86.9) — decode there is instruction-throughput-bound, so the
	// cheaper-to-unpack 2-bit slot wins.
	GPUFamilyNVIDIAHopper GPUFamily = "nvidia-hopper"

	// GPUFamilyNVIDIAAmpere is A100/A30/A40/A10 and the RTX 30-series; the card
	// names A100 on the PQ2_0 side (73.9 vs 54.7), and nothing documents a
	// PTQ1_0 edge for consumer Ampere.
	GPUFamilyNVIDIAAmpere GPUFamily = "nvidia-ampere"

	// GPUFamilyAMDRDNA2 is RDNA 2 (gfx1030-gfx1036, RX 6000 / Radeon Pro
	// W6000). KNOWN_ISSUES: ROCm/HIP aborts on consumer RDNA2 (Bonsai-demo
	// #197); workaround "none yet on HIP; try the Vulkan build".
	GPUFamilyAMDRDNA2 GPUFamily = "amd-rdna2"

	// GPUFamilyAMDRDNA3 is discrete RDNA 3 (gfx1100-gfx1103, RX 7000). No
	// documented failure names it; it exists so a recognized card is not
	// Unknown, and so the RDNA 3.5 iGPU below is distinguishable from it.
	GPUFamilyAMDRDNA3 GPUFamily = "amd-rdna3"

	// GPUFamilyAMDGFX1151 is the RDNA 3.5 iGPU of Strix Halo / Ryzen AI Max
	// (gfx1151). KNOWN_ISSUES: PQ2_0 produces garbled output on Windows HIP
	// there (#223); CPU output is correct. Keyed on the gfx target rather than
	// folded into RDNA 3 because the failure is that one part — folding would
	// degrade working RX 7000 cards. The coverage cost (a Windows HIP
	// description often names no gfx token, so the guard cannot fire) is the
	// honest limit of description-based classification, recorded in the domain
	// spec rather than papered over with a broader rule.
	GPUFamilyAMDGFX1151 GPUFamily = "amd-gfx1151"

	// GPUFamilyIntelArc is an Intel Arc discrete or integrated GPU.
	// KNOWN_ISSUES: PTQ1_0 on Vulkan can hang Arc after about 1,900 tokens
	// (#192) — and c0wrk's Vulkan installs always resolve to PTQ1_0, so this
	// family plus Vulkan is exactly the documented condition. A description
	// must NAME the part: the Mesa Vulkan driver names the Xe2 iGPUs by
	// parenthesized generation code ("Intel(R) Graphics (ARL)", "(LNL)"),
	// which is the classifying token (captured verbatim from the project's
	// live Linux Vulkan host); the bare OpenCL spelling names no generation
	// and classifies as Unknown.
	GPUFamilyIntelArc GPUFamily = "intel-arc"
)

// gpuRules is the classification table, applied in order — the first match
// wins, and the order is load-bearing: unambiguous vendor tokens (Apple, Arc)
// first; gfx1151 before the broader gfx11xx RDNA 3 pattern; the explicit
// "ada" token before any digit-shaped Blackwell pattern ("RTX 5000 Ada"
// contains a 50xx number); newest-first within a vendor, so a driver string
// naming two generations lands on the newer one.
var gpuRules = []struct {
	family   GPUFamily
	patterns []*regexp.Regexp
}{
	{GPUFamilyAppleSilicon, gpuPatterns(`apple\s+m\d`, `apple silicon`)},
	// ARL/LNL are the Mesa Vulkan generation codes for the Xe2 iGPUs — no
	// "Arc" brand to match, so the code is the token. The bare OpenCL spelling
	// ("Intel(R) Graphics") carries no code and stays deliberately
	// unrecognized: no rule keys on a name that does not name the part.
	{GPUFamilyIntelArc, gpuPatterns(`\barc\b`, `\bxe2\b`, `\barl\b`, `\blnl\b`, `\bb[357]\d0\b`, `\ba[357]\d0\b`)},
	{GPUFamilyAMDGFX1151, gpuPatterns(`\bgfx1151\b`, `strix halo`, `ryzen ai max`, `radeon 80[56]0s`)},
	{GPUFamilyAMDRDNA3, gpuPatterns(`\bgfx11\d{2}\b`, `\brx\s*7\d{3}\b`, `rdna ?3`)},
	{GPUFamilyAMDRDNA2, gpuPatterns(`\bgfx103\d`, `\brx\s*6\d{3}\b`, `radeon\s+pro\s+w6\d{3}`, `rdna ?2`)},
	{GPUFamilyNVIDIAAda, gpuPatterns(`\bada\b`, `\brtx\s*40\d{2}\b`, `\bl4\b`, `\bl40s?\b`, `\brtx\s*a\d{4}\b`)},
	{GPUFamilyNVIDIABlackwell, gpuPatterns(`blackwell`, `\brtx\s*50\d{2}\b`, `\bb[12]00\b`, `\bgb200\b`)},
	{GPUFamilyNVIDIAHopper, gpuPatterns(`hopper`, `\bh[12]00\b`, `\bgh200\b`)},
	{GPUFamilyNVIDIAAmpere, gpuPatterns(`ampere`, `\ba100\b`, `\ba30\b`, `\ba40\b`, `\ba10\b`,
		`\brtx\s*30\d{2}\b`)},
}

// gpuPatterns compiles case-insensitive device-description matchers,
// word-boundary anchored wherever a bare number could match inside a longer
// token.
func gpuPatterns(exprs ...string) []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(exprs))
	for _, expr := range exprs {
		patterns = append(patterns, regexp.MustCompile(`(?i)`+expr))
	}
	return patterns
}

// ClassifyGPU maps one accelerator description — the runtime's own name for
// the device — onto a GPUFamily; empty or unrecognized yields
// GPUFamilyUnknown. Pure and total.
func ClassifyGPU(description string) GPUFamily {
	trimmed := strings.TrimSpace(description)
	if trimmed == "" {
		return GPUFamilyUnknown
	}
	for _, rule := range gpuRules {
		for _, pattern := range rule.patterns {
			if pattern.MatchString(trimmed) {
				return rule.family
			}
		}
	}
	return GPUFamilyUnknown
}

// ClassifyGPUs folds an inventory into the one family a plan is made for: the
// first recognized device wins, in the order the runtime printed it, each
// device classified from description first and then slot name ("HIP0",
// "MTL0"). A mixed-vendor machine is not modelled — the runtime would
// tensor-split across two families that can want different packings — so the
// first recognized device is the deterministic choice.
func ClassifyGPUs(devices []DeviceMemory) GPUFamily {
	for _, device := range devices {
		if family := ClassifyGPU(device.Description); family != GPUFamilyUnknown {
			return family
		}
		if family := ClassifyGPU(device.Name); family != GPUFamilyUnknown {
			return family
		}
	}
	return GPUFamilyUnknown
}

// ClassifyTopology is ClassifyGPUs over a probed topology. A failed probe
// classifies as Unknown — the caller's signal to keep the plan it already
// had, never a claim that the machine has no accelerator.
func ClassifyTopology(topology MemoryTopology) GPUFamily {
	return ClassifyGPUs(topology.Devices)
}

// prefersPTQ1_0Decode reports whether the model card measures PTQ1_0 as the
// faster DECODE on this family — Ada only, every Ada row of the throughput
// table agreeing (batch-1 decode there is memory-bandwidth-bound, and PTQ1_0
// moves 17% less weight data per step). Prompt processing is not consulted:
// being compute-bound, it favours PQ2_0 everywhere, and an agent workload is
// decode-dominated. See specs/domains/embedded-llm.md.
func (g GPUFamily) prefersPTQ1_0Decode() bool {
	return g == GPUFamilyNVIDIAAda
}

// ---------------------------------------------------------------------------
// Host capability vocabulary
// ---------------------------------------------------------------------------

// CPUFeature is a tri-state CPU capability verdict, on purpose: "we did not
// look" must be distinguishable from "the CPU does not have it" — the two
// have opposite safe defaults for the segfault-guarding rule that reads it.
type CPUFeature int

const (
	// CPUFeatureUnknown means no probe answered: the zero value, so a caller
	// supplying no host information lands here rather than asserting absence.
	CPUFeatureUnknown CPUFeature = iota
	// CPUFeatureAbsent means a probe answered "this CPU does not have it".
	CPUFeatureAbsent
	// CPUFeaturePresent means a probe answered "this CPU has it".
	CPUFeaturePresent
)

// HostCaps is the CPU-side capability set the packing decision reads. AVX-512
// is the only field, and it exists for one documented failure — PQ2_0
// segfaults while loading on AVX-512 CPUs, even with all layers offloaded —
// fixed in source by PR #245, which makes the rule pin-dependent (see
// minBuildWithAVX512PQ2_0Fix). EXTENSION POINT: c0wrk ships no CPU-feature
// probe yet, so production passes the zero value — safe with the current pin
// (it contains #245, the rule cannot fire) and conservative with an older one
// (Unknown counts as Present, i.e. PTQ1_0); a future /proc/cpuinfo or CPUID
// probe fills this in without touching the decision, already a pure function
// of the value.
type HostCaps struct {
	// AVX512 is the host's AVX-512 verdict. Unknown is treated as Present by
	// decidePacking whenever the pin predates the #245 fix.
	AVX512 CPUFeature
}

// hasAVX512 treats Unknown as capable: the guarded failure is a segfault at
// load, so an unprobed machine takes the packing that cannot crash.
func (h HostCaps) hasAVX512() bool {
	return h.AVX512 != CPUFeatureAbsent
}

// ---------------------------------------------------------------------------
// Fit vocabulary
// ---------------------------------------------------------------------------

// BudgetFit is the verdict of "does this packing fit the memory the machine
// actually has?" — tri-state like CPUFeature for the same reason: a bool's
// zero value would assert "does not fit" for every unprobed caller. The
// measurement belongs to topology.go's budgets and memory.go's projections;
// this type only carries the verdict, so the packing decision stays a pure
// function.
type BudgetFit int

const (
	// FitUnknown means no budget was measured; the conservative default that
	// keeps the packing the backend and GPU generation asked for.
	FitUnknown BudgetFit = iota
	// FitSufficient means the packing was projected to fit its budget.
	FitSufficient
	// FitInsufficient means the packing was projected NOT to fit — the model
	// card's own reason to reach for PTQ1_0 ("the pick wherever memory is
	// tightest").
	FitInsufficient
)

// ---------------------------------------------------------------------------
// Guard decisions
// ---------------------------------------------------------------------------

// GuardID names one compatibility guard. The ids are stable strings persisted
// in the install manifest and rendered in Settings, so renaming one is a
// user-visible change, not a refactor.
type GuardID string

const (
	// GuardCUDA133Crash covers KNOWN_ISSUES "CUDA 13.3 builds crash on some
	// systems" — a Linux segfault, a Windows banner-then-exit. Workaround: the
	// 12.8 build on Linux, 12.4 on Windows. Open (PrismML-Eng/llama.cpp#222).
	GuardCUDA133Crash GuardID = "cuda-13.3-crash"

	// GuardROCmRDNA2Abort covers KNOWN_ISSUES "ROCm/HIP aborts on consumer
	// RDNA2 GPUs (for example gfx1030)"; workaround "try the Vulkan build".
	// Open (PrismML-Eng/Bonsai-demo#197).
	GuardROCmRDNA2Abort GuardID = "rocm-rdna2-abort"

	// GuardWindowsHIPGFX1151Garbled covers KNOWN_ISSUES "PQ2_0 produces
	// garbled output on Windows HIP with gfx1151"; workaround "-ngl 0, or the
	// Vulkan build with PTQ1_0". Open (PrismML-Eng/llama.cpp#223). The one
	// guard whose failure mode is CORRECTNESS rather than availability: the
	// server starts, answers, and the answer is garbage — so it is guarded
	// before the install rather than diagnosed after it.
	GuardWindowsHIPGFX1151Garbled GuardID = "windows-hip-gfx1151-garbled"

	// GuardVulkanIntelArcHang covers KNOWN_ISSUES "PTQ1_0 on Vulkan can hang
	// Intel Arc GPUs after about 1,900 tokens"; workaround "run on CPU for
	// now". Open (PrismML-Eng/llama.cpp#192). Advisory, not a substitution:
	// every Intel Arc Vulkan install is in scope (Vulkan has no PQ2_0
	// kernels), and the only documented workaround gives up GPU acceleration
	// entirely — a trade the guard discloses rather than makes. Live
	// validation on an integrated Arrow Lake-P Xe2 iGPU (2026-10, recorded in
	// the domain spec) fixed the real axis: the trigger is PROMPT size — a
	// ~2,090-token prompt lost the device mid-prefill and aborted the server,
	// while 3,072 generated tokens ran clean.
	GuardVulkanIntelArcHang GuardID = "vulkan-intel-arc-hang"

	// GuardWindowsCUDANoStart covers KNOWN_ISSUES "Windows CUDA builds don't
	// start on some CPUs; the Windows CPU-only build does"; workaround "use
	// the CPU-only build". Open (PrismML-Eng/llama.cpp#241). Advisory because
	// it is not statically decidable — "some CPUs" names no CPUID, no driver
	// version and no card — so the guard's job is the fallback already written
	// down in the install record when the banner-then-exit happens.
	GuardWindowsCUDANoStart GuardID = "windows-cuda-no-start"
)

// GuardAction is what a guard asks the plan to do.
type GuardAction string

const (
	// GuardActionPreferBackend substitutes the runtime archive's backend —
	// actionable only while the plan is on paper; once a runtime is staged,
	// the substitution becomes guidance.
	GuardActionPreferBackend GuardAction = "prefer_backend"

	// GuardActionPreferPacking substitutes the weights packing — actionable
	// until the weights download, later than the runtime, so a guard
	// discovered by the post-staging device probe can still apply one.
	GuardActionPreferPacking GuardAction = "prefer_packing"

	// GuardActionAdvisory changes nothing and says something: the failure is
	// either not statically predictable (#241) or its workaround trades away
	// something the user may not want to trade (#192).
	GuardActionAdvisory GuardAction = "advisory"
)

// GuardReason is the typed cause behind a decision — the "why" a UI can
// render without parsing prose, and the axis a test can assert on.
type GuardReason string

const (
	// GuardReasonCrashOnLoad is a documented crash while the model loads
	// (#222: a Linux segfault, a Windows silent exit after the banner).
	GuardReasonCrashOnLoad GuardReason = "crash_on_load"

	// GuardReasonProcessAbort is a documented process abort on the accelerator
	// path (Bonsai-demo #197: ROCm/HIP aborts on consumer RDNA2).
	GuardReasonProcessAbort GuardReason = "process_abort"

	// GuardReasonGarbledOutput is documented WRONG OUTPUT (#223): the server
	// runs and the tokens are nonsense — classified apart from the crash
	// reasons because nothing else about the install looks wrong.
	GuardReasonGarbledOutput GuardReason = "garbled_output"

	// GuardReasonHang keeps the upstream "hang" name (#192); live validation
	// on Arrow Lake measured the failure as a device-lost abort during PROMPT
	// processing rather than a stall, so the guidance carries the measured
	// mode while the enum keeps the upstream name.
	GuardReasonHang GuardReason = "hang"

	// GuardReasonFailsToStart is a documented start failure with no
	// statically identifiable trigger (#241: Windows CUDA builds on some
	// CPUs).
	GuardReasonFailsToStart GuardReason = "fails_to_start"
)

// GuardSeverity ranks how bad the guarded failure is, for display and triage;
// derived from the reason, never chosen per-guard by hand, so the two cannot
// drift apart.
type GuardSeverity string

const (
	// GuardSeverityCritical is a failure that makes the install unusable or
	// WRONG: a crash, an abort, garbled output.
	GuardSeverityCritical GuardSeverity = "critical"

	// GuardSeverityWarning is a failure that degrades or interrupts use: a
	// mid-session abort once the prompt grows long (#192's measured mode), a
	// start failure that may or may not happen.
	GuardSeverityWarning GuardSeverity = "warning"
)

// severityFor derives the severity from the typed reason; an unmapped reason
// is critical, because an unranked failure must never render as the mildest
// thing on the list. See specs/domains/embedded-llm.md.
func severityFor(reason GuardReason) GuardSeverity {
	switch reason {
	case GuardReasonHang, GuardReasonFailsToStart:
		return GuardSeverityWarning
	case GuardReasonCrashOnLoad, GuardReasonProcessAbort, GuardReasonGarbledOutput:
		return GuardSeverityCritical
	default:
		return GuardSeverityCritical
	}
}

// GuardDecision is one guard's verdict for one machine, JSON-serialized
// because it is persisted in the install `Manifest` and mirrored into the
// Settings status DTO — the record of a degraded install must outlive the
// process that made it.
type GuardDecision struct {
	// Guard is the stable id of the guard that fired.
	Guard GuardID `json:"guard"`
	// Action is what the guard asks for: a backend substitution, a packing
	// substitution, or advice.
	Action GuardAction `json:"action"`
	// Reason is the typed cause, never empty — a decision without a reason is
	// indistinguishable from a bug, and "no guard silently changes behavior
	// without recording a reason" is an invariant of this subsystem.
	Reason GuardReason `json:"reason"`
	// Severity ranks the guarded failure for display.
	Severity GuardSeverity `json:"severity"`
	// Issue is the upstream citation, "<repo>#<number>" — data, not only a
	// comment, so a rendered guard is traceable to the report that justifies
	// it.
	Issue string `json:"issue"`
	// Applied reports whether the plan actually changed — set by the consumer
	// (the resolver, the installer), never by the table, which describes what
	// SHOULD happen while Applied records what DID. A false Applied is still
	// surfaced; that is the whole point of the field.
	Applied bool `json:"applied"`
	// Backend is the substituted backend, set only for GuardActionPreferBackend.
	Backend Backend `json:"backend,omitempty"`
	// Packing is the substituted packing, set only for GuardActionPreferPacking.
	Packing Packing `json:"packing,omitempty"`
	// Guidance is the user-facing sentence: what happened, what c0wrk did or
	// could not do, and what the upstream workaround is.
	Guidance string `json:"guidance"`
}

// CompatibilityGuards is the guard table: a pure lookup of the EFFECTIVE
// backend, GPU family (GPUFamilyUnknown when no probe answered) and platform
// onto every decision that applies, in table order (most severe first). No
// I/O, never fails; a machine that matches nothing returns a nil slice — the
// common and healthy case. Returned decisions carry Applied=false; the
// consumer sets it when it acts on one. See specs/domains/embedded-llm.md
// (Backend compatibility guards).
func CompatibilityGuards(backend Backend, gpu GPUFamily, platform string) []GuardDecision {
	var decisions []GuardDecision

	// #222 — CUDA 13.3 builds crash on some systems (Linux segfault, Windows
	// banner-then-exit). Upstream's workaround names the fallback per OS and
	// c0wrk pins both (linux cuda-12.8, windows cuda-12.4); a 13.3 driver
	// still runs an older-toolkit build, so downgrading costs nothing.
	if backend == BackendCUDA133 {
		switch platform {
		case PlatformLinuxAMD64:
			decisions = append(decisions, GuardDecision{
				Guard:    GuardCUDA133Crash,
				Action:   GuardActionPreferBackend,
				Reason:   GuardReasonCrashOnLoad,
				Issue:    "PrismML-Eng/llama.cpp#222",
				Backend:  BackendCUDA128,
				Guidance: "the CUDA 13.3 build is documented to segfault on some Linux systems, so this install uses the CUDA 12.8 build instead; your newer driver still runs it",
			})
		case PlatformWindowsAMD64:
			decisions = append(decisions, GuardDecision{
				Guard:    GuardCUDA133Crash,
				Action:   GuardActionPreferBackend,
				Reason:   GuardReasonCrashOnLoad,
				Issue:    "PrismML-Eng/llama.cpp#222",
				Backend:  BackendCUDA124,
				Guidance: "the CUDA 13.3 build is documented to print its banner and exit on some Windows systems, so this install uses the CUDA 12.4 build instead; your newer driver still runs it",
			})
		}
	}

	// Bonsai-demo #197 — ROCm/HIP aborts on consumer RDNA2 (gfx1030); no HIP
	// workaround exists, so the guard switches to the pinned Vulkan build,
	// which resolves to PTQ1_0.
	if backend == BackendROCm && gpu == GPUFamilyAMDRDNA2 {
		decisions = append(decisions, GuardDecision{
			Guard:    GuardROCmRDNA2Abort,
			Action:   GuardActionPreferBackend,
			Reason:   GuardReasonProcessAbort,
			Issue:    "PrismML-Eng/Bonsai-demo#197",
			Backend:  BackendVulkan,
			Guidance: "ROCm/HIP is documented to abort on consumer RDNA2 GPUs (gfx1030), so this install uses the Vulkan build with PTQ1_0 instead",
		})
	}

	// #223 — PQ2_0 garbles output on Windows HIP with gfx1151. Of the two
	// documented workarounds the guard applies Vulkan+PTQ1_0: at plan time a
	// different archive costs nothing extra, while -ngl 0 would surrender GPU
	// acceleration on a machine whose unified memory is its whole point. The
	// -ngl 0 alternative stays in the guidance.
	if backend == BackendROCm && gpu == GPUFamilyAMDGFX1151 && platform == PlatformWindowsAMD64 {
		decisions = append(decisions, GuardDecision{
			Guard:    GuardWindowsHIPGFX1151Garbled,
			Action:   GuardActionPreferBackend,
			Reason:   GuardReasonGarbledOutput,
			Issue:    "PrismML-Eng/llama.cpp#223",
			Backend:  BackendVulkan,
			Guidance: "PQ2_0 is documented to produce garbled output on Windows HIP with gfx1151, so this install uses the Vulkan build with PTQ1_0; the alternative workaround is to keep HIP and run with -ngl 0 (CPU), which is correct but slow",
		})
	}

	// #192 — PTQ1_0 on Vulkan loses the Intel Arc GPU on a large PROMPT
	// (live-validated 2026-10: a device-lost abort mid-prefill, not a
	// generation stall). Advisory: the guard does not substitute the CPU
	// build on the user's behalf.
	if backend == BackendVulkan && gpu == GPUFamilyIntelArc {
		decisions = append(decisions, GuardDecision{
			Guard:    GuardVulkanIntelArcHang,
			Action:   GuardActionAdvisory,
			Reason:   GuardReasonHang,
			Issue:    "PrismML-Eng/llama.cpp#192",
			Guidance: "PTQ1_0 on Vulkan loses the Intel Arc GPU once the prompt reaches about 1,900 tokens: prompt processing aborts the llama-server process with a lost device (reproduced on Arrow Lake at a ~2,090-token prompt; generated output is not the trigger), so the upstream workaround is to run on the CPU (reinstall with the CPU build, or start the server with -ngl 0)",
		})
	}

	// #241 — Windows CUDA builds don't start on some CPUs while the Windows
	// CPU-only build does. Advisory: the trigger is unidentified upstream, but
	// the fallback belongs in the record before the failure, not after.
	if platform == PlatformWindowsAMD64 && backend.IsCUDA() {
		decisions = append(decisions, GuardDecision{
			Guard:    GuardWindowsCUDANoStart,
			Action:   GuardActionAdvisory,
			Reason:   GuardReasonFailsToStart,
			Issue:    "PrismML-Eng/llama.cpp#241",
			Guidance: "some Windows CUDA builds are documented not to start on certain CPUs; if the server prints its banner and exits, reinstall with the CPU-only build",
		})
	}

	for i := range decisions {
		decisions[i].Severity = severityFor(decisions[i].Reason)
	}
	return decisions
}

// mergeGuardDecisions merges the two passes' records — the static plan-time
// half and the post-staging device probe — keeping each guard once, in base's
// order, with the Applied flag from the pass that actually acted on it. See
// specs/domains/embedded-llm.md.
func mergeGuardDecisions(base, extra []GuardDecision) []GuardDecision {
	merged := base
	for _, candidate := range extra {
		if guardDecisionPresent(merged, candidate.Guard) {
			continue
		}
		merged = append(merged, candidate)
	}
	return merged
}

// guardDecisionPresent reports whether a guard id already has a decision.
func guardDecisionPresent(decisions []GuardDecision, id GuardID) bool {
	for _, decision := range decisions {
		if decision.Guard == id {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Pin awareness
// ---------------------------------------------------------------------------

// minBuildWithAVX512PQ2_0Fix is the first fork build c0wrk can PROVE contains
// PR #245: b10709 demonstrably predates the merge, and the CVE-reviewed range
// 9a9394a..842b188 demonstrably contains it — the evidence is recorded in
// specs/domains/embedded-llm.md's pin-bump note, and builds in between are
// treated as predating the fix. The asymmetry is deliberate: a fixed build
// treated as unfixed only selects the smaller, slower packing; the reverse
// segfaults at load. Raise only with evidence, never to make a downgrade go
// away.
const minBuildWithAVX512PQ2_0Fix = 10735

// runtimeBuildRE matches the pinned fork release tag shape,
// "prism-b<build>-<commit>"; the build number is a commit counter, not a
// date (b10709 → b10735 is exactly the 26-commit range the pin bump's CVE
// review counted).
var runtimeBuildRE = regexp.MustCompile(`^prism-b(\d+)-[0-9a-f]{7,40}$`)

// parseRuntimeBuild extracts the build number; ok=false for any other shape,
// which callers must treat as "unproven" (0), never as "new".
func parseRuntimeBuild(tag string) (int, bool) {
	match := runtimeBuildRE.FindStringSubmatch(strings.TrimSpace(tag))
	if match == nil {
		return 0, false
	}
	build, err := strconv.Atoi(match[1])
	if err != nil || build <= 0 {
		return 0, false
	}
	return build, true
}

// pinnedRuntimeBuild is the pin's build number, or 0 when the tag shape is
// unrecognized — zero compares below every threshold, the same conservative
// direction as an unproven build.
func pinnedRuntimeBuild() int {
	build, _ := parseRuntimeBuild(RuntimeTag)
	return build
}
