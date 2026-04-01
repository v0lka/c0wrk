package config

// ApplyDefaults sets default values for zero-value fields in the configuration.
func ApplyDefaults(cfg *Config) {
	// Log level defaults
	if cfg.LogLevel == "" {
		cfg.LogLevel = "DEBUG"
	}

	// Theme defaults
	if cfg.Theme == "" {
		cfg.Theme = "system"
	}

	// LLM defaults
	if cfg.LLM.Defaults.MaxTokens == 0 {
		cfg.LLM.Defaults.MaxTokens = 4096
	}
	// Temperature 0.0 is a valid value, but it's also the zero value,
	// so we only set it if not explicitly configured (handled by YAML unmarshaling)

	// Executor defaults
	if cfg.Executor.MaxReactSteps == 0 {
		cfg.Executor.MaxReactSteps = 30
	}
	if cfg.Executor.MaxRetries == 0 {
		cfg.Executor.MaxRetries = 3
	}
	if cfg.Executor.OutputTokenReserve == 0 {
		cfg.Executor.OutputTokenReserve = 4096
	}

	// Compaction defaults
	if cfg.Executor.Compaction.SlidingWindow.KeepFirst == 0 {
		cfg.Executor.Compaction.SlidingWindow.KeepFirst = 3
	}
	if cfg.Executor.Compaction.SlidingWindow.KeepLast == 0 {
		cfg.Executor.Compaction.SlidingWindow.KeepLast = 10
	}
	if cfg.Executor.Compaction.Summarization.BlockSize == 0 {
		cfg.Executor.Compaction.Summarization.BlockSize = 7
	}
	if cfg.Executor.Compaction.Hierarchical.EnabledAboveSteps == 0 {
		cfg.Executor.Compaction.Hierarchical.EnabledAboveSteps = 40
	}

	// Compaction thresholds defaults
	if cfg.Executor.Compaction.Thresholds.PredictivePercent == 0 {
		cfg.Executor.Compaction.Thresholds.PredictivePercent = 85
	}
	if cfg.Executor.Compaction.Thresholds.WarningPercent == 0 {
		cfg.Executor.Compaction.Thresholds.WarningPercent = 92
	}
	if cfg.Executor.Compaction.Thresholds.EmergencyPercent == 0 {
		cfg.Executor.Compaction.Thresholds.EmergencyPercent = 98
	}

	// Models defaults (initialize empty map if nil)
	if cfg.LLM.Models == nil {
		cfg.LLM.Models = make(map[string]ModelOverride)
	}

	// Memory defaults
	if cfg.Memory.Episodic.RetentionDays == 0 {
		cfg.Memory.Episodic.RetentionDays = 90
	}
	if cfg.Memory.Episodic.RetrievalLimit == 0 {
		cfg.Memory.Episodic.RetrievalLimit = 5
	}
	if cfg.Memory.Constitution.UpdateIntervalSessions == 0 {
		cfg.Memory.Constitution.UpdateIntervalSessions = 10
	}

	// Router defaults
	if cfg.Router.HistoryWindow == 0 {
		cfg.Router.HistoryWindow = 10
	}

	// Security defaults
	if cfg.Security.Judge.Enabled == nil {
		v := true
		cfg.Security.Judge.Enabled = &v
	}
	if cfg.Security.DefaultPolicy == "" {
		cfg.Security.DefaultPolicy = "auto"
	}
	if cfg.Security.ToolPolicies == nil {
		cfg.Security.ToolPolicies = make(map[string]ToolPolicyConfig)
	}
	// Default tool policies
	if _, ok := cfg.Security.ToolPolicies["bash_exec"]; !ok {
		cfg.Security.ToolPolicies["bash_exec"] = ToolPolicyConfig{
			Policy: "user_confirm",
			Blacklist: []string{
				`rm\s+-rf\s+/`,
				`sudo\s+`,
				`mkfs`,
				`dd\s+if=`,
				`>\s*/dev/`,
			},
		}
	}
	if _, ok := cfg.Security.ToolPolicies["file_ops"]; !ok {
		cfg.Security.ToolPolicies["file_ops"] = ToolPolicyConfig{
			Policy: "auto",
		}
	}
	if _, ok := cfg.Security.ToolPolicies["web_search"]; !ok {
		cfg.Security.ToolPolicies["web_search"] = ToolPolicyConfig{
			Policy: "always_allow",
		}
	}
	if _, ok := cfg.Security.ToolPolicies["web_fetch"]; !ok {
		cfg.Security.ToolPolicies["web_fetch"] = ToolPolicyConfig{
			Policy: "always_allow",
		}
	}

	// Docker/Skills defaults
	if cfg.Skills.Docker.WarmPoolThreshold == 0 {
		cfg.Skills.Docker.WarmPoolThreshold = 5
	}
	if cfg.Skills.Docker.WarmPoolIdleTimeout == "" {
		cfg.Skills.Docker.WarmPoolIdleTimeout = "60s"
	}
	if cfg.Skills.Docker.DefaultMemory == "" {
		cfg.Skills.Docker.DefaultMemory = "256m"
	}
	if cfg.Skills.Docker.DefaultCPU == "" {
		cfg.Skills.Docker.DefaultCPU = "0.5"
	}
	if cfg.Skills.Docker.DefaultTimeout == "" {
		cfg.Skills.Docker.DefaultTimeout = "30s"
	}

	// Provider defaults: set default base URL for local providers
	for name, prov := range cfg.LLM.Providers {
		if prov.BaseURL == "" {
			switch prov.Type {
			case "lmstudio":
				cfg.LLM.Providers[name] = ProviderConfig{
					Type:      prov.Type,
					APIKey:    prov.APIKey,
					BaseURL:   "http://localhost:1234",
					ProjectID: prov.ProjectID,
					Location:  prov.Location,
					Model:     prov.Model,
				}
			}
		}
	}
}
