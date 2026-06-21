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

func TestPlatformTriple_ReturnsNoError(t *testing.T) {
	triple, err := PlatformTriple()
	if err != nil {
		t.Fatalf("PlatformTriple() returned error: %v", err)
	}
	if triple == "" {
		t.Error("PlatformTriple() returned empty string")
	}
}
