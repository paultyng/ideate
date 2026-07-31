package model

import (
	"testing"
	"time"
)

func TestParseFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantYAML  string
		wantBody  string
		wantError bool
	}{
		{
			name:     "standard frontmatter",
			input:    "---\nname: test\n---\nBody here\n",
			wantYAML: "name: test\n",
			wantBody: "Body here\n",
		},
		{
			name:     "no frontmatter",
			input:    "Just a body\n",
			wantYAML: "",
			wantBody: "Just a body\n",
		},
		{
			name:     "empty body",
			input:    "---\nname: test\n---\n",
			wantYAML: "name: test\n",
			wantBody: "",
		},
		{
			name:     "empty frontmatter",
			input:    "---\n---\nBody\n",
			wantYAML: "",
			wantBody: "Body\n",
		},
		{
			name:      "unclosed frontmatter",
			input:     "---\nname: test\nno closing\n",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			yamlStr, body, err := ParseFrontmatter(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if yamlStr != tt.wantYAML {
				t.Errorf("yaml = %q, want %q", yamlStr, tt.wantYAML)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestParseIdeaFile(t *testing.T) {
	t.Parallel()

	t.Run("full idea", func(t *testing.T) {
		t.Parallel()

		content := "---\nname: My Idea\nstatus: active\nresources:\n  - type: github\n    url: https://github.com/foo/bar\n---\nThis is the summary.\n"
		idea, err := ParseIdeaFile(content)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if idea.Name != "My Idea" {
			t.Errorf("Name = %q, want %q", idea.Name, "My Idea")
		}
		if idea.Status != StatusActive {
			t.Errorf("Status = %q, want %q", idea.Status, StatusActive)
		}
		if len(idea.Resources) != 1 {
			t.Fatalf("Resources len = %d, want 1", len(idea.Resources))
		}
		if idea.Resources[0].Type != "github" {
			t.Errorf("Resource type = %q, want %q", idea.Resources[0].Type, "github")
		}
		if idea.Body != "This is the summary.\n" {
			t.Errorf("Body = %q, want %q", idea.Body, "This is the summary.\n")
		}
	})

	t.Run("no frontmatter", func(t *testing.T) {
		t.Parallel()

		content := "Just some text\n"
		idea, err := ParseIdeaFile(content)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if idea.Name != "" {
			t.Errorf("Name = %q, want empty", idea.Name)
		}
		if idea.Body != "Just some text\n" {
			t.Errorf("Body = %q, want %q", idea.Body, "Just some text\n")
		}
	})
}

func TestSerializeIdeaFile(t *testing.T) {
	t.Parallel()

	idea := &Idea{
		Name:   "Test Idea",
		Status: StatusPaused,
		Body:   "Some notes here.\n",
		Resources: []Resource{
			{Type: "jira", URL: "https://jira.example.com/browse/FOO-1"},
		},
	}

	content, err := SerializeIdeaFile(idea)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parse it back.
	got, err := ParseIdeaFile(content)
	if err != nil {
		t.Fatalf("round-trip parse error: %v", err)
	}
	if got.Name != idea.Name {
		t.Errorf("Name = %q, want %q", got.Name, idea.Name)
	}
	if got.Status != idea.Status {
		t.Errorf("Status = %q, want %q", got.Status, idea.Status)
	}
	if got.Body != idea.Body {
		t.Errorf("Body = %q, want %q", got.Body, idea.Body)
	}
	if len(got.Resources) != 1 {
		t.Fatalf("Resources len = %d, want 1", len(got.Resources))
	}
	if got.Resources[0].URL != idea.Resources[0].URL {
		t.Errorf("Resource URL = %q, want %q", got.Resources[0].URL, idea.Resources[0].URL)
	}
}

func TestSerializeIdeaFileWithPauseUntil(t *testing.T) {
	t.Parallel()

	pause := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	idea := &Idea{
		Name:       "Paused Idea",
		Status:     StatusPaused,
		PauseUntil: &pause,
	}

	content, err := SerializeIdeaFile(idea)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ParseIdeaFile(content)
	if err != nil {
		t.Fatalf("round-trip parse error: %v", err)
	}
	if got.PauseUntil == nil {
		t.Fatal("PauseUntil is nil, want non-nil")
	}
	if !got.PauseUntil.Equal(pause) {
		t.Errorf("PauseUntil = %v, want %v", got.PauseUntil, pause)
	}
}

func TestParseIdeaFile_ReadRepairsUnknownStatus(t *testing.T) {
	t.Parallel()

	content := "---\nname: Old Idea\nstatus: thinking\n---\nSome body.\n"
	idea, err := ParseIdeaFile(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idea.Status != StatusActive {
		t.Errorf("Status = %q, want %q", idea.Status, StatusActive)
	}
}

func TestParseIdeaFile_ReadRepairsGarbageStatus(t *testing.T) {
	t.Parallel()

	content := "---\nname: Garbage Idea\nstatus: garbage-value\n---\nSome body.\n"
	idea, err := ParseIdeaFile(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idea.Status != StatusActive {
		t.Errorf("Status = %q, want %q", idea.Status, StatusActive)
	}
}

func TestParseMarkdownFile(t *testing.T) {
	t.Parallel()

	content := "---\nresources:\n  - type: doc\n    url: https://example.com\n---\n# My Notes\n"
	mf, err := ParseMarkdownFile(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mf.Resources) != 1 {
		t.Fatalf("Resources len = %d, want 1", len(mf.Resources))
	}
	if mf.Resources[0].Type != "doc" {
		t.Errorf("Resource type = %q, want %q", mf.Resources[0].Type, "doc")
	}
	if mf.Body != "# My Notes\n" {
		t.Errorf("Body = %q, want %q", mf.Body, "# My Notes\n")
	}
}
