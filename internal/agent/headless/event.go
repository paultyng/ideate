// Package headless runs non-interactive agent calls (claude --print,
// codex exec --json) and streams their structured output back to
// callers as a sequence of normalized [Event]s.
//
// The package is a substrate: it returns an [io.ReadCloser] from
// [Runner.Run] (NDJSON of [Event]s, one per line) plus a pull-style
// [Decoder] that lets callers consume events at their own pace.
// Backpressure flows naturally through the OS pipe — slow consumers
// stall the subprocess on stdout write, without our code owning any
// background goroutine.
//
// Callers that want chan-style ergonomics build them on top with the
// drop policy that fits the use case (latest-only for hover captions,
// sliding window for live mirrors, no drop for summarizers). The
// substrate stays policy-free.
package headless

// EventKind is the normalized event taxonomy. Each runner adapter
// translates its agent's native wire format (Claude's stream-json,
// Codex's `exec --json`, testagent's fake stream) into this enum.
type EventKind string

const (
	// EventTextDelta carries a chunk of the assistant's user-facing
	// text output. Concatenating every TextDelta in a turn yields
	// the full reply.
	EventTextDelta EventKind = "text_delta"

	// EventThinkingDelta carries a chunk of the assistant's hidden
	// reasoning ("thinking"). Most callers will ignore this and read
	// only TextDelta — it exists so live mirrors / debug viewers can
	// see the reasoning stream without a separate channel.
	EventThinkingDelta EventKind = "thinking_delta"

	// EventToolUse fires when the assistant invokes a tool. Tool
	// carries the call's name and input. The tool's result arrives
	// as a later EventToolResult with the same Tool.ID.
	EventToolUse EventKind = "tool_use"

	// EventToolResult carries the response to a prior EventToolUse.
	// Tool.ID matches the originating tool_use.
	EventToolResult EventKind = "tool_result"

	// EventDone marks the end of the assistant's turn — the next
	// Decoder.Next call returns io.EOF.
	EventDone EventKind = "done"

	// EventError signals a non-recoverable failure mid-stream. The
	// runner closes the underlying reader after this event; callers
	// should stop reading.
	EventError EventKind = "error"
)

// Event is one normalized frame from a headless agent run. Exactly one
// of {Delta, Tool, Err} is populated based on Kind:
//
//   - Kind=text_delta | thinking_delta → Delta
//   - Kind=tool_use | tool_result → Tool
//   - Kind=error → Err
//   - Kind=done → no payload
//
// Adapters MAY drop wire events that don't fit the taxonomy (e.g.
// Claude's message_start, content_block_stop). Callers MUST NOT rely
// on every wire event being represented.
type Event struct {
	Kind  EventKind  `json:"kind"`
	Delta string     `json:"delta,omitempty"`
	Tool  *ToolEvent `json:"tool,omitempty"`
	Err   string     `json:"err,omitempty"`
}

// ToolEvent carries the payload of an EventToolUse or EventToolResult.
// For tool_use, Name and Input are set; for tool_result, Name is empty
// and Output carries the response body (often JSON-encoded).
type ToolEvent struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
	IsErr  bool   `json:"is_err,omitempty"`
}
