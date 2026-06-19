package app

import (
	"testing"
)

// TestHandleOpenURL_BuffersPreStartup asserts the cold-start race
// guard: when Mac.OnUrlOpen fires before Startup wires a.ctx, the URL
// lands in the pending slice instead of crashing on a nil-ctx
// EventsEmit. Drain happens at Startup; verified by integration tests
// + the manual TESTPLAN scenarios (here we only assert the buffer
// behavior since wailsRuntime.EventsEmit requires a real Wails ctx).
func TestHandleOpenURL_BuffersPreStartup(t *testing.T) {
	t.Parallel()

	a := &App{}
	a.HandleOpenURL("ideate://orchestrator")
	a.HandleOpenURL("ideate://ideas/foo")
	a.HandleOpenURL("ideate://ideas/bar/active-session")

	a.deeplinkMu.Lock()
	defer a.deeplinkMu.Unlock()
	got := a.pendingDeeplinks
	want := []string{
		"ideate://orchestrator",
		"ideate://ideas/foo",
		"ideate://ideas/bar/active-session",
	}
	if len(got) != len(want) {
		t.Fatalf("pendingDeeplinks len = %d, want %d (got %+v)", len(got), len(want), got)
	}
	for i, url := range want {
		if got[i] != url {
			t.Errorf("pendingDeeplinks[%d] = %q, want %q", i, got[i], url)
		}
	}
}

// TestDrainPendingDeeplinks_ClearsBuffer asserts that drain consumes
// the pending slice even if dispatch is a no-op (which it is when
// a.ctx is nil, per the defensive guard in dispatchDeeplink). This
// confirms the slice doesn't keep growing across Startup invocations.
func TestDrainPendingDeeplinks_ClearsBuffer(t *testing.T) {
	t.Parallel()

	a := &App{}
	a.pendingDeeplinks = []string{"ideate://test"}
	a.drainPendingDeeplinks()

	a.deeplinkMu.Lock()
	defer a.deeplinkMu.Unlock()
	if len(a.pendingDeeplinks) != 0 {
		t.Errorf("after drain, pendingDeeplinks = %+v, want empty", a.pendingDeeplinks)
	}
}
