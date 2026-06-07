package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/paultyng/ideate/internal/agent"
	"github.com/paultyng/ideate/internal/model"
)

// These tests exercise the agent-ready synchronization end-to-end
// against a real testagent process driven by the production
// agent.AgentCoordinator — no fakeResolver / fakeSessionStarter. The
// goal is to catch drift between the marker contract in agent_ready.go
// and what real PTYs actually emit during boot.
//
// They skip when:
//   - PTYs aren't available (sandboxes, CI without /dev/pts).
//   - testagent fails to build (offline builds, broken module cache).

func requirePTY(t *testing.T) {
	t.Helper()
	cmd := exec.Command("true")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("PTY not available: %v", err)
	}
	_ = ptmx.Close()
	_ = cmd.Wait()
}

func buildTestAgent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "testagent")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/paultyng/testagent")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("testagent build unavailable: %s\n%s", err, out)
	}
	return bin
}

// coordAdapter adapts *agent.AgentCoordinator to satisfy both
// SessionStarter and SessionResolver, mirroring the
// appSessionStarter + App composition production uses without
// pulling in the full App.
type coordAdapter struct {
	coord      *agent.AgentCoordinator
	workingDir string

	mu      sync.Mutex
	nextSeq int
	uuids   []string // ids assigned per StartIdeaSession call
}

func (a *coordAdapter) StartIdeaSession(slug, agentType string, resume bool) (string, error) {
	a.mu.Lock()
	a.nextSeq++
	uuid := "test-uuid-" + agentType + "-" + slug
	if resume {
		// Resume re-spawns under the same UUID as the prior session.
		if len(a.uuids) == 0 {
			a.mu.Unlock()
			return "", errors.New("no prior session to resume")
		}
		uuid = a.uuids[len(a.uuids)-1]
	} else {
		a.uuids = append(a.uuids, uuid)
	}
	a.mu.Unlock()

	cfg := agent.SessionConfig{
		Name:       "test-session",
		WorkingDir: a.workingDir,
		AgentType:  agentType,
		IdeaSlug:   slug,
	}
	if resume {
		cfg.ResumeUUID = uuid
	} else {
		cfg.AgentUUID = uuid
	}
	if _, err := a.coord.Start(context.Background(), cfg); err != nil {
		return "", err
	}
	return uuid, nil
}

func (a *coordAdapter) GetIdeaSlug(uuid string) (string, error) {
	meta, err := a.coord.GetSessionMeta(uuid)
	if err != nil {
		return "", err
	}
	return meta.IdeaSlug, nil
}

func (a *coordAdapter) IsRunning(uuid string) bool { return a.coord.IsRunning(uuid) }

func (a *coordAdapter) GetSessionReplay(uuid string) ([]byte, error) {
	return a.coord.GetSessionReplay(uuid)
}

func (a *coordAdapter) ReadSessionSnapshot(slug, uuid string) ([]byte, error) {
	return nil, nil
}

func (a *coordAdapter) Write(uuid string, data []byte) error { return a.coord.Write(uuid, data) }

func setupRealCoordinator(t *testing.T) (*Manager, *coordAdapter, *fakeStore) {
	t.Helper()
	bin := buildTestAgent(t)
	configDir := t.TempDir()
	coord := agent.NewCoordinator(configDir)
	coord.RegisterRunner("testagent", &agent.TestAgentRunner{BinaryPath: bin})

	store := newFakeStore()
	store.ideas["test-idea"] = &model.Idea{
		Slug: "test-idea", Name: "Test Idea", Status: model.StatusActive,
	}

	adapter := &coordAdapter{coord: coord, workingDir: t.TempDir()}
	m := NewManager(store, adapter, nil)
	m.SetSessionStarter(adapter)

	t.Cleanup(func() {
		coord.Shutdown(context.Background())
	})

	return m, adapter, store
}

// pollReplay polls the coordinator's vscreen replay until want appears
// or the deadline elapses. Returns the last snapshot for failure
// diagnostics.
func pollReplay(t *testing.T, adapter *coordAdapter, uuid, want string, deadline time.Duration) (found bool, last []byte) {
	t.Helper()
	dl := time.Now().Add(deadline)
	for time.Now().Before(dl) {
		snapshot, _ := adapter.GetSessionReplay(uuid)
		last = snapshot
		if strings.Contains(string(snapshot), want) {
			return true, snapshot
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false, last
}

// TestStartIdeaSession_InitialPromptDelivers spawns a real testagent
// PTY via start_idea_session with initial_prompt, then polls
// vscreen until the prompt text echoes back. Without waitForAgentReady,
// the write would race the bubbletea TUI's stdin-raw-mode setup and
// the bytes would be silently dropped — i.e. this test fails without
// the fix in tools_sessions.go.
func TestStartIdeaSession_InitialPromptDelivers(t *testing.T) {
	requirePTY(t)
	m, adapter, store := setupRealCoordinator(t)

	handler := m.handleStartIdeaSession("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"slug":           "test-idea",
		"agent_type":     "testagent",
		"initial_prompt": "ping-from-orchestrator",
	}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	uuid, _ := payload["uuid"].(string)
	if uuid == "" {
		t.Fatalf("response missing uuid: %v", payload)
	}
	if delivered, _ := payload["initial_prompt_delivered"].(bool); !delivered {
		t.Errorf("initial_prompt_delivered = %v, want true; payload=%v", payload["initial_prompt_delivered"], payload)
	}

	ok, snapshot := pollReplay(t, adapter, uuid, "ping-from-orchestrator", 15*time.Second)
	if !ok {
		t.Fatalf("initial_prompt never echoed into vscreen:\n%s", string(snapshot))
	}

	// Audit log: the session_initial_prompt event should have landed.
	var found bool
	for _, ev := range store.history {
		if ev.Event == "session_initial_prompt" && ev.Session == uuid {
			if ev.Fields["text"] != "ping-from-orchestrator" {
				t.Errorf("history text = %v, want ping-from-orchestrator", ev.Fields["text"])
			}
			if ev.Fields["source_session"] != "orchestrator-ses" {
				t.Errorf("source_session = %v, want orchestrator-ses", ev.Fields["source_session"])
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("session_initial_prompt history event not appended; got %v", store.history)
	}
}

// TestSendSessionInput_DormantResumeWaitsForAgentReady exercises the
// send_session_input dormant-resume path against a real testagent.
// A dormant session is pre-seeded (Status=Dormant, no live PTY); the
// handler must resume it (spawning a new testagent), wait for the TUI
// to come up, then deliver the text. Without waitForAgentReady, the
// write would race boot and the test would flake.
func TestSendSessionInput_DormantResumeWaitsForAgentReady(t *testing.T) {
	requirePTY(t)
	m, adapter, store := setupRealCoordinator(t)

	// Pre-seed a dormant session record. Resolver's GetIdeaSlug must
	// find the slug; the resume call path uses the stored agent type.
	uuid := "dormant-uuid-testagent-test-idea"
	dormant := model.AgentSession{
		UUID:    uuid,
		Agent:   "testagent",
		Status:  model.SessionStatusDormant,
		Started: time.Now().Add(-1 * time.Hour),
	}
	if store.sessions == nil {
		store.sessions = map[string][]model.AgentSession{}
	}
	store.sessions["test-idea"] = []model.AgentSession{dormant}

	// Pre-populate the adapter's uuids stack so resume picks our seed.
	adapter.uuids = append(adapter.uuids, uuid)

	handler := m.handleSendSessionInput("orchestrator-ses")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"uuid": uuid,
		"text": "wake-and-receive",
	}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content[0].(mcp.TextContent).Text)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, `"resumed":true`) {
		t.Errorf("response missing resumed:true: %q", text)
	}

	ok, snapshot := pollReplay(t, adapter, uuid, "wake-and-receive", 15*time.Second)
	if !ok {
		t.Fatalf("text never landed in vscreen after dormant resume:\n%s", string(snapshot))
	}
}
