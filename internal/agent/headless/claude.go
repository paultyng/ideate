package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// ClaudeRunner implements [Runner] by invoking the user's `claude`
// CLI in non-interactive mode (`--print --output-format stream-json
// --include-partial-messages --verbose`). The runner translates
// Claude's wire format into normalized [Event]s on the fly via an
// internal goroutine; callers only ever see the normalized NDJSON.
type ClaudeRunner struct {
	// Binary, if non-empty, overrides the path to the claude CLI.
	// Defaults to "claude" (resolved via $PATH).
	Binary string

	// Subcommand, if non-empty, is inserted as the first arg before
	// the standard --print flags. Used to drive testagent's `claude`
	// subcommand (`testagent claude --print ...`) through the same
	// code path. Empty (the typical case) means a flat invocation.
	Subcommand string

	// ExtraArgs are appended to the CLI invocation after the standard
	// flags. Useful for forwarding workspace/permission flags from
	// the calling App context.
	ExtraArgs []string
}

// Run satisfies [Runner].
func (c *ClaudeRunner) Run(ctx context.Context, prompt string, opts Opts) (io.ReadCloser, error) {
	bin := c.Binary
	if bin == "" {
		bin = "claude"
	}
	var args []string
	if c.Subcommand != "" {
		args = append(args, c.Subcommand)
	}
	args = append(args,
		"--print",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
	)
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	args = append(args, c.ExtraArgs...)

	runCtx, cancel := ctxWithOptTimeout(ctx, opts.Timeout)
	cmd := exec.CommandContext(runCtx, bin, args...)
	if opts.WorkingDir != "" {
		cmd.Dir = opts.WorkingDir
	}
	cmd.Stdin = strings.NewReader(prompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claude stdout pipe: %w", err)
	}
	// Stderr is drained into a buffer so error events can include it
	// without leaking the pipe (an unread stderr eventually blocks
	// the subprocess).
	stderrBuf := &capBuffer{cap: 4096}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("claude start: %w", err)
	}

	pr, pw := io.Pipe()
	go translateClaudeStream(stdout, pw, cmd, stderrBuf, cancel)

	return pr, nil
}

// translateClaudeStream reads Claude's wire-format NDJSON from in,
// emits normalized [Event]s to out as NDJSON, then waits for the
// subprocess to exit. Closes out on completion (or on error). Runs
// in its own goroutine for the lifetime of a single Run call.
func translateClaudeStream(in io.Reader, out *io.PipeWriter, cmd *exec.Cmd, stderr *capBuffer, cancel context.CancelFunc) {
	defer cancel()
	defer func() { _ = out.Close() }()
	s := bufio.NewScanner(in)
	s.Buffer(make([]byte, 64*1024), maxFrameBytes)
	enc := json.NewEncoder(out)
	for s.Scan() {
		ev, ok := claudeWireToEvent(s.Bytes())
		if !ok {
			continue
		}
		if err := enc.Encode(ev); err != nil {
			// Reader closed — abandon the stream; cancel kills cmd.
			return
		}
	}
	if err := s.Err(); err != nil {
		_ = enc.Encode(Event{Kind: EventError, Err: fmt.Sprintf("claude scan: %v", err)})
	}
	if err := cmd.Wait(); err != nil {
		errMsg := err.Error()
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			errMsg = errMsg + ": " + tail
		}
		_ = enc.Encode(Event{Kind: EventError, Err: errMsg})
	}
}

// claudeWireToEvent maps one line of `claude --output-format
// stream-json` to a normalized [Event]. Returns (_, false) for wire
// frames that don't translate to anything user-visible (init, status,
// hook_*, rate_limit_event, periodic assistant snapshots, etc.).
func claudeWireToEvent(line []byte) (Event, bool) {
	var w claudeWire
	if err := json.Unmarshal(line, &w); err != nil {
		return Event{}, false
	}
	switch w.Type {
	case "stream_event":
		return claudeStreamEventToEvent(w.Event)
	case "result":
		// `result` always arrives as the final frame. Emit Done so
		// callers stop reading; surface is_error as EventError.
		if w.IsError {
			return Event{Kind: EventError, Err: w.Result}, true
		}
		return Event{Kind: EventDone}, true
	default:
		// system/init, system/status, system/hook_*, rate_limit_event,
		// assistant (periodic snapshot) — nothing to forward.
		return Event{}, false
	}
}

func claudeStreamEventToEvent(e claudeStreamEvent) (Event, bool) {
	switch e.Type {
	case "content_block_delta":
		switch e.Delta.Type {
		case "text_delta":
			return Event{Kind: EventTextDelta, Delta: e.Delta.Text}, true
		case "thinking_delta":
			return Event{Kind: EventThinkingDelta, Delta: e.Delta.Thinking}, true
		case "input_json_delta", "signature_delta":
			// input_json_delta accumulates a tool_use's input JSON;
			// signature_delta is opaque metadata. Neither shows up in
			// the normalized taxonomy.
			return Event{}, false
		}
	case "content_block_start":
		if e.ContentBlock.Type == "tool_use" {
			input, _ := json.Marshal(e.ContentBlock.Input)
			return Event{
				Kind: EventToolUse,
				Tool: &ToolEvent{
					ID:    e.ContentBlock.ID,
					Name:  e.ContentBlock.Name,
					Input: string(input),
				},
			}, true
		}
	case "message_stop":
		return Event{Kind: EventDone}, true
	}
	return Event{}, false
}

// claudeWire is the outer envelope of one stream-json line. Only the
// fields the runner cares about are unmarshaled; everything else is
// ignored by Go's JSON decoder.
type claudeWire struct {
	Type    string            `json:"type"`
	Event   claudeStreamEvent `json:"event"`
	Result  string            `json:"result,omitempty"`
	IsError bool              `json:"is_error,omitempty"`
}

type claudeStreamEvent struct {
	Type         string             `json:"type"`
	Index        int                `json:"index,omitempty"`
	ContentBlock claudeContentBlock `json:"content_block"`
	Delta        claudeDelta        `json:"delta"`
}

type claudeContentBlock struct {
	Type  string         `json:"type"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
	Text  string         `json:"text,omitempty"`
}

type claudeDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}
