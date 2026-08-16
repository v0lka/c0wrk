.PHONY: build test lint fmt-check dev-desktop bump fetch-onnx fetch-embedding-model clean-onnx clean frontend-deps

# ONNX Runtime version
ONNX_VERSION := 1.24.1

# Detect OS and architecture
UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)

# Platform-specific configuration
ifeq ($(UNAME_S),Darwin)
	ifeq ($(UNAME_M),arm64)
		ONNX_ARCHIVE := onnxruntime-osx-arm64-$(ONNX_VERSION).tgz
		ONNX_LIB_NAME := libonnxruntime.$(ONNX_VERSION).dylib
	else
		ONNX_ARCHIVE := onnxruntime-osx-x86_64-$(ONNX_VERSION).tgz
		ONNX_LIB_NAME := libonnxruntime.$(ONNX_VERSION).dylib
	endif
	ONNX_LIB_OUT := libonnxruntime.dylib
else ifeq ($(UNAME_S),Linux)
	ifeq ($(UNAME_M),aarch64)
		ONNX_ARCHIVE := onnxruntime-linux-aarch64-$(ONNX_VERSION).tgz
	else
		ONNX_ARCHIVE := onnxruntime-linux-x64-$(ONNX_VERSION).tgz
	endif
	ONNX_LIB_NAME := libonnxruntime.so.$(ONNX_VERSION)
	ONNX_LIB_OUT := libonnxruntime.so
else
	# Windows (assumed via wails build)
	ONNX_ARCHIVE := onnxruntime-win-x64-$(ONNX_VERSION).zip
	ONNX_LIB_NAME := onnxruntime.dll
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

# Embedding model configuration
MODELS_CACHE_DIR := .cache/models
EMBEDDING_MODEL_URL := https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/model.onnx
EMBEDDING_TOKENIZER_URL := https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/tokenizer.json
EMBEDDING_MODEL_NAME := jina-v2-small.onnx
EMBEDDING_TOKENIZER_NAME := jina-v2-small-tokenizer.json

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
# (uses Expand-Archive; no /tmp, tar, unzip, or install_name_tool on the path).
ifneq ($(filter $(UNAME_S),Darwin Linux),)
fetch-onnx:
	@mkdir -p $(APP_BUNDLE_DIR); \
	if [ -f "$(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT)" ]; then \
		echo "ONNX Runtime library already exists at $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT)"; \
	elif [ -f "$(ONNX_CACHE_DIR)/$(ONNX_LIB_OUT)" ]; then \
		echo "Using cached ONNX Runtime library..."; \
		cp $(ONNX_CACHE_DIR)/$(ONNX_LIB_OUT) $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT); \
		if [ "$(UNAME_S)" = "Darwin" ]; then install_name_tool -id @loader_path/libonnxruntime.dylib $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT); fi; \
		echo "ONNX Runtime library installed to $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT)"; \
	else \
		mkdir -p $(ONNX_CACHE_DIR); \
		echo "Downloading ONNX Runtime $(ONNX_VERSION) for $(UNAME_S)/$(UNAME_M)..."; \
		curl -L -o /tmp/$(ONNX_ARCHIVE) $(ONNX_URL); \
		echo "Extracting ONNX Runtime library..."; \
		if [ "$(UNAME_S)" = "Darwin" ] || [ "$(UNAME_S)" = "Linux" ]; then \
			tar -xzf /tmp/$(ONNX_ARCHIVE) -C /tmp; \
			cp /tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME) $(ONNX_CACHE_DIR)/$(ONNX_LIB_OUT); \
			cp /tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME) $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT); \
			if [ "$(UNAME_S)" = "Darwin" ]; then \
				install_name_tool -id @loader_path/libonnxruntime.dylib $(ONNX_CACHE_DIR)/$(ONNX_LIB_OUT); \
				install_name_tool -id @loader_path/libonnxruntime.dylib $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT); \
			fi; \
		else \
			unzip -o /tmp/$(ONNX_ARCHIVE) -d /tmp; \
			cp /tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME) $(ONNX_CACHE_DIR)/$(ONNX_LIB_OUT); \
			cp /tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME) $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT); \
		fi; \
		rm -rf /tmp/$(ONNX_ARCHIVE) /tmp/$(ONNX_DIR); \
		echo "ONNX Runtime library installed to $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT)"; \
	fi
else
fetch-onnx:
	@powershell -ExecutionPolicy Bypass -File scripts/fetch-onnx.ps1 -Version $(ONNX_VERSION) -OutputDir $(APP_BUNDLE_DIR) -CacheDir $(ONNX_CACHE_DIR)
endif

# Download embedding model and tokenizer next to the executable.
# Darwin/Linux: bash recipe below. Windows: delegate to
# scripts/fetch-embedding-model.ps1 (Invoke-WebRequest; no /tmp/tar on the path).
ifneq ($(filter $(UNAME_S),Darwin Linux),)
fetch-embedding-model:
	@mkdir -p $(APP_MODELS_DIR); \
	if [ -f "$(APP_MODELS_DIR)/$(EMBEDDING_MODEL_NAME)" ] && [ -f "$(APP_MODELS_DIR)/$(EMBEDDING_TOKENIZER_NAME)" ]; then \
		echo "Embedding model already exists at $(APP_MODELS_DIR)/"; \
	elif [ -f "$(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME)" ] && [ -f "$(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME)" ]; then \
		echo "Using cached embedding model..."; \
		cp $(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME) $(APP_MODELS_DIR)/$(EMBEDDING_MODEL_NAME); \
		cp $(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME) $(APP_MODELS_DIR)/$(EMBEDDING_TOKENIZER_NAME); \
		echo "Embedding model installed to $(APP_MODELS_DIR)/"; \
	else \
		mkdir -p $(MODELS_CACHE_DIR); \
		echo "Downloading embedding model..."; \
		curl -L -o $(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME) $(EMBEDDING_MODEL_URL); \
		echo "Downloading tokenizer..."; \
		curl -L -o $(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME) $(EMBEDDING_TOKENIZER_URL); \
		cp $(MODELS_CACHE_DIR)/$(EMBEDDING_MODEL_NAME) $(APP_MODELS_DIR)/$(EMBEDDING_MODEL_NAME); \
		cp $(MODELS_CACHE_DIR)/$(EMBEDDING_TOKENIZER_NAME) $(APP_MODELS_DIR)/$(EMBEDDING_TOKENIZER_NAME); \
		echo "Embedding model installed to $(APP_MODELS_DIR)/"; \
	fi
else
fetch-embedding-model:
	@powershell -ExecutionPolicy Bypass -File scripts/fetch-embedding-model.ps1 -OutputDir $(APP_MODELS_DIR) -CacheDir $(MODELS_CACHE_DIR) -ModelUrl $(EMBEDDING_MODEL_URL) -TokenizerUrl $(EMBEDDING_TOKENIZER_URL) -ModelName $(EMBEDDING_MODEL_NAME) -TokenizerName $(EMBEDDING_TOKENIZER_NAME)
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
# in the parent go.work.
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
