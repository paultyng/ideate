//go:build !darwin

package sleep

// New returns a noop inhibitor on non-darwin platforms.
//
// Windows (SetThreadExecutionState) and Linux (systemd-inhibit /
// org.freedesktop.ScreenSaver) have analogous APIs; wire them in here
// when those targets ship.
func New() Inhibitor { return Noop() }
