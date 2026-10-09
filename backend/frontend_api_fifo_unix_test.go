//go:build unix

package backend

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The read-path RPCs below run synchronously on the Wails RPC goroutine the UI
// waits on. Before safeio, a FIFO planted at any of these paths blocked the
// read-open — inside open(2), where no context check or deadline can interrupt
// it — and froze the UI. Each test drives the real RPC with a watchdog so a
// regression to a blocking open fails instead of hanging CI.

func TestReadFile_RefusesFifoWithoutHanging(t *testing.T) {
	dir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: dir}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := f.ReadFile(fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ReadFile(FIFO) succeeded, want a non-regular refusal")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadFile hung on a FIFO (blocking open)")
	}
}

func TestReadFileAsDataURL_RefusesFifoWithoutHanging(t *testing.T) {
	dir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: dir}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	fifo := filepath.Join(dir, "pipe.png")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := f.ReadFileAsDataURL(fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ReadFileAsDataURL(FIFO) succeeded, want a non-regular refusal")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadFileAsDataURL hung on a FIFO (blocking open)")
	}
}

func TestAppendToGitignore_RefusesFifoWithoutHanging(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := syscall.Mkfifo(filepath.Join(dir, ".gitignore"), 0o644); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}

		done := make(chan error, 1)
		go func() {
			done <- f.AppendToGitignore("build/")
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("AppendToGitignore(FIFO .gitignore) succeeded, want an error")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("AppendToGitignore hung on a FIFO .gitignore (blocking open)")
		}
	})
}
