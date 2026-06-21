package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// fakeNotifier records every call so the test can assert the OS layer was
// reached with the expected title/body. Concurrent-safe so the rate-limit
// test can hammer the handler from multiple goroutines without false data
// races, even though the current test does not.
type fakeNotifier struct {
	mu    sync.Mutex
	calls []struct{ title, body string }
	err   error
}

func (f *fakeNotifier) call(title, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct{ title, body string }{title, body})
	return f.err
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newNotifyManager(t *testing.T, notifier func(title, body string) error) *Manager {
	t.Helper()
	store := newFakeStore()
	resolver := &fakeResolver{mapping: map[string]string{}}
	m := NewManager(store, resolver, nil)
	m.SetNotifier(notifier)
	return m
}

func callNotify(t *testing.T, m *Manager, sessionID, title, body string) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "notify_user"
	req.Params.Arguments = map[string]any{"title": title, "body": body}
	res, err := m.handleNotifyUser(sessionID)(context.Background(), req)
	if err != nil {
		t.Fatalf("handleNotifyUser: %v", err)
	}
	return res
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("empty result")
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("non-text content: %T", res.Content[0])
	}
	return tc.Text
}

func TestNotifyUser_HappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeNotifier{}
	m := newNotifyManager(t, fake.call)

	res := callNotify(t, m, "ses-1", "Hello", "world")
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	if got := resultText(t, res); got != "ok" {
		t.Errorf("text = %q, want %q", got, "ok")
	}
	if fake.count() != 1 {
		t.Errorf("notifier called %d times, want 1", fake.count())
	}
	if c := fake.calls[0]; c.title != "Hello" || c.body != "world" {
		t.Errorf("call = %+v, want {Hello, world}", c)
	}
}

func TestNotifyUser_MissingArgs(t *testing.T) {
	t.Parallel()
	fake := &fakeNotifier{}
	m := newNotifyManager(t, fake.call)

	for _, tc := range []struct {
		name, title, body string
	}{
		{"empty title", "", "body"},
		{"empty body", "title", ""},
		{"both empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := callNotify(t, m, "ses-x", tc.title, tc.body)
			if !res.IsError {
				t.Fatalf("expected error result, got %q", resultText(t, res))
			}
		})
	}
	if fake.count() != 0 {
		t.Errorf("notifier called %d times, want 0", fake.count())
	}
}

func TestNotifyUser_RateLimit(t *testing.T) {
	t.Parallel()
	fake := &fakeNotifier{}
	m := newNotifyManager(t, fake.call)

	first := callNotify(t, m, "ses-rl", "A", "first")
	if first.IsError {
		t.Fatalf("first call errored: %s", resultText(t, first))
	}

	second := callNotify(t, m, "ses-rl", "B", "second")
	if !second.IsError {
		t.Fatalf("second call should be rate-limited; got %q", resultText(t, second))
	}
	if got := resultText(t, second); !strings.Contains(got, "rate-limited") {
		t.Errorf("error text = %q, want rate-limit phrasing", got)
	}
	if fake.count() != 1 {
		t.Errorf("notifier reached %d times, want 1 (second call must not fire)", fake.count())
	}
}

func TestNotifyUser_RateLimitIsPerSession(t *testing.T) {
	t.Parallel()
	fake := &fakeNotifier{}
	m := newNotifyManager(t, fake.call)

	// Two different sessions both fire once — no cross-session bleed.
	for _, sessionID := range []string{"ses-A", "ses-B"} {
		res := callNotify(t, m, sessionID, "T", "B")
		if res.IsError {
			t.Fatalf("session %s: errored: %s", sessionID, resultText(t, res))
		}
	}
	if fake.count() != 2 {
		t.Errorf("notifier reached %d times, want 2", fake.count())
	}
}

func TestNotifyUser_NotifierError(t *testing.T) {
	t.Parallel()
	want := errors.New("osascript missing")
	fake := &fakeNotifier{err: want}
	m := newNotifyManager(t, fake.call)

	res := callNotify(t, m, "ses-err", "T", "B")
	if !res.IsError {
		t.Fatalf("expected error result")
	}
	if got := resultText(t, res); !strings.Contains(got, want.Error()) {
		t.Errorf("error text = %q, want substring %q", got, want.Error())
	}
}
