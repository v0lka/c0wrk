.PHONY: build test lint dev-desktop fetch-onnx clean-onnx

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
BUILD_BIN_DIR := bin

build:
	wails build

test:
	go test ./...

lint:
	golangci-lint run

dev-desktop:
	cd frontend && npm run dev

# Download and extract ONNX Runtime library to bin/ directory
fetch-onnx:
	@mkdir -p $(BUILD_BIN_DIR)
	@if [ -f "$(BUILD_BIN_DIR)/$(ONNX_LIB_OUT)" ]; then \
		echo "ONNX Runtime library already exists at $(BUILD_BIN_DIR)/$(ONNX_LIB_OUT)"; \
		exit 0; \
	fi
	@echo "Downloading ONNX Runtime $(ONNX_VERSION) for $(UNAME_S)/$(UNAME_M)..."
	@curl -L -o /tmp/$(ONNX_ARCHIVE) $(ONNX_URL)
	@echo "Extracting ONNX Runtime library..."
	@if [ "$(UNAME_S)" = "Darwin" ] || [ "$(UNAME_S)" = "Linux" ]; then \
		tar -xzf /tmp/$(ONNX_ARCHIVE) -C /tmp; \
		cp /tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME) $(BUILD_BIN_DIR)/$(ONNX_LIB_OUT); \
	else \
		unzip -o /tmp/$(ONNX_ARCHIVE) -d /tmp; \
		cp /tmp/$(ONNX_DIR)/lib/$(ONNX_LIB_NAME) $(BUILD_BIN_DIR)/$(ONNX_LIB_OUT); \
	fi
	@rm -rf /tmp/$(ONNX_ARCHIVE) /tmp/$(ONNX_DIR)
	@echo "ONNX Runtime library installed to $(BUILD_BIN_DIR)/$(ONNX_LIB_OUT)"

# Remove downloaded ONNX Runtime files
clean-onnx:
	@rm -f $(BUILD_BIN_DIR)/libonnxruntime.dylib
	@rm -f $(BUILD_BIN_DIR)/libonnxruntime.so
	@rm -f $(BUILD_BIN_DIR)/onnxruntime.dll
	@echo "ONNX Runtime library removed from $(BUILD_BIN_DIR)/"
