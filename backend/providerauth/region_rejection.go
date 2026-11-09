package providerauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/v0lka/c0wrk/backend/config"
)

type regionRejection struct {
	Region string `json:"region"`
}

func regionRejectionPath(agentDir string) (string, error) {
	if agentDir == "" {
		return "", errors.New("providerauth: AgentDir is required for region rejection")
	}
	return config.KimiAuthRegionRejectionPath(agentDir), nil
}

func writeRegionRejection(agentDir, region string) error {
	path, err := regionRejectionPath(agentDir)
	if err != nil {
		return err
	}
	data, err := json.Marshal(regionRejection{Region: region})
	if err != nil {
		return fmt.Errorf("providerauth: encoding region rejection: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("providerauth: persisting region rejection: %w", err)
	}
	return nil
}

func readRegionRejection(agentDir string) (region string, rejected bool, err error) {
	path, err := regionRejectionPath(agentDir)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("providerauth: reading region rejection: %w", err)
	}
	var rejection regionRejection
	if err := json.Unmarshal(data, &rejection); err != nil {
		return "", false, fmt.Errorf("providerauth: decoding region rejection: %w", err)
	}
	if rejection.Region == "" {
		return "", false, errors.New("providerauth: region rejection carries no region")
	}
	return rejection.Region, true, nil
}

func clearRegionRejection(agentDir string) error {
	path, err := regionRejectionPath(agentDir)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("providerauth: clearing region rejection: %w", err)
	}
	return nil
}
