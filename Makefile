.PHONY: build test lint fmt-check vulncheck dev-desktop bump fetch-onnx fetch-embedding-model clean-onnx clean frontend-deps

# govulncheck version pinned for reproducible vulnerability scans (CI runs the
# same `make vulncheck` command; upgrade deliberately, both repos in lockstep).
GOVULNCHECK_VERSION := v1.7.0

# ONNX Runtime version
ONNX_VERSION := 1.28.1

# Detect OS and architecture
UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)

# Platform-specific configuration, together with the pinned SHA256 digests of
# the release archive (ONNX_SHA256) and of the shared library extracted from it
# (ONNX_LIB_SHA256 — the artifact cached in $(ONNX_CACHE_DIR)).
#
# No checksum FILES are attached to onnxruntime GitHub releases, so the digests
# are pinned trust-on-first-use from fresh downloads of the official release
# URLs. For v1.28.1 every archive digest was additionally cross-verified
# against the server-computed "digest" (sha256) field GitHub publishes for
# release assets in the REST API (api.github.com .../releases/tags/v<version>).
# When bumping ONNX_VERSION, recompute and update every digest below (see also
# scripts/fetch-onnx.ps1, which carries the win-x64 digests):
#   curl -LO <ONNX_URL> && shasum -a 256 <archive>
#   tar -xzf <archive> && shasum -a 256 <archive-top>/lib/<lib name>
ifeq ($(UNAME_S),Darwin)
	ifeq ($(UNAME_M),arm64)
		ONNX_ARCHIVE := onnxruntime-osx-arm64-$(ONNX_VERSION).tgz
		ONNX_LIB_NAME := libonnxruntime.$(ONNX_VERSION).dylib
		ONNX_SHA256 := 18c853e5c5deba90e244e1b953a65121f23747ba2c04e6743885caa6ce1ea12f
		ONNX_LIB_SHA256 := a3504a59b3178c40a972772730a27eda06bf447fb6d4f26c88bf5df81746a167
	else
		# onnxruntime v1.28.1 publishes no Intel-only macOS asset (same as v1.24.1):
		# the download
		# URL below returns a 9-byte "Not Found" body, and the osx-arm64 dylib is
		# arm64-only (not universal2). No digest can be pinned for a nonexistent
		# asset, so verification fails closed with a clear message instead of
		# tar's earlier "Unrecognized archive format".
		ONNX_ARCHIVE := onnxruntime-osx-x86_64-$(ONNX_VERSION).tgz
		ONNX_LIB_NAME := libonnxruntime.$(ONNX_VERSION).dylib
		ONNX_SHA256 :=
		ONNX_LIB_SHA256 :=
	endif
	ONNX_LIB_OUT := libonnxruntime.dylib
else ifeq ($(UNAME_S),Linux)
	ifeq ($(UNAME_M),aarch64)
		ONNX_ARCHIVE := onnxruntime-linux-aarch64-$(ONNX_VERSION).tgz
		ONNX_SHA256 := 53bab9a5c6ae198b7be75663b780c64caa388d30257fac458c71fbff0a82e98e
		ONNX_LIB_SHA256 := 0ce5f75809ba44fdc766b64edf8961014f0ab7ea3686b1be466a61f1474918ac
	else
		ONNX_ARCHIVE := onnxruntime-linux-x64-$(ONNX_VERSION).tgz
		ONNX_SHA256 := 2529aef968d0ad0603365054bc46ebefa7f0fe3bc12f28c5f729c99ddffe2a81
		ONNX_LIB_SHA256 := e38d2cec3d582c41786bdd428865fc017145599666cb7c82410e52496e7e066d
	endif
	ONNX_LIB_NAME := libonnxruntime.so.$(ONNX_VERSION)
	ONNX_LIB_OUT := libonnxruntime.so
else
	# Windows (assumed via wails build): digests are passed to
	# scripts/fetch-onnx.ps1, which performs the verification with Get-FileHash.
	ONNX_ARCHIVE := onnxruntime-win-x64-$(ONNX_VERSION).zip
	ONNX_LIB_NAME := onnxruntime.dll
	ONNX_SHA256 := e46ac7652def5da0e5223372be21185ffff553e0419459f66e0114d460c38162
	ONNX_LIB_SHA256 := ab48e807eb96ad3d399c72e5f67dd93fe9c8b452e051fbf27f72d546e1882f4a
	ONNX_LIB_OUT := onnxruntime.dll
endif

# Go build tags for Wails (Linux requires webkit2_41 for Ubuntu 24.04+)
ifeq ($(UNAME_S),Linux)
	WAILS_TAGS := -tags webkit2_41
else
	WAILS_TAGS :=
endif

# Platform-specific output directories
ifeq ($(UNAME_S),Darwin)
	APP_BUNDLE_DIR := build/bin/c0wrk-desktop.app/Contents/MacOS
	APP_MODELS_DIR := build/bin/c0wrk-desktop.app/Contents/Resources/models
else ifeq ($(UNAME_S),Linux)
	APP_BUNDLE_DIR := build/bin
	APP_MODELS_DIR := build/bin/models
else
	APP_BUNDLE_DIR := build/bin
	APP_MODELS_DIR := build/bin/models
endif

ONNX_URL := https://github.com/microsoft/onnxruntime/releases/download/v$(ONNX_VERSION)/$(ONNX_ARCHIVE)
ONNX_DIR := $(ONNX_ARCHIVE:.tgz=)
# Cache directory for downloaded ONNX library
ONNX_CACHE_DIR := .cache
# Version stamp written next to the installed library. Guards against a stale
# library surviving an ONNX_VERSION bump: the "already installed" short-circuit
# compares this stamp against ONNX_VERSION and refreshes the library on
# mismatch. Mirror the same mechanism in scripts/fetch-onnx.ps1 (Windows).
ONNX_STAMP := $(APP_BUNDLE_DIR)/.onnxruntime-version

# Embedding model configuration
MODELS_CACHE_DIR := .cache/models
EMBEDDING_MODEL_URL := https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/model.onnx
EMBEDDING_TOKENIZER_URL := https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/tokenizer.json
EMBEDDING_MODEL_NAME := jina-v2-small.onnx
EMBEDDING_TOKENIZER_NAME := jina-v2-small-tokenizer.json
# Pinned SHA256 digests of the downloaded files.
# EMBEDDING_MODEL_SHA256 is the official Hugging Face LFS oid, taken from the
# repository's LFS pointer file
# (https://huggingface.co/jinaai/jina-embeddings-v2-small-en/raw/main/model.onnx).
# tokenizer.json is stored as a plain git blob (no LFS oid), so its digest is
# trust-on-first-use, pinned from a fresh download. Mirror both values in
# scripts/fetch-embedding-model.ps1 (used on Windows). On a version/model bump,
# recompute: curl -L <url> | shasum -a 256 (the model digest must equal the
# `oid sha256:` line of the raw pointer file).
EMBEDDING_MODEL_SHA256 := 974fdefe71fc9889258f569132b35acae6278874c8d09dbdf7806d23ad0b4497
EMBEDDING_TOKENIZER_SHA256 := e9f999ac74497843ed9f4303246a8f43d9f100ee8aab8e133667903f447ceb48

# --- SHA256 verification (fail-closed) -------------------------------------------
# sha256sum is standard on Linux; macOS ships the Perl shasum instead. Override
# with `make SHASUM=...` on exotic setups. If neither tool exists the digest
# comes back empty and never matches — verification fails closed.
SHASUM ?= $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo "shasum -a 256")

# Compares the digest of file $(1) against expected hex digest $(2). On mismatch
# (including an unset/empty $(2)) it prints an error, removes the bad file and
# aborts the whole recipe with a non-zero exit — nothing unverified is ever
# installed. Must stay a single shell line: the recipe lines it is expanded into
# run in one shell, and the inner `exit 1` has to terminate that shell.
define verify_sha256
sha_actual=$$($(SHASUM) "$(1)" | awk '{print $$1}'); \
if [ "$$sha_actual" != "$(2)" ]; then \
	echo "ERROR: SHA256 mismatch for $(1)" >&2; \
	echo "  expected: $(2)" >&2; \
	echo "  actual:   $$sha_actual" >&2; \
	if [ -z "$(2)" ]; then echo "  (no digest pinned for this platform/asset - refusing unverified install)" >&2; fi; \
	rm -f "$(1)"; \
	exit 1; \
fi; \
echo "SHA256 verified: $(1)"
endef

# --- Build-time version metadata -------------------------------------------------
# Injected into the binary via linker flags (-X). Each variable honours an
# explicit env/override (e.g. `VERSION=v1.0.0 make build`); otherwise it falls
# back to a git-derived value, then to a safe default when git is unavailable.
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GITCOMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILDDATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_LDFLAGS := -X github.com/v0lka/c0wrk/core/version.Version=$(VERSION) -X github.com/v0lka/c0wrk/core/version.GitCommit=$(GITCOMMIT) -X github.com/v0lka/c0wrk/core/version.BuildDate=$(BUILDDATE)

# Install frontend dependencies
frontend-deps:
	cd frontend && npm install

build: frontend-deps
	wails build $(WAILS_TAGS) -ldflags "$(VERSION_LDFLAGS)"
	$(MAKE) fetch-onnx
	$(MAKE) fetch-embedding-model

test:
	go test ./...
	cd frontend && npm test

lint: fmt-check
	golangci-lint run
	cd frontend && npm run lint

# Go dependency vulnerability gate: fails when a vulnerability from the
# official Go vulnerability database is reachable from this module's code.
# Uses the same pinned govulncheck version as the CI `security` job, so local
# and CI results are identical. Requires network access to fetch vuln.go.dev.
# Windows (no make): go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# golangci-lint's `run` mode skips the formatters section (gofumpt is enabled
# under linters.formatters but only executes via `golangci-lint fmt`), so
# gofmt violations would otherwise pass `make lint` silently. This check
# fails on any file that gofmt would rewrite. Every Go file in the module is
# covered: the package dirs plus the root main.go and internal/.
fmt-check:
	@unformatted=$$(gofmt -l main.go internal core backend desktop 2>/dev/null); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt violations (run gofmt -w on these files):"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

dev-desktop:
	cd frontend && npm run dev

# Download and extract ONNX Runtime library next to the executable.
# Darwin/Linux: bash recipe below. Windows: delegate to scripts/fetch-onnx.ps1
# (uses Expand-Archive; no /tmp, tar, or unzip on the path).
# Every artifact is SHA256-verified before use, fail-closed: a mismatch removes
# the bad file and aborts with a non-zero exit. This covers the downloaded
# archive, the library extracted from it, and the cached copy. The installed
# copy is byte-identical to the cache and is NEVER rewritten in place: an
# install_name_tool-style edit modifies the signed Mach-O after signing and
# invalidates its embedded code signature, so macOS SIGKILLs the process at
# dlopen (CODESIGNING Invalid Page). The library is dlopened by absolute path
# (desktop/startup.go resolveONNXLibPath), so its LC_ID_DYLIB install name is
# never consulted. The installed copy is guarded by a version stamp: an
# ONNX_VERSION bump replaces the stale installed library instead of skipping on
# "already exists".
ifneq ($(filter $(UNAME_S),Darwin Linux),)
fetch-onnx:
	@mkdir -p $(APP_BUNDLE_DIR); \
	if [ -f "$(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT)" ] && [ "$$(cat "$(ONNX_STAMP)" 2>/dev/null)" = "$(ONNX_VERSION)" ]; then \
		echo "ONNX Runtime $(ONNX_VERSION) already installed at $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT)"; \
	elif [ -f "$(ONNX_CACHE_DIR)/$(ONNX_LIB_OUT)" ]; then \
		echo "Using cached ONNX Runtime library..."; \
		$(call verify_sha256,$(ONNX_CACHE_DIR)/$(ONNX_LIB_OUT),$(ONNX_LIB_SHA256)); \
		cp $(ONNX_CACHE_DIR)/$(ONNX_LIB_OUT) $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT); \
		echo "$(ONNX_VERSION)" > $(ONNX_STAMP); \
		echo "ONNX Runtime library installed to $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT)"; \
	else \
		mkdir -p $(ONNX_CACHE_DIR); \
		echo "Downloading ONNX Runtime $(ONNX_VERSION) for $(UNAME_S)/$(UNAME_M)..."; \
		curl -f -L -o /tmp/$(ONNX_ARCHIVE) $(ONNX_URL); \
		$(call verify_sha256,/tmp/$(ONNX_ARCHIVE),$(ONNX_SHA256)); \
		echo "Extracting ONNX Runtime library..."; \
		tar -xzf /tmp/$(ONNX_ARCHIVE) -C /tmp; \
		$(call verify_sha256,/tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME),$(ONNX_LIB_SHA256)); \
		cp /tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME) $(ONNX_CACHE_DIR)/$(ONNX_LIB_OUT); \
		cp /tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME) $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT); \
		echo "$(ONNX_VERSION)" > $(ONNX_STAMP); \
		rm -rf /tmp/$(ONNX_ARCHIVE) /tmp/$(ONNX_DIR); \
		echo "ONNX Runtime library installed to $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT)"; \
	fi
else
fetch-onnx:
	@powershell -ExecutionPolicy Bypass -File scripts/fetch-onnx.ps1 -Version $(ONNX_VERSION) -OutputDir $(APP_BUNDLE_DIR) -CacheDir $(ONNX_CACHE_DIR) -ArchiveSha256 $(ONNX_SHA256) -LibSha256 $(ONNX_LIB_SHA256)
endif

# Download embedding model and tokenizer next to the executable.
# Darwin/Linux: bash recipe below. Windows: delegate to
# scripts/fetch-embedding-model.ps1 (Invoke-WebRequest; no /tmp/tar on the path).
# All copies (installed, cached, freshly downloaded) are SHA256-verified
# fail-closed: a mismatch removes the bad file and aborts with a non-zero exit.
ifneq ($(filter $(UNAME_S),Darwin Linux),)
fetch-embedding-model:
	@mkdir -p $(APP_MODELS_DIR); \
	if [ -f "$(APP_MODELS_DIR)/$(EMBEDDING_MODEL_NAME)" ] && [ -f "$(APP_MODELS_DIR)/$(EMBEDDING_TOKENIZER_NAME)" ]; then \
		echo "Embedding model already exists at $(APP_MODELS_DIR)/"; \
		$(call verify_sha256,$(APP_MODELS_DIR)/$(EMBEDDING_MODEL_NAME),$(EMBEDDING_MODEL_SHA256)); \
		$(call verify_sha256,$(APP_MODELS_DIR)/$(EMBEDDING_TOKENIZER_NAME),$(EMBEDDING_TOKENIZER_SHA256)); \
	elif [ -f "$(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME)" ] && [ -f "$(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME)" ]; then \
		echo "Using cached embedding model..."; \
		$(call verify_sha256,$(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME),$(EMBEDDING_MODEL_SHA256)); \
		$(call verify_sha256,$(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME),$(EMBEDDING_TOKENIZER_SHA256)); \
		cp $(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME) $(APP_MODELS_DIR)/$(EMBEDDING_MODEL_NAME); \
		cp $(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME) $(APP_MODELS_DIR)/$(EMBEDDING_TOKENIZER_NAME); \
		echo "Embedding model installed to $(APP_MODELS_DIR)/"; \
	else \
		mkdir -p $(MODELS_CACHE_DIR); \
		if [ ! -f "$(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME)" ]; then \
			echo "Downloading embedding model..."; \
			curl -f -L -o $(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME) $(EMBEDDING_MODEL_URL); \
		fi; \
		$(call verify_sha256,$(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME),$(EMBEDDING_MODEL_SHA256)); \
		if [ ! -f "$(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME)" ]; then \
			echo "Downloading tokenizer..."; \
			curl -f -L -o $(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME) $(EMBEDDING_TOKENIZER_URL); \
		fi; \
		$(call verify_sha256,$(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME),$(EMBEDDING_TOKENIZER_SHA256)); \
		cp $(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME) $(APP_MODELS_DIR)/$(EMBEDDING_MODEL_NAME); \
		cp $(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME) $(APP_MODELS_DIR)/$(EMBEDDING_TOKENIZER_NAME); \
		echo "Embedding model installed to $(APP_MODELS_DIR)/"; \
	fi
else
fetch-embedding-model:
	@powershell -ExecutionPolicy Bypass -File scripts/fetch-embedding-model.ps1 -OutputDir $(APP_MODELS_DIR) -CacheDir $(MODELS_CACHE_DIR) -ModelUrl $(EMBEDDING_MODEL_URL) -TokenizerUrl $(EMBEDDING_TOKENIZER_URL) -ModelName $(EMBEDDING_MODEL_NAME) -TokenizerName $(EMBEDDING_TOKENIZER_NAME) -ModelSha256 $(EMBEDDING_MODEL_SHA256) -TokenizerSha256 $(EMBEDDING_TOKENIZER_SHA256)
endif

# Remove downloaded ONNX Runtime files
clean-onnx:
	@rm -f $(APP_BUNDLE_DIR)/libonnxruntime.dylib
	@rm -f $(APP_BUNDLE_DIR)/libonnxruntime.so
	@rm -f $(APP_BUNDLE_DIR)/onnxruntime.dll
	@rm -f $(ONNX_CACHE_DIR)/libonnxruntime.dylib
	@rm -f $(ONNX_CACHE_DIR)/libonnxruntime.so
	@rm -f $(ONNX_CACHE_DIR)/onnxruntime.dll
	@echo "ONNX Runtime library removed from $(APP_BUNDLE_DIR)/ and $(ONNX_CACHE_DIR)/"

clean:
	rm -rf build/bin .cache frontend/dist
	@echo "All build artifacts removed"

# --- sp4rk dependency ------------------------------------------------------------

# sp4rk SDK module path and its public VCS remote. The remote is queried
# directly (git ls-remote) to discover the latest commit WITHOUT requiring a
# local checkout — the sibling ../sp4rk clone is optional and may be absent.
# Override the remote for a fork:
#   make bump SP4RK_REMOTE=https://github.com/yourfork/sp4rk
SP4RK_MODULE := github.com/v0lka/sp4rk
SP4RK_REMOTE ?= https://github.com/v0lka/sp4rk

# Bump the sp4rk dependency pseudo-version to the latest commit of its remote
# repository. The commit is resolved directly from the remote (git ls-remote
# HEAD), so NO local checkout is needed. GOWORK=off forces resolution from the
# module source (proxy/VCS) instead of the local workspace replacement defined
# in the local go.work (repo root, gitignored).
bump:
	@set -eu; \
	COMMIT=$$(git ls-remote $(SP4RK_REMOTE) HEAD | awk '{print $$1}'); \
	if [ -z "$$COMMIT" ]; then \
		echo "bump: could not resolve HEAD from $(SP4RK_REMOTE)" >&2; \
		echo "      check network access or override: make bump SP4RK_REMOTE=<url>" >&2; \
		exit 1; \
	fi; \
	echo "Bumping $(SP4RK_MODULE) to $$COMMIT ($(SP4RK_REMOTE) HEAD) ..."; \
	GOWORK=off go get $(SP4RK_MODULE)@$$COMMIT; \
	GOWORK=off go mod tidy
