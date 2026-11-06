package providerauth

import (
	"errors"
	"os"
	"runtime"
	"strings"

	"github.com/google/uuid"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/version"
)

func deviceHeaders(agentDir string) (map[string]string, error) {
	if agentDir == "" {
		return nil, errors.New("providerauth: AgentDir is required for device headers")
	}
	deviceID, err := stableDeviceID(agentDir)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	return map[string]string{
		"X-Msh-Platform":     "c0wrk",
		"X-Msh-Version":      version.Version,
		"X-Msh-Device-Name":  asciiHeader(host),
		"X-Msh-Device-Model": runtime.GOOS + " " + runtime.GOARCH,
		"X-Msh-Os-Version":   runtime.GOOS + "/" + runtime.GOARCH,
		"X-Msh-Device-Id":    deviceID,
	}, nil
}

func stableDeviceID(agentDir string) (string, error) {
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return "", err
	}
	path := config.DeviceIDPath(agentDir)
	if id, err := readDeviceID(path); err == nil {
		return id, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	id := uuid.NewString()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return readDeviceID(path)
	}
	if err != nil {
		return "", err
	}
	if _, err := file.WriteString(id + "\n"); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return id, nil
}

func readDeviceID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id, err := uuid.Parse(strings.TrimSpace(string(data)))
	if err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return id.String(), nil
}

func asciiHeader(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 0x20 && r <= 0x7e {
			b.WriteRune(r)
		}
	}
	if value := strings.TrimSpace(b.String()); value != "" {
		return value
	}
	return "unknown"
}
