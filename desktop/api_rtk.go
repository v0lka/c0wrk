package desktop

import (
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	beRtk "github.com/user/agent/backend/rtk"
)

// CheckRtk checks if the rtk CLI tool is installed and returns its status.
func (a *App) CheckRtk() beRtk.RtkStatus {
	return beRtk.CheckRtk()
}

// InstallRtk downloads and installs the rtk CLI binary.
func (a *App) InstallRtk() error {
	progress := func(status string) {
		wailsRuntime.EventsEmit(a.ctx, EventRtkInstallProgress, status)
	}

	installPath, err := beRtk.InstallRtk(progress, nil)
	if err != nil {
		return err
	}

	a.log().Info("rtk installed", "path", installPath)

	// Hot-update the running bash tool with the new rtk path
	if a.app != nil {
		a.app.SetBashRtkPath(installPath)
	}

	// Emit updated status so RtkBanner and settings panel update immediately
	status := beRtk.CheckRtk()
	wailsRuntime.EventsEmit(a.ctx, EventRtkStatus, status)

	wailsRuntime.EventsEmit(a.ctx, EventRtkInstallProgress, "done")
	return nil
}
