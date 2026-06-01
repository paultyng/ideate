package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/paultyng/ideate/internal/pubsub"
)

// sleepSafetyInterval is the watchdog cadence for the sleep inhibitor.
// Recompute is normally event-driven (broker-tick on session activity,
// SetSleepEnabled flips), but a session that exits without firing its
// final Stop hook leaves Activity stuck at "active" until the next
// event nudges a recompute. A coarse periodic tick closes that gap so
// the assertion is released within at most this interval after the
// underlying state actually went idle. Cheap (a few file reads) and
// silent when the state hasn't changed.
const sleepSafetyInterval = 60 * time.Second

// SleepState is the renderable status pair surfaced to the frontend.
// Enabled tracks the user toggle; Held tracks whether the OS assertion
// is currently in effect (which only happens when Enabled && there's
// at least one busy session).
type SleepState struct {
	Enabled bool `json:"enabled"`
	Held    bool `json:"held"`
}

// EventSleepStateChanged is emitted whenever (Enabled, Held) flips.
const EventSleepStateChanged = "sleep:state-changed"

// startSleepWatcher subscribes to the app event broker and re-evaluates
// the sleep assertion on every event. Recomputation is cheap (one
// store.List + ListSessions per idea via BusyRunningSessions) and
// idempotent; running it on any event keeps the watcher logic trivial
// at the cost of a few wasted recomputes on unrelated events. A
// sleepSafetyInterval ticker also drives recompute as a backstop for
// sessions that exit without firing a Stop hook.
//
// Exits when the broker closes.
func (a *App) startSleepWatcher() {
	ch, cancel := a.events.Subscribe()
	a.sleepCancel = cancel
	a.sleepStop = make(chan struct{})
	a.sleepDone = make(chan struct{})
	ticker := time.NewTicker(sleepSafetyInterval)
	go func() {
		defer close(a.sleepDone)
		defer ticker.Stop()
		// Initial recompute so toggle-without-events still latches.
		a.recomputeSleep()
		for {
			select {
			case <-a.sleepStop:
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				a.recomputeSleep()
			case <-ticker.C:
				a.recomputeSleep()
			}
		}
	}()
}

// recomputeSleep reconciles the inhibitor with (enabled, busy-count).
// Emits sleep:state-changed only when the externally-visible (enabled,
// held) tuple actually flips — the safety-tick recompute would
// otherwise republish the same SleepState every minute and spam every
// broker subscriber.
//
// The frontend's optimistic flip in SetSleepEnabled handles the
// confirm-the-click case without needing an unconditional ack here.
func (a *App) recomputeSleep() {
	busy := len(a.BusyRunningSessions()) > 0

	a.sleepMu.Lock()
	enabled := a.sleepEnabled
	wasHeld := a.sleepInhibitor.Held()
	switch {
	case enabled && busy && !wasHeld:
		a.sleepInhibitor.Acquire("agent session active")
	case (!enabled || !busy) && wasHeld:
		a.sleepInhibitor.Release()
	}
	nowHeld := a.sleepInhibitor.Held()
	state := SleepState{Enabled: enabled, Held: nowHeld}
	changed := state != a.sleepLastEmitted
	if changed {
		a.sleepLastEmitted = state
	}
	a.sleepMu.Unlock()

	if nowHeld != wasHeld {
		slog.Info("sleep: state changed",
			slog.Bool("enabled", enabled),
			slog.Bool("held", nowHeld))
	}
	if changed && a.events != nil {
		a.events.Publish(pubsub.Event{
			Name: EventSleepStateChanged,
			Data: state,
		})
	}
}

// GetSleepState returns the current toggle state. Bound to the frontend.
func (a *App) GetSleepState() SleepState {
	a.sleepMu.Lock()
	defer a.sleepMu.Unlock()
	return SleepState{
		Enabled: a.sleepEnabled,
		Held:    a.sleepInhibitor != nil && a.sleepInhibitor.Held(),
	}
}

// SetSleepEnabled flips the user toggle. State is in-memory only —
// app restart resets to disabled. Triggers an immediate recompute so
// the assertion is acquired/released without waiting for the next
// session event.
func (a *App) SetSleepEnabled(enabled bool) {
	a.sleepMu.Lock()
	a.sleepEnabled = enabled
	a.sleepMu.Unlock()
	a.recomputeSleep()
}

// appSleepController adapts App to the mcp.SleepController interface
// so the orchestrator's set_sleep_enabled tool can flip the toggle
// without internal/mcp depending on internal/app.
type appSleepController struct{ a *App }

func (c appSleepController) SetSleepEnabled(enabled bool) { c.a.SetSleepEnabled(enabled) }
func (c appSleepController) SleepState() (enabled, held bool) {
	s := c.a.GetSleepState()
	return s.Enabled, s.Held
}

// stopSleepWatcher releases any held assertion and unsubscribes from
// the broker. Called from Shutdown so the laptop can sleep again
// even if the app crashes mid-session (caffeinate's -w fallback
// covers the SIGKILL case; this covers clean exits).
func (a *App) stopSleepWatcher(_ context.Context) {
	if a.sleepInhibitor != nil {
		a.sleepInhibitor.Release()
	}
	// Signal the watcher goroutine to exit before unsubscribing.
	// Subscribe's cancel only removes the subscription from the broker;
	// it does not close the receive channel, so without an explicit
	// quit signal the goroutine would block on <-ch until events.Close()
	// — which happens later in OnShutdown — deadlocking the close path.
	if a.sleepStop != nil {
		close(a.sleepStop)
	}
	if a.sleepCancel != nil {
		a.sleepCancel()
	}
	if a.sleepDone != nil {
		<-a.sleepDone
	}
}
