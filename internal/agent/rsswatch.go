package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// rssReader abstracts `ps` calls so tests can inject a fake without
// spawning real processes.
type rssReader func(ctx context.Context, pids []int) (map[int]int, error) // pid → rss_kb

// sessionLister is the subset of AgentCoordinator that RSSWatch needs.
// Defined at the consumer site per Go interface conventions.
type sessionLister interface {
	ActiveSessionInfo() []RSSSessionInfo
}

// RSSWatch polls per-session RSS on a fixed interval and emits structured
// slog lines. The 60 s default matches the falsifiability horizon in the
// memory-mitigations plan (session.rss.summary total_mb < 25000 over 24h).
type RSSWatch struct {
	coord    sessionLister
	interval time.Duration
	readRSS  rssReader
	logger   *slog.Logger
}

// NewRSSWatch creates an RSSWatch backed by real ps output. Pass
// interval <= 0 to disable polling entirely (env-override escape hatch).
func NewRSSWatch(coord *AgentCoordinator, interval time.Duration) *RSSWatch {
	return &RSSWatch{
		coord:    coord,
		interval: interval,
		readRSS:  execRSSReader,
		logger:   slog.Default(),
	}
}

// Start runs the polling loop until ctx is cancelled. Call in a goroutine.
func (w *RSSWatch) Start(ctx context.Context) {
	if w.interval <= 0 {
		return
	}
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *RSSWatch) tick(ctx context.Context) {
	sessions := w.coord.ActiveSessionInfo()

	totalMB := 0
	if len(sessions) > 0 {
		pids := make([]int, 0, len(sessions))
		for _, s := range sessions {
			pids = append(pids, s.PID)
		}

		rssByPID, err := w.readRSS(ctx, pids)
		if err != nil {
			w.logger.Warn("session.rss.read", slog.String("err", err.Error()))
			return
		}

		for _, s := range sessions {
			kb, ok := rssByPID[s.PID]
			if !ok {
				continue
			}
			// ps reports RSS in KB; divide by 1024 for MB to match the plan's
			// threshold unit (total_mb < 25000).
			mb := kb / 1024
			totalMB += mb
			w.logger.Info("session.rss",
				slog.String("session_id", s.UUID),
				slog.String("agent", s.AgentType),
				slog.String("idea_slug", s.IdeaSlug),
				slog.Int("pid", s.PID),
				slog.Int("rss_mb", mb),
				slog.Int("age_s", int(time.Since(s.Started).Seconds())),
			)
		}
	}

	// Emit every tick so the falsifiability metric (total_mb < 25000 over 24h)
	// is defined even in the idle state (count=0).
	w.logger.Info("session.rss.summary",
		slog.Int("count", len(sessions)),
		slog.Int("total_mb", totalMB),
	)
}

// execRSSReader runs `ps -o pid=,rss= -p <pid,...>` and parses the output.
// Both macOS and Linux ps use KB as the default rss unit, so no conversion
// is done here — callers divide by 1024 to get MB.
func execRSSReader(ctx context.Context, pids []int) (map[int]int, error) {
	if len(pids) == 0 {
		return nil, nil
	}

	pidStrs := make([]string, len(pids))
	for i, p := range pids {
		pidStrs[i] = strconv.Itoa(p)
	}

	out, err := exec.CommandContext(ctx, "ps", "-o", "pid=,rss=", "-p", strings.Join(pidStrs, ",")).Output()
	if err != nil {
		// ps exits non-zero when none of the requested PIDs exist; treat
		// that as an empty result rather than a hard error.
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) == 0 {
			return map[int]int{}, nil
		}
		return nil, fmt.Errorf("ps: %w", err)
	}

	return parsePSOutput(string(out))
}

// parsePSOutput parses lines of the form "<pid> <rss_kb>" as emitted by
// `ps -o pid=,rss=`. Malformed lines are skipped.
func parsePSOutput(output string) (map[int]int, error) {
	result := make(map[int]int)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		rss, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		result[pid] = rss
	}
	return result, nil
}
