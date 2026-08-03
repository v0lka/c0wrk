//go:build !windows

package terminal

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

func testManager(t *testing.T) (mgr *Manager, output chan []byte) {
	t.Helper()

	outputChan := make(chan []byte, 100)
	emitFunc := func(_ string, data []byte) {
		select {
		case outputChan <- data:
		default:
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewManager(context.Background(), logger, emitFunc), outputChan
}

func TestManager_StartStop(t *testing.T) {
	mgr, _ := testManager(t)

	workDir := t.TempDir()
	if err := mgr.Start("sess-1", workDir); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !mgr.IsActive("sess-1") {
		t.Error("expected terminal to be active")
	}

	if err := mgr.Stop("sess-1"); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if mgr.IsActive("sess-1") {
		t.Error("expected terminal to be inactive after stop")
	}
}

func TestManager_StartDuplicate(t *testing.T) {
	mgr, _ := testManager(t)

	workDir := t.TempDir()
	if err := mgr.Start("sess-1", workDir); err != nil {
		t.Fatalf("first Start failed: %v", err)
	}

	if err := mgr.Start("sess-1", workDir); err == nil {
		t.Error("expected error starting duplicate terminal")
	}

	_ = mgr.Stop("sess-1")
}

func TestManager_Write(t *testing.T) {
	mgr, outputChan := testManager(t)

	workDir := t.TempDir()
	if err := mgr.Start("sess-1", workDir); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = mgr.Stop("sess-1") }()

	// Write a command
	if err := mgr.Write("sess-1", []byte("echo hello_test\n")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Poll for output with a retry loop instead of a fixed sleep.
	var output bytes.Buffer
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case data := <-outputChan:
			output.Write(data)
			if bytes.Contains(output.Bytes(), []byte("hello_test")) {
				goto checkOutput
			}
		case <-time.After(100 * time.Millisecond):
			if bytes.Contains(output.Bytes(), []byte("hello_test")) {
				goto checkOutput
			}
		}
	}
checkOutput:
	if !bytes.Contains(output.Bytes(), []byte("hello_test")) {
		t.Errorf("expected output to contain 'hello_test', got: %s", output.String())
	}
}

func TestManager_Resize(t *testing.T) {
	mgr, _ := testManager(t)

	workDir := t.TempDir()
	if err := mgr.Start("sess-1", workDir); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = mgr.Stop("sess-1") }()

	if err := mgr.Resize("sess-1", 80, 24); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
}

func TestManager_ResizeNotFound(t *testing.T) {
	mgr, _ := testManager(t)

	if err := mgr.Resize("nonexistent", 80, 24); err == nil {
		t.Error("expected error resizing non-existent terminal")
	}
}

func TestManager_WriteNotFound(t *testing.T) {
	mgr, _ := testManager(t)

	if err := mgr.Write("nonexistent", []byte("test\n")); err == nil {
		t.Error("expected error writing to non-existent terminal")
	}
}

func TestManager_StopNotFound(t *testing.T) {
	mgr, _ := testManager(t)

	if err := mgr.Stop("nonexistent"); err == nil {
		t.Error("expected error stopping non-existent terminal")
	}
}

func TestManager_ConcurrentAccess(t *testing.T) {
	mgr, _ := testManager(t)

	workDir := t.TempDir()
	if err := mgr.Start("sess-1", workDir); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = mgr.Stop("sess-1") }()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.IsActive("sess-1")
			_ = mgr.Resize("sess-1", 80, 24)
			_ = mgr.Write("sess-1", []byte("echo test\n"))
		}()
	}
	wg.Wait()
}

func TestManager_StopAll(t *testing.T) {
	mgr, _ := testManager(t)

	workDir := t.TempDir()
	for i := 0; i < 3; i++ {
		id := "sess-" + string(rune('a'+i))
		if err := mgr.Start(id, workDir); err != nil {
			t.Fatalf("Start %s failed: %v", id, err)
		}
	}

	mgr.StopAll()

	for i := 0; i < 3; i++ {
		id := "sess-" + string(rune('a'+i))
		if mgr.IsActive(id) {
			t.Errorf("expected terminal %s to be inactive after StopAll", id)
		}
	}
}
