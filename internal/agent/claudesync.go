package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/paultyng/ideate/internal/agent/transcript/claudefmt"
	"github.com/paultyng/ideate/internal/model"
)

// ClaudeSyncStore is the small slice of FSStore that SyncClaudeSessions
// needs. Defined here at the consumer site so the agent package doesn't
// pull store internals.
type ClaudeSyncStore interface {
	List(ctx context.Context) ([]model.Idea, error)
	ListSessions(ctx context.Context, slug string) ([]model.AgentSession, error)
	WriteSessionPassive(ctx context.Context, slug, key string, session model.AgentSession) error
}

// SyncClaudeSessions reconciles Ideate's per-idea session history with
// Claude's on-disk transcripts in projectsDir.
//
// Phase A (ingest): for each idea, encode its root path the way Claude
// does and look for an unknown <session-uuid>.jsonl. Skip non-interactive
// transcripts (entrypoint != "cli"|"claude-desktop") and subagent
// transcripts (under "<uuid>/subagents/..."). Newly ingested records are
// written terminal — never running — so the M14 lock is preserved.
//
// Phase B (orphan): for each existing claude-code/claude-code-debug
// record whose backing transcript no longer exists on disk and which
// isn't running, mark StopReason=orphaned (idempotent — already-orphaned
// records are skipped).
//
// Errors during sync are logged but never fatal — sync runs as a
// best-effort startup step that must not block the app.
func SyncClaudeSessions(ctx context.Context, store ClaudeSyncStore, ideasDir, projectsDir string) error {
	if projectsDir == "" {
		return nil
	}
	if _, err := os.Stat(projectsDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat claude projects dir: %w", err)
	}

	ideas, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("listing ideas: %w", err)
	}

	// Build encoded-cwd → slug map for lookup. If two ideas encode to the
	// same project dir (extremely unlikely; would require slugs that only
	// differ by "/" vs "-") we skip ingest for that dir to avoid
	// misattribution.
	encodedToSlug := map[string]string{}
	collisions := map[string]bool{}
	for _, idea := range ideas {
		ideaPath := filepath.Join(ideasDir, idea.Slug)
		enc := claudefmt.EncodeProjectDir(ideaPath)
		if _, exists := encodedToSlug[enc]; exists {
			collisions[enc] = true
			continue
		}
		encodedToSlug[enc] = idea.Slug
	}

	if err := ingestUnknownTranscripts(ctx, store, projectsDir, ideasDir, encodedToSlug, collisions); err != nil {
		return err
	}
	return orphanMissingTranscripts(ctx, store, projectsDir, ideasDir, ideas)
}

func ingestUnknownTranscripts(
	ctx context.Context,
	store ClaudeSyncStore,
	projectsDir, ideasDir string,
	encodedToSlug map[string]string,
	collisions map[string]bool,
) error {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return fmt.Errorf("reading claude projects dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if collisions[entry.Name()] {
			slog.Warn("skipping ambiguous claude project dir",
				slog.String("dir", entry.Name()))
			continue
		}
		slug, ok := encodedToSlug[entry.Name()]
		if !ok {
			continue
		}

		existing, err := store.ListSessions(ctx, slug)
		if err != nil {
			slog.Warn("listing sessions for sync",
				slog.String("slug", slug), slog.Any("err", err))
			continue
		}
		known := map[string]bool{}
		for _, s := range existing {
			known[s.UUID] = true
		}

		projectDir := filepath.Join(projectsDir, entry.Name())
		_ = filepath.WalkDir(projectDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			// Subagent transcripts live at <uuid>/subagents/agent-*.jsonl —
			// not user-facing sessions.
			if strings.Contains(path, string(filepath.Separator)+"subagents"+string(filepath.Separator)) {
				return nil
			}

			sessUUID := strings.TrimSuffix(d.Name(), ".jsonl")
			if _, parseErr := uuid.Parse(sessUUID); parseErr != nil {
				return nil
			}
			if known[sessUUID] {
				return nil // existing record always wins
			}

			meta, parseErr := claudefmt.Parse(path)
			if parseErr != nil {
				slog.Warn("parsing transcript",
					slog.String("path", path), slog.Any("err", parseErr))
				return nil
			}
			if meta == nil || !meta.HasContent {
				return nil
			}
			if !claudefmt.IsInteractive(meta.Entrypoint) {
				return nil
			}

			ingested := buildIngestedSession(sessUUID, slug, ideasDir, meta)
			if err := store.WriteSessionPassive(ctx, slug, sessUUID, ingested); err != nil {
				slog.Warn("writing ingested session",
					slog.String("slug", slug),
					slog.String("uuid", sessUUID),
					slog.Any("err", err))
			}
			return nil
		})
	}
	return nil
}

func buildIngestedSession(sessUUID, slug, ideasDir string, meta *claudefmt.Meta) model.AgentSession {
	end := meta.Last
	status := model.SessionStatusStopped
	// Treat older transcripts as cleanly completed — their process is
	// long gone. The 24h window matches our auto-resume / crash semantics.
	if !end.IsZero() && time.Since(end) > 24*time.Hour {
		status = model.SessionStatusCompleted
	}
	return model.AgentSession{
		UUID:       sessUUID,
		Agent:      "claude-code",
		Status:     status,
		StopReason: model.SessionStopReasonExit,
		Started:    meta.First,
		Ended:      &end,
		Outcome:    "ingested from claude transcript",
		WorkingDir: filepath.Join(ideasDir, slug),
	}
}

func orphanMissingTranscripts(
	ctx context.Context,
	store ClaudeSyncStore,
	projectsDir, ideasDir string,
	ideas []model.Idea,
) error {
	for _, idea := range ideas {
		sessions, err := store.ListSessions(ctx, idea.Slug)
		if err != nil {
			slog.Warn("listing sessions for orphan sweep",
				slog.String("slug", idea.Slug), slog.Any("err", err))
			continue
		}
		ideaPath := filepath.Join(ideasDir, idea.Slug)
		encDir := filepath.Join(projectsDir, claudefmt.EncodeProjectDir(ideaPath))
		for _, s := range sessions {
			if s.Agent != "claude-code" && s.Agent != "claude-code-debug" {
				continue
			}
			if s.Status == model.SessionStatusRunning {
				continue
			}
			path := filepath.Join(encDir, s.UUID+".jsonl")
			_, statErr := os.Stat(path)
			switch {
			case statErr == nil:
				// Transcript present. If we previously marked this
				// record orphaned, the file has reappeared — clear the
				// orphan marker so resume works again.
				if s.StopReason != model.SessionStopReasonOrphaned &&
					s.Outcome != "claude transcript deleted" {
					continue
				}
				if s.StopReason == model.SessionStopReasonOrphaned {
					s.StopReason = ""
				}
				if s.Outcome == "claude transcript deleted" {
					s.Outcome = ""
				}
			case os.IsNotExist(statErr):
				if s.StopReason == model.SessionStopReasonOrphaned {
					continue
				}
				s.StopReason = model.SessionStopReasonOrphaned
				if s.Outcome == "" {
					s.Outcome = "claude transcript deleted"
				}
			default:
				continue
			}
			if err := store.WriteSessionPassive(ctx, idea.Slug, s.UUID, s); err != nil {
				slog.Warn("reconciling orphan state for session",
					slog.String("slug", idea.Slug),
					slog.String("uuid", s.UUID),
					slog.Any("err", err))
			}
		}
	}
	return nil
}
