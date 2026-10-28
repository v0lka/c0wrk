package embeddedllm

// MLX engine pins (ADR-092): the second embedded-LLM runtime family for
// darwin/arm64 — the pinned MTPLX release (a Python/MLX stack running
// Ternary-Bonsai-2-27B natively with MTP speculative decoding) plus the
// MLX-format weight pack. Supply chain (ADR-066 D11 posture): every artifact
// below is a compile-time pin — release, managed CPython, every wheel, every
// pack file; no upstream queries, no auto-updates; a bump is a hand-run,
// CVE-reviewed re-record of the whole table. Wheel SHA256s: PyPI JSON API;
// pack SHA256s: HF LFS OIDs / revision content hashes. The wheel set was
// resolved with `uv pip compile --generate-hashes` (cp312, aarch64-apple-
// darwin, deployment target 14.0) and is replayed offline via
// `pip install --require-hashes`. License: Apache-2.0 with a NOTICE clause —
// the Settings block surfaces "Powered by MTPLX" when this engine runs.
// See specs/decisions/092-embedded-llm-mlx-mtplx.md.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Engine identifies which inference runtime family the embedded subsystem
// provisions and supervises. The zero value is not valid input; config
// spelling "" means auto (see ResolveEngine).
type Engine string

const (
	// EngineAuto is the config spelling of "pick for the platform": mtplx on
	// darwin/arm64, llama everywhere else.
	EngineAuto Engine = "auto"
	// EngineLlama provisions the pinned PrismML-Eng llama.cpp fork (the
	// original ADR-066 family, unchanged).
	EngineLlama Engine = "llama"
	// EngineMTPLX provisions the pinned MTPLX release (MLX + MTP, macOS 14+
	// Apple Silicon only — the wheel tags below refuse older macOS).
	EngineMTPLX Engine = "mtplx"
)

// ErrEngineUnknown reports a config engine spelling the vocabulary does not
// define. ErrEngineUnsupportedPlatform reports an engine requested for a
// machine shape it cannot serve (mtplx off darwin/arm64).
var (
	ErrEngineUnknown             = errors.New("unknown embedded-LLM engine")
	ErrEngineUnsupportedPlatform = errors.New("embedded-LLM engine not supported on this platform")
)

// NormalizeEngine maps the empty config spelling to EngineAuto; vocabulary
// validation is ResolveEngine's job.
func NormalizeEngine(e Engine) Engine {
	if e == "" {
		return EngineAuto
	}
	return e
}

// ResolveEngine resolves the config request against the platform: auto picks
// mtplx on darwin/arm64 and llama elsewhere; an explicit mtplx off
// darwin/arm64 is a typed refusal, never a silent llama substitution.
func ResolveEngine(requested Engine, platform string) (Engine, error) {
	switch NormalizeEngine(requested) {
	case EngineAuto:
		if platform == PlatformDarwinARM64 {
			return EngineMTPLX, nil
		}
		return EngineLlama, nil
	case EngineLlama:
		if !IsSupportedPlatform(platform) {
			return "", fmt.Errorf("%w: platform %q", ErrEngineUnsupportedPlatform, platform)
		}
		return EngineLlama, nil
	case EngineMTPLX:
		if platform != PlatformDarwinARM64 {
			return "", fmt.Errorf("%w: engine %q needs %q, got %q (use engine %q)",
				ErrEngineUnsupportedPlatform, EngineMTPLX, PlatformDarwinARM64, platform, EngineLlama)
		}
		return EngineMTPLX, nil
	default:
		return "", fmt.Errorf("%w: %q (want %q, %q or %q)", ErrEngineUnknown,
			string(requested), EngineAuto, EngineLlama, EngineMTPLX)
	}
}

// mtplxPypiBase is the pinned MTPLX release, used by the smoke test and the
// About/attribution line.
const (
	MTPLXVersion = "2.12.2"

	// MTPLXPackRepo is the MLX weight pack: the Optimized-Speed pack with the
	// vision tower and MTP head inside (no separate projector artifact).
	mtlxPackRepoOwner = "Youssofal"
	mtlxPackRepoName  = "Ternary-Bonsai-2-27B-MTPLX-Optimized-Speed"
	// MLXPackRevision is the pinned Hugging Face revision of the pack.
	MLXPackRevision = "03bd60bb82755f2446f5083426dca9708eb8e0fe"
)

const mlxPackResolveBase = "https://huggingface.co/" + mtlxPackRepoOwner + "/" + mtlxPackRepoName + "/resolve/" + MLXPackRevision + "/"

// Managed CPython, from the python-build-standalone release. Pinning the
// archive keeps every byte under this package's fail-closed SHA256
// verification; the extracted interpreter — relocatable plain files, no venv,
// no uv — installs the pinned wheel set itself, offline (ADR-092 D2).
const (
	pythonInstallTag     = "20261013"
	pythonInstallVersion = "3.12.15"
	pythonArchiveName    = "cpython-" + pythonInstallVersion + "+" + pythonInstallTag + "-aarch64-apple-darwin-install_only.tar.gz"
	pythonReleaseBase    = "https://github.com/astral-sh/python-build-standalone/releases/download/" + pythonInstallTag + "/"
)

// pythonArchiveAsset is the managed CPython 3.12 for Apple Silicon. The
// install_only tree unpacks to ./python with bin/python3.12 inside.
var pythonArchiveAsset = Asset{
	Component:   ComponentPython,
	URL:         pythonReleaseBase + pythonArchiveName,
	SHA256:      "ba32036ad8f4c434d97a1ee7feef8b7edc0fac16777639b97e39c74a108e4440",
	SizeBytes:   25180390,
	ArchiveName: pythonArchiveName,
}

// PythonArchiveAsset returns the pinned managed-CPython archive for the mtplx
// engine. Callers gate the platform before asking; ok is always true.
func PythonArchiveAsset() (Asset, bool) { return pythonArchiveAsset, true }

// mtplxWheelPins is the complete, closed wheel set of the engine tree:
// mtplx itself, its pinned MLX (exactly 0.32.2 — upstream's exactness-gated
// pin), and every transitive dependency for cp312/aarch64-apple-darwin —
// replayed as a requirements file for `pip install --require-hashes`, so an
// unsigned registry response cannot enter the tree (ADR-092 D1).

var mtplxWheelPins = []WheelAsset{
	{Name: "annotated-doc", Version: "0.0.5",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/3e/30/e900b21425a860e195f32e37657aa1f7c7f2b1bfb26f03ca209b90933c06/annotated_doc-0.0.5-py3-none-any.whl",
			SHA256: "117bac03a25ede5df5440e855b32d556049ca169ead221505badf432fed4b101", SizeBytes: 5302,
			ArchiveName: "annotated_doc-0.0.5-py3-none-any.whl"}},
	{Name: "annotated-types", Version: "0.8.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/99/91/8acff4f5e50511b911bbccb72b8628a49c68ce14148cd9f6431094859a90/annotated_types-0.8.0-py3-none-any.whl",
			SHA256: "f072f4d804ea359e4eaf198b1af7a8b0943881a87f31bb764f8bf219bb9419e0", SizeBytes: 13427,
			ArchiveName: "annotated_types-0.8.0-py3-none-any.whl"}},
	{Name: "anyio", Version: "4.15.1",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/12/b8/4bd346e22b28902df4d651910f5242c28d84e4a5c2435ca5c3f797ed7e2e/anyio-4.15.1-py3-none-any.whl",
			SHA256: "6152fdbbf9a77fdec97731721bebf7c4c44f7c29b424b0065826173efc7ed101", SizeBytes: 132079,
			ArchiveName: "anyio-4.15.1-py3-none-any.whl"}},
	{Name: "certifi", Version: "2026.10.19",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/c4/51/29046e48f693c54e5b7412216d447a1d8362c9653148d98c02cdf65fa733/certifi-2026.10.19-py3-none-any.whl",
			SHA256: "42d35340cc5b9ba78e818ddbff0c5b1c2b779de448a340fd1b989f5fa8670e1b", SizeBytes: 136464,
			ArchiveName: "certifi-2026.10.19-py3-none-any.whl"}},
	{Name: "click", Version: "8.5.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/58/50/6c0d534c5f134586a8e1ba4e330569e32f057e33372ae556463212fb4cd3/click-8.5.0-py3-none-any.whl",
			SHA256: "255bc9599cf7748b4b1a446ccc735421bd08a2ae529a8b88597d3de5664ee360", SizeBytes: 125251,
			ArchiveName: "click-8.5.0-py3-none-any.whl"}},
	{Name: "fastapi", Version: "0.150.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/14/41/7c472e281c147dae4f1dd4a85d9a1b7f6ce8b919e4053f14c83abb1488c7/fastapi-0.150.0-py3-none-any.whl",
			SHA256: "e566bc3f1347cfacf61f2f9ea1d7ab4576fbcff02eb9747c2807b8ced16d9e9a", SizeBytes: 145281,
			ArchiveName: "fastapi-0.150.0-py3-none-any.whl"}},
	{Name: "filelock", Version: "4.2.4",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/e0/1b/5e74289a1878e666bb4b65fe5805a04cafcac2aa5f519ef1ead67ef91c56/filelock-4.2.4-py3-none-any.whl",
			SHA256: "301241438c57936ef93f4243a94f2559022268bfcf9b375a9a72eb0f8db8358e", SizeBytes: 162475,
			ArchiveName: "filelock-4.2.4-py3-none-any.whl"}},
	{Name: "fsspec", Version: "2026.9.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/6c/c0/a98505f18594f1bce828bb159cec0fcf9860562f1a2c85913409fc8f3d9e/fsspec-2026.9.0-py3-none-any.whl",
			SHA256: "8dd6e646e99ea382bd85f97a45e6b526a442d79423a7dc673f1e2756d05fcb5f", SizeBytes: 221738,
			ArchiveName: "fsspec-2026.9.0-py3-none-any.whl"}},
	{Name: "h11", Version: "0.16.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/04/4b/29cac41a4d98d144bf5f6d33995617b185d14b22401f75ca86f384e87ff1/h11-0.16.0-py3-none-any.whl",
			SHA256: "63cf8bbe7522de3bf65932fda1d9c2772064ffb3dae62d55932da54b31cb6c86", SizeBytes: 37515,
			ArchiveName: "h11-0.16.0-py3-none-any.whl"}},
	{Name: "hf-xet", Version: "1.7.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/c3/47/a26ebdce7056a61e931f228439bc0ab08cbec239d1690f965e5e637cba79/hf_xet-1.7.0-cp38-abi3-macosx_11_0_arm64.whl",
			SHA256: "59fba37039233c7fcbe196817d6cdcf1b40dfb17b410f229d85b0cf0a1848da4", SizeBytes: 3560352,
			ArchiveName: "hf_xet-1.7.0-cp38-abi3-macosx_11_0_arm64.whl"}},
	{Name: "httpcore", Version: "1.0.9",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/7e/f5/f66802a942d491edb555dd61e3a9961140fd64c90bce1eafd741609d334d/httpcore-1.0.9-py3-none-any.whl",
			SHA256: "2d400746a40668fc9dec9810239072b40b4484b640a8c38fd654a024c7a1bf55", SizeBytes: 78784,
			ArchiveName: "httpcore-1.0.9-py3-none-any.whl"}},
	{Name: "httpx", Version: "0.28.1",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/2a/39/e50c7c3a983047577ee07d2a9e53faf5a69493943ec3f6a384bdc792deb2/httpx-0.28.1-py3-none-any.whl",
			SHA256: "d909fcccc110f8c7faf814ca82a9a4d816bc5a6dbfea25d6591d6985b8ba59ad", SizeBytes: 73517,
			ArchiveName: "httpx-0.28.1-py3-none-any.whl"}},
	{Name: "huggingface-hub", Version: "1.33.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/fc/16/963096d224b80909432dc16561a615fd33d2d13beef3ce4c63fa25e40867/huggingface_hub-1.33.0-py3-none-any.whl",
			SHA256: "04e434b06e100eddbce9a6e817d72693a7884b10a79bd67ab48080d5c07eb899", SizeBytes: 846435,
			ArchiveName: "huggingface_hub-1.33.0-py3-none-any.whl"}},
	{Name: "idna", Version: "3.20",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/58/a2/bb081bab032533a855d44de1d56f8e8426114ff1ba5d1f07a438a0a654f8/idna-3.20-py3-none-any.whl",
			SHA256: "ab7ae7122974553370f0bdb919e1a960b2cd1bc1ef0276416d896db81c14582c", SizeBytes: 69583,
			ArchiveName: "idna-3.20-py3-none-any.whl"}},
	{Name: "jinja2", Version: "3.1.6",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/62/a1/3d680cbfd5f4b8f15abc1d571870c5fc3e594bb582bc3b64ea099db13e56/jinja2-3.1.6-py3-none-any.whl",
			SHA256: "85ece4451f492d0c13c5dd7c13a64681a86afae63a5f347908daf103ce6d2f67", SizeBytes: 134899,
			ArchiveName: "jinja2-3.1.6-py3-none-any.whl"}},
	{Name: "llguidance", Version: "1.9.1",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/6e/6e/f4acb8bd966e3533ebc888eeb7854482ece71668e002401a41bbfe37dd07/llguidance-1.9.1-cp39-abi3-macosx_11_0_arm64.whl",
			SHA256: "a942487cd6221b793df3a11450881a6c9d25a45460a900c56d99b682633c45fe", SizeBytes: 3141060,
			ArchiveName: "llguidance-1.9.1-cp39-abi3-macosx_11_0_arm64.whl"}},
	{Name: "markdown-it-py", Version: "4.2.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/b3/81/4da04ced5a082363ecfa159c010d200ecbd959ae410c10c0264a38cac0f5/markdown_it_py-4.2.0-py3-none-any.whl",
			SHA256: "9f7ebbcd14fe59494226453aed97c1070d83f8d24b6fc3a3bcf9a38092641c4a", SizeBytes: 91687,
			ArchiveName: "markdown_it_py-4.2.0-py3-none-any.whl"}},
	{Name: "markupsafe", Version: "3.0.4",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/a2/f0/d6613774d86fbf6d145751d43c59875e47a6f9f17daee0aef173bd36d90e/markupsafe-3.0.4-cp312-cp312-macosx_11_0_arm64.whl",
			SHA256: "0930db9bdc62d22944e10b066448bb65dc9abe9112880c7cab8da54db4284d5f", SizeBytes: 12050,
			ArchiveName: "markupsafe-3.0.4-cp312-cp312-macosx_11_0_arm64.whl"}},
	{Name: "mdurl", Version: "0.1.2",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/b3/38/89ba8ad64ae25be8de66a6d463314cf1eb366222074cfda9ee839c56a4b4/mdurl-0.1.2-py3-none-any.whl",
			SHA256: "84008a41e51615a49fc9966191ff91509e3c40b939176e643fd50a5c2196b8f8", SizeBytes: 9979,
			ArchiveName: "mdurl-0.1.2-py3-none-any.whl"}},
	{Name: "mlx", Version: "0.32.2",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/38/f7/5664e13821491559c953494e6705c818203d6880d713a0c22e12bead9564/mlx-0.32.2-cp312-cp312-macosx_14_0_arm64.whl",
			SHA256: "77217798a2b036bae9f213b851d4cde4581893787c9964458b7d471f86036bd6", SizeBytes: 619370,
			ArchiveName: "mlx-0.32.2-cp312-cp312-macosx_14_0_arm64.whl"}},
	{Name: "mlx-lm", Version: "0.31.3",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/90/02/9a67b8e4f87e3e2e5cd7b1ad79304b93c09a0db6af34bee75e6551c06c60/mlx_lm-0.31.3-py3-none-any.whl",
			SHA256: "758cfddf1180053b7613db76fad3d246a331a2a905808e1164a275621fc983b8", SizeBytes: 408890,
			ArchiveName: "mlx_lm-0.31.3-py3-none-any.whl"}},
	{Name: "mlx-metal", Version: "0.32.2",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/f7/ab/ba1952908c5d2a5070cf1cfbfea0161c4751ea62299e2776819810917483/mlx_metal-0.32.2-py3-none-macosx_14_0_arm64.whl",
			SHA256: "3825fff379dbc107dd3413e564a06caeaa24819910ec49c0439e454c06a1b9b8", SizeBytes: 42516623,
			ArchiveName: "mlx_metal-0.32.2-py3-none-macosx_14_0_arm64.whl"}},
	{Name: "mtplx", Version: "2.12.2",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/cc/0b/1b73edee858783c5d22f9f96ae5f700ed88c85920ab91e579263b75cfcd4/mtplx-2.12.2-py3-none-any.whl",
			SHA256: "6f77365395d5574316be5d521a16d23681cd9c44622ce555d5d7242e00d8a91f", SizeBytes: 3328884,
			ArchiveName: "mtplx-2.12.2-py3-none-any.whl"}},
	{Name: "nanobind", Version: "3.1.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/36/c1/6fd56f7a02a478d54a1a3e6000eccd34b446b1c45cbf0d002fe5d2937126/nanobind-3.1.0-py3-none-any.whl",
			SHA256: "96984dbe8154ffb86e653b1b60aaeed6985c710c5e75ad0279eb87943c66292d", SizeBytes: 316917,
			ArchiveName: "nanobind-3.1.0-py3-none-any.whl"}},
	{Name: "numpy", Version: "2.5.4",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/ff/a9/bac826765e971d8e16e2064e9ac7525fd69b40ac17c905033a7f5442023f/numpy-2.5.4-cp312-cp312-macosx_11_0_arm64.whl",
			SHA256: "b11e8fda06a7d69f15ebf542660b74466c2e51094800c1fb794f47ad4faeef17", SizeBytes: 12015718,
			ArchiveName: "numpy-2.5.4-cp312-cp312-macosx_11_0_arm64.whl"}},
	{Name: "opentelemetry-api", Version: "1.45.1",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/1e/41/f7dcf80b81ee8e71c1a2b59f14208bc723edbd89ed027a73b175abf6348e/opentelemetry_api-1.45.1-py3-none-any.whl",
			SHA256: "b31553efa588ae44bc306f863c785c5333a9ecc091248c6ee68b4b6c87fdedfb", SizeBytes: 60256,
			ArchiveName: "opentelemetry_api-1.45.1-py3-none-any.whl"}},
	{Name: "packaging", Version: "26.3",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/63/34/ba1c580383c9eada3711951fef0795c80b829a078d72188184bcab9dd527/packaging-26.3-py3-none-any.whl",
			SHA256: "d7193f7c8e4e93f444fde0262bf90af30e16fa0ad0ad44cb553c87339b23cd1c", SizeBytes: 129956,
			ArchiveName: "packaging-26.3-py3-none-any.whl"}},
	{Name: "pillow", Version: "13.0.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/39/4b/f2ed53d83aa44ac19599ac0a54897f867fafb95f406980d1e24083f8cbe3/pillow-13.0.0-cp312-cp312-macosx_11_0_arm64.whl",
			SHA256: "fc481f742ccda20e9ed760b8f978dff50f2a4e9bc253ac167fb0a05b0783ef52", SizeBytes: 4852329,
			ArchiveName: "pillow-13.0.0-cp312-cp312-macosx_11_0_arm64.whl"}},
	{Name: "protobuf", Version: "7.36.2",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/e4/04/d52c7016b04b6c5108f26691f9d33ec82a9b65d041f1a9c771137693d618/protobuf-7.36.2-py3-none-any.whl",
			SHA256: "bdb3a345d48db958e6ce1f18e508beb0cc981d64f24088427549c866cd039f1e", SizeBytes: 179806,
			ArchiveName: "protobuf-7.36.2-py3-none-any.whl"}},
	{Name: "pydantic", Version: "2.14.1",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/35/ea/a56b9fe5066f3537b7882f77e9c5dfb26d8c2eefdaed9b5fc73d57b4dc22/pydantic-2.14.1-py3-none-any.whl",
			SHA256: "9195d967ec791692a04438115466764fb8b9a27b31f14a760437694f40d6b454", SizeBytes: 488106,
			ArchiveName: "pydantic-2.14.1-py3-none-any.whl"}},
	{Name: "pydantic-core", Version: "2.50.1",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/db/a3/f8b1b39349c33037c73d7a1b794c9c29d8f07d5bd2b74399c1375a529cc6/pydantic_core-2.50.1-cp312-cp312-macosx_11_0_arm64.whl",
			SHA256: "e8e1d6ce820aa23317e8209a86bd65a540973c12dc7552b48a4f6c8e9926815e", SizeBytes: 2025779,
			ArchiveName: "pydantic_core-2.50.1-cp312-cp312-macosx_11_0_arm64.whl"}},
	{Name: "pygments", Version: "2.21.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/71/46/17f022dd3e953bf20a04a028a21ec746d942f8d2af30fa0f124fa0e6a684/pygments-2.21.0-py3-none-any.whl",
			SHA256: "2363c69b61c4a97c838da3b130dcd6468f4848992b21a82f2a63ec34377137d9", SizeBytes: 1250147,
			ArchiveName: "pygments-2.21.0-py3-none-any.whl"}},
	{Name: "pyyaml", Version: "6.0.3",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/89/a0/6cf41a19a1f2f3feab0e9c0b74134aa2ce6849093d5517a0c550fe37a648/pyyaml-6.0.3-cp312-cp312-macosx_11_0_arm64.whl",
			SHA256: "fc09d0aa354569bc501d4e787133afc08552722d3ab34836a80547331bb5d4a0", SizeBytes: 173973,
			ArchiveName: "pyyaml-6.0.3-cp312-cp312-macosx_11_0_arm64.whl"}},
	{Name: "regex", Version: "2026.9.29",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/37/14/f3f0ba083d2094392d5eabf56db5ea6ba469fd6e927afd187042054ea68a/regex-2026.9.29-cp312-cp312-macosx_11_0_arm64.whl",
			SHA256: "f57dc6b8fef170f105d2cf5cdce254f47b137d7755086cf7050f47e16582abba", SizeBytes: 293633,
			ArchiveName: "regex-2026.9.29-cp312-cp312-macosx_11_0_arm64.whl"}},
	{Name: "rich", Version: "15.0.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/82/3b/64d4899d73f91ba49a8c18a8ff3f0ea8f1c1d75481760df8c68ef5235bf5/rich-15.0.0-py3-none-any.whl",
			SHA256: "33bd4ef74232fb73fe9279a257718407f169c09b78a87ad3d296f548e27de0bb", SizeBytes: 310654,
			ArchiveName: "rich-15.0.0-py3-none-any.whl"}},
	{Name: "safetensors", Version: "0.8.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/f5/b1/fa7c600e7dceae12e9606c7578cbc9ff1e1ed55844883ee5c92205e86226/safetensors-0.8.0-cp310-abi3-macosx_11_0_arm64.whl",
			SHA256: "c80201d22cbf405b80647a60ada77bba06c8fba2da2743ba1e89cdcc39a81f25", SizeBytes: 484562,
			ArchiveName: "safetensors-0.8.0-cp310-abi3-macosx_11_0_arm64.whl"}},
	{Name: "sentencepiece", Version: "0.2.2",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/bd/44/caa9cab5f261a019e2808bc5046152775dc57352ba9cbae7525e9e7a1ed4/sentencepiece-0.2.2-cp312-cp312-macosx_11_0_arm64.whl",
			SHA256: "38111ed1f79268f399c505028023d5eaaf0ab4e5eafceb709468b0d3323e7838", SizeBytes: 1347176,
			ArchiveName: "sentencepiece-0.2.2-cp312-cp312-macosx_11_0_arm64.whl"}},
	{Name: "shellingham", Version: "1.5.4",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/e0/f9/0595336914c5619e5f28a1fb793285925a8cd4b432c9da0a987836c7f822/shellingham-1.5.4-py2.py3-none-any.whl",
			SHA256: "7ecfff8f2fd72616f7481040475a65b2bf8af90a56c89140852d1120324e8686", SizeBytes: 9755,
			ArchiveName: "shellingham-1.5.4-py2.py3-none-any.whl"}},
	{Name: "starlette", Version: "1.9.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/f3/93/181df62699d9b5aca1d87e5cfda131dac87e3cdaebf668662f7206ca3470/starlette-1.9.0-py3-none-any.whl",
			SHA256: "fd3f159936e882d06c1108f84688f7cda2cf85fc6a592a50edbd175e882c2ddc", SizeBytes: 80335,
			ArchiveName: "starlette-1.9.0-py3-none-any.whl"}},
	{Name: "tokenizers", Version: "0.23.3",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/b2/72/01e49f032bb346e5aaf06c10c74fe8aeec847173adbadd66eb7c53054bf2/tokenizers-0.23.3-cp310-abi3-macosx_11_0_arm64.whl",
			SHA256: "68649e97d5b43c44c031d8d848874a6eecae8f8fe40ea989aa777a5a83aca716", SizeBytes: 3101381,
			ArchiveName: "tokenizers-0.23.3-cp310-abi3-macosx_11_0_arm64.whl"}},
	{Name: "tqdm", Version: "4.70.1",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/a7/03/921a3d3c75785aca9ebfbfcabfbc3a1be12e2ab5265deb026d55a5a3f83e/tqdm-4.70.1-py3-none-any.whl",
			SHA256: "c293e525e6fef9c20e8728fd4612df02a0aa31bb5fe91ecd93e123b1b7bffa73", SizeBytes: 80199,
			ArchiveName: "tqdm-4.70.1-py3-none-any.whl"}},
	{Name: "transformers", Version: "5.16.1",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/0d/4d/ee3728674c0bbc637bb4af88ccf0be697f92e4e90b55f5dc110c44d61b61/transformers-5.16.1-py3-none-any.whl",
			SHA256: "2f2d5b98a5ad3718713653734298fa620754ed683702a635ebb587df3ed29c7e", SizeBytes: 12080592,
			ArchiveName: "transformers-5.16.1-py3-none-any.whl"}},
	{Name: "typer", Version: "0.27.3",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/07/ea/2e31b67051e91a133189e9c000c222502ddc6969856416de0d095de4c0b0/typer-0.27.3-py3-none-any.whl",
			SHA256: "e50022f28b82a86313e54501317a1db64bf8f8d036ff8cfe5ca7e47675454aff", SizeBytes: 123312,
			ArchiveName: "typer-0.27.3-py3-none-any.whl"}},
	{Name: "typing-extensions", Version: "4.16.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/49/d3/b8441a820a491ddfc024b0b0cf0393375b75ea13866d9c66727e54c2fc80/typing_extensions-4.16.0-py3-none-any.whl",
			SHA256: "481caa481374e813c1b176ada14e97f1f67a4539ce9cfeb3f350d78d6370c2e8", SizeBytes: 45571,
			ArchiveName: "typing_extensions-4.16.0-py3-none-any.whl"}},
	{Name: "typing-inspection", Version: "0.4.4",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/67/81/4add07e5172b7ac40d8ed5ff580409a7801a4fe26d529bdd915401dabfbe/typing_inspection-0.4.4-py3-none-any.whl",
			SHA256: "65b8397ba37ccbce054456aaccddfc91e6e3083c92824df348d96ca832f3f147", SizeBytes: 14750,
			ArchiveName: "typing_inspection-0.4.4-py3-none-any.whl"}},
	{Name: "uvicorn", Version: "0.54.0",
		Asset: Asset{Component: ComponentEngineWheels,
			URL:    "https://files.pythonhosted.org/packages/38/0c/b54a4fdd7f90a3af8b02ebc9ce6712c2c208b7926a2f7bad95c33ebbe943/uvicorn-0.54.0-py3-none-any.whl",
			SHA256: "505bdb0f318731d45f1f712071fc781a8981f6847a31c902c9f5e652d4f67faf", SizeBytes: 87427,
			ArchiveName: "uvicorn-0.54.0-py3-none-any.whl"}},
}

// mlxPackPins is the complete file set of the pinned MLX weight pack, in
// download order (weights, MTP sidecar, tokenizer stack, rotation table,
// serving contract). LFS-file SHA256s are the HF LFS OIDs of MLXPackRevision,
// the rest revision content hashes. The pack is a plain directory launched
// with `--model <dir>`; nothing is fetched through `mtplx pull`.
var mlxPackPins = []Asset{
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "model.safetensors",
		SHA256: "130de5925082c168b7866b2e91b52e44abbafc99017e3ca352b77b5b55a269ed", SizeBytes: 8595477990,
		ArchiveName: "model.safetensors"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "mtp.safetensors",
		SHA256: "f803737b0e64bcce54fcfa22f2c7ee342ad37192b8e2a3299fc940387a828c9d", SizeBytes: 238934226,
		ArchiveName: "mtp.safetensors"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "tokenizer.json",
		SHA256: "0997f410c57a1f4e53b09e4be8f4a172d90edd9564368fb0847030937229b9f3", SizeBytes: 12809320,
		ArchiveName: "tokenizer.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "hadamard.json",
		SHA256: "7132a3ec364f0bdac1f08f905f24f0ad2f14245060f592637a0396826d3b5fe6", SizeBytes: 297903,
		ArchiveName: "hadamard.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "config.json",
		SHA256: "c9b40bb2ffae58ebedaea92b21d946cf58ab729d0c4efe4edba5863bb594cfd5", SizeBytes: 58623,
		ArchiveName: "config.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "generation_config.json",
		SHA256: "cddd0dbd24dbf13229f872b4100c5a0bfe5186eeb09e171b2fc2bac701e98034", SizeBytes: 85,
		ArchiveName: "generation_config.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "tokenizer_config.json",
		SHA256: "b11349aafa7cdc6a320767cf7ceb29ed82f7eda5d65e8e0819e76f0ce947bf27", SizeBytes: 17928,
		ArchiveName: "tokenizer_config.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "chat_template.jinja",
		SHA256: "c3cf9e34abf4f9e36c2d72165aa9c132d3e2a725b6c2586aaa3a8af9d7a81041", SizeBytes: 8952,
		ArchiveName: "chat_template.jinja"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "model.safetensors.index.json",
		SHA256: "b6babfc6f8ed4030304445b00d0cb27760a3fed35b9422fe08edc8797000cd11", SizeBytes: 192933,
		ArchiveName: "model.safetensors.index.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "mtplx_runtime.json",
		SHA256: "719027b3fc6833ee72c7f8f90dc011fdd24f5f44dd4806e5be537c3782f225e6", SizeBytes: 99936,
		ArchiveName: "mtplx_runtime.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "MTPLX_PACK_MANIFEST.json",
		SHA256: "c3c9c2572d2a824a5566407796f738c15f35bac94ebc631b02acb4138adedfaf", SizeBytes: 3795,
		ArchiveName: "MTPLX_PACK_MANIFEST.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "preprocessor_config.json",
		SHA256: "27225450ac9c6529872ee1924fcb0962ff5634834f817040f444118116f4e516", SizeBytes: 390,
		ArchiveName: "preprocessor_config.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "processor_config.json",
		SHA256: "dcc7ce0121d6d1c514804e68e5673e04c239ad2547339ea6330289d272a748f8", SizeBytes: 367,
		ArchiveName: "processor_config.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "video_preprocessor_config.json",
		SHA256: "7768af27c1fafa9cc9011c1dc20067e03f8915e03b63504550e11d5066986d13", SizeBytes: 385,
		ArchiveName: "video_preprocessor_config.json"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "README.md",
		SHA256: "a27209556d5b7926efb2e014f78d5ada12ab737b11a2ca1daf143349fc9db7f1", SizeBytes: 3968,
		ArchiveName: "README.md"},
	{Component: ComponentMLXPack, URL: mlxPackResolveBase + "NOTICE.txt",
		SHA256: "de0e0c48fb6f691a31e74f338e3ccf93f9ecdfe2866ab769c4bf8b79a7636a30", SizeBytes: 411,
		ArchiveName: "NOTICE.txt"},
}

// WheelAsset is one pinned wheel, carrying its distribution identity for
// diagnostics and its fail-closed download pin.
type WheelAsset struct {
	Name    string // PyPI project name (lowercase, hyphenated)
	Version string
	Asset   Asset // Component is ComponentEngineWheels
}

// EngineWheel returns the pinned wheel for a distribution name; false for an
// unlisted dependency — a resolution failure, never an unpinned download.
func EngineWheel(name string) (WheelAsset, bool) {
	for _, w := range mtplxWheelPins {
		if w.Name == name {
			return w, true
		}
	}
	return WheelAsset{}, false
}

// EngineWheels returns every pinned wheel in the registry's stable order;
// correctness does not depend on it (pip matches by name+hash).
func EngineWheels() []WheelAsset { return slices.Clone(mtplxWheelPins) }

// TotalWheelBytes sums the pinned sizes of the whole engine wheel set; the
// installer uses it for the disk guard and the progress total.
func TotalWheelBytes() int64 {
	var total int64
	for _, w := range mtplxWheelPins {
		total += w.Asset.SizeBytes
	}
	return total
}

// MLXPackFiles returns the pinned pack files in download order. The returned
// slice is a copy: callers may sort or filter it freely.
func MLXPackFiles() []Asset { return slices.Clone(mlxPackPins) }

// MLXPackFile returns one pinned pack file by name. ok is false for a file
// the pin does not carry.
func MLXPackFile(name string) (Asset, bool) {
	for _, a := range mlxPackPins {
		if a.ArchiveName == name {
			return a, true
		}
	}
	return Asset{}, false
}

// MLXPackBytes sums the exact pinned sizes of the whole pack.
func MLXPackBytes() int64 { return TotalBytes(mlxPackPins) }

// EngineArtifactSet resolves the ordered download set for an mtplx install on
// darwin/arm64: managed CPython, wheel set, weight pack (vision tower and MTP
// head inside the pack — no projector, no cudart). Fail-closed off darwin/arm64.
func EngineArtifactSet(platform string) ([]Asset, error) {
	if platform != PlatformDarwinARM64 {
		return nil, fmt.Errorf("%w: engine %q needs %q, got %q",
			ErrEngineUnsupportedPlatform, EngineMTPLX, PlatformDarwinARM64, platform)
	}
	set := make([]Asset, 0, 2+len(mtplxWheelPins))
	set = append(set, pythonArchiveAsset)
	for _, w := range mtplxWheelPins {
		set = append(set, w.Asset)
	}
	return append(set, mlxPackPins...), nil
}

// ValidateMLXPins self-checks the engine pin tables with the same contract as
// ValidateRegistry, plus wheel-identity uniqueness and the required
// distributions (the pinned engine and its pinned MLX included).
func ValidateMLXPins() error {
	return validateMLXPinTables(pythonArchiveAsset, mtplxWheelPins, mlxPackPins)
}

// validateMLXPinTables is ValidateMLXPins over explicit tables, so a test
// proves the validator has teeth without mutating shared state.
func validateMLXPinTables(python Asset, wheels []WheelAsset, pack []Asset) error {
	var errs []error
	check := func(where string, a Asset) {
		if a.URL == "" {
			errs = append(errs, fmt.Errorf("%s: empty URL", where))
		}
		if !validSHA256Hex(a.SHA256) {
			errs = append(errs, fmt.Errorf("%s: invalid sha256 %q", where, a.SHA256))
		}
		if a.SizeBytes <= 0 {
			errs = append(errs, fmt.Errorf("%s: non-positive size %d", where, a.SizeBytes))
		}
		if a.ArchiveName == "" {
			errs = append(errs, fmt.Errorf("%s: empty archive name", where))
		}
		if strings.Contains(a.ArchiveName, "/") || strings.Contains(a.ArchiveName, "\\") {
			errs = append(errs, fmt.Errorf("%s: archive name %q is not a plain file name", where, a.ArchiveName))
		}
	}

	check("python archive", python)
	if python.Component != ComponentPython {
		errs = append(errs, fmt.Errorf("python archive: component is %q, want %q", python.Component, ComponentPython))
	}
	if !strings.HasPrefix(python.URL, pythonReleaseBase) {
		errs = append(errs, errors.New("python archive: URL is not under the pinned release"))
	}

	seen := make(map[string]string, len(wheels))
	for _, w := range wheels {
		where := "wheel " + w.Name
		check(where, w.Asset)
		if w.Asset.Component != ComponentEngineWheels {
			errs = append(errs, fmt.Errorf("%s: component is %q, want %q", where, w.Asset.Component, ComponentEngineWheels))
		}
		if !strings.HasPrefix(w.Asset.URL, "https://files.pythonhosted.org/packages/") {
			errs = append(errs, fmt.Errorf("%s: URL is not a pinned files.pythonhosted.org path", where))
		}
		if w.Asset.ArchiveName != w.Asset.URL[strings.LastIndex(w.Asset.URL, "/")+1:] {
			errs = append(errs, fmt.Errorf("%s: archive name %q does not match the URL tail", where, w.Asset.ArchiveName))
		}
		if prev, dup := seen[w.Name]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate distribution (already pinned at %s)", where, prev))
		}
		seen[w.Name] = w.Version
	}
	for _, required := range []string{"mtplx", "mlx", "mlx-metal", "mlx-lm", "transformers"} {
		if _, ok := seen[required]; !ok {
			errs = append(errs, fmt.Errorf("wheel set is missing the required distribution %q", required))
		}
	}
	if w, ok := EngineWheel("mlx"); ok && w.Version != "0.32.2" {
		errs = append(errs, fmt.Errorf("wheel mlx is pinned at %s, want the upstream exactness pin 0.32.2", w.Version))
	}

	seenFiles := make(map[string]bool, len(pack))
	for _, a := range pack {
		where := "pack file " + a.ArchiveName
		check(where, a)
		if a.Component != ComponentMLXPack {
			errs = append(errs, fmt.Errorf("%s: component is %q, want %q", where, a.Component, ComponentMLXPack))
		}
		if !strings.HasPrefix(a.URL, mlxPackResolveBase) {
			errs = append(errs, fmt.Errorf("%s: URL is not under the pinned pack revision", where))
		}
		if want := mlxPackResolveBase + a.ArchiveName; a.URL != want {
			errs = append(errs, fmt.Errorf("%s: URL %q does not match archive name", where, a.URL))
		}
		if seenFiles[a.ArchiveName] {
			errs = append(errs, fmt.Errorf("%s: duplicate pack file", where))
		}
		seenFiles[a.ArchiveName] = true
	}
	for _, required := range []string{
		"model.safetensors", "mtp.safetensors", "tokenizer.json", "config.json",
		"mtplx_runtime.json", "hadamard.json", "NOTICE.txt",
	} {
		if !seenFiles[required] {
			errs = append(errs, fmt.Errorf("pack is missing the required file %q", required))
		}
	}

	return errors.Join(errs...)
}
