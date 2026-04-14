package config

import (
	"log/slog"
	"os"
	"path/filepath"
)

// ResolvedConfig contains the result of config path resolution and loading.
type ResolvedConfig struct {
	Config         *Config
	ConfigPath     string
	AgentDir       string
	Migrated       bool
	MigrationMsg   string
	LoadErrors     []string
}

// ResolveAndLoad determines the config file path, creates the agent directory,
// loads the config (with fallback), and returns the resolved result.
// It follows the convention: prefer ~/.c0wrk/config.yaml, fallback to ./config.yaml.
// On total failure it returns a default config with load errors populated.
func ResolveAndLoad(log *slog.Logger) *ResolvedConfig {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Error("failed to get user home directory", "error", err)
		homeDir = "."
	}
	agentDir := filepath.Join(homeDir, DefaultAgentDir)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		log.Error("failed to create agent directory", "error", err)
	}

	configPath := filepath.Join(agentDir, "config.yaml")
	result, err := LoadWithResult(configPath)
	if err != nil {
		// Fallback to local config.yaml if present
		fallbackPath := "config.yaml"
		if _, statErr := os.Stat(fallbackPath); statErr == nil {
			result, err = LoadWithResult(fallbackPath)
			if err == nil {
				configPath = fallbackPath
			}
		}
	}

	resolved := &ResolvedConfig{
		ConfigPath: configPath,
		AgentDir:   agentDir,
	}

	if err != nil || result == nil {
		log.Error("failed to load config", "error", err)
		cfg := &Config{}
		ApplyDefaults(cfg)
		log.Warn("config load failed, check your config.yaml syntax")
		errMsg := "Failed to load config"
		if err != nil {
			errMsg += ": " + err.Error()
		}
		resolved.Config = cfg
		resolved.LoadErrors = []string{errMsg}
	} else {
		resolved.Config = result.Config
		resolved.Migrated = result.Migrated
		resolved.MigrationMsg = result.MigrationMsg
		resolved.LoadErrors = result.LoadErrors
		if result.Migrated {
			log.Info("config migrated", "message", result.MigrationMsg)
		}
		if len(result.LoadErrors) > 0 {
			for _, e := range result.LoadErrors {
				log.Warn("config warning", "error", e)
			}
		}
	}

	return resolved
}
