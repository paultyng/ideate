package ipc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	ipcv1 "github.com/paultyng/ideate/internal/gen/ideate/ipc/v1"
	"github.com/paultyng/ideate/internal/gen/ideate/ipc/v1/ipcv1connect"
	"github.com/paultyng/ideate/internal/version"
)

// Server is the IPC server that listens on a Unix domain socket and implements
// the IdeateService Connect handler.
type Server struct {
	listener   net.Listener
	httpServer *http.Server
	socketPath string
	startTime  time.Time

	// NavigateFunc is called by RPC handlers to tell the Wails app to navigate.
	NavigateFunc func(view string, params map[string]string)

	// SleepStateFunc returns the current (enabled, held) tuple of the
	// sleep inhibitor, surfaced via GetStatus so `ideate status` can
	// report it. nil-safe — when unset, the response leaves both
	// fields at their zero default (false).
	SleepStateFunc func() (enabled, held bool)
}

// NewServer creates a new IPC server that will listen on the default socket path.
func NewServer(navigateFn func(view string, params map[string]string)) (*Server, error) {
	sockPath, err := SocketPath()
	if err != nil {
		return nil, fmt.Errorf("resolving socket path: %w", err)
	}
	return newServer(sockPath, navigateFn), nil
}

// newServer creates a server bound to a specific socket path (used by tests).
func newServer(socketPath string, navigateFn func(view string, params map[string]string)) *Server {
	return &Server{
		socketPath:   socketPath,
		NavigateFunc: navigateFn,
	}
}

// Start begins listening on the Unix domain socket and serves Connect RPCs.
func (s *Server) Start() error {
	if err := s.cleanStaleSocket(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o700); err != nil {
		return fmt.Errorf("creating socket directory: %w", err)
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.socketPath, err)
	}
	s.listener = ln
	s.startTime = time.Now()

	mux := http.NewServeMux()
	path, handler := ipcv1connect.NewIdeateServiceHandler(s)
	mux.Handle(path, handler)

	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("ipc server", slog.Any("err", err))
		}
	}()

	return nil
}

// Shutdown gracefully stops the server and removes the socket file.
func (s *Server) Shutdown(ctx context.Context) error {
	var shutdownErr error
	if s.httpServer != nil {
		shutdownErr = s.httpServer.Shutdown(ctx)
	}
	// Always attempt to remove the socket file.
	_ = os.Remove(s.socketPath)
	return shutdownErr
}

// cleanStaleSocket checks whether another instance is already running on the
// socket. If a connection succeeds, another instance is alive and we return an
// error. If the dial fails, we remove the stale file.
func (s *Server) cleanStaleSocket() error {
	conn, err := net.DialTimeout("unix", s.socketPath, 500*time.Millisecond)
	if err != nil {
		// Nobody listening — remove stale file (ignore error if it doesn't exist).
		_ = os.Remove(s.socketPath)
		return nil
	}
	_ = conn.Close()
	return fmt.Errorf("another ideate instance is already running (socket %s is active)", s.socketPath)
}

// ---------------------------------------------------------------------------
// IdeateServiceHandler implementation
// ---------------------------------------------------------------------------

func (s *Server) OpenReview(_ context.Context, req *connect.Request[ipcv1.OpenReviewRequest]) (*connect.Response[ipcv1.OpenReviewResponse], error) {
	if s.NavigateFunc != nil {
		params := map[string]string{
			"pr":   req.Msg.GetPr(),
			"repo": req.Msg.GetRepo(),
			"base": req.Msg.GetBase(),
			"head": req.Msg.GetHead(),
		}
		if id := req.Msg.GetReviewId(); id != "" {
			params["reviewId"] = id
		}
		s.NavigateFunc("review", params)
	}
	return connect.NewResponse(&ipcv1.OpenReviewResponse{}), nil
}

func (s *Server) GetStatus(_ context.Context, _ *connect.Request[ipcv1.GetStatusRequest]) (*connect.Response[ipcv1.GetStatusResponse], error) {
	var enabled, held bool
	if s.SleepStateFunc != nil {
		enabled, held = s.SleepStateFunc()
	}
	return connect.NewResponse(&ipcv1.GetStatusResponse{
		Version:      version.Version,
		Uptime:       time.Since(s.startTime).Round(time.Second).String(),
		SleepEnabled: enabled,
		SleepHeld:    held,
	}), nil
}
