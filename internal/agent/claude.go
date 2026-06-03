package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/creack/pty"

	"github.com/paultyng/ideate/internal/claudecode"
	"github.com/paultyng/ideate/internal/model"
	"github.com/paultyng/ideate/internal/store"
)

// ClaudeCodeRunner spawns the claude CLI as an interactive PTY session.
// Port is the MCP/hooks HTTP server port, set by the App on startup.
// IdeaGetter is set by the App to load idea context for system prompt injection.
// Debug toggles `--debug` on the claude invocation and tees PTY output to a
// per-session file under DebugLogDir for post-session introspection.
// DebugLogDir is required when Debug is true; otherwise debug output is
// only visible in the live terminal.
type ClaudeCodeRunner struct {
	Port       int
	IdeaGetter func(slug string) (*IdeaContext, error)
	// ConfigGetter is invoked on each Run() so config.json edits land
	// on the next session start or resume without an app restart.
	// Returns the zero value if unset.
	ConfigGetter func() store.ClaudeAgent
	Debug        bool
	DebugLogDir  string
}

// IdeaContext holds the idea data needed for context injection.
type IdeaContext struct {
	Idea    model.Idea
	AddDirs []string // additional directories for --add-dir
}

// buildClaudeEnv builds the env for a claude PTY subprocess. The parent
// env is inherited and then layered with Ideate's terminal identity
// (TERM/COLORTERM) and any user-configured overrides.
//
// TERM and COLORTERM are forced to xterm.js's emulation capability, not
// inherited. From the subprocess's perspective Ideate IS the terminal —
// whatever the parent shell happened to advertise is irrelevant. Dock
// launches inherit neither (causing claude to render monochrome); some
// shells set TERM=dumb (same outcome via a different path). User config
// (cfg.Env) still wins via last-occurrence on the env slice, so anyone
// who genuinely needs a different TERM can pin it in agents.claude.env.
func buildClaudeEnv(parentEnv []string, cfg store.ClaudeAgent) []string {
	out := make([]string, 0, len(parentEnv)+2+len(cfg.Env))
	for _, kv := range parentEnv {
		if strings.HasPrefix(kv, "TERM=") || strings.HasPrefix(kv, "COLORTERM=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "TERM=xterm-256color", "COLORTERM=truecolor")
	for k, v := range cfg.Env {
		out = append(out, k+"="+v)
	}
	return out
}

// mergeAddDirs unions two --add-dir lists, keeping the first list's
// order and dropping later entries that are already present. Used so
// per-idea AddDirs (idea root + linked repo paths) always appear
// before user-configured paths.
func mergeAddDirs(first, second []string) []string {
	seen := make(map[string]struct{}, len(first)+len(second))
	out := make([]string, 0, len(first)+len(second))
	for _, p := range first {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, p := range second {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// expandPaths runs each entry through os.ExpandEnv and substitutes a
// leading "~" with the caller's home directory. Empty post-expansion
// entries are dropped. Lets a single config.json travel across
// machines (e.g. "~/.claude/skills" / "$HOME/.claude/skills").
func expandPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	home, _ := os.UserHomeDir()
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = os.ExpandEnv(p)
		if home != "" && strings.HasPrefix(p, "~") {
			p = home + strings.TrimPrefix(p, "~")
		}
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (r *ClaudeCodeRunner) CanResumeSession() bool { return true }

// ReplaysOwnState — Claude Code reprints the prior conversation when
// invoked with --resume, so its post-resume terminal state is
// regenerated from the agent's own session log. Persisting a vscreen
// snapshot on top would double the history. Opt out.
func (r *ClaudeCodeRunner) ReplaysOwnState() bool { return true }

func (r *ClaudeCodeRunner) Run(_ context.Context, config SessionConfig, outputFunc OutputFunc) (*Session, error) {
	ccConfig := claudecode.CommandConfig{
		Name:       config.Name,
		AgentUUID:  config.AgentUUID,
		ResumeUUID: config.ResumeUUID,
		SessionID:  config.SessionID,
		IdeaSlug:   config.IdeaSlug,
		Debug:      r.Debug,
	}

	if r.Port > 0 {
		ccConfig.HooksURL = fmt.Sprintf("http://localhost:%d/hooks", r.Port)
		ccConfig.MCPURL = fmt.Sprintf("http://localhost:%d/mcp", r.Port)

		if config.IdeaSlug != "" {
			if r.IdeaGetter != nil {
				ideaCtx, err := r.IdeaGetter(config.IdeaSlug)
				if err == nil {
					ccConfig.SystemPrompt = claudecode.BuildSystemPrompt(ideaCtx.Idea)
					ccConfig.AddDirs = ideaCtx.AddDirs
				}
			}
		} else {
			// Root orchestrator — no idea context, but still inject a system
			// prompt so the agent knows it's the cross-idea workspace.
			ccConfig.SystemPrompt = claudecode.BuildOrchestratorSystemPrompt()
		}
	}

	// Layer user-configured extras on top. ConfigGetter is invoked
	// per Run so live config edits land on the next start/resume.
	var claudeCfg store.ClaudeAgent
	if r.ConfigGetter != nil {
		claudeCfg = r.ConfigGetter()
		ccConfig.AddDirs = mergeAddDirs(ccConfig.AddDirs, expandPaths(claudeCfg.AddDirs))
		ccConfig.ExtraArgs = claudeCfg.ExtraArgs
		ccConfig.Env = claudeCfg.Env
	}

	ccConfig.BinaryPath = resolveClaudeBinaryOverride(os.Getenv("IDEATE_CLAUDE_BINARY"), claudeCfg.Binary)

	cmd, tempFiles, err := claudecode.BuildCommand(ccConfig)
	if err != nil {
		return nil, err
	}

	cmd.Dir = config.WorkingDir
	cmd.Env = buildClaudeEnv(os.Environ(), claudeCfg)
	cmd.SysProcAttr = newSysProcAttr()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		cleanupTempFiles(tempFiles)
		return nil, fmt.Errorf("starting claude PTY: %w", err)
	}

	id := config.AgentUUID
	if id == "" {
		id = config.ResumeUUID
	}
	init := sessionInit{
		ideaSlug:     config.IdeaSlug,
		tempFiles:    tempFiles,
		snapshotPath: config.SnapshotPath,
	}

	if r.Debug && r.DebugLogDir != "" {
		// Sibling to the dev/build/test logs in <repo>/logs (when
		// IDEATE_LOG_DIR points there) or under <configDir>/logs in
		// production. Naming pairs with the existing log conventions and
		// gets picked up by `task clean:logs`.
		uuidForLog := config.AgentUUID
		if uuidForLog == "" {
			uuidForLog = config.ResumeUUID
		}
		if uuidForLog == "" {
			uuidForLog = id
		}
		if err := os.MkdirAll(r.DebugLogDir, 0o755); err == nil {
			path := filepath.Join(r.DebugLogDir, uuidForLog+".claudedebug.log")
			if f, err := os.Create(path); err == nil {
				init.debugWriter = f
			}
		}
	}

	return newSession(id, config.Name, config.AgentType, ptmx, cmd, outputFunc, init), nil
}

// TestAgentRunner spawns the testagent binary for integration testing.
// BinaryPath is optional — if empty, walks up from cwd looking for the
// task-built binary at cmd/ideate/build/bin/testagent. HooksURL and
// MCPURL (set by App on startup) get serialized into Claude-shaped
// settings.json + mcp-config.json temp files per session, matching
// the upstream `paultyng/testagent` claude-subcommand argv shape.
type TestAgentRunner struct {
	BinaryPath string
	HooksURL   string
	MCPURL     string
}

func (r *TestAgentRunner) CanResumeSession() bool { return true }

func (r *TestAgentRunner) Run(_ context.Context, config SessionConfig, outputFunc OutputFunc) (*Session, error) {
	bin, err := r.findBinary()
	if err != nil {
		return nil, err
	}

	// Default --think-delay (was --delay on the in-tree testagent).
	// 1s keeps the dev-mode prompt responsive without making the
	// thinking spinner invisible. TESTAGENT_DELAY env knob is kept
	// so existing scripts (Taskfile test:ui) work unchanged.
	testDelay := os.Getenv("TESTAGENT_DELAY")
	if testDelay == "" {
		testDelay = "1s"
	}
	// `claude` subcommand: explicit even though upstream defaults to
	// it on bare invocation, so an upstream restructure that changes
	// the default doesn't break us silently.
	testArgs := []string{"claude", "--name", config.Name, "--think-delay", testDelay}
	// TESTAGENT_AUTO_EXIT used to map to upstream's --auto-exit, but
	// upstream's flag fires on wall time with no input-activity reset
	// while the in-tree testagent reset on every stdin byte. The
	// orchestrator playwright tests rely on the inactivity semantics
	// — target sessions must survive long enough to receive an
	// MCP-mediated WriteToSession from another session. We restore
	// that behavior via Session.WatchInactivity below; the env var
	// remains the same duration knob, just reinterpreted on Ideate's
	// side.
	autoExitDur := time.Duration(0)
	if v := os.Getenv("TESTAGENT_AUTO_EXIT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			autoExitDur = d
		}
	}
	if config.ResumeUUID != "" {
		testArgs = append(testArgs, "--resume", config.ResumeUUID)
	} else if config.AgentUUID != "" {
		testArgs = append(testArgs, "--session-id", config.AgentUUID)
	}

	// Hooks + MCP go through Claude-shaped JSON files. Reuse the
	// same generator the real Claude runner uses so the hook URL
	// scheme + per-event mapping + custom headers stay in lockstep.
	var tempFiles []string
	if r.HooksURL != "" {
		hooksOpts := []claudecode.Option{}
		if config.IdeaSlug != "" {
			hooksOpts = append(hooksOpts, claudecode.WithHeader(claudecode.IdeaSlugHeader, config.IdeaSlug))
		} else {
			hooksOpts = append(hooksOpts, claudecode.WithHeader(claudecode.IdeaSlugHeader, model.OrchestratorSlug))
		}
		path, err := claudecode.GenerateSettingsFile(r.HooksURL, hooksOpts...)
		if err != nil {
			return nil, fmt.Errorf("generating testagent settings: %w", err)
		}
		tempFiles = append(tempFiles, path)
		testArgs = append(testArgs, "--settings", path)
	}
	if r.MCPURL != "" && config.SessionID != "" {
		path, err := claudecode.GenerateMCPConfigFile(r.MCPURL,
			claudecode.WithHeader(claudecode.SessionHeader, config.SessionID),
		)
		if err != nil {
			cleanupTempFiles(tempFiles)
			return nil, fmt.Errorf("generating testagent mcp-config: %w", err)
		}
		tempFiles = append(tempFiles, path)
		testArgs = append(testArgs, "--mcp-config", path)
	}

	cmd := exec.Command(bin, testArgs...)
	cmd.Dir = config.WorkingDir
	// Inherit env unchanged — testagent's Bubble Tea TUI parses
	// stdin differently when TERM advertises full xterm capability
	// (alt-screen, raw input mode), breaking the playwright tests
	// that drive `/exit` and other slash commands via page.keyboard.type.
	// The PTY env override for color rendering lives in buildClaudeEnv
	// and only applies to the real claude runner.
	cmd.Env = os.Environ()
	cmd.SysProcAttr = newSysProcAttr()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		cleanupTempFiles(tempFiles)
		return nil, fmt.Errorf("starting testagent PTY: %w", err)
	}

	id := config.AgentUUID
	if id == "" {
		id = config.ResumeUUID
	}
	sess := newSession(id, config.Name, config.AgentType, ptmx, cmd, outputFunc, sessionInit{
		ideaSlug:     config.IdeaSlug,
		tempFiles:    tempFiles,
		snapshotPath: config.SnapshotPath,
	})
	if autoExitDur > 0 {
		sess.WatchInactivity(autoExitDur, []byte("/exit\r"))
	}
	return sess, nil
}

// resolveClaudeBinaryOverride picks the bindisco Override path from the
// two user-supplied sources. Env wins over config so a single launch can
// target a different claude (e.g. testing a beta build) without editing
// config.json. Both empty → empty result, letting bindisco walk the
// standard tiers.
func resolveClaudeBinaryOverride(envBinary, cfgBinary string) string {
	if envBinary != "" {
		return envBinary
	}
	return cfgBinary
}

func (r *TestAgentRunner) findBinary() (string, error) {
	if r.BinaryPath != "" {
		return r.BinaryPath, nil
	}
	// Only the locally-built binary is acceptable — a `go install`ed
	// copy of testagent from another repo will not match the flag
	// surface this build expects (e.g. --transcript) and would exit
	// with a misleading "flag provided but not defined" error. Walk
	// up from cwd looking for cmd/ideate/build/bin/testagent (handles
	// wails dev from cmd/ideate/).
	dir, _ := os.Getwd()
	for range 5 {
		candidate := filepath.Join(dir, "cmd", "ideate", "build", "bin", "testagent")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("testagent binary not found in cmd/ideate/build/bin/ (run: task build:testagent)")
}
