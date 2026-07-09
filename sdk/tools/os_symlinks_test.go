package tools

import "testing"

func TestIsWellKnownOSSymlink_MacOS(t *testing.T) {
	for _, p := range []string{"/etc", "/tmp", "/var", "/cores"} {
		if !IsWellKnownOSSymlink(p) {
			t.Errorf("expected %q to be a well-known OS symlink", p)
		}
		// Dirty variants must still match (filepath.Clean + normalization).
		if !IsWellKnownOSSymlink(p + "/") {
			t.Errorf("expected %q (trailing slash) to match", p+"/")
		}
		if !IsWellKnownOSSymlink(p + "/.") {
			t.Errorf("expected %q to match after Clean", p+"/.")
		}
	}
}

func TestIsWellKnownOSSymlink_LinuxUsrMerge(t *testing.T) {
	for _, p := range []string{"/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32"} {
		if !IsWellKnownOSSymlink(p) {
			t.Errorf("expected %q to be a well-known OS symlink", p)
		}
	}
}

func TestIsWellKnownOSSymlink_LinuxRunMigration(t *testing.T) {
	for _, p := range []string{"/var/run", "/var/lock"} {
		if !IsWellKnownOSSymlink(p) {
			t.Errorf("expected %q to be a well-known OS symlink", p)
		}
	}
}

func TestIsWellKnownOSSymlink_WindowsJunctionCaseInsensitive(t *testing.T) {
	// Windows junctions must match case-insensitively regardless of casing.
	for _, p := range []string{
		`C:\Documents and Settings`,
		`c:\documents and settings`,
		`C:\DOCUMENTS AND SETTINGS`,
	} {
		if !IsWellKnownOSSymlink(p) {
			t.Errorf("expected %q to match (case-insensitive)", p)
		}
	}
	// A different drive / unknown path must NOT match.
	for _, p := range []string{
		`D:\Documents and Settings`,
		`C:\Users`,
		`E:\Program Files`,
	} {
		if IsWellKnownOSSymlink(p) {
			t.Errorf("expected %q NOT to match", p)
		}
	}
}

func TestIsWellKnownOSSymlink_NonSymlinks(t *testing.T) {
	for _, p := range []string{"", "/home/user", "/usr/bin", "/Users/x/proj", "/opt", "/private", "/var/log"} {
		if IsWellKnownOSSymlink(p) {
			t.Errorf("expected %q NOT to be a well-known OS symlink", p)
		}
	}
}

func TestIsWellKnownOSSymlink_TargetLookup(t *testing.T) {
	if tgt, ok := wellKnownOSSymlinks["/tmp"]; !ok || tgt != "/private/tmp" {
		t.Errorf("expected /tmp → /private/tmp, got %q (ok=%v)", tgt, ok)
	}
	if tgt, ok := wellKnownOSSymlinks["/bin"]; !ok || tgt != "/usr/bin" {
		t.Errorf("expected /bin → /usr/bin, got %q (ok=%v)", tgt, ok)
	}
}

func TestIsOSLevelSymlink_WellKnown(t *testing.T) {
	if !IsOSLevelSymlink("/tmp") {
		t.Error("well-known /tmp should be OS-level even without roots")
	}
}

func TestIsOSLevelSymlink_WorkspaceAncestor(t *testing.T) {
	// /var is an ancestor of the workspace root when the workspace lives under
	// /var/folders/... (a non-well-known symlink, but reached via an ancestor).
	// Use a synthetic name that is NOT in the well-known list to exercise the
	// ancestor branch purely.
	ancestor := "/srv/data"
	workspace := "/srv/data/projects/x"
	if !IsOSLevelSymlink(ancestor, workspace) {
		t.Errorf("expected %q to be OS-level as ancestor of %q", ancestor, workspace)
	}
	// A sibling that is NOT an ancestor must not match.
	if IsOSLevelSymlink("/srv/other", workspace) {
		t.Error("non-ancestor must not be OS-level")
	}
}

func TestIsOSLevelSymlink_TempAncestor(t *testing.T) {
	// macOS layout: temp dir reached via /var (a well-known symlink) and also
	// an ancestor of the temp root.
	tempDir := "/var/folders/aa/T"
	if !IsOSLevelSymlink("/var", tempDir) {
		t.Error("expected /var to be OS-level relative to temp dir")
	}
}

func TestIsOSLevelSymlink_EmptyAndRoot(t *testing.T) {
	if IsOSLevelSymlink("") {
		t.Error("empty path must not be OS-level")
	}
	if IsOSLevelSymlink("/", "/anything") {
		t.Error("bare root '/' must not be treated as a symlink ancestor")
	}
}
