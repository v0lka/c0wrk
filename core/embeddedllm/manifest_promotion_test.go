package embeddedllm

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestManifestPromotionFaults controls contention without depending on NTFS or
// antivirus timing. The real writer still creates, syncs and closes its temp.
func TestManifestPromotionFaults(t *testing.T) {
	sharing := errors.New("injected sharing collision")
	permanent := errors.New("injected permanent rename failure")
	cleanup := errors.New("injected cleanup failure")
	tests := []struct {
		name          string
		failures      int
		renameErr     error
		cleanupErr    error
		vanished      bool
		wantAttempts  int
		wantErr       error
		wantWaitCount int
	}{
		{name: "first attempt succeeds", wantAttempts: 1},
		{name: "collisions then success", failures: 2, renameErr: sharing, wantAttempts: 3, wantWaitCount: 2},
		{name: "transient exhaustion", failures: manifestPromoteAttempts, renameErr: sharing, wantAttempts: manifestPromoteAttempts, wantWaitCount: manifestPromoteAttempts - 1, wantErr: sharing},
		{name: "permanent error is not retried", failures: 1, renameErr: permanent, wantAttempts: 1, wantErr: permanent},
		{name: "cleanup error is retained", failures: 1, renameErr: permanent, cleanupErr: cleanup, wantAttempts: 1, wantErr: permanent},
		{name: "already absent temp preserves promotion error", failures: 1, renameErr: permanent, vanished: true, wantAttempts: 1, wantErr: permanent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, ManifestFileName)
			old := Manifest{ModelFile: "old.gguf", Port: 1024, ContextSize: 16384}
			next := Manifest{ModelFile: "next.gguf", Port: 1025, ContextSize: 32768}
			if err := writeManifest(path, old, nil); err != nil {
				t.Fatal(err)
			}
			attempts, removed := 0, 0
			var waits []time.Duration
			var temp string
			promote := func(tmp, dst string, logger *slog.Logger) error {
				temp = tmp
				if filepath.Dir(tmp) != dir || dst != path || tmp == path {
					t.Errorf("promotion paths = %q -> %q, want unique sibling -> %q", tmp, dst, path)
				}
				return promoteManifestWithOps(tmp, dst, logger, func(src, target string) error {
					attempts++
					got, err := ReadManifest(src)
					if err != nil || !reflect.DeepEqual(got, next) {
						t.Errorf("temporary before rename = %#v, %v, want %#v", got, err, next)
					}
					got, err = ReadManifest(target)
					if err != nil || !reflect.DeepEqual(got, old) {
						t.Errorf("destination before promotion = %#v, %v, want preserved %#v", got, err, old)
					}
					if attempts <= tt.failures {
						if tt.vanished {
							if err := os.Remove(src); err != nil {
								t.Fatal(err)
							}
						}
						return tt.renameErr
					}
					return os.Rename(src, target)
				}, func(err error) bool { return errors.Is(err, sharing) }, func(d time.Duration) { waits = append(waits, d) })
			}
			err := writeManifestWithOps(path, next, nil, promote, func(name string) error {
				removed++
				if name != temp || name == path {
					t.Errorf("cleanup(%q), want temp %q only", name, temp)
				}
				if tt.cleanupErr != nil {
					return tt.cleanupErr
				}
				return os.Remove(name)
			})
			if !errors.Is(err, tt.wantErr) || (tt.cleanupErr != nil && !errors.Is(err, tt.cleanupErr)) {
				t.Errorf("writeManifestWithOps() error = %v, want %v and cleanup %v", err, tt.wantErr, tt.cleanupErr)
			}
			if attempts != tt.wantAttempts || len(waits) != tt.wantWaitCount {
				t.Errorf("promotion attempts/waits = %d/%v, want %d/%d", attempts, waits, tt.wantAttempts, tt.wantWaitCount)
			}
			for i, d := range waits {
				if want := manifestPromoteBackoff * time.Duration(1<<i); d != want {
					t.Errorf("backoff[%d] = %v, want %v", i, d, want)
				}
			}
			want := next
			wantRemoved := 0
			if tt.wantErr != nil {
				want, wantRemoved = old, 1
			}
			if removed != wantRemoved {
				t.Errorf("cleanup calls = %d, want %d", removed, wantRemoved)
			}
			got, readErr := ReadManifest(path)
			if readErr != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("ReadManifest after promotion = %#v, %v, want %#v", got, readErr, want)
			}
			if tt.cleanupErr != nil {
				// A failed cleanup must be observable, not silently called clean.
				if _, statErr := os.Stat(temp); statErr != nil {
					t.Errorf("failed cleanup temp = %v, want existing temp", statErr)
				}
				if removeErr := os.Remove(temp); removeErr != nil {
					t.Fatal(removeErr)
				}
			}
			requireNoManifestTemp(t, dir)
		})
	}
}
