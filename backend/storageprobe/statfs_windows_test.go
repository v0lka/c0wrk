//go:build windows

package storageprobe

import "testing"

// Pure decode-helper tests: the windows syscall calls themselves are
// best-effort production reads; these pin the classification logic that
// turns raw API values into verdicts. Compiled (and type-checked) wherever
// the package builds; executed on windows runners.

func TestDriveTypeIsRemote(t *testing.T) {
	cases := []struct {
		driveType uint32
		want      bool
	}{
		{0, false}, // DRIVE_UNKNOWN
		{1, false}, // DRIVE_NO_ROOT_DIR
		{2, false}, // DRIVE_REMOVABLE
		{3, false}, // DRIVE_FIXED (local disk)
		{4, true},  // DRIVE_REMOTE
		{5, false}, // DRIVE_CDROM
		{6, false}, // DRIVE_RAMDISK
	}
	for _, tc := range cases {
		if got := driveTypeIsRemote(tc.driveType); got != tc.want {
			t.Errorf("driveTypeIsRemote(%d) = %v, want %v", tc.driveType, got, tc.want)
		}
	}
}

func TestAttrsHaveRecall(t *testing.T) {
	const recall = uint32(0x00400000) // FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS
	if attrsHaveRecall(0) {
		t.Error("zero attributes reported recall, want false")
	}
	if attrsHaveRecall(0x10 | 0x20) { // DIRECTORY | ARCHIVE — an ordinary dir
		t.Error("ordinary directory attributes reported recall, want false")
	}
	if !attrsHaveRecall(recall) {
		t.Error("recall attribute alone not detected")
	}
	if !attrsHaveRecall(0x10 | recall) {
		t.Error("directory + recall not detected")
	}
}

func TestVolumeRoot(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\ws\proj`, `C:\`},
		{`C:ws\proj`, `C:\`},
		{`D:\`, `D:\`},
		{`\\srv\share\ws`, `\\srv\share`},
		{`\\?\C:\ws`, `\\?\C:`},
		{`relative\path`, `relative\path`},
	}
	for _, tc := range cases {
		if got := volumeRoot(tc.in); got != tc.want {
			t.Errorf("volumeRoot(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
