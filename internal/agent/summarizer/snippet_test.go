package summarizer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSnippetGenerator_PrefersLastAssistantText(t *testing.T) {
	t.Parallel()
	tail := strings.Join([]string{
		"user: please refactor",
		"assistant: I'll start by extracting the auth helper.",
		"user: also rename it",
		"assistant: Renaming and pulling the helper into a new package.",
	}, "\n\n")
	got, err := SnippetGenerator{}.Generate(context.Background(), GenerateInput{
		IdeaName:       "Auth refactor",
		IdeaBody:       "Some long body text that should NOT win.",
		TranscriptTail: tail,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Line != "Renaming and pulling the helper into a new package." {
		t.Errorf("got %q", got)
	}
}

func TestSnippetGenerator_FallsBackToBody(t *testing.T) {
	t.Parallel()
	got, err := SnippetGenerator{}.Generate(context.Background(), GenerateInput{
		IdeaName: "X",
		IdeaBody: "Refactor the auth layer. Then deploy.",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Line != "Refactor the auth layer." {
		t.Errorf("got %q", got)
	}
}

func TestSnippetGenerator_ErrEmptyOnNothing(t *testing.T) {
	t.Parallel()
	_, err := SnippetGenerator{}.Generate(context.Background(), GenerateInput{IdeaName: "X"})
	if !errors.Is(err, ErrEmpty) {
		t.Errorf("err = %v, want ErrEmpty", err)
	}
}

func TestSnippetGenerator_TruncatesAtMaxChars(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a ", 100) // ~200 chars, no sentence boundary
	got, _ := SnippetGenerator{}.Generate(context.Background(), GenerateInput{IdeaBody: long})
	if len(got.Line) > snippetMaxChars {
		t.Errorf("len(got) = %d, want <= %d", len(got.Line), snippetMaxChars)
	}
}

func TestSnippetGenerator_FlattensWhitespace(t *testing.T) {
	t.Parallel()
	got, _ := SnippetGenerator{}.Generate(context.Background(), GenerateInput{
		IdeaBody: "Line one.\nLine two.",
	})
	if got.Line != "Line one." {
		t.Errorf("got %q", got)
	}
}

func TestSnippetGenerator_SkipsEmptyAssistantBlocks(t *testing.T) {
	t.Parallel()
	tail := strings.Join([]string{
		"assistant: First reply.",
		"user: ok",
		"assistant: ", // empty body — should be skipped
	}, "\n\n")
	got, _ := SnippetGenerator{}.Generate(context.Background(), GenerateInput{
		TranscriptTail: tail,
	})
	if got.Line != "First reply." {
		t.Errorf("got %q, want %q", got.Line, "First reply.")
	}
}
