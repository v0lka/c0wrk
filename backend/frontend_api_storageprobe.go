package backend

// Storage-liveness advisories: one-shot notes about WHERE a workspace (and
// the c0wrk data directory) physically lives — network volumes, cloud
// placeholder trees, cloud-sync roots. Detection only, zero behavior change:
// a probe result becomes a single Warn log line plus a single runtime_error
// toast note per project per session (error_code workspace_storage_advisory,
// cataloged in specs/contracts/event-catalog.md). Fired from SwitchProject's
// real-switch path, fire-and-forget like the auto-fetch and stale-branch
// sweeps around it; never runs on the already-active re-selection path. See
// specs/domains/workspace.md ("Workspace storage liveness advisory").

import (
	"strings"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/storageprobe"
)

// storageAdvisoryErrCode is the runtime_error error_code of these notes.
const storageAdvisoryErrCode = "workspace_storage_advisory"

// storageAdvisoryConfigDirKey marks the once-per-session config-dir note in
// FrontendAPI.storageAdvisoryNotified (the config directory is global — it
// does not belong to any project).
const storageAdvisoryConfigDirKey = "config-dir"

// probeWorkspaceFn / probeConfigDirFn are test-only seams over the
// storageprobe package functions (nil-guarded by their silence contract);
// tests swap them and restore with t.Cleanup.
var (
	probeWorkspaceFn = storageprobe.ProbeWorkspace
	probeConfigDirFn = storageprobe.ProbeConfigDir
)

// probeStorageAdvisories runs both liveness probes for a freshly switched
// project: the workspace probe (once per project per session) and the global
// config-directory probe (once per session). Advisory only — neither probe
// can influence the switch or any later behavior.
func (f *FrontendAPI) probeStorageAdvisories(p *project.ProjectInfo) {
	f.probeWorkspaceLiveness(p)
	f.probeConfigDirLiveness()
}

// probeWorkspaceLiveness probes p's workspace storage class: on findings, one
// Warn + one toast note, then the project is marked notified for the rest of
// the session. The mark is taken BEFORE emitting (LoadOrStore), so concurrent
// double-fires dedup; a clean or failed probe marks nothing, so a later
// switch to the same project re-probes instead of inheriting a stale verdict.
func (f *FrontendAPI) probeWorkspaceLiveness(p *project.ProjectInfo) {
	if p == nil || p.IsNoProject || strings.TrimSpace(p.WorkspacePath) == "" {
		return
	}
	findings := probeWorkspaceFn(p.WorkspacePath)
	if len(findings) == 0 {
		return
	}
	if _, dup := f.storageAdvisoryNotified.LoadOrStore("project:"+p.ID, struct{}{}); dup {
		return
	}
	kinds := make([]string, 0, len(findings))
	causes := make([]string, 0, len(findings))
	for _, fnd := range findings {
		kinds = append(kinds, fnd.Kind)
		causes = append(causes, fnd.Detail)
	}
	f.log().Warn("storage advisory: project workspace storage class",
		"project", p.ID,
		"path", p.WorkspacePath,
		"kinds", strings.Join(kinds, ","),
		"cause", strings.Join(causes, "; "))
	f.emitStorageAdvisoryNote(storageAdvisoryMessage(p.WorkspacePath, findings))
}

// probeConfigDirLiveness probes the c0wrk data directory once per session:
// its databases, caches and sidecars stall when the directory lives behind a
// sync engine or a network volume, which no single project's probe would
// surface.
func (f *FrontendAPI) probeConfigDirLiveness() {
	if strings.TrimSpace(f.agentDir) == "" {
		return
	}
	if _, dup := f.storageAdvisoryNotified.LoadOrStore(storageAdvisoryConfigDirKey, struct{}{}); dup {
		return
	}
	findings := probeConfigDirFn(f.agentDir)
	if len(findings) == 0 {
		return
	}
	kinds := make([]string, 0, len(findings))
	causes := make([]string, 0, len(findings))
	for _, fnd := range findings {
		kinds = append(kinds, fnd.Kind)
		causes = append(causes, fnd.Detail)
	}
	f.log().Warn("storage advisory: c0wrk data directory storage class",
		"path", f.agentDir,
		"kinds", strings.Join(kinds, ","),
		"cause", strings.Join(causes, "; "))
	f.emitStorageAdvisoryNote(storageAdvisoryMessage(f.agentDir, findings))
}

// emitStorageAdvisoryNote raises the user-visible toast note — the global
// runtime_error channel, the same shape as every other backend emitter
// (id, message, error_code); nil-guarded like the other early-startup
// emitters.
func (f *FrontendAPI) emitStorageAdvisoryNote(message string) {
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventRuntimeError, map[string]string{
		"id":         uuid.New().String(),
		"message":    message,
		"error_code": storageAdvisoryErrCode,
	})
}

// storageAdvisoryMessage renders findings into one operator-facing sentence
// chain (the human text; the machine-readable cause lives in the Warn's
// "cause" attribute).
func storageAdvisoryMessage(dir string, findings []storageprobe.Finding) string {
	sentences := make([]string, 0, len(findings))
	for _, fnd := range findings {
		switch fnd.Kind {
		case storageprobe.KindNetworkVolume:
			sentences = append(sentences, dir+" is on a network volume — file operations may be slow or stall when connectivity drops.")
		case storageprobe.KindPlaceholderFiles:
			sentences = append(sentences, dir+" contains cloud placeholder files (e.g. OneDrive Files On-Demand) — reading an un-downloaded file stalls until it is fetched.")
		case storageprobe.KindSyncedConfigDir:
			sentences = append(sentences, "The c0wrk data directory ("+dir+") lives under a cloud-sync provider (FileProvider/iCloud) — its databases, caches and sidecars may stall while the sync provider fetches data.")
		default:
			sentences = append(sentences, dir+" uses an unusual storage class — file operations may be slower than on local disk.")
		}
	}
	return strings.Join(sentences, " ")
}
