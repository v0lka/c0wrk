package storageprobe

import (
	"errors"
	"testing"
)

// The seam tests below swap statfsProbe — the build-tag-selected syscall
// reader — for a fake statfs/drive-type responder, so the detection logic is
// exercised on every platform without a real network mount. Seam swaps are
// process-global: none of these tests may run in parallel.

// withStatfsFake replaces statfsProbe for the duration of t and restores it
// via t.Cleanup.
func withStatfsFake(t *testing.T, fake func(path string) (volumeClass, error)) {
	t.Helper()
	orig := statfsProbe
	statfsProbe = fake
	t.Cleanup(func() { statfsProbe = orig })
}

// TestProbeWorkspace_NetworkMountDetected is the seam test the acceptance
// criteria name: a fake statfs reporting a non-local (network) volume must
// surface exactly one network_volume finding carrying the cause.
func TestProbeWorkspace_NetworkMountDetected(t *testing.T) {
	withStatfsFake(t, func(path string) (volumeClass, error) {
		return volumeClass{Network: true, NetworkDetail: "fake statfs: flags lack MNT_LOCAL"}, nil
	})

	findings := ProbeWorkspace("/ws/on-nfs")
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", findings)
	}
	if findings[0].Kind != KindNetworkVolume {
		t.Errorf("finding kind = %q, want %q", findings[0].Kind, KindNetworkVolume)
	}
	if findings[0].Detail != "fake statfs: flags lack MNT_LOCAL" {
		t.Errorf("finding detail = %q, want the fake's cause", findings[0].Detail)
	}
}

// TestProbeWorkspace_LocalVolumeSilent pins the no-false-positive half: a
// local volume (flags carrying MNT_LOCAL, no placeholder management) must
// produce NO findings — the advisory stays quiet on ordinary local storage.
func TestProbeWorkspace_LocalVolumeSilent(t *testing.T) {
	withStatfsFake(t, func(path string) (volumeClass, error) {
		return volumeClass{}, nil
	})

	if findings := ProbeWorkspace("/ws/local"); findings != nil {
		t.Fatalf("local volume findings = %+v, want none", findings)
	}
}

// TestProbeWorkspace_PlaceholderDetected covers the windows-shaped verdict:
// placeholder management of the path itself surfaces as its own finding kind.
func TestProbeWorkspace_PlaceholderDetected(t *testing.T) {
	withStatfsFake(t, func(path string) (volumeClass, error) {
		return volumeClass{
			Placeholder:       true,
			PlaceholderDetail: "fake attrs: RECALL_ON_DATA_ACCESS",
		}, nil
	})

	findings := ProbeWorkspace("/ws/onedrive")
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", findings)
	}
	if findings[0].Kind != KindPlaceholderFiles {
		t.Errorf("finding kind = %q, want %q", findings[0].Kind, KindPlaceholderFiles)
	}
	if findings[0].Detail != "fake attrs: RECALL_ON_DATA_ACCESS" {
		t.Errorf("finding detail = %q, want the fake's cause", findings[0].Detail)
	}
}

// TestProbeWorkspace_ProbeFailureSilent pins the silence contract: a failed
// statfs must produce NO findings and NO error — the advisory never invents
// a failure the caller would have to handle.
func TestProbeWorkspace_ProbeFailureSilent(t *testing.T) {
	withStatfsFake(t, func(path string) (volumeClass, error) {
		return volumeClass{}, errors.New("fake statfs: I/O error")
	})

	if findings := ProbeWorkspace("/ws/broken"); findings != nil {
		t.Fatalf("failed-probe findings = %+v, want none", findings)
	}
}

// TestProbeWorkspace_EmptyPathNoProbe pins the guard: an empty path never
// reaches the seam.
func TestProbeWorkspace_EmptyPathNoProbe(t *testing.T) {
	called := false
	withStatfsFake(t, func(path string) (volumeClass, error) {
		called = true
		return volumeClass{}, nil
	})

	if findings := ProbeWorkspace(""); findings != nil {
		t.Fatalf("empty-path findings = %+v, want none", findings)
	}
	if called {
		t.Fatal("empty path reached the platform seam")
	}
}

// TestProbeWorkspace_BothVerdictsSurface covers the volume that reports both
// signals (a network drive carrying placeholder attributes on windows):
// exactly two findings, each with its own cause.
func TestProbeWorkspace_BothVerdictsSurface(t *testing.T) {
	withStatfsFake(t, func(path string) (volumeClass, error) {
		return volumeClass{
			Network:           true,
			NetworkDetail:     "fake: DRIVE_REMOTE",
			Placeholder:       true,
			PlaceholderDetail: "fake: RECALL_ON_DATA_ACCESS",
		}, nil
	})

	findings := ProbeWorkspace("/ws/both")
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want exactly two", findings)
	}
	kinds := map[string]string{}
	for _, f := range findings {
		kinds[f.Kind] = f.Detail
	}
	if kinds[KindNetworkVolume] != "fake: DRIVE_REMOTE" {
		t.Errorf("network finding = %+v, want the drive-type cause", kinds[KindNetworkVolume])
	}
	if kinds[KindPlaceholderFiles] != "fake: RECALL_ON_DATA_ACCESS" {
		t.Errorf("placeholder finding = %+v, want the attribute cause", kinds[KindPlaceholderFiles])
	}
}

// TestProbeConfigDir_StatfsFindingFlows pins the config-dir probe's shared
// leg: the volume-class verdict about the config path itself flows through
// unchanged (the platform-specific additions are covered per-platform).
func TestProbeConfigDir_StatfsFindingFlows(t *testing.T) {
	withStatfsFake(t, func(path string) (volumeClass, error) {
		return volumeClass{Network: true, NetworkDetail: "fake statfs: network config dir"}, nil
	})

	findings := ProbeConfigDir("/synced/.c0wrk")
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one (the volume verdict)", findings)
	}
	if findings[0].Kind != KindNetworkVolume || findings[0].Detail != "fake statfs: network config dir" {
		t.Errorf("finding = %+v, want the network verdict verbatim", findings[0])
	}
}
