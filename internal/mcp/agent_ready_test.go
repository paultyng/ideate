package mcp

import (
	"testing"
)

// matchAgentReadyMarker is the pure-function predicate driving
// waitForAgentReady. Test the predicate exhaustively here — the
// polling loop itself is exercised by the real-PTY integration tests
// against testagent (no fakeResolver / crafted-snapshot drift).
func TestMatchAgentReadyMarker(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		data       []byte
		wantMatch  bool
		wantMarker string
	}{
		{
			name:       "empty input",
			data:       []byte(""),
			wantMatch:  false,
			wantMarker: "",
		},
		{
			name:       "claude prompt glyph plus space",
			data:       []byte("some output\n❯ \n"),
			wantMatch:  true,
			wantMarker: "❯ ",
		},
		{
			name:       "claude prompt glyph without trailing space — not a match (would be 'Try…' hint)",
			data:       []byte("❯Try"),
			wantMatch:  false,
			wantMarker: "",
		},
		{
			name:       "testagent mcp-connected lifecycle marker",
			data:       []byte("[mcp connected: 31 tools]"),
			wantMatch:  true,
			wantMarker: "mcp connected:",
		},
		{
			name:       "marker embedded inside ANSI escape context",
			data:       []byte("\x1b[1m❯ \x1b[0msome more output"),
			wantMatch:  true,
			wantMarker: "❯ ",
		},
		{
			name:       "neither marker present",
			data:       []byte("starting...\nloading config\n"),
			wantMatch:  false,
			wantMarker: "",
		},
		{
			name:       "both markers present — earlier-listed marker wins",
			data:       []byte("[mcp connected: 31 tools]\n❯ "),
			wantMatch:  true,
			wantMarker: "❯ ", // ❯ is checked first in agentReadyMarkers
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			marker, ok := matchAgentReadyMarker(tc.data)
			if ok != tc.wantMatch {
				t.Errorf("match=%v, want %v", ok, tc.wantMatch)
			}
			if marker != tc.wantMarker {
				t.Errorf("marker=%q, want %q", marker, tc.wantMarker)
			}
		})
	}
}
