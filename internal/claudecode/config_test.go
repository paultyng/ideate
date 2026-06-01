package claudecode

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/paultyng/ideate/internal/model"
)

func TestGenerateSettingsFile(t *testing.T) {
	t.Parallel()

	path, err := GenerateSettingsFile(
		"http://localhost:9876/hooks",
		WithHeader(SessionHeader, "ses-1-test"),
	)
	if err != nil {
		t.Fatalf("GenerateSettingsFile: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parsing JSON: %v", err)
	}

	// Verify hooks exist.
	// SessionStart is intentionally absent — Claude Code v2.x doesn't
	// support HTTP hooks for it; sibling-record creation lives in
	// HandleEnd via the SessionEnd reason field.
	for _, event := range []string{"Stop", "PreToolUse", "PostToolUse", "SessionEnd", "UserPromptSubmit", "Notification", "PreCompact"} {
		matchers, ok := settings.Hooks[event]
		if !ok {
			t.Errorf("missing hook event: %s", event)
			continue
		}
		hook := matchers[0].Hooks[0]
		if !strings.Contains(hook.URL, "localhost:9876/hooks/") {
			t.Errorf("%s: URL = %q, want localhost:9876/hooks/...", event, hook.URL)
		}
		if hook.Headers[SessionHeader] != "ses-1-test" {
			t.Errorf("%s: header %s = %q, want %q", event, SessionHeader, hook.Headers[SessionHeader], "ses-1-test")
		}
	}
}

func TestGenerateSettingsFileNoHeaders(t *testing.T) {
	t.Parallel()

	path, err := GenerateSettingsFile("http://localhost:1234/hooks")
	if err != nil {
		t.Fatalf("GenerateSettingsFile: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parsing JSON: %v", err)
	}

	hook := settings.Hooks["Stop"][0].Hooks[0]
	if hook.Headers != nil {
		t.Errorf("expected no headers, got %v", hook.Headers)
	}
}

// Orchestrator sessions have no idea slug. BuildCommand must still set the
// IdeaSlugHeader to the model.OrchestratorSlug sentinel so the hook server's
// header-required guard doesn't 400 every hook.
func TestBuildCommand_OrchestratorSentinelHeader(t *testing.T) {
	t.Parallel()

	cmd, tempFiles, err := BuildCommand(CommandConfig{
		Name:      "test",
		AgentUUID: "00000000-0000-0000-0000-000000000000",
		HooksURL:  "http://localhost:9876/hooks",
		// IdeaSlug deliberately empty — orchestrator path.
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	t.Cleanup(func() {
		for _, p := range tempFiles {
			_ = os.Remove(p)
		}
	})
	if cmd == nil {
		t.Fatal("cmd is nil")
	}

	// Find the --settings file path in the args, then read it.
	var settingsPath string
	for i, a := range cmd.Args {
		if a == "--settings" && i+1 < len(cmd.Args) {
			settingsPath = cmd.Args[i+1]
			break
		}
	}
	if settingsPath == "" {
		t.Fatal("BuildCommand did not pass --settings")
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings: %v", err)
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parsing settings: %v", err)
	}
	hook := settings.Hooks["Stop"][0].Hooks[0]
	if hook.Headers[IdeaSlugHeader] != model.OrchestratorSlug {
		t.Errorf("IdeaSlug header = %q, want %q", hook.Headers[IdeaSlugHeader], model.OrchestratorSlug)
	}
}

// BuildCommand's ExtraArgs / AddDirs composition.
// The Ideate-managed flag block stays in front; user extras land last
// so the claude CLI's last-occurrence-wins rule lets users override.
func TestBuildCommand_ExtraArgsAndAddDirs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		cfg          CommandConfig
		wantContains [][]string // each inner slice must appear in order in cmd.Args
		wantTail     []string   // exact final tail of cmd.Args after the claude path
	}{
		{
			name: "no extras leaves argv unchanged at the tail",
			cfg: CommandConfig{
				Name:      "test",
				AgentUUID: "uuid-1",
			},
			wantTail: []string{"-n", "test", "--session-id", "uuid-1"},
		},
		{
			name: "extra --add-dir appears as a separate trailing --add-dir",
			cfg: CommandConfig{
				Name:      "test",
				AgentUUID: "uuid-2",
				AddDirs:   []string{"/idea/root"},
				ExtraArgs: []string{"--add-dir", "/tmp/x"},
			},
			wantContains: [][]string{
				{"--add-dir", "/idea/root"},
				{"--add-dir", "/tmp/x"},
			},
			wantTail: []string{"--add-dir", "/tmp/x"},
		},
		{
			name: "Ideate --debug plus user --model both present; --model at end",
			cfg: CommandConfig{
				Name:      "test",
				AgentUUID: "uuid-3",
				Debug:     true,
				ExtraArgs: []string{"--model", "claude-opus-4-7-20260219"},
			},
			wantContains: [][]string{
				{"--debug"},
				{"--model", "claude-opus-4-7-20260219"},
			},
			wantTail: []string{"--debug", "--model", "claude-opus-4-7-20260219"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, tempFiles, err := BuildCommand(tc.cfg)
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			t.Cleanup(func() {
				for _, p := range tempFiles {
					_ = os.Remove(p)
				}
			})

			// cmd.Args[0] is the claude binary path; the rest is the argv.
			argv := cmd.Args[1:]

			for _, want := range tc.wantContains {
				if !containsInOrder(argv, want) {
					t.Errorf("argv missing %v (in order); got %v", want, argv)
				}
			}

			if tail := argv[len(argv)-len(tc.wantTail):]; !slicesEqual(tail, tc.wantTail) {
				t.Errorf("argv tail = %v, want %v (full argv: %v)", tail, tc.wantTail, argv)
			}
		})
	}
}

// containsInOrder reports whether the elements of want appear in argv as a
// contiguous run.
func containsInOrder(argv, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(argv); i++ {
		if slicesEqual(argv[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGenerateMCPConfigFile(t *testing.T) {
	t.Parallel()

	path, err := GenerateMCPConfigFile(
		"http://localhost:5555/mcp",
		WithHeader(SessionHeader, "ses-2-test"),
	)
	if err != nil {
		t.Fatalf("GenerateMCPConfigFile: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	var config MCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parsing JSON: %v", err)
	}

	ideate, ok := config.MCPServers["ideate"]
	if !ok {
		t.Fatal("ideate server missing")
	}
	if ideate.URL != "http://localhost:5555/mcp" {
		t.Errorf("url = %q", ideate.URL)
	}
	if ideate.Headers[SessionHeader] != "ses-2-test" {
		t.Errorf("header %s = %q, want %q", SessionHeader, ideate.Headers[SessionHeader], "ses-2-test")
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	t.Parallel()

	idea := model.Idea{
		Name:   "Batch Processing",
		Status: model.StatusActive,
		Resources: []model.Resource{
			{Type: "github_pr", URL: "https://github.com/owner/repo/pull/1", Label: "Core PR", Status: "approved"},
			{Type: "notion", Label: "Design doc"},
		},
	}

	prompt := BuildSystemPrompt(idea)

	if !strings.Contains(prompt, "Batch Processing") {
		t.Error("prompt missing idea name")
	}
	if !strings.Contains(prompt, "active") {
		t.Error("prompt missing status")
	}
	// Resources are intentionally NOT inlined; the agent fetches them via
	// list_resources on demand. Ensure they don't leak into the prompt.
	if strings.Contains(prompt, "Core PR") {
		t.Error("prompt should not inline resource labels — agent fetches via list_resources")
	}
	if strings.Contains(prompt, "Current branch") {
		t.Error("prompt should not include a branch line — sessions are idea-rooted")
	}
}

func TestBuildSystemPromptFileScopeGuidance(t *testing.T) {
	t.Parallel()
	idea := model.Idea{Name: "Scope Test", Status: model.StatusActive}
	prompt := BuildSystemPrompt(idea)
	for _, want := range []string{
		"<file-scope>",
		"link_repo",
		"Do not navigate to a canonical clone",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("file-scope missing %q in prompt:\n%s", want, prompt)
		}
	}
}

func TestBuildSystemPrompt_ReviewPointerPresent(t *testing.T) {
	t.Parallel()
	prompt := BuildSystemPrompt(model.Idea{Name: "Review Pointer", Status: model.StatusActive})
	for _, want := range []string{
		"<reviews>",
		"request_diff_review",
		"request_markdown_review",
		"cancel_review",
		"long-polls",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("review pointer missing %q in prompt:\n%s", want, prompt)
		}
	}
	// Skill names are no longer referenced — review surface is MCP tools.
	for _, unwanted := range []string{
		"request-diff-review",
		"request-markdown-review",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("prompt should not reference removed skill name %q:\n%s", unwanted, prompt)
		}
	}
}

func TestBuildSystemPromptNoResources(t *testing.T) {
	t.Parallel()

	idea := model.Idea{
		Name:   "Simple Idea",
		Status: model.StatusPaused,
	}

	prompt := BuildSystemPrompt(idea)
	if !strings.Contains(prompt, "Simple Idea") {
		t.Error("prompt missing idea name")
	}
}

func TestBuildSystemPrompt_HasResourcesBlock(t *testing.T) {
	t.Parallel()
	prompt := BuildSystemPrompt(model.Idea{Name: "Resources Block", Status: model.StatusActive})
	for _, want := range []string{
		"<resources>",
		"PROACTIVELY",
		"add_resource",
		"GitHub PR",
		"Jira ticket",
		"Notion doc",
		"WebFetch",
		"link_repo",
		"feature flag",
		"canonical-URL dedupe",
		"</resources>",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("resources block missing %q in prompt:\n%s", want, prompt)
		}
	}
}

func TestBuildOrchestratorSystemPrompt_NoResourcesBlock(t *testing.T) {
	t.Parallel()
	prompt := BuildOrchestratorSystemPrompt()
	// Orchestrator delegates resource-creating work to child sessions;
	// it should NOT carry the <resources> pointer block.
	if strings.Contains(prompt, "<resources>") {
		t.Errorf("orchestrator prompt should not contain <resources> block:\n%s", prompt)
	}
}
