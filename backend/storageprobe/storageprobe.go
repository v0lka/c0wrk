// Package storageprobe reports the storage class of workspace and config
// paths — advisory liveness hints only, never policy. A probe answers one
// question: "will filesystem operations on this path behave like local
// storage?" Network mounts (macOS statfs flags, Windows drive types) and
// cloud placeholder management (Windows FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS,
// macOS FileProvider/iCloud sync roots) slow reads, stall watchers and can
// wedge the vector index and sidecar caches; callers surface the findings as
// a one-shot user note and nothing more (detection only — no behavior
// change). Platform readers live behind the statfsProbe seam so unit tests
// can fake statfs/drive-type responses.
package storageprobe

// Finding kinds carried in a Finding.Kind.
const (
	// KindNetworkVolume — the path sits on a network (or otherwise non-local)
	// volume: statfs flags lack MNT_LOCAL on darwin, GetDriveTypeW reports
	// DRIVE_REMOTE on windows.
	KindNetworkVolume = "network_volume"
	// KindPlaceholderFiles — the path itself is managed by a cloud
	// placeholder provider (windows: FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS,
	// e.g. OneDrive Files On-Demand); reading an un-downloaded file stalls.
	KindPlaceholderFiles = "placeholder_files"
	// KindSyncedConfigDir — the path resolves under a cloud-sync provider
	// root (darwin FileProvider/iCloud containment); the data directory's
	// databases, caches and sidecars live under the sync engine.
	KindSyncedConfigDir = "synced_config_dir"
)

// Finding is one advisory observation about a path's storage class.
type Finding struct {
	// Kind is one of the Kind* constants.
	Kind string
	// Detail is a short human-readable cause ("statfs flags lack MNT_LOCAL
	// …"), suitable for a log attribute or a parenthetical in a user note.
	Detail string
}

// volumeClass is a platform reader's verdict for one path.
type volumeClass struct {
	// Network reports a network / non-local volume.
	Network bool
	// Placeholder reports cloud placeholder management of the path itself.
	Placeholder bool
	// NetworkDetail explains a Network verdict; empty when Network is false.
	NetworkDetail string
	// PlaceholderDetail explains a Placeholder verdict; empty when
	// Placeholder is false.
	PlaceholderDetail string
}

// statfsProbe is the platform seam: it resolves to the build-tag-selected
// syscall reader (statfsVolume). Unit tests replace it with a fake statfs /
// drive-type responder — production code never touches this variable.
var statfsProbe = statfsVolume

// ProbeWorkspace returns the advisory findings for a project workspace path.
// A probe failure (unreadable path) is silent: the probe is best-effort and
// must never surface an error of its own. Returns nil when the path is local
// and unremarkable.
func ProbeWorkspace(path string) []Finding {
	return probe(path, false)
}

// ProbeConfigDir returns the advisory findings for the c0wrk data directory
// (~/.c0wrk): beyond the shared volume checks it asks the platform whether
// the directory resolves under a cloud-sync provider (sidecars and caches
// would stall behind the sync engine). Same silence contract as
// ProbeWorkspace.
func ProbeConfigDir(path string) []Finding {
	return probe(path, true)
}

// probe is the shared assembly over the platform seam: one volume-class read
// for every probe, plus the config-dir-specific platform findings.
func probe(path string, configDir bool) []Finding {
	if path == "" {
		return nil
	}
	vc, err := statfsProbe(path)
	if err != nil {
		// Unreadable path — stay silent; an advisory must never invent an
		// error the caller would have to handle.
		return nil
	}
	var findings []Finding
	if vc.Network {
		findings = append(findings, Finding{Kind: KindNetworkVolume, Detail: vc.NetworkDetail})
	}
	if vc.Placeholder {
		findings = append(findings, Finding{Kind: KindPlaceholderFiles, Detail: vc.PlaceholderDetail})
	}
	if configDir {
		findings = append(findings, configDirFindings(path)...)
	}
	return findings
}
