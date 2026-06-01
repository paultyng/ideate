package headless

import "testing"

func TestClaudeWireToEvent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
		want Event
		ok   bool
	}{
		{
			name: "text_delta",
			line: `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hey Paul"}}}`,
			want: Event{Kind: EventTextDelta, Delta: "Hey Paul"},
			ok:   true,
		},
		{
			name: "thinking_delta",
			line: `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The user wants"}}}`,
			want: Event{Kind: EventThinkingDelta, Delta: "The user wants"},
			ok:   true,
		},
		{
			name: "tool_use start",
			line: `{"type":"stream_event","event":{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"ls"}}}}`,
			want: Event{Kind: EventToolUse, Tool: &ToolEvent{ID: "toolu_01", Name: "Bash", Input: `{"command":"ls"}`}},
			ok:   true,
		},
		{
			name: "message_stop emits done",
			line: `{"type":"stream_event","event":{"type":"message_stop"}}`,
			want: Event{Kind: EventDone},
			ok:   true,
		},
		{
			name: "result success emits done",
			line: `{"type":"result","subtype":"success","is_error":false,"result":"Hey Paul.","session_id":"s1"}`,
			want: Event{Kind: EventDone},
			ok:   true,
		},
		{
			name: "result error emits error",
			line: `{"type":"result","subtype":"error","is_error":true,"result":"context_window_exceeded"}`,
			want: Event{Kind: EventError, Err: "context_window_exceeded"},
			ok:   true,
		},
		{
			name: "init system event drops",
			line: `{"type":"system","subtype":"init","cwd":"/tmp"}`,
			ok:   false,
		},
		{
			name: "rate limit drops",
			line: `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
			ok:   false,
		},
		{
			name: "signature_delta drops",
			line: `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque"}}}`,
			ok:   false,
		},
		{
			name: "input_json_delta drops",
			line: `{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}}`,
			ok:   false,
		},
		{
			name: "content_block_stop drops",
			line: `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
			ok:   false,
		},
		{
			name: "garbage drops",
			line: `not json at all`,
			ok:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, ok := claudeWireToEvent([]byte(c.line))
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if got.Kind != c.want.Kind {
				t.Errorf("kind = %v, want %v", got.Kind, c.want.Kind)
			}
			if got.Delta != c.want.Delta {
				t.Errorf("delta = %q, want %q", got.Delta, c.want.Delta)
			}
			if got.Err != c.want.Err {
				t.Errorf("err = %q, want %q", got.Err, c.want.Err)
			}
			if c.want.Tool != nil {
				if got.Tool == nil {
					t.Fatalf("tool = nil, want %+v", c.want.Tool)
				}
				if got.Tool.ID != c.want.Tool.ID ||
					got.Tool.Name != c.want.Tool.Name ||
					got.Tool.Input != c.want.Tool.Input {
					t.Errorf("tool = %+v, want %+v", got.Tool, c.want.Tool)
				}
			}
		})
	}
}
