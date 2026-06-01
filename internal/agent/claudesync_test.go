package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/paultyng/ideate/internal/agent/transcript/claudefmt"
	"github.com/paultyng/ideate/internal/model"
)

// fakeStore is a tiny in-memory ClaudeSyncStore for sync tests. Keyed by
// (slug, uuid). Tracks insertion / mutation count separately from final
// state so we can assert on idempotency.
type fakeStore struct {
	ideas    []model.Idea
	sessions map[string]map[string]model.AgentSession // slug → uuid → session
	writes   int
}

func newFakeStore(ideas ...model.Idea) *fakeStore {
	return &fakeStore{
		ideas:    ideas,
		sessions: make(map[string]map[string]model.AgentSession),
	}
}

func (f *fakeStore) List(_ context.Context) ([]model.Idea, error) {
	return f.ideas, nil
}

func (f *fakeStore) ListSessions(_ context.Context, slug string) ([]model.AgentSession, error) {
	m := f.sessions[slug]
	out := make([]model.AgentSession, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UUID < out[j].UUID })
	return out, nil
}

func (f *fakeStore) WriteSessionPassive(_ context.Context, slug, key string, s model.AgentSession) error {
	if f.sessions[slug] == nil {
		f.sessions[slug] = make(map[string]model.AgentSession)
	}
	f.sessions[slug][key] = s
	f.writes++
	return nil
}

// writeJSONL writes the given lines to the matching project dir for the
// given absolute idea path, creating parents as needed.
func writeJSONL(t *testing.T, projectsDir, ideaPath, sessUUID string, lines []string) {
	t.Helper()
	dir := filepath.Join(projectsDir, claudefmt.EncodeProjectDir(ideaPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, sessUUID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
}

func cliRecord(sessUUID, cwd string, ts time.Time) string {
	return `{"type":"user","sessionId":"` + sessUUID + `","cwd":"` + cwd +
		`","entrypoint":"cli","timestamp":"` + ts.Format(time.RFC3339Nano) + `"}`
}

func TestSyncClaude_IngestsCliTranscript(t *testing.T) {
	t.Parallel()
	ideasDir := t.TempDir()
	projectsDir := t.TempDir()
	store := newFakeStore(model.Idea{Slug: "2026-05-04-foo", Name: "Foo"})

	ideaPath := filepath.Join(ideasDir, "2026-05-04-foo")
	sessUUID := "11111111-2222-3333-4444-555555555555"
	now := time.Now().Add(-1 * time.Hour)
	writeJSONL(t, projectsDir, ideaPath, sessUUID, []string{
		cliRecord(sessUUID, ideaPath, now),
		cliRecord(sessUUID, ideaPath, now.Add(5*time.Minute)),
	})

	if err := SyncClaudeSessions(context.Background(), store, ideasDir, projectsDir); err != nil {
		t.Fatalf("SyncClaudeSessions: %v", err)
	}

	got, _ := store.ListSessions(context.Background(), "2026-05-04-foo")
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	s := got[0]
	if s.UUID != sessUUID {
		t.Errorf("UUID = %q, want %q", s.UUID, sessUUID)
	}
	if s.Agent != "claude-code" {
		t.Errorf("Agent = %q, want claude-code", s.Agent)
	}
	if s.Status != model.SessionStatusStopped {
		t.Errorf("Status = %q, want stopped (recent transcript)", s.Status)
	}
	if s.StopReason != model.SessionStopReasonExit {
		t.Errorf("StopReason = %q, want exit", s.StopReason)
	}
	if s.Outcome == "" {
		t.Error("Outcome is empty; want a non-empty marker")
	}
}

func TestSyncClaude_SkipsNonInteractive(t *testing.T) {
	t.Parallel()
	ideasDir := t.TempDir()
	projectsDir := t.TempDir()
	store := newFakeStore(model.Idea{Slug: "2026-05-04-foo", Name: "Foo"})

	ideaPath := filepath.Join(ideasDir, "2026-05-04-foo")
	sessUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	writeJSONL(t, projectsDir, ideaPath, sessUUID, []string{
		`{"type":"user","sessionId":"` + sessUUID + `","cwd":"` + ideaPath +
			`","entrypoint":"sdk-cli","timestamp":"` + time.Now().Format(time.RFC3339Nano) + `"}`,
	})

	if err := SyncClaudeSessions(context.Background(), store, ideasDir, projectsDir); err != nil {
		t.Fatalf("SyncClaudeSessions: %v", err)
	}
	got, _ := store.ListSessions(context.Background(), "2026-05-04-foo")
	if len(got) != 0 {
		t.Errorf("got %d sessions, want 0 (sdk-cli must be skipped)", len(got))
	}
}

func TestSyncClaude_SkipsSubagentTranscripts(t *testing.T) {
	t.Parallel()
	ideasDir := t.TempDir()
	projectsDir := t.TempDir()
	store := newFakeStore(model.Idea{Slug: "2026-05-04-foo", Name: "Foo"})

	ideaPath := filepath.Join(ideasDir, "2026-05-04-foo")
	parentUUID := "abcdef01-0000-0000-0000-000000000000"
	subDir := filepath.Join(projectsDir, claudefmt.EncodeProjectDir(ideaPath), parentUUID, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "agent-abc123.jsonl"),
		[]byte(cliRecord(parentUUID, ideaPath, time.Now())+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := SyncClaudeSessions(context.Background(), store, ideasDir, projectsDir); err != nil {
		t.Fatalf("SyncClaudeSessions: %v", err)
	}
	got, _ := store.ListSessions(context.Background(), "2026-05-04-foo")
	if len(got) != 0 {
		t.Errorf("got %d sessions, want 0 (subagent transcripts must be skipped)", len(got))
	}
}

func TestSyncClaude_ExistingRecordWins(t *testing.T) {
	t.Parallel()
	ideasDir := t.TempDir()
	projectsDir := t.TempDir()
	idea := model.Idea{Slug: "2026-05-04-foo", Name: "Foo"}
	store := newFakeStore(idea)

	ideaPath := filepath.Join(ideasDir, idea.Slug)
	sessUUID := "11111111-2222-3333-4444-555555555555"

	// Existing record with rich state — sync must not overwrite.
	existing := model.AgentSession{
		UUID:       sessUUID,
		Agent:      "claude-code",
		Status:     model.SessionStatusCompleted,
		StopReason: model.SessionStopReasonUser,
		Outcome:    "user-stopped before completion",
		Started:    time.Now().Add(-2 * time.Hour),
	}
	_ = store.WriteSessionPassive(context.Background(), idea.Slug, sessUUID, existing)
	preWrites := store.writes

	writeJSONL(t, projectsDir, ideaPath, sessUUID, []string{
		cliRecord(sessUUID, ideaPath, time.Now()),
	})

	if err := SyncClaudeSessions(context.Background(), store, ideasDir, projectsDir); err != nil {
		t.Fatalf("SyncClaudeSessions: %v", err)
	}
	got, _ := store.ListSessions(context.Background(), idea.Slug)
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if got[0].StopReason != model.SessionStopReasonUser {
		t.Errorf("StopReason = %q, want user (existing record must win)", got[0].StopReason)
	}
	if got[0].Outcome != existing.Outcome {
		t.Errorf("Outcome = %q, want %q", got[0].Outcome, existing.Outcome)
	}
	if store.writes != preWrites {
		t.Errorf("writes = %d, want %d (existing UUID must not be re-written)", store.writes, preWrites)
	}
}

func TestSyncClaude_OrphansMissingTranscript(t *testing.T) {
	t.Parallel()
	ideasDir := t.TempDir()
	projectsDir := t.TempDir()
	idea := model.Idea{Slug: "2026-05-04-foo", Name: "Foo"}
	store := newFakeStore(idea)

	// A claude session record whose transcript was never written to disk.
	sessUUID := "11111111-2222-3333-4444-555555555555"
	end := time.Now().Add(-2 * time.Hour)
	_ = store.WriteSessionPassive(context.Background(), idea.Slug, sessUUID, model.AgentSession{
		UUID:       sessUUID,
		Agent:      "claude-code",
		Status:     model.SessionStatusCompleted,
		StopReason: model.SessionStopReasonExit,
		Started:    time.Now().Add(-3 * time.Hour),
		Ended:      &end,
	})

	if err := SyncClaudeSessions(context.Background(), store, ideasDir, projectsDir); err != nil {
		t.Fatalf("SyncClaudeSessions: %v", err)
	}
	got, _ := store.ListSessions(context.Background(), idea.Slug)
	if got[0].StopReason != model.SessionStopReasonOrphaned {
		t.Errorf("StopReason = %q, want orphaned", got[0].StopReason)
	}
	if got[0].Outcome == "" {
		t.Error("Outcome should be set to a marker; got empty")
	}
}

func TestSyncClaude_UnorphansWhenTranscriptReappears(t *testing.T) {
	t.Parallel()
	ideasDir := t.TempDir()
	projectsDir := t.TempDir()
	idea := model.Idea{Slug: "2026-05-04-foo", Name: "Foo"}
	store := newFakeStore(idea)

	ideaPath := filepath.Join(ideasDir, idea.Slug)
	sessUUID := "11111111-2222-3333-4444-555555555555"
	end := time.Now().Add(-2 * time.Hour)
	_ = store.WriteSessionPassive(context.Background(), idea.Slug, sessUUID, model.AgentSession{
		UUID:       sessUUID,
		Agent:      "claude-code",
		Status:     model.SessionStatusCompleted,
		StopReason: model.SessionStopReasonOrphaned,
		Outcome:    "claude transcript deleted",
		Started:    time.Now().Add(-3 * time.Hour),
		Ended:      &end,
	})

	writeJSONL(t, projectsDir, ideaPath, sessUUID, []string{
		cliRecord(sessUUID, ideaPath, end),
	})

	if err := SyncClaudeSessions(context.Background(), store, ideasDir, projectsDir); err != nil {
		t.Fatalf("SyncClaudeSessions: %v", err)
	}
	got, _ := store.ListSessions(context.Background(), idea.Slug)
	if got[0].StopReason != "" {
		t.Errorf("StopReason = %q, want empty (transcript reappeared)", got[0].StopReason)
	}
	if got[0].Outcome != "" {
		t.Errorf("Outcome = %q, want empty (transcript reappeared)", got[0].Outcome)
	}
}

func TestSyncClaude_NeverOrphansRunning(t *testing.T) {
	t.Parallel()
	ideasDir := t.TempDir()
	projectsDir := t.TempDir()
	idea := model.Idea{Slug: "2026-05-04-foo", Name: "Foo"}
	store := newFakeStore(idea)

	sessUUID := "11111111-2222-3333-4444-555555555555"
	_ = store.WriteSessionPassive(context.Background(), idea.Slug, sessUUID, model.AgentSession{
		UUID:    sessUUID,
		Agent:   "claude-code",
		Status:  model.SessionStatusRunning,
		Started: time.Now(),
	})

	if err := SyncClaudeSessions(context.Background(), store, ideasDir, projectsDir); err != nil {
		t.Fatalf("SyncClaudeSessions: %v", err)
	}
	got, _ := store.ListSessions(context.Background(), idea.Slug)
	if got[0].Status != model.SessionStatusRunning {
		t.Errorf("Status = %q, want running (sync must never touch running records)", got[0].Status)
	}
	if got[0].StopReason != "" {
		t.Errorf("StopReason = %q, want empty", got[0].StopReason)
	}
}

func TestSyncClaude_Idempotent(t *testing.T) {
	t.Parallel()
	ideasDir := t.TempDir()
	projectsDir := t.TempDir()
	idea := model.Idea{Slug: "2026-05-04-foo", Name: "Foo"}
	store := newFakeStore(idea)

	ideaPath := filepath.Join(ideasDir, idea.Slug)
	// Two transcripts: one to ingest, one whose record exists already
	// without a transcript on disk (will be orphaned on first run).
	ingestUUID := "11111111-2222-3333-4444-555555555555"
	writeJSONL(t, projectsDir, ideaPath, ingestUUID, []string{
		cliRecord(ingestUUID, ideaPath, time.Now()),
	})
	orphanUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	end := time.Now().Add(-2 * time.Hour)
	_ = store.WriteSessionPassive(context.Background(), idea.Slug, orphanUUID, model.AgentSession{
		UUID: orphanUUID, Agent: "claude-code",
		Status: model.SessionStatusCompleted, StopReason: model.SessionStopReasonExit,
		Started: time.Now().Add(-3 * time.Hour), Ended: &end,
	})

	for i := 0; i < 5; i++ {
		if err := SyncClaudeSessions(context.Background(), store, ideasDir, projectsDir); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	got, _ := store.ListSessions(context.Background(), idea.Slug)
	if len(got) != 2 {
		t.Fatalf("got %d sessions across 5 syncs, want 2", len(got))
	}

	// After 5 runs we expect: 1 ingest (write 1), 1 orphan flip (write 1
	// for the existing record + write 1 to set orphaned). Subsequent
	// runs should be no-ops — measure the delta over the last 4 runs by
	// comparing post-run-1 to post-run-5.
	postFive := store.writes
	if err := SyncClaudeSessions(context.Background(), store, ideasDir, projectsDir); err != nil {
		t.Fatalf("run 6: %v", err)
	}
	if store.writes != postFive {
		t.Errorf("run 6 added %d writes; want 0 (idempotency)", store.writes-postFive)
	}
}

func TestSyncClaude_IgnoresNonExistingProjectsDir(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	if err := SyncClaudeSessions(context.Background(), store, t.TempDir(),
		filepath.Join(t.TempDir(), "does-not-exist")); err != nil {
		t.Fatalf("expected nil error for missing dir; got %v", err)
	}
}
