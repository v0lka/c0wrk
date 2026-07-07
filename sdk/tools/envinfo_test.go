package tools

import (
	"context"
	"strings"
	"testing"
)

func TestCollectEnvInfo(t *testing.T) {
	info := CollectEnvInfo()

	if info.OS == "" {
		t.Error("OS should not be empty")
	}
	if info.Arch == "" {
		t.Error("Arch should not be empty")
	}
	t.Logf("Collected: OS=%q Arch=%q Shell=%q Home=%q Go=%q Node=%q Python=%q",
		info.OS, info.Arch, info.Shell, info.HomeDir, info.GoVersion, info.NodeVersion, info.PythonVersion)
}

func TestEnvInfoContext(t *testing.T) {
	// Bare context returns nil.
	ctx := context.Background()
	if got := EnvInfoFrom(ctx); got != nil {
		t.Errorf("expected nil from bare context, got %+v", got)
	}

	// Round-trip: same pointer returned.
	info := &EnvInfo{OS: "TestOS", Arch: "testarch"}
	ctx = WithEnvInfo(ctx, info)
	got := EnvInfoFrom(ctx)
	if got != info {
		t.Errorf("expected same pointer; got %p, want %p", got, info)
	}
}

func TestFormatFullEnvBlock(t *testing.T) {
	info := &EnvInfo{
		OS:            "macOS 15.4 (Darwin 24.4.0)",
		Arch:          "arm64",
		Shell:         "/bin/zsh",
		HomeDir:       "/Users/test",
		GoVersion:     "1.23.1",
		NodeVersion:   "22.5.0",
		PythonVersion: "3.12.4",
	}

	out := FormatFullEnvBlock(info, EnvFormatOptions{})

	expected := []string{
		"## Environment",
		"OS: macOS 15.4 (Darwin 24.4.0)",
		"Architecture: arm64",
		"Shell: /bin/zsh",
		"Home directory: /Users/test",
		"Go: 1.23.1",
		"Node.js: 22.5.0",
		"Python: 3.12.4",
		"Date:",
		"Timezone:",
	}
	for _, s := range expected {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\nGot:\n%s", s, out)
		}
	}
}

func TestFormatCompactEnvBlock(t *testing.T) {
	info := &EnvInfo{
		OS:            "Linux 6.1.0",
		Arch:          "amd64",
		Shell:         "/bin/bash",
		HomeDir:       "/home/test",
		GoVersion:     "1.23.1",
		NodeVersion:   "22.5.0",
		PythonVersion: "3.12.4",
	}

	out := FormatCompactEnvBlock(info)

	// Should contain.
	for _, s := range []string{"## Environment", "OS: Linux 6.1.0", "Date:", "Timezone:"} {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\nGot:\n%s", s, out)
		}
	}

	// Should NOT contain detailed fields.
	for _, s := range []string{"Architecture:", "Shell:", "Go:"} {
		if strings.Contains(out, s) {
			t.Errorf("compact output should not contain %q\nGot:\n%s", s, out)
		}
	}
}

func TestFormatEnvBlock_Nil(t *testing.T) {
	if out := FormatFullEnvBlock(nil, EnvFormatOptions{}); out != "" {
		t.Errorf("expected empty string for nil, got %q", out)
	}
	if out := FormatCompactEnvBlock(nil); out != "" {
		t.Errorf("expected empty string for nil, got %q", out)
	}
}

func TestFormatFullEnvBlock_MissingRuntime(t *testing.T) {
	info := &EnvInfo{
		OS:            "Linux 6.1.0",
		Arch:          "amd64",
		Shell:         "/bin/bash",
		HomeDir:       "/home/test",
		GoVersion:     "1.23.1",
		NodeVersion:   "22.5.0",
		PythonVersion: "", // not installed
	}

	out := FormatFullEnvBlock(info, EnvFormatOptions{})

	if !strings.Contains(out, "Python: not installed") {
		t.Errorf("expected 'Python: not installed' in output\nGot:\n%s", out)
	}
	// Go and Node should show versions, not "not installed".
	if strings.Contains(out, "Go: not installed") {
		t.Errorf("Go should show version, not 'not installed'\nGot:\n%s", out)
	}
}

func TestFormatFullEnvBlock_HideHomeDir(t *testing.T) {
	info := &EnvInfo{
		OS:      "macOS 15.4",
		Arch:    "arm64",
		Shell:   "/bin/zsh",
		HomeDir: "/Users/secret",
	}

	out := FormatFullEnvBlock(info, EnvFormatOptions{HideHomeDir: true})

	if strings.Contains(out, "Home directory") {
		t.Errorf("expected Home directory to be hidden\nGot:\n%s", out)
	}
	if strings.Contains(out, "/Users/secret") {
		t.Errorf("expected home dir path to be absent\nGot:\n%s", out)
	}
	// Other fields should still be present.
	if !strings.Contains(out, "OS: macOS 15.4") {
		t.Errorf("expected OS info\nGot:\n%s", out)
	}
}

func TestFormatTimezoneLabel_Local(t *testing.T) {
	// When location name is "Local", should use zone abbreviation.
	label := formatTimezoneLabel("Local", "MST", -25200) // UTC-7
	if !strings.Contains(label, "MST") {
		t.Errorf("expected zone abbreviation 'MST' for Local, got %q", label)
	}
	if !strings.Contains(label, "UTC-7") {
		t.Errorf("expected 'UTC-7' in label, got %q", label)
	}
}

func TestFormatTimezoneLabel_Empty(t *testing.T) {
	// Empty location name → falls back to zone.
	label := formatTimezoneLabel("", "EST", -18000) // UTC-5
	if !strings.Contains(label, "EST") {
		t.Errorf("expected zone abbreviation 'EST' for empty location, got %q", label)
	}
	if !strings.Contains(label, "UTC-5") {
		t.Errorf("expected 'UTC-5' in label, got %q", label)
	}
}

func TestFormatTimezoneLabel_NamedLocation(t *testing.T) {
	// Named location like "Europe/Moscow".
	label := formatTimezoneLabel("Europe/Moscow", "MSK", 10800) // UTC+3
	if !strings.Contains(label, "Europe/Moscow") {
		t.Errorf("expected location name 'Europe/Moscow', got %q", label)
	}
	if !strings.Contains(label, "UTC+3") {
		t.Errorf("expected 'UTC+3' in label, got %q", label)
	}
}

func TestFormatTimezoneLabel_PositiveOffset(t *testing.T) {
	label := formatTimezoneLabel("Asia/Tokyo", "JST", 32400) // UTC+9
	if !strings.Contains(label, "UTC+9") {
		t.Errorf("expected 'UTC+9', got %q", label)
	}
}

func TestFormatTimezoneLabel_HalfHourOffset(t *testing.T) {
	// India is UTC+5:30 (19800 seconds). The label must include the minutes,
	// not truncate to UTC+5.
	label := formatTimezoneLabel("Asia/Kolkata", "IST", 19800) // UTC+5:30
	if !strings.Contains(label, "UTC+5:30") {
		t.Errorf("expected 'UTC+5:30' for half-hour offset, got %q", label)
	}
	if strings.Contains(label, "UTC+5:") && !strings.Contains(label, "UTC+5:30") {
		t.Errorf("expected minutes rendered, got %q", label)
	}
}

func TestFormatTimezoneLabel_NegativeHalfHourOffset(t *testing.T) {
	// Newfoundland is UTC-3:30 (-12600 seconds).
	label := formatTimezoneLabel("America/St_Johns", "NST", -12600) // UTC-3:30
	if !strings.Contains(label, "UTC-3:30") {
		t.Errorf("expected 'UTC-3:30' for negative half-hour offset, got %q", label)
	}
}

func TestRuntimeOrNotInstalled(t *testing.T) {
	if got := runtimeOrNotInstalled(""); got != "not installed" {
		t.Errorf("expected 'not installed' for empty, got %q", got)
	}
	if got := runtimeOrNotInstalled("1.23.1"); got != "1.23.1" {
		t.Errorf("expected '1.23.1', got %q", got)
	}
}
