package summarizer

import (
	"context"
	"strings"
)

// SnippetGenerator is the deterministic, subprocess-free default
// Generator. Synthesizes a summary line from input alone — no LLM
// call, no cost. The output is stable for the same input.
//
// Priority (first non-empty wins):
//
//  1. Last assistant text reply in TranscriptTail (the agent's most
//     recent natural-language output). Trimmed to the first sentence
//     or 120 chars, whichever is shorter.
//  2. First sentence of IdeaBody. Same length cap.
//  3. ErrEmpty if neither source has usable text.
//
// The pattern matches what an LLM call would produce in shape — one
// short sentence — so the dashboard card layout doesn't need to know
// which generator wrote the sidecar.
type SnippetGenerator struct{}

// Generate implements [Generator].
func (SnippetGenerator) Generate(_ context.Context, in GenerateInput) (GenerateResult, error) {
	if line := snippetFromTranscript(in.TranscriptTail); line != "" {
		return GenerateResult{Line: line}, nil
	}
	if line := snippetFromBody(in.IdeaBody); line != "" {
		return GenerateResult{Line: line}, nil
	}
	return GenerateResult{}, ErrEmpty
}

// snippetFromTranscript scans the transcript tail (formatted as
// "user: ..." / "assistant: ..." blocks separated by blank lines)
// from the bottom up for the last assistant block. Returns the
// trimmed first sentence of that block, or "" when no assistant
// text is present.
func snippetFromTranscript(tail string) string {
	if tail == "" {
		return ""
	}
	blocks := splitTranscriptBlocks(tail)
	for i := len(blocks) - 1; i >= 0; i-- {
		body, ok := strings.CutPrefix(blocks[i], "assistant: ")
		if !ok {
			continue
		}
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		return firstSentence(body, snippetMaxChars)
	}
	return ""
}

// snippetFromBody pulls the first sentence of the idea body. Markdown
// is treated as plain text; headings, list markers, and code fences
// pass through into the snippet, which is fine for the dashboard
// card where they get truncated/flattened anyway.
func snippetFromBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return firstSentence(body, snippetMaxChars)
}

// snippetMaxChars caps the deterministic line so the dashboard card
// stays one line tall regardless of source content.
const snippetMaxChars = 120

// firstSentence returns the leading sentence of s (cut at the first
// `.`, `!`, `?`, or newline followed by whitespace/end-of-string),
// or the first max chars when no sentence boundary appears within
// that window. Whitespace is flattened.
func firstSentence(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return cutAtSentenceBoundary(s)
	}
	return cutAtSentenceBoundary(s[:max])
}

func cutAtSentenceBoundary(s string) string {
	// Look for the first terminator. Conservative: only `.`, `!`, `?`
	// followed by a space or end-of-string count, so abbreviations in
	// the middle of a sentence don't break the cut.
	for i := range len(s) {
		c := s[i]
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		if i+1 == len(s) || s[i+1] == ' ' {
			return strings.TrimSpace(s[:i+1])
		}
	}
	return strings.TrimSpace(s)
}

// splitTranscriptBlocks splits the transcript tail into "role: body"
// blocks. claudefmt.LastNTurns joins blocks with a blank line ("\n\n")
// so a string.Split on that boundary gives us each turn.
func splitTranscriptBlocks(tail string) []string {
	parts := strings.Split(tail, "\n\n")
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
