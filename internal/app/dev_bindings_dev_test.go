//go:build dev

package app

import (
	"testing"

	"github.com/paultyng/ideate/internal/model"
)

func TestForceSessionActivity(t *testing.T) {
	t.Parallel()
	a, _ := newSessionTestApp(t)

	mkIdea := func(name string) string {
		idea := &model.Idea{Name: name, Status: model.StatusActive}
		if err := a.store.Create(a.ctx, idea); err != nil {
			t.Fatalf("Create: %v", err)
		}
		return idea.Slug
	}
	write := func(slug, uuid string, status model.SessionStatus, act model.SessionActivity) {
		t.Helper()
		sess := model.AgentSession{UUID: uuid, Status: status, Activity: act}
		if err := a.store.WriteSession(a.ctx, slug, uuid, sess); err != nil {
			t.Fatalf("WriteSession: %v", err)
		}
	}
	activityOf := func(slug, uuid string) model.SessionActivity {
		t.Helper()
		s, err := a.store.ReadSession(a.ctx, slug, uuid)
		if err != nil {
			t.Fatalf("ReadSession: %v", err)
		}
		return s.Activity
	}

	slugA := mkIdea("Activity A")
	slugB := mkIdea("Activity B")
	write(slugA, "run-a", model.SessionStatusRunning, model.SessionActivityActive)
	write(slugB, "run-b", model.SessionStatusRunning, model.SessionActivityActive)
	write(slugA, "stopped-a", model.SessionStatusStopped, "")

	t.Run("sets activity on the matching running session only", func(t *testing.T) {
		if err := a.ForceSessionActivity("run-a", "waiting"); err != nil {
			t.Fatalf("ForceSessionActivity: %v", err)
		}
		if got := activityOf(slugA, "run-a"); got != model.SessionActivityWaiting {
			t.Errorf("run-a activity = %q, want waiting", got)
		}
		if got := activityOf(slugB, "run-b"); got != model.SessionActivityActive {
			t.Errorf("run-b activity = %q, want unchanged active", got)
		}
	})

	t.Run("idempotent for the same value", func(t *testing.T) {
		for range 2 {
			if err := a.ForceSessionActivity("run-b", "idle"); err != nil {
				t.Fatalf("ForceSessionActivity: %v", err)
			}
		}
		if got := activityOf(slugB, "run-b"); got != model.SessionActivityIdle {
			t.Errorf("run-b activity = %q, want idle", got)
		}
	})

	errCases := []struct {
		name, uuid, activity string
	}{
		{"unknown uuid", "nope", "idle"},
		{"empty uuid", "", "idle"},
		{"non-running session", "stopped-a", "idle"},
		{"invalid activity", "run-a", "bogus"},
		{"empty activity", "run-a", ""},
	}
	for _, tc := range errCases {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			if err := a.ForceSessionActivity(tc.uuid, tc.activity); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	t.Run("rejected calls leave state untouched", func(t *testing.T) {
		if got := activityOf(slugA, "run-a"); got != model.SessionActivityWaiting {
			t.Errorf("run-a activity = %q, want waiting", got)
		}
		if got := activityOf(slugA, "stopped-a"); got != "" {
			t.Errorf("stopped-a activity = %q, want empty", got)
		}
	})
}
