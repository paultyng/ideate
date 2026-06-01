package agent

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/paultyng/ideate/internal/agent/vscreen"
)

// initialCols / initialRows match the PTY's default size in claude.go.
// The frontend resizes shortly after mount via Coordinator.Resize, which
// also forwards the new dimensions into the vscreen buffer.
const (
	initialCols = 80
	initialRows = 24
)

// Session represents a running agent subprocess attached to a PTY.
type Session struct {
	id        string
	name      string
	agentType string

	// Idea-linked session fields (M5).
	ideaSlug  string
	tempFiles []string

	// Optional sink that receives every PTY chunk in addition to outputFunc
	// + the replay ring. Used by Claude (Debug) to capture --debug output
	// to a file for post-session introspection. Closed in readLoop on exit.
	debugWriter io.WriteCloser

	mu        sync.Mutex
	status    string
	exitCode  int
	ptmx      *os.File
	cmd       *exec.Cmd
	startedAt time.Time
	done      chan struct{}

	// Virtual-screen emulator. Replaces the prior 4 MB byte ring: state
	// size is bounded by rows × cols (plus xterm.js-default scrollback),
	// so replay after a route switch or long-running session always
	// reproduces the current visible screen — never starts mid-stream.
	screen *vscreen.Buffer

	// lastWriteAt is the wall-clock nanoseconds of the most recent
	// Write call (or session start). Used by WatchInactivity to drive
	// the testagent inactivity-exit watchdog without modifying upstream.
	lastWriteAt atomic.Int64

	// lastReadAt tracks PTY OUTPUT (agent activity). Populated in drainLoop
	// on every chunk so idle-stop (idlestop.go) only fires when both
	// directions have been quiet — otherwise an agent processing a long turn
	// with no user input would be wrongly classified as idle.
	lastReadAt atomic.Int64
}

// newSession creates a Session and starts a goroutine that reads PTY output
// and forwards it to outputFunc. The goroutine closes the done channel when
// the process exits.
// outputBufSize is the number of chunks buffered between the PTY read loop
// and the output callback. If the consumer is slow, the oldest chunks are
// dropped to avoid blocking the agent process.
const outputBufSize = 256

// sessionInit carries optional per-session metadata. Fields here must be set
// before goroutines start (in newSession) — appending after construction
// would race with readLoop/Info reads.
type sessionInit struct {
	ideaSlug     string
	tempFiles    []string
	debugWriter  io.WriteCloser
	snapshotPath string // path for vscreen periodic + on-close persistence
}

func newSession(id, name, agentType string, ptmx *os.File, cmd *exec.Cmd, outputFunc OutputFunc, init sessionInit) *Session {
	screen := vscreen.New(initialCols, initialRows)
	if init.snapshotPath != "" {
		screen.SetSnapshotPath(init.snapshotPath)
	}
	s := &Session{
		id:          id,
		name:        name,
		agentType:   agentType,
		ideaSlug:    init.ideaSlug,
		tempFiles:   init.tempFiles,
		debugWriter: init.debugWriter,
		status:      StatusRunning,
		ptmx:        ptmx,
		cmd:         cmd,
		startedAt:   time.Now(),
		done:        make(chan struct{}),
		screen:      screen,
	}

	if outputFunc != nil {
		outputCh := make(chan []byte, outputBufSize)
		drainDone := make(chan struct{})
		go s.readLoop(outputCh, drainDone)
		go s.drainLoop(outputCh, outputFunc, drainDone)
	} else {
		go s.readLoop(nil, nil)
	}
	return s
}

// readLoop reads PTY output and forwards chunks to outputCh. When outputCh is
// non-nil (drainLoop path), it closes outputCh on PTY EOF and waits for
// drainDone before closing s.done — ensuring the final snapshot is written
// after all buffered output has been fed to the emulator. When outputCh is nil,
// it closes the screen directly.
func (s *Session) readLoop(outputCh chan []byte, drainDone <-chan struct{}) {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 && outputCh != nil {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case outputCh <- chunk:
			default:
				// Channel full — drop oldest, write new. Always-on Warn (not
				// gated on termLog) because a silent drop corrupts the vscreen
				// replay on resume — the user needs visibility into bursts.
				<-outputCh
				outputCh <- chunk
				slog.Warn("session output channel full — dropped oldest chunk",
					slog.String("session_id", s.id),
					slog.String("idea_slug", s.ideaSlug),
					slog.Int("new_bytes", len(chunk)))
			}
		}
		if err != nil {
			break
		}
	}

	if outputCh != nil {
		close(outputCh)
		// Wait for drainLoop to finish consuming all buffered PTY output and
		// writing the final snapshot before we close s.done.
		<-drainDone
	} else {
		// No drainLoop — close screen directly.
		s.screen.Close()
	}

	// Wait for process exit and capture exit code.
	exitCode := 0
	if err := s.cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}

	s.mu.Lock()
	if s.status == StatusRunning {
		s.status = StatusExited
	}
	s.exitCode = exitCode
	tempFiles := s.tempFiles
	s.mu.Unlock()

	// Clean up temp files created for context injection.
	cleanupTempFiles(tempFiles)

	close(s.done)
}

// drainLoop reads chunks from the output channel, feeds the vscreen
// emulator, and calls the output callback. If a debugWriter was attached
// at session construction, every chunk is also tee'd there for
// post-session introspection — closed in readLoop on session exit so the
// file flushes when the agent terminates.
// drainDone is closed after the final snapshot write so readLoop can
// sequence s.done after all output is persisted.
func (s *Session) drainLoop(outputCh <-chan []byte, outputFunc OutputFunc, drainDone chan<- struct{}) {
	defer close(drainDone)
	for chunk := range outputCh {
		s.screen.Feed(chunk)
		s.lastReadAt.Store(time.Now().UnixNano())
		if s.debugWriter != nil {
			_, _ = s.debugWriter.Write(chunk)
		}
		outputFunc(chunk)
	}
	termLog.Info("drain loop closed",
		slog.String("session_id", s.id),
		slog.String("idea_slug", s.ideaSlug))
	if s.debugWriter != nil {
		_ = s.debugWriter.Close()
	}
	// All PTY output has been fed to the emulator. Flush the final snapshot.
	// Safe to call even when Stop() already called it — Buffer.Close() is idempotent.
	s.screen.Close()
}

// Replay returns the current screen state as ANSI bytes. Suitable for
// writing into a fresh xterm.js instance to reproduce the visible state
// at the moment of the call (cursor, SGR styling, OSC 8 links, etc.).
func (s *Session) Replay() []byte {
	snap := s.screen.Snapshot()
	termLog.Info("replay snapshot",
		slog.String("session_id", s.id),
		slog.String("idea_slug", s.ideaSlug),
		slog.Int("bytes", len(snap)))
	return snap
}

// PreloadSnapshot feeds previously-persisted ANSI bytes into the
// session's vscreen so a fresh emulator picks up where the prior
// instance left off (cross-restart continuity for runners whose
// agents don't regenerate their own terminal state on resume). Call
// immediately after Session construction and BEFORE the new agent's
// first byte arrives — buffered output then concatenates naturally
// below the preloaded history.
func (s *Session) PreloadSnapshot(data []byte) {
	if len(data) == 0 {
		return
	}
	termLog.Info("preload snapshot",
		slog.String("session_id", s.id),
		slog.String("idea_slug", s.ideaSlug),
		slog.Int("bytes", len(data)))
	s.screen.Feed(data)
}

// Write sends data to the PTY (i.e. to the agent's stdin).
func (s *Session) Write(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != StatusRunning {
		return fmt.Errorf("session %s is not running", s.id)
	}
	_, err := s.ptmx.Write(data)
	if err != nil {
		return fmt.Errorf("writing to session %s: %w", s.id, err)
	}
	s.lastWriteAt.Store(time.Now().UnixNano())
	return nil
}

// LastActivity returns the later of lastWriteAt and lastReadAt, giving a
// unified "something happened" timestamp used by IdleStop to classify a
// session as idle. If neither field has been set (session just spawned), it
// returns the session's startedAt time.
func (s *Session) LastActivity() time.Time {
	w := s.lastWriteAt.Load()
	r := s.lastReadAt.Load()
	if r > w {
		w = r
	}
	if w == 0 {
		s.mu.Lock()
		t := s.startedAt
		s.mu.Unlock()
		return t
	}
	return time.Unix(0, w)
}

// WatchInactivity sends `exitBytes` to the PTY when no Write has
// happened for `duration`. Resets each time Write is called. Used by
// TestAgentRunner to recreate the in-tree testagent's
// inactivity-style --auto-exit behavior on top of upstream's
// wall-time semantics — preserves orchestrator tests that drive
// active sessions for >5s while keeping idle test sessions short.
//
// The watchdog goroutine exits when the session ends or the parent
// context is cancelled.
func (s *Session) WatchInactivity(duration time.Duration, exitBytes []byte) {
	if duration <= 0 || len(exitBytes) == 0 {
		return
	}
	s.lastWriteAt.Store(time.Now().UnixNano())
	go func() {
		// Tick at half the duration so the worst-case overshoot is
		// bounded at 1.5×. Cheap (just a few atomic loads + a clock
		// read per tick).
		t := time.NewTicker(duration / 2)
		defer t.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-t.C:
				last := time.Unix(0, s.lastWriteAt.Load())
				if time.Since(last) < duration {
					continue
				}
				s.mu.Lock()
				running := s.status == StatusRunning
				s.mu.Unlock()
				if !running {
					return
				}
				if _, err := s.ptmx.Write(exitBytes); err != nil {
					termLog.Info("inactivity exit write failed",
						slog.String("session_id", s.id),
						slog.Any("err", err))
				}
				return
			}
		}
	}()
}

// Resize changes the PTY window size and forwards the new dimensions to
// the vscreen emulator so snapshot output matches xterm.js's reflow.
func (s *Session) Resize(rows, cols uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != StatusRunning {
		return fmt.Errorf("session %s is not running", s.id)
	}
	if err := pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		return err
	}
	s.screen.Resize(int(cols), int(rows))
	termLog.Info("resize",
		slog.String("session_id", s.id),
		slog.String("idea_slug", s.ideaSlug),
		slog.Int("cols", int(cols)),
		slog.Int("rows", int(rows)))
	return nil
}

// Stop sends SIGTERM, waits up to 5 seconds, then sends SIGKILL.
func (s *Session) Stop() error {
	s.mu.Lock()
	if s.status != StatusRunning {
		s.mu.Unlock()
		return nil
	}
	s.status = StatusStopped
	pid := s.cmd.Process.Pid
	s.mu.Unlock()

	// SIGTERM the process group (negative PID).
	_ = syscall.Kill(-pid, syscall.SIGTERM)

	// Wait for exit or timeout.
	select {
	case <-s.done:
		return nil
	case <-time.After(5 * time.Second):
	}

	_ = syscall.Kill(-pid, syscall.SIGKILL)
	<-s.done

	// Close the PTY master after the process is gone.
	_ = s.ptmx.Close()
	s.screen.Close()
	return nil
}

// Info returns a frontend-facing snapshot of the session.
func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{
		ID:        s.id,
		Name:      s.name,
		AgentType: s.agentType,
		Status:    s.status,
		StartedAt: s.startedAt,
		IdeaSlug:  s.ideaSlug,
	}
}

// Wait returns a channel that is closed when the session process exits.
func (s *Session) Wait() <-chan struct{} {
	return s.done
}

// ExitCode returns the exit code after the session has finished.
// Only meaningful after Wait() has closed.
func (s *Session) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}
