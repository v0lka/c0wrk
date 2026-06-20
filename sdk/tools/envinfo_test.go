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

	out := FormatFullEnvBlock(context.Background(), info)

	expected := []string{
		"## Environment",
		"OS: macOS 15.4 (Darwin 24.4.0)",
		"Architecture: arm64",
		"Shell: /bin/zsh",
		"Home directory: /Users/test",
		"Go: 1.23.1",
		"Node.js: 22.5.0",
		"Python: 3.12.4",
		"Current time:",
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

	out := FormatCompactEnvBlock(context.Background(), info)

	// Should contain.
	for _, s := range []string{"## Environment", "OS: Linux 6.1.0", "Current time:", "Timezone:"} {
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
	if out := FormatFullEnvBlock(context.Background(), nil); out != "" {
		t.Errorf("expected empty string for nil, got %q", out)
	}
	if out := FormatCompactEnvBlock(context.Background(), nil); out != "" {
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

	out := FormatFullEnvBlock(context.Background(), info)

	if !strings.Contains(out, "Python: not installed") {
		t.Errorf("expected 'Python: not installed' in output\nGot:\n%s", out)
	}
	// Go and Node should show versions, not "not installed".
	if strings.Contains(out, "Go: not installed") {
		t.Errorf("Go should show version, not 'not installed'\nGot:\n%s", out)
	}
}
