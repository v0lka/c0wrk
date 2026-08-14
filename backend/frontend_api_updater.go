package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/c0wrk/core/updater"
	"github.com/v0lka/c0wrk/core/version"
)

// Self-update DTOs exposed to the frontend via Wails bindings.

// UpdateInfo is the outcome of an update check, surfaced to the frontend.
type UpdateInfo struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	ReleaseNotes   string `json:"release_notes"`
	PublishedAt    string `json:"published_at"`
	HTMLURL        string `json:"html_url"`
	AssetName      string `json:"asset_name"`
}

// UpdateSettings carries the user's self-update preferences.
type UpdateSettings struct {
	// AutoCheck controls whether the app polls for updates automatically on
	// startup. It mirrors the updates.auto_check setting in config.yaml; the
	// master enable/disable gate is updates.enabled.
	AutoCheck bool `json:"auto_check"`
	// SkippedVersion is the tag the user explicitly dismissed; that exact
	// release is suppressed until a newer one is published.
	SkippedVersion string `json:"skipped_version"`
	// CurrentVersion echoes the running build version for display.
	CurrentVersion string `json:"current_version"`
	// OperatorEnabled reflects the operator-level master gate (config.yaml
	// updates.enabled). When false, the entire update subsystem is disabled by
	// an administrator: CheckForUpdates reports no update, the background
	// auto-check never runs, and the UI disables all update affordances.
	OperatorEnabled bool `json:"operator_enabled"`
}

// UpdateProgress is the payload of the update:progress event, carrying the
// bytes downloaded so far and the total archive size.
type UpdateProgress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

// downloadTimeout caps a single archive download. It matches the generous
// fallback used by updater.NewDownloader so the proxy client honours the same
// bound.
const downloadTimeout = 10 * time.Minute

// autoCheckEnabled reports whether automatic background checks are enabled,
// reading updates.auto_check from config.yaml. Defaults to true when unset
// (nil pointer), matching the ApplyDefaults convention.
func (f *FrontendAPI) autoCheckEnabled() bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil || f.config.Updates.AutoCheck == nil {
		return true
	}
	return *f.config.Updates.AutoCheck
}

// loadSkippedVersion reads the user-dismissed release tag from update_state.json
// (the authoritative runtime-state home). Returns "" when the file is missing,
// unreadable, or the field is unset — never returns an error so a missing state
// file never blocks the update flow.
func (f *FrontendAPI) loadSkippedVersion() string {
	if f.agentDir == "" {
		return ""
	}
	state, _ := updater.LoadState(config.UpdateStatePath(f.agentDir))
	return state.SkippedVersion
}

// operatorUpdateCheckEnabled reports whether the operator-level master gate
// (config.yaml updates.enabled) permits the update subsystem. It defaults to
// true when the config is unset, matching derefBoolDefaultTrue semantics.
// This is the SOLE enable/disable switch: when false, CheckForUpdates reports
// no update and the background auto-check never runs.
func (f *FrontendAPI) operatorUpdateCheckEnabled() bool {
	f.configMu.RLock()
	cfg := f.config
	f.configMu.RUnlock()
	if cfg == nil || cfg.Updates.Enabled == nil {
		return true
	}
	return *cfg.Updates.Enabled
}

// proxyConfigForUpdater builds a proxy.Config from the current application
// config under the config read-lock, mirroring the conversion in
// configadapter.ToBuilderConfig.
func (f *FrontendAPI) proxyConfigForUpdater() proxy.Config {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil {
		return proxy.Config{}
	}
	return proxy.Config{
		Enabled:      f.config.Proxy.Enabled,
		URL:          config.ExpandEnvVars(f.config.Proxy.URL),
		BypassList:   f.config.Proxy.BypassList,
		TLSCertDir:   config.ExpandEnvVars(f.config.Proxy.TLSCertDir),
		SetGlobalEnv: derefBool(f.config.Proxy.SetGlobalEnv),
	}
}

// emitUpdateError emits a structured update:error event with a message and
// logs it. The payload is a map[string]string so the frontend can render it
// without a bespoke type guard.
func (f *FrontendAPI) emitUpdateError(msg string) {
	f.log().Error("update error", "error", msg)
	f.emitEvent(EventUpdateError, map[string]string{"message": msg})
}

// sumsURLFor derives the SHA256SUMS download URL from an asset's
// browser_download_url. Both live under the same
// `…/releases/download/<tag>/` prefix; the sums file is always named
// SHA256SUMS (see .github/workflows/release.yml).
func sumsURLFor(assetURL string) string {
	idx := strings.LastIndex(assetURL, "/")
	if idx < 0 {
		return ""
	}
	return assetURL[:idx+1] + "SHA256SUMS"
}

// checkAndCache performs a single update check against GitHub using the
// configured proxy-aware client, then caches the result (under updateMu) so a
// subsequent DownloadUpdate can proceed without re-checking. It is the single
// check path shared by the manual CheckForUpdates RPC and the background
// auto-check — ensuring every discovered update is downloadable regardless of
// which path found it.
func (f *FrontendAPI) checkAndCache(ctx context.Context, skippedVersion string) (updater.Result, error) {
	checker, err := updater.NewCheckerWithProxy(
		updater.Config{
			CurrentVersion: version.Version,
			SkippedVersion: skippedVersion,
		},
		f.proxyConfigForUpdater(),
		f.log(),
	)
	if err != nil {
		return updater.Result{}, err
	}
	result, err := checker.Check(ctx)
	if err != nil {
		return updater.Result{}, err
	}
	f.updateMu.Lock()
	f.lastCheckResult = &result
	f.downloadedArchivePath = "" // a new check invalidates any prior download
	f.updateMu.Unlock()
	return result, nil
}

// CheckForUpdates queries GitHub for the latest release and reports whether an
// update is available for the current platform. The result is cached so the
// subsequent DownloadUpdate call does not need to re-check. Emits
// update:available, update:none, or update:error.
//
// The operator-level master gate (config.yaml updates.enabled) is the sole
// enable/disable switch: when it is false, this method short-circuits and
// reports no update (without touching the network).
func (f *FrontendAPI) CheckForUpdates() (*UpdateInfo, error) {
	current := version.Version

	if !f.operatorUpdateCheckEnabled() {
		info := &UpdateInfo{Available: false, CurrentVersion: current}
		f.emitEvent(EventUpdateNone, info)
		return info, nil
	}

	result, err := f.checkAndCache(f.ctx(), f.loadSkippedVersion())
	if err != nil {
		f.emitUpdateError(err.Error())
		return nil, err
	}

	info := &UpdateInfo{
		Available:      result.Available,
		CurrentVersion: result.CurrentVersion,
		LatestVersion:  result.LatestVersion,
		ReleaseNotes:   result.ReleaseNotes,
		PublishedAt:    result.PublishedAt,
		HTMLURL:        result.HTMLURL,
		AssetName:      result.AssetName,
	}

	if result.Available {
		f.emitEvent(EventUpdateAvailable, info)
	} else {
		f.emitEvent(EventUpdateNone, info)
	}
	return info, nil
}

// DownloadUpdate fetches and integrity-verifies the release archive selected by
// the most recent CheckForUpdates into the update-staging directory. Progress
// is streamed as update:progress events. Emits update:downloaded on success or
// update:error on failure.
func (f *FrontendAPI) DownloadUpdate() error {
	f.updateMu.Lock()
	result := f.lastCheckResult
	f.updateMu.Unlock()

	if result == nil || !result.Available || result.AssetURL == "" {
		err := errors.New("no update available; call CheckForUpdates first")
		f.emitUpdateError(err.Error())
		return err
	}

	stagingDir := config.UpdateStagingDir(f.agentDir)
	sumsURL := sumsURLFor(result.AssetURL)

	// Build a proxy-aware HTTP client (mirroring CheckForUpdates via
	// checkAndCache) so the archive download honours corporate proxies and
	// custom CA bundles — a nil client would fall back to a bare transport and
	// silently fail behind a proxy.
	client, err := proxy.BuildClient(f.proxyConfigForUpdater(), downloadTimeout, f.log())
	if err != nil {
		f.emitUpdateError("build proxy client: " + err.Error())
		return err
	}
	dl := updater.NewDownloader(client, nil)

	progress := func(done, total int64) {
		f.emitEvent(EventUpdateProgress, UpdateProgress{Done: done, Total: total})
	}

	dlResult, err := dl.Download(f.ctx(), result.AssetURL, sumsURL, result.AssetName, stagingDir, progress)
	if err != nil {
		f.emitUpdateError(err.Error())
		return err
	}

	f.updateMu.Lock()
	f.downloadedArchivePath = dlResult.ArchivePath
	f.updateMu.Unlock()

	f.log().Info("update downloaded and verified",
		"archive", dlResult.ArchivePath, "bytes", dlResult.Bytes)
	f.emitEvent(EventUpdateDownloaded, map[string]string{
		"archive": dlResult.ArchivePath,
	})
	return nil
}

// ApplyUpdate prepares the self-update re-exec: it stages a copy of the
// running binary, copies the downloaded archive next to it, and launches the
// staging updater as a detached process. The updater waits for this process to
// exit (via --pid), then atomically swaps the install tree and relaunches.
//
// After launching the updater, ApplyUpdate triggers a coordinated graceful
// quit (wailsRuntime.Quit) so the Wails Shutdown hooks run before the process
// exits — it never force-exits. Returns an error (without quitting) when any
// preparation step fails.
func (f *FrontendAPI) ApplyUpdate() error {
	f.updateMu.Lock()
	archivePath := f.downloadedArchivePath
	f.updateMu.Unlock()

	if archivePath == "" {
		err := errors.New("no downloaded update to apply; run DownloadUpdate first")
		f.emitUpdateError(err.Error())
		return err
	}
	if _, err := os.Stat(archivePath); err != nil {
		f.emitUpdateError("downloaded archive missing: " + err.Error())
		return err
	}

	target, err := updater.DiscoverInstallRoot()
	if err != nil {
		f.emitUpdateError(err.Error())
		return err
	}

	stagingDir, updaterPath, err := updater.PrepareSelfUpdate()
	if err != nil {
		f.emitUpdateError(err.Error())
		return err
	}

	// Copy the verified archive into the staging dir so the updater's
	// findStagedArchive(stageDir) locates it.
	destArchive := filepath.Join(stagingDir, filepath.Base(archivePath))
	if err := copyFileForUpdate(archivePath, destArchive); err != nil {
		_ = os.RemoveAll(stagingDir)
		f.emitUpdateError(err.Error())
		return err
	}

	pid := os.Getpid()
	args := updater.SelfUpdateArgs(pid, stagingDir, target)

	f.log().Info("launching self-update re-exec",
		"updater", updaterPath, "pid", pid, "target", target)

	// Launch the staging updater detached so it survives this process's exit.
	// The updater polls the parent PID until it dies, then swaps + relaunches.
	// context.Background() is intentional: the app context is cancelled on the
	// graceful quit that follows, which would kill a CommandContext tied to it.
	cmd := exec.CommandContext(context.Background(), updaterPath, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(stagingDir)
		f.emitUpdateError("launch updater: " + err.Error())
		return err
	}

	// Coordinated quit: let the frontend/Wails shut down gracefully so the
	// process exits naturally and the waiting updater proceeds. We do NOT
	// os.Exit here — that would skip Shutdown hooks.
	if f.quitApp != nil {
		f.quitApp()
	}
	return nil
}

// SkipVersion records that the user dismissed the given release tag, so the
// checker suppresses it until a newer release is published. An empty version
// clears the skip. Persists immediately to update_state.json (preserving the
// last-check timestamp) and invalidates any cached check result so the next
// CheckForUpdates reflects it.
func (f *FrontendAPI) SkipVersion(ver string) error {
	if f.agentDir == "" {
		return errors.New("agent directory not configured")
	}
	statePath := config.UpdateStatePath(f.agentDir)
	state, _ := updater.LoadState(statePath) // any read error → zero state
	state.SkippedVersion = ver
	if err := updater.SaveState(statePath, state); err != nil {
		f.emitUpdateError(err.Error())
		return err
	}

	// Invalidate the cached check: the next CheckForUpdates must re-evaluate
	// against the new skip preference.
	f.updateMu.Lock()
	f.lastCheckResult = nil
	f.updateMu.Unlock()

	f.log().Info("update version skip preference saved", "version", ver)
	return nil
}

// GetUpdateSettings returns the current self-update preferences (auto-check,
// skipped version) plus the running version for display and the operator-level
// master gate (operator_enabled) so the UI can reflect an
// administrator-disabled state.
func (f *FrontendAPI) GetUpdateSettings() UpdateSettings {
	return UpdateSettings{
		AutoCheck:       f.autoCheckEnabled(),
		SkippedVersion:  f.loadSkippedVersion(),
		CurrentVersion:  version.Version,
		OperatorEnabled: f.operatorUpdateCheckEnabled(),
	}
}

// SetUpdateSettings persists the auto-check preference to config.yaml
// (updates.auto_check) and returns the resolved settings. An explicit false is
// honoured (never reset to the default). Invalidates any cached check result
// so the next CheckForUpdates reflects the new gate.
func (f *FrontendAPI) SetUpdateSettings(autoCheck bool) (UpdateSettings, error) {
	f.configMu.Lock()
	defer f.configMu.Unlock()
	if f.config == nil {
		return UpdateSettings{}, errors.New("config not initialized")
	}
	// Snapshot the previous value so it can be restored if the persist fails —
	// otherwise the in-memory config would hold the unpersisted value and the
	// UI's revert-on-failure (GetUpdateSettings) would read it back, silently
	// keeping the rejected change.
	prev := f.config.Updates.AutoCheck
	f.config.Updates.AutoCheck = &autoCheck
	if err := f.persistConfig(); err != nil {
		f.config.Updates.AutoCheck = prev
		f.emitUpdateError(err.Error())
		return UpdateSettings{}, err
	}

	// Invalidate the cached check so the new gate takes effect on the next
	// check.
	f.updateMu.Lock()
	f.lastCheckResult = nil
	f.updateMu.Unlock()

	f.log().Info("update settings saved", "auto_check", autoCheck)
	// The write lock is still held here, so read the operator gate directly
	// from f.config instead of operatorUpdateCheckEnabled() (which takes the
	// read lock and would deadlock).
	operatorEnabled := f.config.Updates.Enabled == nil || *f.config.Updates.Enabled
	return UpdateSettings{
		AutoCheck:       autoCheck,
		SkippedVersion:  f.loadSkippedVersion(),
		CurrentVersion:  version.Version,
		OperatorEnabled: operatorEnabled,
	}, nil
}

// backgroundCheckTimeout bounds the background update check so a dead network
// connection can never leave the goroutine lingering.
const backgroundCheckTimeout = 20 * time.Second

// defaultCheckInterval is the fallback interval used when the configured
// updates.check_interval is missing or unparseable (mirrors config defaults).
const defaultCheckInterval = 6 * time.Hour

// RunBackgroundUpdateCheck performs a single gated background update check,
// intended to be called once at startup (from desktop) in a goroutine. It is
// the SOLE automatic check path: it honours the operator master gate
// (config.yaml updates.enabled) and the auto-check preference (config.yaml
// updates.auto_check), respects the check interval and skipped version
// recorded in update_state.json, caches the result via checkAndCache (so any
// discovered update is immediately downloadable), and emits update:available
// when one is found.
//
// Network failures are swallowed (logged at debug) and never break startup.
func (f *FrontendAPI) RunBackgroundUpdateCheck() {
	log := f.log()

	// Operator gate (config.yaml): when an administrator disables updates, the
	// background check never runs.
	if !f.operatorUpdateCheckEnabled() {
		log.Debug("automatic update check disabled by config (updates.enabled=false)")
		return
	}

	// Auto-check gate (config.yaml): respect the updates.auto_check
	// preference — when false only the automatic background poll is
	// suppressed; manual checks from the UI still work.
	if !f.autoCheckEnabled() {
		log.Debug("automatic update check disabled by config (updates.auto_check=false)")
		return
	}

	// Interval gate (update_state.json): avoid re-checking too frequently.
	f.configMu.RLock()
	checkInterval := ""
	if f.config != nil {
		checkInterval = f.config.Updates.CheckInterval
	}
	f.configMu.RUnlock()
	interval, err := time.ParseDuration(checkInterval)
	if err != nil || interval <= 0 {
		interval = defaultCheckInterval
	}
	statePath := config.UpdateStatePath(f.agentDir)
	state, _ := updater.LoadState(statePath) // any read error → zero state, never blocks
	if !updater.ShouldCheck(state.LastCheck, interval, time.Now()) {
		log.Debug("automatic update check skipped (interval not elapsed)",
			"last_check", state.LastCheck, "interval", interval)
		return
	}

	// Bounded check via the shared path — caches the result so a discovered
	// update is downloadable regardless of which path found it.
	checkCtx, cancel := context.WithTimeout(f.ctx(), backgroundCheckTimeout)
	defer cancel()
	result, err := f.checkAndCache(checkCtx, state.SkippedVersion)

	// Record that a check was attempted (success or failure) so a transient
	// network blip does not cause a retry storm on every startup. Re-read the
	// state just before saving: the network check above can take up to
	// backgroundCheckTimeout, and a SkipVersion call completed during that
	// window must not be clobbered by the stale pre-check snapshot.
	fresh, _ := updater.LoadState(statePath) // any read error → keep pre-check snapshot
	skipped := state.SkippedVersion
	if fresh.SkippedVersion != "" {
		skipped = fresh.SkippedVersion
	}
	recorded := updater.State{LastCheck: time.Now(), SkippedVersion: skipped}
	if wErr := updater.SaveState(statePath, recorded); wErr != nil {
		log.Debug("could not persist update state", "error", wErr)
	}

	if err != nil {
		// Network / rate-limit / parse error: silent skip. Never emit a
		// user-facing error event from the background path.
		log.Debug("automatic update check failed (silent skip)", "error", err)
		return
	}

	if result.Available {
		info := &UpdateInfo{
			Available:      result.Available,
			CurrentVersion: result.CurrentVersion,
			LatestVersion:  result.LatestVersion,
			ReleaseNotes:   result.ReleaseNotes,
			PublishedAt:    result.PublishedAt,
			HTMLURL:        result.HTMLURL,
			AssetName:      result.AssetName,
		}
		f.emitEvent(EventUpdateAvailable, info)
		log.Info("update available",
			"current", result.CurrentVersion, "latest", result.LatestVersion)
	} else {
		log.Debug("no update available", "current", result.CurrentVersion)
	}
}

// copyFileForUpdate copies src to dst preserving the source file mode. Used to
// place the downloaded archive beside the staging updater binary.
func copyFileForUpdate(src, dst string) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source archive: %w", err)
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source archive: %w", err)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("create destination archive: %w", err)
	}
	defer func() {
		_ = out.Close()
		if retErr != nil {
			_ = os.Remove(dst)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy archive contents: %w", err)
	}
	return nil
}
