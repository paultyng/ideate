package app

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// setupFileLogging tees slog's default logger to <logsDir>/ideate.log
// alongside stderr. Dock-launched .app bundles have no visible stderr,
// so without this file the only signal from a production crash is the
// macOS crash reporter — slog Warn/Error calls (the bulk of our
// observability) disappear. Returns a cleanup func that closes the
// file; safe to call even if setup failed (no-op when fallback to
// stderr-only).
//
// Best-effort: any failure to open the file is logged via the existing
// stderr logger and the app continues. We deliberately do not implement
// rotation in v1 — single append-only file, users can delete it if it
// grows. Add rotation when a user actually hits the size problem.
func setupFileLogging(logsDir string) func() {
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "ideate: cannot create logs dir %s: %v\n", logsDir, err)
		return func() {}
	}
	path := filepath.Join(logsDir, "ideate.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ideate: cannot open log file %s: %v\n", path, err)
		return func() {}
	}
	w := io.MultiWriter(os.Stderr, f)
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	slog.Info("file logging started", slog.String("path", path))
	return func() { _ = f.Close() }
}
