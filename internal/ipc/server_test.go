package ipc

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/version"
)

func TestServerClientIntegration(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "ideate.sock")

	var mu sync.Mutex
	var navigatedView string
	var navigatedParams map[string]string

	navigateFn := func(view string, params map[string]string) {
		mu.Lock()
		defer mu.Unlock()
		navigatedView = view
		navigatedParams = params
	}

	srv := newServer(sockPath, navigateFn)
	srv.SleepStateFunc = func() (bool, bool) { return true, true }

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	client := newClient(sockPath)

	// --- GetStatus ---
	status, err := client.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus() error: %v", err)
	}
	if status.GetVersion() != version.Version {
		t.Errorf("GetStatus().Version = %q, want %q", status.GetVersion(), version.Version)
	}
	if status.GetUptime() == "" {
		t.Error("GetStatus().Uptime is empty")
	}
	if !status.GetSleepEnabled() || !status.GetSleepHeld() {
		t.Errorf("GetStatus() sleep tuple = (%v, %v), want (true, true) per the test SleepStateFunc",
			status.GetSleepEnabled(), status.GetSleepHeld())
	}

	// --- OpenReview ---
	if err := client.OpenReview(context.Background(), OpenReviewArgs{
		PR:       "owner/repo#42",
		Repo:     "/tmp/repo",
		Base:     "main",
		Head:     "feature",
		ReviewID: "rev-123",
	}); err != nil {
		t.Fatalf("OpenReview() error: %v", err)
	}
	mu.Lock()
	if navigatedView != "review" {
		t.Errorf("NavigateFunc view = %q, want %q", navigatedView, "review")
	}
	if navigatedParams["pr"] != "owner/repo#42" {
		t.Errorf("NavigateFunc params[pr] = %q, want %q", navigatedParams["pr"], "owner/repo#42")
	}
	if navigatedParams["repo"] != "/tmp/repo" {
		t.Errorf("NavigateFunc params[repo] = %q, want %q", navigatedParams["repo"], "/tmp/repo")
	}
	if navigatedParams["base"] != "main" {
		t.Errorf("NavigateFunc params[base] = %q, want %q", navigatedParams["base"], "main")
	}
	if navigatedParams["head"] != "feature" {
		t.Errorf("NavigateFunc params[head] = %q, want %q", navigatedParams["head"], "feature")
	}
	mu.Unlock()

	// --- Shutdown removes socket ---
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket file still exists after shutdown")
	}
}
