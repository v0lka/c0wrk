#!/bin/sh
# Launcher for the packaged c0wrk desktop app.
#
# c0wrk creates its main window hidden (StartHidden in main.go) and reveals it
# later from the startup sequence via runtime.WindowShow. When the backend
# starts fast enough, that show request lands before GTK has realized the
# window: it is lost (glib logs "assertion 'GDK_IS_WINDOW (window)' failed")
# and the window stays Withdrawn forever — the process runs, logs a clean
# startup, and never shows a window. A backend start of a few milliseconds,
# which is what an unconfigured or unparseable config.yaml produces, makes
# this likely rather than rare.
#
# Creating the window visible from the start removes the race entirely: the
# later WindowShow calls become no-ops. C0WRK_START_HIDDEN is upstream's own
# switch for this, and only the exact string "false" disables the hidden start.
#
# An explicit value from the environment always wins, so
# `C0WRK_START_HIDDEN=true c0wrk` still gets upstream's default behaviour.
#
# exec replaces this shell, so /proc/self/exe — and with it Go's
# os.Executable() — still points at /opt/c0wrk/c0wrk-desktop. The app therefore
# keeps finding libonnxruntime.so and models/ next to itself.
[ -n "${C0WRK_START_HIDDEN+x}" ] || C0WRK_START_HIDDEN=false
export C0WRK_START_HIDDEN

exec /opt/c0wrk/c0wrk-desktop "$@"
