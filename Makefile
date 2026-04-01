.PHONY: build test lint dev-desktop fetch-onnx clean-onnx clean

# ONNX Runtime version
ONNX_VERSION := 1.21.0

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

build:
	wails build
	$(MAKE) fetch-onnx

test:
	go test ./...

lint:
	golangci-lint run

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
		else \
			unzip -o /tmp/$(ONNX_ARCHIVE) -d /tmp; \
			cp /tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME) $(ONNX_CACHE_DIR)/$(ONNX_LIB_OUT); \
			cp /tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME) $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT); \
		fi; \
		rm -rf /tmp/$(ONNX_ARCHIVE) /tmp/$(ONNX_DIR); \
		echo "ONNX Runtime library installed to $(APP_BUNDLE_DIR)/$(ONNX_LIB_OUT)"; \
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
