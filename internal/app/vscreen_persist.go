package app

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/paultyng/ideate/internal/agent"
	"github.com/paultyng/ideate/internal/atomicfile"
	"github.com/paultyng/ideate/internal/model"
)

// vscreenSnapshotFilename is the per-session file that holds the
// rendered ANSI snapshot of the prior life's vscreen. Sits alongside
// the session JSON record under <ideasDir>/<slug>/sessions/.
func vscreenSnapshotPath(ideasDir, slug, uuid string) string {
	if slug == "" {
		slug = model.OrchestratorSlug
	}
	return filepath.Join(ideasDir, slug, "sessions", uuid+".vscreen.ansi")
}

// sessionSnapshotPath returns the path for the Buffer-level snapshot
// written by vscreen.Buffer.Close() and periodically during the session.
// This is separate from vscreenSnapshotPath (which the app writes on
// graceful app shutdown) so Task 3a's dormant-view reader has a file
// even when the app was killed unexpectedly.
func sessionSnapshotPath(ideasDir, slug, uuid string) string {
	if slug == "" {
		slug = model.OrchestratorSlug
	}
	return filepath.Join(ideasDir, slug, "sessions", uuid+".snapshot.ans")
}

// vscreenSnapshotMetaPath is the sidecar holding the cols/rows that
// the snapshot was laid out for. Restored before preload on resume so
// the historic content lands on the same grid (cursor + column anchors
// stay aligned) instead of being reflowed into the default 24x80.
func vscreenSnapshotMetaPath(ideasDir, slug, uuid string) string {
	if slug == "" {
		slug = model.OrchestratorSlug
	}
	return filepath.Join(ideasDir, slug, "sessions", uuid+".vscreen.meta.json")
}

// vscreenSnapshotMeta is the JSON shape of the sidecar file.
type vscreenSnapshotMeta struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// persistVscreenSnapshots writes a per-session vscreen snapshot to
// disk for every running session whose runner does NOT regenerate
// its own terminal state on resume (i.e. testagent and any future
// stateless agent). Runners that do regenerate (Claude Code via
// --resume) are skipped — preloading on top would double the
// rendered history. Best-effort: one bad write must not abort the
// sweep.
func (a *App) persistVscreenSnapshots() {
	entries := a.coordinator.SnapshotAllRunningForPersistence()
	for _, e := range entries {
		runner := a.coordinator.GetRunner(e.AgentType)
		if runner == nil {
			continue
		}
		if r, ok := runner.(agent.AgentStateReplayer); ok && r.ReplaysOwnState() {
			continue
		}
		path := vscreenSnapshotPath(a.ideasDir, e.IdeaSlug, e.UUID)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			slog.Warn("creating sessions dir for vscreen snapshot",
				slog.String("uuid", e.UUID), slog.Any("err", err))
			continue
		}
		if err := atomicfile.Write(path, e.Snapshot, 0o644); err != nil {
			slog.Warn("writing vscreen snapshot",
				slog.String("uuid", e.UUID), slog.Any("err", err))
			continue
		}
		if e.Cols > 0 && e.Rows > 0 {
			metaBytes, err := json.Marshal(vscreenSnapshotMeta{Cols: e.Cols, Rows: e.Rows})
			if err == nil {
				if err := atomicfile.Write(vscreenSnapshotMetaPath(a.ideasDir, e.IdeaSlug, e.UUID), metaBytes, 0o644); err != nil {
					slog.Warn("writing vscreen snapshot meta",
						slog.String("uuid", e.UUID), slog.Any("err", err))
				}
			}
		}
	}
}

// loadAndConsumeVscreenSnapshot reads the persisted snapshot for a
// (slug, uuid) pair and removes the file on success — a snapshot is
// "consumed" by exactly one resume so a second restart without
// intervening output doesn't double-preload. Returns nil bytes if
// the file doesn't exist or is empty. cols/rows are returned from the
// sidecar meta file when present; zero values mean "no meta, keep the
// default 24x80 grid".
func (a *App) loadAndConsumeVscreenSnapshot(slug, uuid string) (data []byte, cols, rows int) {
	path := vscreenSnapshotPath(a.ideasDir, slug, uuid)
	bytes, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("reading vscreen snapshot",
				slog.String("path", path), slog.Any("err", err))
		}
		return nil, 0, 0
	}
	if err := os.Remove(path); err != nil {
		slog.Warn("removing consumed vscreen snapshot",
			slog.String("path", path), slog.Any("err", err))
	}

	metaPath := vscreenSnapshotMetaPath(a.ideasDir, slug, uuid)
	if metaBytes, err := os.ReadFile(metaPath); err == nil {
		var meta vscreenSnapshotMeta
		if err := json.Unmarshal(metaBytes, &meta); err == nil {
			cols, rows = meta.Cols, meta.Rows
		}
		if err := os.Remove(metaPath); err != nil {
			slog.Warn("removing consumed vscreen snapshot meta",
				slog.String("path", metaPath), slog.Any("err", err))
		}
	}
	return bytes, cols, rows
}
