package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeSessionLister implements sessionLister with a static list.
type fakeSessionLister struct {
	sessions []RSSSessionInfo
}

func (f *fakeSessionLister) ActiveSessionInfo() []RSSSessionInfo {
	return f.sessions
}

func TestRSSWatch_ParsesPSOutput(t *testing.T) {
	t.Parallel()

	input := "  1234  2048000\n  5678  1024000\n"
	got, err := parsePSOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[1234] != 2048000 {
		t.Errorf("pid 1234: got %d, want 2048000", got[1234])
	}
	if got[5678] != 1024000 {
		t.Errorf("pid 5678: got %d, want 1024000", got[5678])
	}
}

func TestRSSWatch_Tick_EmitsPerSessionAndSummary(t *testing.T) {
	t.Parallel()

	now := time.Now()
	coord := &fakeSessionLister{
		sessions: []RSSSessionInfo{
			{UUID: "uuid-1", AgentType: "claude-code", IdeaSlug: "idea-a", PID: 1001, Started: now.Add(-30 * time.Second)},
			{UUID: "uuid-2", AgentType: "claude-code", IdeaSlug: "idea-b", PID: 1002, Started: now.Add(-60 * time.Second)},
		},
	}

	var buf bytes.Buffer
	w := &RSSWatch{
		coord:    coord,
		interval: time.Minute,
		logger:   slog.New(slog.NewTextHandler(&buf, nil)),
		readRSS: func(_ context.Context, pids []int) (map[int]int, error) {
			return map[int]int{
				// 2048000 KB = 2000 MB; 1024000 KB = 1000 MB; total = 3000 MB
				1001: 2048000,
				1002: 1024000,
			}, nil
		},
	}

	w.tick(context.Background())

	out := buf.String()
	rssLines := countOccurrences(out, "msg=session.rss ")
	summaryLines := countOccurrences(out, "msg=session.rss.summary")
	if rssLines != 2 {
		t.Errorf("expected 2 session.rss lines, got %d\noutput:\n%s", rssLines, out)
	}
	if summaryLines != 1 {
		t.Errorf("expected 1 session.rss.summary line, got %d\noutput:\n%s", summaryLines, out)
	}
	if !strings.Contains(out, "total_mb=3000") {
		t.Errorf("expected total_mb=3000 in summary\noutput:\n%s", out)
	}
	if !strings.Contains(out, "count=2") {
		t.Errorf("expected count=2 in summary\noutput:\n%s", out)
	}
}

func TestRSSWatch_Tick_NoSessions(t *testing.T) {
	t.Parallel()

	coord := &fakeSessionLister{}

	var buf bytes.Buffer
	w := &RSSWatch{
		coord:    coord,
		interval: time.Minute,
		logger:   slog.New(slog.NewTextHandler(&buf, nil)),
		readRSS: func(_ context.Context, pids []int) (map[int]int, error) {
			t.Error("readRSS called with no sessions")
			return nil, nil
		},
	}

	w.tick(context.Background())

	out := buf.String()
	summaryLines := countOccurrences(out, "msg=session.rss.summary")
	if summaryLines != 1 {
		t.Errorf("expected 1 session.rss.summary line, got %d\noutput:\n%s", summaryLines, out)
	}
	if !strings.Contains(out, "count=0") {
		t.Errorf("expected count=0 in summary\noutput:\n%s", out)
	}
	if !strings.Contains(out, "total_mb=0") {
		t.Errorf("expected total_mb=0 in summary\noutput:\n%s", out)
	}
	if countOccurrences(out, "msg=session.rss ") != 0 {
		t.Errorf("expected no per-session session.rss lines\noutput:\n%s", out)
	}
}

func countOccurrences(s, substr string) int {
	count := 0
	for {
		idx := strings.Index(s, substr)
		if idx < 0 {
			break
		}
		count++
		s = s[idx+len(substr):]
	}
	return count
}
