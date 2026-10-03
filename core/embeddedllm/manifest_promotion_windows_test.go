//go:build windows

package embeddedllm

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestManifestRenameWindowsErrorClassification(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "access denied", err: windows.ERROR_ACCESS_DENIED, want: true},
		{name: "sharing violation", err: windows.ERROR_SHARING_VIOLATION, want: true},
		{name: "lock violation", err: windows.ERROR_LOCK_VIOLATION, want: true},
		{name: "not found", err: windows.ERROR_FILE_NOT_FOUND},
		{name: "disk full", err: windows.ERROR_DISK_FULL},
		{name: "ordinary error", err: errors.New("permanent")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := &os.LinkError{Op: "rename", Old: "temp", New: "manifest", Err: tt.err}
			if got := isTransientRenameError(err); got != tt.want {
				t.Errorf("isTransientRenameError(%v) = %t, want %t", err, got, tt.want)
			}
		})
	}
}

// TestManifestPromotionWindowsHeldHandle pins a real sharing denial, not a
// scheduler-dependent collision. Replacing a held target must fail honestly,
// preserve the old record and clean the temp; releasing it permits promotion.
func TestManifestPromotionWindowsHeldHandle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ManifestFileName)
	old := Manifest{ModelFile: "old.gguf", Port: 1024}
	next := Manifest{ModelFile: "next.gguf", Port: 1025}
	if err := writeManifest(path, old, nil); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately omit FILE_SHARE_DELETE so MoveFileEx cannot replace it.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if err := windows.CloseHandle(handle); err != nil {
				t.Error(err)
			}
		}
	})
	if err := writeManifest(path, next, nil); err == nil || !isTransientRenameError(err) {
		t.Errorf("writeManifest with held target = %v, want reported Windows sharing denial", err)
	}
	got, err := ReadManifest(path)
	if err != nil || !reflect.DeepEqual(got, old) {
		t.Errorf("held manifest = %#v, %v, want preserved %#v", got, err, old)
	}
	requireNoManifestTemp(t, dir)
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	closed = true
	if err := writeManifest(path, next, nil); err != nil {
		t.Fatalf("writeManifest after handle release: %v", err)
	}
	got, err = ReadManifest(path)
	if err != nil || !reflect.DeepEqual(got, next) {
		t.Errorf("released manifest = %#v, %v, want %#v", got, err, next)
	}
	requireNoManifestTemp(t, dir)
}
