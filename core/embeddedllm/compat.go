// Backend compatibility guards: the machine-class failures the pinned runtime
// documents about ITSELF, turned into decisions c0wrk makes before it spends a
// multi-gigabyte download on a plan that is known to crash, hang, abort — or
// quietly produce garbage.
//
// Everything here is derived from two upstream documents of record for the
// pinned model and runtime, fetched and read on 2026-09-25 against the pin in
// registry.go (`RuntimeTag` = prism-b10735-842b188):
//
//   - KNOWN_ISSUES.md in the pinned weights repository
//     (huggingface.co/prism-ml/Ternary-Bonsai-2-27B-gguf, "Last checked:
//     2026-09-23"), which lists each failure with its workaround, its status
//     and the upstream issue number;
//   - the model card in the same repository ("Choosing a Packing" and the
//     "Cross-Platform Throughput" table), which measures the two packings per
//     GPU generation.
//
// Every constant below cites the issue it encodes. A guard is NEVER invented
// from a hunch: if upstream has not documented the failure, it is not a guard.
// The corollary is that this file must be re-read at every pin bump — a fixed
// upstream issue means a guard that should stop firing, and a stale guard is a
// needless degradation. `specs/domains/embedded-llm.md` records that duty.
//
// Two design rules follow from the failures themselves:
//
//  1. A guard is DATA, not control flow. `CompatibilityGuards` is a pure table
//     lookup returning decisions with a typed reason, a severity, an issue
//     citation and human guidance. Nothing in this file downloads, spawns or
//     mutates anything, so the whole backend x GPU x platform matrix is
//     table-testable and the same table can be read by the resolver (to apply
//     it) and by the install report (to disclose it).
//  2. A degradation is never silent. The resolver and the installer record
//     every decision they act on — and every decision they could NOT act on —
//     in `Resolution.Guards`, `InstallReport.Guards`, the on-disk `Manifest`
//     and the Settings status DTO. "It works, but not the way you think" is
//     exactly the state a user must be able to see.
package embeddedllm

import (
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// GPU generation vocabulary
// ---------------------------------------------------------------------------

// GPUFamily is the accelerator GENERATION a device description identifies.
//
// It exists because the decisions this package makes about an accelerator are
// generation-shaped, not backend-shaped, and two independent consumers read
// the same answer:
//
//   - the packing decision, because the pinned model's card MEASURES the two
//     ternary packings per GPU generation (see prefersPTQ1_0Decode);
//   - the compatibility guards, because KNOWN_ISSUES documents its failures
//     against named parts rather than backends (RDNA2 on ROCm, gfx1151 on
//     Windows HIP, Intel Arc on Vulkan);
//   - the memory planner (plan.go), because memory.go's profile was measured on
//     ONE family and the family is what prices the uncertainty of a
//     device/host split nobody measured (splitAllowanceMiB).
//
// The vocabulary covers the classes those decisions name, plus the generations
// the throughput table measures so that a recognized card is never reported as
// Unknown. Anything else — an unmodelled part, a description no rule matches —
// is GPUFamilyUnknown, which fires no GPU-specific guard, keeps the default
// packing and pays the largest memory allowance. Unknown is therefore not "no
// GPU": it is "nothing to decide, and nothing measured".
//
// Classification reads the DESCRIPTION string the runtime prints for a device
// (`--list-devices`, or the `-lv 4` parameter dump), never the backend: the
// backend says which archive was downloaded, the description says which
// silicon answered.
type GPUFamily string

const (
	// GPUFamilyUnknown is an accelerator no rule below recognizes, or no
	// accelerator at all.
	GPUFamilyUnknown GPUFamily = ""

	// GPUFamilyAppleSilicon is an Apple M-series part (Metal). The model card's
	// only Apple throughput row is measured on PQ2_0, so this family keeps the
	// default packing; it is named so the classifier's answer is explicit rather
	// than falling through to Unknown, and because it is the family every figure
	// in memory.go was measured on.
	GPUFamilyAppleSilicon GPUFamily = "apple-silicon"

	// GPUFamilyNVIDIAAda is NVIDIA's Ada Lovelace generation: the RTX 40-series,
	// the RTX x000 Ada workstation cards, and the L4/L40/L40S inference cards.
	// Model card, "Choosing a Packing": "PTQ1_0 is the faster decode on the
	// Ada-generation cards and the L4" — measured TG128 PTQ1_0 > PQ2_0 on every
	// Ada row (RTX 6000 Ada 90.4 vs 82.8, RTX 4090 91.1 vs 81.2, L40S 81.8 vs
	// 74.4, L4 32.1 vs 29.8), because batch-1 decode there is bound by memory
	// bandwidth and PTQ1_0 moves 17% less weight data per step.
	GPUFamilyNVIDIAAda GPUFamily = "nvidia-ada"

	// GPUFamilyNVIDIABlackwell is NVIDIA's Blackwell generation (RTX 50-series,
	// RTX PRO Blackwell, B100/B200/GB200). PQ2_0 decodes faster here: RTX 5090
	// 129.9 vs 120.5, RTX PRO 6000 Blackwell 124.8 vs 117.9.
	GPUFamilyNVIDIABlackwell GPUFamily = "nvidia-blackwell"

	// GPUFamilyNVIDIAHopper is H100/H200/GH200. PQ2_0 decodes faster: H100 SXM
	// 113.9 vs 86.9 TG128 — batch-1 decode is limited by instruction throughput
	// and launch overhead rather than bandwidth, so the cheaper-to-unpack 2-bit
	// slot representation wins.
	GPUFamilyNVIDIAHopper GPUFamily = "nvidia-hopper"

	// GPUFamilyNVIDIAAmpere is A100/A30/A40/A10 and the RTX 30-series. The model
	// card names A100 explicitly on the PQ2_0 side (73.9 vs 54.7 TG128);
	// consumer Ampere is not in the table and is grouped here because nothing
	// documents a PTQ1_0 advantage for it.
	GPUFamilyNVIDIAAmpere GPUFamily = "nvidia-ampere"

	// GPUFamilyAMDRDNA2 is AMD's RDNA 2 generation (gfx1030-gfx1036: the RX
	// 6000 series and the Radeon Pro W6000 series). KNOWN_ISSUES: "ROCm/HIP
	// aborts on consumer RDNA2 GPUs (for example gfx1030)", workaround "none yet
	// on HIP; try the Vulkan build", status open (Bonsai-demo #197).
	GPUFamilyAMDRDNA2 GPUFamily = "amd-rdna2"

	// GPUFamilyAMDRDNA3 is AMD's discrete RDNA 3 generation (gfx1100-gfx1103,
	// the RX 7000 series). No documented failure names it, so no guard keys on
	// it; it exists so a recognized card is not reported as Unknown, and so the
	// integrated RDNA 3.5 part below can be told apart from it.
	GPUFamilyAMDRDNA3 GPUFamily = "amd-rdna3"

	// GPUFamilyAMDGFX1151 is the RDNA 3.5 integrated GPU of the Strix Halo /
	// Ryzen AI Max APUs (gfx1151). KNOWN_ISSUES: "PQ2_0 produces garbled output
	// on Windows HIP with gfx1151 (Strix Halo integrated GPU). CPU (-ngl 0)
	// output is correct", status open (#223).
	//
	// It is keyed on the gfx target rather than folded into RDNA 3 because the
	// documented failure is that one part: folding the two would degrade a
	// working RX 7000 on Windows HIP to protect a broken iGPU. The cost is
	// coverage — a Windows HIP description often carries no gfx token at all
	// ("AMD Radeon(TM) Graphics"), and an unrecognized Strix Halo iGPU then
	// classifies as Unknown, so #223's guard cannot fire. That is the honest
	// limit of description-based classification, and it is recorded in the
	// domain spec rather than papered over with a broader rule.
	GPUFamilyAMDGFX1151 GPUFamily = "amd-gfx1151"

	// GPUFamilyIntelArc is an Intel Arc discrete or integrated GPU.
	// KNOWN_ISSUES: "PTQ1_0 on Vulkan can hang Intel Arc GPUs after about 1,900
	// tokens", workaround "run on CPU for now", status open (#192). Vulkan is
	// the only backend c0wrk ships for Intel GPUs and Vulkan always resolves to
	// PTQ1_0, so this family plus Vulkan is exactly the documented condition.
	GPUFamilyIntelArc GPUFamily = "intel-arc"
)

// gpuRules is the classification table, applied in order: the first rule with a
// matching pattern wins. Order is load-bearing, and every inversion below is
// deliberate:
//
//   - Apple and Intel Arc come first because their tokens ("Apple M", "Arc")
//     are unambiguous and cannot appear inside an NVIDIA or AMD product name.
//   - The gfx1151 rule precedes the RDNA 3 rule: both are AMD, and the one part
//     a correctness guard names must not be swallowed by the broader
//     `gfx11xx` pattern that also covers discrete RDNA 3.
//   - The Ada rule precedes Blackwell: "RTX 5000 Ada" and "RTX 4000 Ada" are
//     Ada workstation cards whose names contain a 50xx/40xx number, so the
//     explicit "ada" token must be matched before any digit-shaped Blackwell
//     pattern.
//   - Generations are ordered newest-first within a vendor, so a driver string
//     naming two generations lands on the newer one.
var gpuRules = []struct {
	family   GPUFamily
	patterns []*regexp.Regexp
}{
	{GPUFamilyAppleSilicon, gpuPatterns(`apple\s+m\d`, `apple silicon`)},
	{GPUFamilyIntelArc, gpuPatterns(`\barc\b`, `\bxe2\b`, `\bb[357]\d0\b`, `\ba[357]\d0\b`)},
	{GPUFamilyAMDGFX1151, gpuPatterns(`\bgfx1151\b`, `strix halo`, `ryzen ai max`, `radeon 80[56]0s`)},
	{GPUFamilyAMDRDNA3, gpuPatterns(`\bgfx11\d{2}\b`, `\brx\s*7\d{3}\b`, `rdna ?3`)},
	{GPUFamilyAMDRDNA2, gpuPatterns(`\bgfx103\d`, `\brx\s*6\d{3}\b`, `radeon\s+pro\s+w6\d{3}`, `rdna ?2`)},
	{GPUFamilyNVIDIAAda, gpuPatterns(`\bada\b`, `\brtx\s*40\d{2}\b`, `\bl4\b`, `\bl40s?\b`, `\brtx\s*a\d{4}\b`)},
	{GPUFamilyNVIDIABlackwell, gpuPatterns(`blackwell`, `\brtx\s*50\d{2}\b`, `\bb[12]00\b`, `\bgb200\b`)},
	{GPUFamilyNVIDIAHopper, gpuPatterns(`hopper`, `\bh[12]00\b`, `\bgh200\b`)},
	{GPUFamilyNVIDIAAmpere, gpuPatterns(`ampere`, `\ba100\b`, `\ba30\b`, `\ba40\b`, `\ba10\b`,
		`\brtx\s*30\d{2}\b`)},
}

// gpuPatterns compiles case-insensitive device-description matchers. Patterns
// are word-boundary anchored wherever a bare number could otherwise match
// inside a longer token.
func gpuPatterns(exprs ...string) []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(exprs))
	for _, expr := range exprs {
		patterns = append(patterns, regexp.MustCompile(`(?i)`+expr))
	}
	return patterns
}

// ClassifyGPU maps one accelerator description — the `Description` half of a
// `DeviceMemory` entry, i.e. the runtime's own name for the device — onto a
// GPUFamily. An empty or unrecognized description yields GPUFamilyUnknown.
//
// It is pure and total: no I/O, no failure, and no allocation beyond the
// result. The price of totality is the coverage limit documented on
// GPUFamilyAMDGFX1151.
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

// ClassifyGPUs folds a device inventory into the one family a plan is made for:
// the first device with a recognized family wins, in the order the runtime
// printed it (which is its own preference order). An inventory with nothing
// recognizable — including an empty one — is GPUFamilyUnknown.
//
// Each device is classified from both identity strings the runtime reports for
// it, description first: a backend that prints a terse description still carries
// a slot name ("HIP0", "MTL0"), and a description that names no part is
// sometimes accompanied by one that does.
//
// A mixed-vendor machine (an Intel iGPU beside an NVIDIA card, say) is not
// modelled: the runtime would tensor-split across both and the two families can
// want different packings. Taking the first recognized device is the
// deterministic choice and matches how the probe orders its output.
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

// ClassifyTopology is ClassifyGPUs over a probed topology: the family of the
// accelerator a plan will actually use. A failed probe carries no devices and
// classifies as Unknown, which is the caller's signal to keep the plan it
// already had — never a claim that the machine has no accelerator.
func ClassifyTopology(topology MemoryTopology) GPUFamily {
	return ClassifyGPUs(topology.Devices)
}

// prefersPTQ1_0Decode reports whether the pinned model's card measures PTQ1_0
// as the faster DECODE on this family. Only Ada qualifies — "PTQ1_0 is the
// faster decode on the Ada-generation cards and the L4" — and every Ada row of
// the throughput table agrees: RTX 6000 Ada 90.4 vs 82.8 TG128, RTX 4090 91.1
// vs 81.2, L40S 81.8 vs 74.4, L4 32.1 vs 29.8. Batch-1 decode there is bound
// by memory bandwidth, and PTQ1_0 moves 17% less weight data per step.
//
// The families the card puts on the PQ2_0 side return false: H100 SXM 113.9 vs
// 86.9, A100 SXM 73.9 vs 54.7, RTX 5090 129.9 vs 120.5, RTX PRO 6000
// Blackwell 124.8 vs 117.9 — batch-1 decode there is limited by instruction
// throughput and launch overhead instead. Prompt processing is not consulted
// because "Prompt processing, being compute-bound, favors PQ2_0 everywhere",
// and an agent workload is decode-dominated.
func (g GPUFamily) prefersPTQ1_0Decode() bool {
	return g == GPUFamilyNVIDIAAda
}

// ---------------------------------------------------------------------------
// Host capability vocabulary
// ---------------------------------------------------------------------------

// CPUFeature is a tri-state CPU capability verdict. It is tri-state on purpose:
// "we did not look" must be distinguishable from "the CPU does not have it",
// because the two have opposite safe defaults for the one rule that reads it
// (an unprobed AVX-512 host is treated as AVX-512-capable, since the failure
// it guards is a segfault).
type CPUFeature int

const (
	// CPUFeatureUnknown means no probe answered. It is the zero value, so a
	// caller that supplies no host information at all lands here rather than
	// accidentally asserting absence.
	CPUFeatureUnknown CPUFeature = iota
	// CPUFeatureAbsent means a probe answered "this CPU does not have it".
	CPUFeatureAbsent
	// CPUFeaturePresent means a probe answered "this CPU has it".
	CPUFeaturePresent
)

// HostCaps is the CPU-side capability set the packing decision reads.
//
// AVX-512 is the only field today, and it exists for one documented failure:
// KNOWN_ISSUES "CPU crash on load (AVX-512 CPUs)" — "PQ2_0 segfaults while
// loading on CPUs with AVX-512, including AMD Zen 4 and Zen 5 (Ryzen AI 300
// series, Strix Halo) and some server and virtualised CPUs. It crashes even
// with all layers offloaded to a GPU." Fixed in source by PR #245 (merged
// 2026-09-23; reports #180, #204, #219, Bonsai-demo #182), which makes the
// rule pin-dependent — see minBuildWithAVX512PQ2_0Fix.
//
// EXTENSION POINT: c0wrk ships no CPU-feature probe yet, so production passes
// the zero value and every field reads CPUFeatureUnknown. That is safe with
// the current pin (which contains #245, so the rule cannot fire) and
// conservative with an older one (Unknown is treated as Present, i.e. PTQ1_0).
// A future `/proc/cpuinfo` flags or CPUID probe fills this in without touching
// the decision, which is already a pure function of the value.
type HostCaps struct {
	// AVX512 is the host's AVX-512 verdict. Unknown is treated as Present by
	// decidePacking whenever the pin predates the #245 fix.
	AVX512 CPUFeature
}

// hasAVX512 reports whether the host must be treated as AVX-512-capable. The
// unknown case resolves to true: the guarded failure is a segfault at load, so
// an unprobed machine takes the packing that cannot crash.
func (h HostCaps) hasAVX512() bool {
	return h.AVX512 != CPUFeatureAbsent
}

// ---------------------------------------------------------------------------
// Fit vocabulary
// ---------------------------------------------------------------------------

// BudgetFit is the verdict of "does this packing fit the memory the machine
// actually has?". Like CPUFeature it is tri-state, and for the same reason: a
// bool's zero value would assert "does not fit" for every caller that has not
// measured anything, silently degrading the packing on machines that were never
// probed.
//
// The measurement itself belongs to the device-memory topology (topology.go's
// budgets) and the model's memory profile (memory.go's projections). This type
// only carries the verdict, so the packing decision stays a pure function and
// the gate that produces the verdict can evolve independently.
type BudgetFit int

const (
	// FitUnknown means no budget was measured, so capacity cannot justify a
	// downgrade. It is the zero value and the conservative default: it keeps
	// the packing the backend and GPU generation asked for.
	FitUnknown BudgetFit = iota
	// FitSufficient means the packing was projected to fit its budget.
	FitSufficient
	// FitInsufficient means the packing was projected NOT to fit, which is the
	// model card's own reason to reach for PTQ1_0: "the pick wherever memory is
	// tightest" (5.95 GB against PQ2_0's 7.21 GB).
	FitInsufficient
)

// ---------------------------------------------------------------------------
// Guard decisions
// ---------------------------------------------------------------------------

// GuardID names one compatibility guard. The ids are stable strings: they are
// persisted in the install manifest and rendered in Settings, so renaming one
// is a user-visible change, not a refactor.
type GuardID string

const (
	// GuardCUDA133Crash covers KNOWN_ISSUES "CUDA 13.3 builds crash on some
	// systems. On Linux this is a segfault; on Windows the server prints its
	// banner and exits without a message." Documented workaround: "use the CUDA
	// 12.8 build on Linux or the CUDA 12.4 build on Windows." Status: open
	// (PrismML-Eng/llama.cpp#222).
	GuardCUDA133Crash GuardID = "cuda-13.3-crash"

	// GuardROCmRDNA2Abort covers KNOWN_ISSUES "ROCm/HIP aborts on consumer
	// RDNA2 GPUs (for example gfx1030)." Documented workaround: "none yet on
	// HIP; try the Vulkan build." Status: open
	// (PrismML-Eng/Bonsai-demo#197).
	GuardROCmRDNA2Abort GuardID = "rocm-rdna2-abort"

	// GuardWindowsHIPGFX1151Garbled covers KNOWN_ISSUES "PQ2_0 produces garbled
	// output on Windows HIP with gfx1151 (Strix Halo integrated GPU). CPU
	// (-ngl 0) output is correct." Documented workaround: "-ngl 0, or the
	// Vulkan build with PTQ1_0." Status: open (PrismML-Eng/llama.cpp#223).
	//
	// This is the one guard whose failure mode is CORRECTNESS rather than
	// availability: the server starts, answers, and the answer is garbage. On a
	// unified-memory iGPU it would look like a model-quality problem, so it is
	// guarded before the install rather than diagnosed after it.
	GuardWindowsHIPGFX1151Garbled GuardID = "windows-hip-gfx1151-garbled"

	// GuardVulkanIntelArcHang covers KNOWN_ISSUES "PTQ1_0 on Vulkan can hang
	// Intel Arc GPUs after about 1,900 tokens." Documented workaround: "run on
	// CPU for now." Status: open (PrismML-Eng/llama.cpp#192).
	//
	// Advisory, not a substitution: c0wrk's Vulkan installs always resolve to
	// PTQ1_0 (Vulkan has no PQ2_0 kernels), so every Intel Arc Vulkan install
	// is in scope, and the only documented workaround gives up GPU acceleration
	// entirely. That trade is the user's, so the guard records and discloses it
	// instead of making it silently — and unlike the two aborting guards it
	// degrades after ~1,900 tokens of OUTPUT, which short answers may never
	// reach.
	GuardVulkanIntelArcHang GuardID = "vulkan-intel-arc-hang"

	// GuardWindowsCUDANoStart covers KNOWN_ISSUES "Windows CUDA builds don't
	// start on some CPUs; the Windows CPU-only build does." Documented
	// workaround: "use the CPU-only build while this is investigated." Status:
	// open (PrismML-Eng/llama.cpp#241).
	//
	// Advisory because it is not statically decidable: "some CPUs" names no
	// CPUID, no driver version and no card, so no table can predict it. What
	// the guard can do is make sure the fallback is already written down in the
	// install record when a Windows CUDA server prints its banner and exits,
	// instead of leaving the user to rediscover it.
	GuardWindowsCUDANoStart GuardID = "windows-cuda-no-start"
)

// GuardAction is what a guard asks the plan to do.
type GuardAction string

const (
	// GuardActionPreferBackend substitutes the runtime archive's backend. It is
	// only actionable while the plan is still on paper: once a runtime has been
	// downloaded and staged, the substitution becomes guidance.
	GuardActionPreferBackend GuardAction = "prefer_backend"

	// GuardActionPreferPacking substitutes the weights packing. Actionable
	// until the weights are downloaded, which is later than the runtime, so a
	// guard discovered by the post-staging device probe can still apply one.
	GuardActionPreferPacking GuardAction = "prefer_packing"

	// GuardActionAdvisory changes nothing and says something: the failure is
	// either not statically predictable (#241) or its workaround trades away
	// something the user may not want to trade (#192).
	GuardActionAdvisory GuardAction = "advisory"
)

// GuardReason is the typed cause behind a decision — the "why" a UI can render
// without parsing prose, and the axis a test can assert on.
type GuardReason string

const (
	// GuardReasonCrashOnLoad is a documented crash while the model loads
	// (#222: a Linux segfault, a Windows silent exit after the banner).
	GuardReasonCrashOnLoad GuardReason = "crash_on_load"

	// GuardReasonProcessAbort is a documented process abort on the accelerator
	// path (Bonsai-demo #197: ROCm/HIP aborts on consumer RDNA2).
	GuardReasonProcessAbort GuardReason = "process_abort"

	// GuardReasonGarbledOutput is documented WRONG OUTPUT: the server runs and
	// the tokens are nonsense (#223: PQ2_0 on Windows HIP with gfx1151).
	// Classified apart from the crash reasons because nothing else about the
	// install looks wrong — this is the failure that would otherwise be
	// attributed to the model.
	GuardReasonGarbledOutput GuardReason = "garbled_output"

	// GuardReasonHang is a documented hang partway through generation (#192:
	// PTQ1_0 on Vulkan with Intel Arc, after about 1,900 tokens).
	GuardReasonHang GuardReason = "hang"

	// GuardReasonFailsToStart is a documented start failure with no statically
	// identifiable trigger (#241: Windows CUDA builds on some CPUs).
	GuardReasonFailsToStart GuardReason = "fails_to_start"
)

// GuardSeverity ranks how bad the guarded failure is, for display and for
// triage. It is derived from the reason, never chosen per-guard by hand, so
// the two cannot drift apart.
type GuardSeverity string

const (
	// GuardSeverityCritical is a failure that makes the install unusable or
	// WRONG: a crash, an abort, garbled output.
	GuardSeverityCritical GuardSeverity = "critical"

	// GuardSeverityWarning is a failure that degrades or interrupts use: a hang
	// after a long output, a start failure that may or may not happen.
	GuardSeverityWarning GuardSeverity = "warning"
)

// severityFor ranks a typed reason. Every reason maps to a severity, and a
// reason with no mapping is critical: an unranked failure must never be
// rendered as the mildest thing on the list.
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

// GuardDecision is one guard's verdict for one machine. It is JSON-serializable
// because it is persisted in the install `Manifest` and mirrored into the
// Settings status DTO: the record of a degraded install has to outlive the
// process that made it.
type GuardDecision struct {
	// Guard is the stable id of the guard that fired.
	Guard GuardID `json:"guard"`
	// Action is what the guard asks for: a backend substitution, a packing
	// substitution, or advice.
	Action GuardAction `json:"action"`
	// Reason is the typed cause. Never empty — a decision without a reason is
	// indistinguishable from a bug, and "no guard silently changes behavior
	// without recording a reason" is an invariant of this subsystem.
	Reason GuardReason `json:"reason"`
	// Severity ranks the guarded failure for display.
	Severity GuardSeverity `json:"severity"`
	// Issue is the upstream citation, "<repo>#<number>"
	// (e.g. "PrismML-Eng/llama.cpp#222"). It is data, not only a comment, so a
	// rendered guard is traceable to the report that justifies it.
	Issue string `json:"issue"`
	// Applied reports whether c0wrk actually changed the plan because of this
	// decision. It is set by the consumer (the resolver, the installer), never
	// by the table: the table describes what SHOULD happen, Applied records
	// what DID. A decision with Applied=false is still surfaced — that is the
	// whole point of the field.
	Applied bool `json:"applied"`
	// Backend is the substituted backend, set only for GuardActionPreferBackend.
	Backend Backend `json:"backend,omitempty"`
	// Packing is the substituted packing, set only for GuardActionPreferPacking.
	Packing Packing `json:"packing,omitempty"`
	// Guidance is the user-facing sentence: what happened, what c0wrk did or
	// could not do, and what the upstream workaround is.
	Guidance string `json:"guidance"`
}

// CompatibilityGuards is the guard table: a pure lookup of
// (backend, GPU family, platform) onto every decision that applies, in table
// order (most severe first). It performs no I/O and never fails; a machine that
// matches nothing returns a nil slice, which is the common and healthy case.
//
// backend is the EFFECTIVE backend (what c0wrk would provision), gpu is
// ClassifyGPUs' answer for the machine's accelerator (GPUFamilyUnknown when no
// device probe answered), and platform is the "<goos>-<goarch>" key.
//
// The returned decisions carry Applied=false; the consumer sets it when it acts
// on one.
func CompatibilityGuards(backend Backend, gpu GPUFamily, platform string) []GuardDecision {
	var decisions []GuardDecision

	// #222 — CUDA 13.3 builds crash on some systems: a segfault on Linux, a
	// banner-then-exit on Windows. Upstream's workaround names the fallback per
	// OS, and c0wrk pins both (linux cuda-12.8, windows cuda-12.4), so the
	// substitution is exact. A CUDA 13.3 driver still runs an older-toolkit
	// build — that is the same backwards-compatibility rule clampCUDABackend
	// already relies on when an archive is missing — so downgrading costs
	// nothing but an unpinned-newer toolkit.
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

	// Bonsai-demo #197 — ROCm/HIP aborts on consumer RDNA2 GPUs (gfx1030). No
	// HIP workaround exists, so the guard switches to the Vulkan build, which
	// the same document recommends and which c0wrk pins for both ROCm-capable
	// platforms. Vulkan resolves to PTQ1_0, the packing Vulkan has kernels for.
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

	// #223 — PQ2_0 produces GARBLED OUTPUT on Windows HIP with gfx1151 (Strix
	// Halo's iGPU); CPU output is correct. Of the two documented workarounds
	// ("-ngl 0, or the Vulkan build with PTQ1_0") the guard applies the Vulkan
	// build, because at plan time a different runtime archive costs nothing
	// extra while -ngl 0 would give up GPU acceleration on a machine whose
	// unified memory is its whole point. The -ngl 0 alternative stays in the
	// guidance so a user who prefers it can see it.
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

	// #192 — PTQ1_0 on Vulkan can hang Intel Arc GPUs after about 1,900
	// tokens. Advisory: see the constant's doc comment for why the guard does
	// not substitute the CPU build on the user's behalf.
	if backend == BackendVulkan && gpu == GPUFamilyIntelArc {
		decisions = append(decisions, GuardDecision{
			Guard:    GuardVulkanIntelArcHang,
			Action:   GuardActionAdvisory,
			Reason:   GuardReasonHang,
			Issue:    "PrismML-Eng/llama.cpp#192",
			Guidance: "PTQ1_0 on Vulkan is documented to hang Intel Arc GPUs after about 1,900 generated tokens; if generation stalls, the upstream workaround is to run on the CPU (reinstall with the CPU build, or start the server with -ngl 0)",
		})
	}

	// #241 — Windows CUDA builds don't start on some CPUs while the Windows
	// CPU-only build does. Advisory: the trigger is unidentified upstream, so
	// nothing can be predicted here, but the fallback belongs in the record
	// before the failure rather than after it.
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

// mergeGuardDecisions appends the entries of extra whose Guard id is not
// already present in base, preserving base's order and its Applied flags.
//
// It exists because one install can learn about a machine twice — once from the
// static half of the table at plan time and once from a device probe after the
// runtime is staged — and the record must contain each guard once, with the
// flag from the pass that actually acted on it.
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
// PR #245, the fix for "PQ2_0 segfaults while loading on CPUs with AVX-512".
//
// The proof is two-sided and both sides are recorded in
// specs/domains/embedded-llm.md's pin-bump note: build 10709 (published
// 2026-09-18) demonstrably predates the fix, which merged 2026-09-23, and the
// CVE review of the 26-commit range 9a9394a..842b188 records #245 as landing
// inside it, so the current pin 10735 demonstrably contains it. Builds in
// between are unproven, so the threshold is the proven one and an unproven
// build is treated as predating the fix.
//
// That asymmetry is deliberate: treating an unfixed build as fixed segfaults
// the install at load, while treating a fixed build as unfixed only selects the
// smaller, slower-decoding packing. Raise this constant only with evidence
// (a release note, or the merge commit's position in the tag range), never to
// make a downgrade go away.
const minBuildWithAVX512PQ2_0Fix = 10735

// runtimeBuildRE matches the pinned fork release tag shape, "prism-b<build>-<commit>"
// (RuntimeTag = "prism-b10735-842b188"). The build number is a commit counter,
// not a date: b10709 → b10735 is exactly the 26-commit range the pin bump's
// CVE review counted.
var runtimeBuildRE = regexp.MustCompile(`^prism-b(\d+)-[0-9a-f]{7,40}$`)

// parseRuntimeBuild extracts the build number from a fork release tag. ok is
// false for any other shape, which callers must treat as "unproven" (0) rather
// than as "new".
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

// pinnedRuntimeBuild is the build number of the pin in registry.go, or 0 when
// RuntimeTag has a shape this file does not recognize. Zero compares below
// every threshold, so an unrecognizable pin gets the conservative treatment —
// the same direction as an unproven one.
func pinnedRuntimeBuild() int {
	build, _ := parseRuntimeBuild(RuntimeTag)
	return build
}
