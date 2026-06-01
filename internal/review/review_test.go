package review

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestGenerateReviewID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		base, head string
		ref        string
		want       string
	}{
		{"with ref", "abc1234567890", "def4567890123", "feat/batch-processing", "abc1234-def4567-feat-batch-processing"},
		{"no ref", "abc1234567890", "def4567890123", "", "abc1234-def4567"},
		{"short shas", "abc", "def", "main", "abc-def-main"},
		{"special chars in ref", "abc1234567890", "def4567890123", "fix/JIRA-123_foo", "abc1234-def4567-fix-jira-123-foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := GenerateReviewID(tt.base, tt.head, tt.ref)
			if got != tt.want {
				t.Errorf("GenerateReviewID(%q, %q, %q) = %q, want %q", tt.base, tt.head, tt.ref, got, tt.want)
			}
		})
	}
}

func TestResolveRef(t *testing.T) {
	t.Parallel()

	// Create a temp git repo.
	dir := t.TempDir()
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	cmds := [][]string{
		{"init"},
		{"-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	ctx := context.Background()

	// HEAD should resolve to a 40-char SHA.
	sha, err := ResolveRef(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if len(sha) != 40 {
		t.Errorf("sha length = %d, want 40: %q", len(sha), sha)
	}

	// Short SHA should also resolve.
	sha2, err := ResolveRef(ctx, dir, sha[:7])
	if err != nil {
		t.Fatalf("ResolveRef short: %v", err)
	}
	if sha2 != sha {
		t.Errorf("short resolved to %q, want %q", sha2, sha)
	}
}

func TestSlugify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"feat/batch-processing", "feat-batch-processing"},
		{"fix/JIRA-123_foo", "fix-jira-123-foo"},
		{"main", "main"},
		{"", ""},
		{"---leading---trailing---", "leading-trailing"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got := slugify(tt.in)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		wantErr bool
	}{
		{"", true},
		{"abc1234-def4567-feat-test", false},
		{"abc-def", false},
		{"../../../etc/passwd", true},
		{"foo/bar", true},
		{"foo bar", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			err := ValidID(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidID(%q) err = %v, wantErr = %v", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestParseCriticMarks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []CriticMark
	}{
		{
			name: "no marks",
			in:   "plain text without any marks",
			want: nil,
		},
		{
			name: "single insertion",
			in:   "before {++added++} after",
			want: []CriticMark{
				{Type: CriticInsertion, Start: 7, Length: 11, Text: "added"},
			},
		},
		{
			name: "single deletion",
			in:   "before {--removed--} after",
			want: []CriticMark{
				{Type: CriticDeletion, Start: 7, Length: 13, Text: "removed"},
			},
		},
		{
			name: "single substitution",
			in:   "before {~~old~>new~~} after",
			want: []CriticMark{
				{Type: CriticSubstitution, Start: 7, Length: 14, Old: "old", New: "new"},
			},
		},
		{
			name: "single comment",
			in:   "before {>>note<<} after",
			want: []CriticMark{
				{Type: CriticComment, Start: 7, Length: 10, Text: "note"},
			},
		},
		{
			name: "all four kinds in order",
			in:   "{++ins++} {--del--} {~~old~>new~~} {>>com<<}",
			want: []CriticMark{
				{Type: CriticInsertion, Start: 0, Length: 9, Text: "ins"},
				{Type: CriticDeletion, Start: 10, Length: 9, Text: "del"},
				{Type: CriticSubstitution, Start: 20, Length: 14, Old: "old", New: "new"},
				{Type: CriticComment, Start: 35, Length: 9, Text: "com"},
			},
		},
		{
			name: "substitution does not get mis-parsed as deletion",
			in:   "{~~a~>b~~}",
			want: []CriticMark{
				{Type: CriticSubstitution, Start: 0, Length: 10, Old: "a", New: "b"},
			},
		},
		{
			name: "stray opening brace is skipped",
			in:   "before {not a mark} {++ins++} after",
			want: []CriticMark{
				{Type: CriticInsertion, Start: 20, Length: 9, Text: "ins"},
			},
		},
		{
			name: "marks separated by other content do not fuse",
			in:   "{--A--} text {--B--}{++C++} more",
			want: []CriticMark{
				{Type: CriticDeletion, Start: 0, Length: 7, Text: "A"},
				{Type: CriticDeletion, Start: 13, Length: 7, Text: "B"},
				{Type: CriticInsertion, Start: 20, Length: 7, Text: "C"},
			},
		},
		{
			name: "multiline content inside a mark",
			in:   "{--line one\nline two--}",
			want: []CriticMark{
				{Type: CriticDeletion, Start: 0, Length: 23, Text: "line one\nline two"},
			},
		},
		{
			name: "empty payloads are valid",
			in:   "{++++} {----} {~~~>~~} {>><<}",
			want: []CriticMark{
				{Type: CriticInsertion, Start: 0, Length: 6, Text: ""},
				{Type: CriticDeletion, Start: 7, Length: 6, Text: ""},
				{Type: CriticSubstitution, Start: 14, Length: 8, Old: "", New: ""},
				{Type: CriticComment, Start: 23, Length: 6, Text: ""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseCriticMarks(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseCriticMarks(%q) returned %d marks, want %d\ngot:  %+v\nwant: %+v",
					tt.in, len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("mark[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
			// Verify Start/Length correctly slice back to the literal mark
			// in the input — gives consumers a cheap sanity check.
			for _, m := range got {
				slice := tt.in[m.Start : m.Start+m.Length]
				if slice[0] != '{' || slice[len(slice)-1] != '}' {
					t.Errorf("mark slice %q does not look like CriticMarkup literal", slice)
				}
			}
		})
	}
}

func TestNewCriticMarks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		original, markedUp string
		want               []CriticMark
	}{
		{
			name:     "no baseline marks: all marks are new",
			original: "Plain prose.",
			markedUp: "Plain {++added++} prose.",
			want: []CriticMark{
				{Type: CriticInsertion, Start: 6, Length: 11, Text: "added"},
			},
		},
		{
			name:     "baseline literal in original is filtered out",
			original: "Doc with `{--example--}` literal.",
			markedUp: "Doc with `{--example--}` literal.{++ user added++}",
			want: []CriticMark{
				{Type: CriticInsertion, Start: 33, Length: 17, Text: " user added"},
			},
		},
		{
			name:     "user-added duplicate of existing literal is consumed by baseline",
			original: "{--del--}",
			markedUp: "{--del--} extra {--del--}",
			// Baseline has one {--del--}; markedUp has two. First one is
			// matched, second one survives as new.
			want: []CriticMark{
				{Type: CriticDeletion, Start: 16, Length: 9, Text: "del"},
			},
		},
		{
			name:     "baseline substitution with same payloads filtered",
			original: "Look: `{~~old~>new~~}`.",
			markedUp: "Look: `{~~old~>new~~}`.{~~real~>edit~~}",
			want: []CriticMark{
				{Type: CriticSubstitution, Start: 23, Length: 16, Old: "real", New: "edit"},
			},
		},
		{
			name:     "no marks anywhere",
			original: "Boring",
			markedUp: "Less boring",
			want:     nil,
		},
		{
			name:     "marked_up identical to original (no edits)",
			original: "Has {++literal++} as text.",
			markedUp: "Has {++literal++} as text.",
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NewCriticMarks(tt.original, tt.markedUp)
			if len(got) != len(tt.want) {
				t.Fatalf("NewCriticMarks returned %d marks, want %d\ngot:  %+v\nwant: %+v",
					len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("mark[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
