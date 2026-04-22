// Package desktop implements the Wails desktop application layer.
// It provides lifecycle management (Startup/Shutdown), Wails event-listener
// wiring, and the native PickDirectory dialog. All frontend API methods live
// on [backend.FrontendAPI], which is embedded in [App] so that promoted
// methods are visible to the Wails binding generator.
package desktop
