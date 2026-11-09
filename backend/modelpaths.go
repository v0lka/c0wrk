package backend

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/v0lka/c0wrk/backend/config"
)

// resolveModelPath checks the app bundle Resources/models/ first, then <agentDir>/models/.
// Returns empty string if the file is not found in either location.
func resolveModelPath(filename, agentDir string) string {
	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		// macOS app bundle: <app>/Contents/MacOS/../Resources/models/<filename>
		bundlePath := filepath.Join(exeDir, "..", "Resources", "models", filename)
		if _, statErr := os.Stat(bundlePath); statErr == nil {
			return bundlePath
		}

		// Flat layout (Linux/Windows): models/ next to the binary
		flatPath := filepath.Join(exeDir, "models", filename)
		if _, statErr := os.Stat(flatPath); statErr == nil {
			return flatPath
		}
	}

	// Fallback: <agentDir>/models/<filename>
	userPath := filepath.Join(config.ModelsDir(agentDir), filename)
	if _, statErr := os.Stat(userPath); statErr == nil {
		return userPath
	}

	return ""
}

// resolveONNXLibPath finds the ONNX Runtime shared library next to the executable.
func resolveONNXLibPath() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	exeDir := filepath.Dir(exePath)

	var libName string
	switch runtime.GOOS {
	case "darwin":
		libName = "libonnxruntime.dylib"
	case "linux":
		libName = "libonnxruntime.so"
	case "windows":
		libName = "onnxruntime.dll"
	default:
		return ""
	}

	libPath := filepath.Join(exeDir, libName)
	if _, statErr := os.Stat(libPath); statErr == nil {
		return libPath
	}
	return ""
}
