package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTerminalConfigParsing(t *testing.T) {
	src := `
terminal:
  env:
    TERM_PROGRAM: c0wrk
    C0WRK_TERMINAL: "1"
    MY_FLAG: ${MY_ENV_FLAG}
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}

	if len(cfg.Terminal.Env) != 3 {
		t.Fatalf("terminal.env has %d entries, want 3: %v", len(cfg.Terminal.Env), cfg.Terminal.Env)
	}
	if got := cfg.Terminal.Env["TERM_PROGRAM"]; got != "c0wrk" {
		t.Errorf("terminal.env[TERM_PROGRAM] = %q, want c0wrk", got)
	}
	if got := cfg.Terminal.Env["C0WRK_TERMINAL"]; got != "1" {
		t.Errorf("terminal.env[C0WRK_TERMINAL] = %q, want 1", got)
	}
	// ${VAR} references are preserved raw in the struct (config convention)
	// and resolved later via ExpandEnvVars.
	if got := cfg.Terminal.Env["MY_FLAG"]; got != "${MY_ENV_FLAG}" {
		t.Errorf("terminal.env[MY_FLAG] = %q, want raw ${MY_ENV_FLAG}", got)
	}
}

func TestTerminalConfigExpandEnvVars(t *testing.T) {
	t.Setenv("MY_ENV_FLAG", "expanded-value")

	var cfg Config
	src := `
terminal:
  env:
    MY_FLAG: ${MY_ENV_FLAG}
`
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}

	if got := ExpandEnvVars(cfg.Terminal.Env["MY_FLAG"]); got != "expanded-value" {
		t.Errorf("ExpandEnvVars() = %q, want expanded-value", got)
	}
}

func TestTerminalConfigOmitted(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("log_level: debug\n"), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}
	if len(cfg.Terminal.Env) != 0 {
		t.Errorf("terminal.env = %v, want empty for omitted terminal section", cfg.Terminal.Env)
	}
}
