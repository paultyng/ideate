package model

import (
	"testing"
	"time"
)

func TestParseSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		slug      string
		wantTime  time.Time
		wantName  string
		wantError bool
	}{
		{
			name:     "date only",
			slug:     "2026-04-21-batch-processing",
			wantTime: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
			wantName: "batch-processing",
		},
		{
			name:     "date and time",
			slug:     "2026-04-21-1858-batch-processing",
			wantTime: time.Date(2026, 4, 21, 18, 58, 0, 0, time.UTC),
			wantName: "batch-processing",
		},
		{
			name:     "date with multi-word name",
			slug:     "2025-01-15-my-cool-idea",
			wantTime: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
			wantName: "my-cool-idea",
		},
		{
			name:     "date+time with single word name",
			slug:     "2026-04-21-0900-refactor",
			wantTime: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			wantName: "refactor",
		},
		{
			// Bare slugs are valid post-Phase A — the slug itself is
			// returned as the name; the zero created-time signals
			// that the caller should fall back to the idea record's
			// own `created` frontmatter.
			name:     "no date prefix",
			slug:     "my-cool-idea",
			wantTime: time.Time{},
			wantName: "my-cool-idea",
		},
		{
			name:      "empty string",
			slug:      "",
			wantError: true,
		},
		{
			// "2026-04-21" doesn't match either date regex (no
			// trailing -name part), so it falls through to the bare
			// path and round-trips as the slug.
			name:     "only date no name",
			slug:     "2026-04-21",
			wantTime: time.Time{},
			wantName: "2026-04-21",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, name, err := ParseSlug(tt.slug)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(tt.wantTime) {
				t.Errorf("time = %v, want %v", got, tt.wantTime)
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
		})
	}
}

func TestGenerateSlug(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 4, 21, 18, 58, 0, 0, time.UTC)

	tests := []struct {
		name        string
		ideaName    string
		includeTime bool
		want        string
	}{
		{
			name:     "date only",
			ideaName: "Batch Processing",
			want:     "2026-04-21-batch-processing",
		},
		{
			name:        "with time",
			ideaName:    "Batch Processing",
			includeTime: true,
			want:        "2026-04-21-1858-batch-processing",
		},
		{
			name:     "special chars stripped",
			ideaName: "My Cool Idea!!! (v2)",
			want:     "2026-04-21-my-cool-idea-v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := GenerateSlug(tt.ideaName, ts, tt.includeTime)
			if got != tt.want {
				t.Errorf("GenerateSlug() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"lowercase", "Hello World", "hello-world"},
		{"special chars", "My Idea! @#$%", "my-idea"},
		{"multiple spaces", "too   many   spaces", "too-many-spaces"},
		{"multiple hyphens", "a---b---c", "a-b-c"},
		{"leading trailing hyphens", "---test---", "test"},
		{"mixed", "  Hello, World!  (v2)  ", "hello-world-v2"},
		{"already clean", "batch-processing", "batch-processing"},
		{"uppercase", "SCREAMING", "screaming"},
		{"numbers", "idea42", "idea42"},
		{"tabs", "tab\there", "tab-here"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Slugify(tt.input)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
