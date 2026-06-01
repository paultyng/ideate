package claudefmt

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// fullRecord is the slice of a Claude transcript record needed for
// text extraction. Goes beyond [Record] (which only carries metadata
// sync needs) by parsing the full message payload.
type fullRecord struct {
	Type    string            `json:"type"`
	Message fullRecordMessage `json:"message"`
	IsMeta  bool              `json:"isMeta,omitempty"`
}

type fullRecordMessage struct {
	Role       string          `json:"role"`
	StringBody string          `json:"-"`
	Blocks     []fullRecordBlk `json:"-"`
}

type fullRecordBlk struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// UnmarshalJSON handles `content` being either a string (user message,
// classic shape) or an array of block objects (assistant message,
// post-Messages-API shape).
func (m *fullRecordMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	if len(raw.Content) == 0 {
		return nil
	}
	if raw.Content[0] == '"' {
		return json.Unmarshal(raw.Content, &m.StringBody)
	}
	return json.Unmarshal(raw.Content, &m.Blocks)
}

// LastNTurns reads path and returns the text content of the most
// recent n user-or-assistant turns, formatted as
//
//	user: <text>
//	assistant: <text>
//	...
//
// Tool calls, tool results, and thinking blocks are dropped — only
// natural-language text is included. The intent is to feed the result
// into a summarizer prompt where the structural details are noise.
//
// Returns ("", nil) when the file has no qualifying records.
func LastNTurns(path string, n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	// Ring buffer: keep the last n qualifying turns.
	buf := make([]string, 0, n)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r fullRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		if r.IsMeta {
			continue
		}
		if r.Type != "user" && r.Type != "assistant" {
			continue
		}
		text := extractText(r.Message)
		if text == "" {
			continue
		}
		entry := r.Message.Role + ": " + text
		if len(buf) < n {
			buf = append(buf, entry)
			continue
		}
		copy(buf, buf[1:])
		buf[len(buf)-1] = entry
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("scanning transcript: %w", err)
	}
	return strings.Join(buf, "\n\n"), nil
}

// extractText returns the natural-language text from a message. For
// string-content messages (user prompts), it's the whole string. For
// block-content messages (assistant replies), it's every text block
// joined by a space.
func extractText(m fullRecordMessage) string {
	if m.StringBody != "" {
		return strings.TrimSpace(m.StringBody)
	}
	var parts []string
	for _, b := range m.Blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
