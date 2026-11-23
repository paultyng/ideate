package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Not parallel: swaps the default slog logger and sets env.
func TestNew_RunsOKFMigration(t *testing.T) {
	ideasDir := t.TempDir()
	t.Setenv("IDEATE_IDEAS_DIR", ideasDir)
	t.Setenv("IDEATE_CONFIG_DIR", t.TempDir())

	legacy := "---\nname: Legacy\nstatus: active\nupdated: 2026-04-01T00:00:00Z\n---\nBody.\n"
	if err := os.MkdirAll(filepath.Join(ideasDir, "legacy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ideasDir, "legacy", "idea.md"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	New(LaunchConfig{})

	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decoding log line %q: %v", line, err)
		}
		if rec["msg"] == "store: okf migration" {
			lines = append(lines, rec)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("got %d migration log lines, want 1; log:\n%s", len(lines), buf.String())
	}
	rec := lines[0]
	if rec["level"] != "INFO" || rec["migrated"] != float64(1) {
		t.Errorf("log = %v, want INFO migrated=1", rec)
	}
	backup, _ := rec["backup"].(string)
	if backup == "" {
		t.Fatalf("backup = %v, want path", rec["backup"])
	}
	if _, err := os.Stat(filepath.Join(backup, "legacy", "idea.md")); err != nil {
		t.Errorf("backup copy missing: %v", err)
	}
}
