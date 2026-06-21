package toolmanager

import (
	"runtime"
	"testing"
)

func TestPlatform_MatchesRuntime(t *testing.T) {
	p := Platform()
	expected := runtime.GOOS + "-" + runtime.GOARCH
	if p != expected {
		t.Errorf("Platform() = %q, want %q", p, expected)
	}
}

func TestPlatform_NotEmpty(t *testing.T) {
	if Platform() == "" {
		t.Error("Platform() returned empty string")
	}
}
