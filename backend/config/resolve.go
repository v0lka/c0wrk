package config

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/v0lka/sp4rk/safeio"
)

// ResolvedConfig contains the result of config path resolution and loading.
type ResolvedConfig struct {
	Config     *Config
	ConfigPath string
	AgentDir   string
	LoadErrors []string
}

// CreateDefault builds a Config with all defaults applied and saves it to path.
// The resulting file can be used as a starting point; the user should fill in
// provider-specific fields (API keys, models) before the next launch.
func CreateDefault(path string) (*Config, error) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if err := Save(cfg, path); err != nil {
		return cfg, fmt.Errorf("failed to create default config: %w", err)
	}
	return cfg, nil
}

// ResolveAndLoad determines the config file path, creates the agent directory,
// loads the config, and returns the resolved result.
// The only accepted document is the primary ~/.c0wrk/config.yaml: a missing
// file is created from defaults; a broken file surfaces its load errors and
// keeps the parsed values when only validation failed. There is deliberately
// NO fallback to ./config.yaml in the process working directory — a
// directory-planted file would otherwise be adopted silently (MCP commands to
// spawn, security policies, provider endpoints) whenever the primary file is
// absent or broken. On total failure it returns a default config with load
// errors populated.
func ResolveAndLoad(log *slog.Logger) *ResolvedConfig {
	agentDir := AgentDir()
	// The agent-data root must be usable as a real directory tree:
	// MkdirAllReal only ever creates real directories — a dangling link, a
	// non-directory component, or a link swapped into a created component is
	// refused loudly (log + load error) and no config is created or loaded
	// through a redirected tree — while an operator-symlinked ~/.c0wrk
	// resolves, keeping every derived path (the config with plaintext
	// provider keys, the database, projects, logs, themes) inside the
	// operator's chosen target.
	if err := safeio.MkdirAllReal(agentDir, 0o755); err != nil {
		log.Error("agent directory is not a real directory — refusing to create or load any config through it",
			"dir", agentDir, "error", err)
		cfg := &Config{}
		ApplyDefaults(cfg)
		return &ResolvedConfig{
			Config:     cfg,
			ConfigPath: ConfigPath(agentDir),
			AgentDir:   agentDir,
			LoadErrors: []string{"Agent directory " + agentDir + " is not a real directory (" + err.Error() + "); no config was created or loaded through it"},
		}
	}

	configPath := ConfigPath(agentDir)

	// If the primary config file does not exist, create a default config at
	// the primary path.
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		log.Info("no config file found, creating default config", "path", configPath)
		cfg, createErr := CreateDefault(configPath)
		resolved := &ResolvedConfig{
			Config:     cfg,
			ConfigPath: configPath,
			AgentDir:   agentDir,
		}
		if createErr != nil {
			log.Error("failed to create default config file", "error", createErr)
			resolved.LoadErrors = []string{createErr.Error()}
		}
		return resolved
	}

	result, err := LoadWithResult(configPath)

	resolved := &ResolvedConfig{
		ConfigPath: configPath,
		AgentDir:   agentDir,
	}

	if err != nil || result == nil {
		if err != nil {
			log.Error("failed to load config", "error", err)
		} else {
			log.Error("failed to load config: result is nil")
		}
		if result != nil && result.Config != nil {
			// The document parsed but failed validate(). Keep the PARSED
			// config: discarding it for defaults would silently reset every
			// operator setting — providers and API keys, the security-group
			// policies and the execute blocklist, trusted repos — and the
			// next save would persist those defaults over config.yaml,
			// making the loss permanent. The load errors below surface the
			// specific validation failure; the app keeps running on the
			// operator's real values until the file is fixed.
			log.Warn("config validation failed; keeping the parsed configuration so operator settings are not reset — fix the reported problem in config.yaml")
			resolved.Config = result.Config
			resolved.LoadErrors = result.LoadErrors
			for _, e := range result.LoadErrors {
				log.Warn("config warning", "error", e)
			}
		} else {
			cfg := &Config{}
			ApplyDefaults(cfg)
			log.Warn("config load failed, check your config.yaml syntax")
			errMsg := "Failed to load config"
			if err != nil {
				errMsg += ": " + err.Error()
			}
			resolved.Config = cfg
			resolved.LoadErrors = []string{errMsg}
		}
	} else {
		resolved.Config = result.Config
		resolved.LoadErrors = result.LoadErrors
		if len(result.LoadErrors) > 0 {
			for _, e := range result.LoadErrors {
				log.Warn("config warning", "error", e)
			}
		}
	}

	// Surface model-profile profile resolution problems through the same
	// load-warnings channel the UI displays (configLoadErrors): a broken
	// custom-profile store or a stale model_profiles.active_profile must never fail the
	// run, but the operator should still see the soft fallback to "generic".
	modelProfilesCatalog, modelProfilesWarnings := LoadModelProfilesCatalog(agentDir)
	_, modelProfilesResolveWarnings := ResolveModelProfilesConfig(resolved.Config.ModelProfiles, modelProfilesCatalog)
	modelProfilesWarnings = append(modelProfilesWarnings, modelProfilesResolveWarnings...)
	for _, w := range modelProfilesWarnings {
		log.Warn("model-profile profile warning", "warning", w)
		resolved.LoadErrors = append(resolved.LoadErrors, w)
	}

	return resolved
}
