.PHONY: build test lint dev-desktop fetch-onnx fetch-embedding-model clean-onnx clean frontend-deps

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

ONNX_URL := https://github.com/microsoft/onnxruntime/releases/download/v$(ONNX_VERSION)/$(ONNX_ARCHIVE)
ONNX_DIR := $(ONNX_ARCHIVE:.tgz=)
# Cache directory for downloaded ONNX library
ONNX_CACHE_DIR := .cache
# Target directory inside the .app bundle
APP_BUNDLE_DIR := build/bin/c0wrk-desktop.app/Contents/MacOS

# Embedding model configuration
MODELS_CACHE_DIR := .cache/models
APP_MODELS_DIR := build/bin/c0wrk-desktop.app/Contents/Resources/models
EMBEDDING_MODEL_URL := https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/model.onnx
EMBEDDING_TOKENIZER_URL := https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/tokenizer.json
EMBEDDING_MODEL_NAME := jina-v2-small.onnx
EMBEDDING_TOKENIZER_NAME := jina-v2-small-tokenizer.json

# Install frontend dependencies
frontend-deps:
	cd frontend && npm install

build: frontend-deps
	wails build
	$(MAKE) fetch-onnx
	$(MAKE) fetch-embedding-model

test:
	go test ./...
	cd frontend && npm test

lint:
	golangci-lint run
	cd frontend && npm run lint

dev-desktop:
	cd frontend && npm run dev

# Download and extract ONNX Runtime library to the .app bundle
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

# Download embedding model and tokenizer to the .app bundle
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
