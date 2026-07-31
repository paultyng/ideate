package model

import (
	"strings"
	"testing"
	"time"

	okf "github.com/paultyng/go-okf"
)

func TestIdeaConceptRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("archived", func(t *testing.T) {
		t.Parallel()

		created := time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC)
		updated := time.Date(2026, 3, 1, 12, 30, 0, 0, time.UTC)
		idea := &Idea{
			Name:        "Archived Idea",
			Description: "an idea that got archived",
			Created:     created,
			Updated:     updated,
			Status:      StatusArchived,
			Body:        "Body text.\n",
			Resources: []Resource{
				{Type: "github_pr", URL: "https://github.com/foo/bar/pull/1", Label: "PR", Status: "approved"},
			},
		}

		content, err := SerializeIdeaFile(idea)
		if err != nil {
			t.Fatalf("SerializeIdeaFile: %v", err)
		}

		got, err := ParseIdeaFile(content)
		if err != nil {
			t.Fatalf("ParseIdeaFile: %v", err)
		}

		if got.Name != idea.Name {
			t.Errorf("Name = %q, want %q", got.Name, idea.Name)
		}
		if got.Description != idea.Description {
			t.Errorf("Description = %q, want %q", got.Description, idea.Description)
		}
		if got.Body != idea.Body {
			t.Errorf("Body = %q, want %q", got.Body, idea.Body)
		}
		if !got.Updated.Equal(idea.Updated) {
			t.Errorf("Updated = %v, want %v", got.Updated, idea.Updated)
		}
		if !got.Created.Equal(idea.Created) {
			t.Errorf("Created = %v, want %v", got.Created, idea.Created)
		}
		if got.Status != StatusArchived {
			t.Errorf("Status = %q, want %q", got.Status, StatusArchived)
		}
		if got.PauseUntil != nil {
			t.Errorf("PauseUntil = %v, want nil", got.PauseUntil)
		}
		if !strings.Contains(content, "archived:") {
			t.Errorf("serialized output missing archived key; got:\n%s", content)
		}
		if len(got.Resources) != 1 || got.Resources[0].URL != idea.Resources[0].URL {
			t.Errorf("Resources = %+v, want %+v", got.Resources, idea.Resources)
		}
	})

	t.Run("paused", func(t *testing.T) {
		t.Parallel()

		updated := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		pause := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		idea := &Idea{
			Name:       "Paused Idea",
			Updated:    updated,
			Status:     StatusPaused,
			PauseUntil: &pause,
			Body:       "Notes.\n",
		}

		content, err := SerializeIdeaFile(idea)
		if err != nil {
			t.Fatalf("SerializeIdeaFile: %v", err)
		}

		got, err := ParseIdeaFile(content)
		if err != nil {
			t.Fatalf("ParseIdeaFile: %v", err)
		}

		if got.Status != StatusPaused {
			t.Errorf("Status = %q, want %q", got.Status, StatusPaused)
		}
		if got.PauseUntil == nil || !got.PauseUntil.Equal(pause) {
			t.Errorf("PauseUntil = %v, want %v", got.PauseUntil, pause)
		}
		if strings.Contains(content, "archived:") {
			t.Errorf("serialized output has archived key, want none; got:\n%s", content)
		}
	})
}

func TestLifecycleDerivation(t *testing.T) {
	t.Parallel()

	t.Run("archived actor derives archived status", func(t *testing.T) {
		t.Parallel()

		content := "---\ntype: Ideate Idea\ntitle: X\narchived:\n  at: 2026-01-01T00:00:00Z\n  by: \"\"\n---\nBody.\n"
		idea, err := ParseIdeaFile(content)
		if err != nil {
			t.Fatalf("ParseIdeaFile: %v", err)
		}
		if idea.Status != StatusArchived {
			t.Errorf("Status = %q, want %q", idea.Status, StatusArchived)
		}
		if !idea.IsArchived() {
			t.Error("IsArchived() = false, want true")
		}
	})

	t.Run("future active_after derives paused, and IsPaused is true today", func(t *testing.T) {
		t.Parallel()

		content := "---\ntype: Ideate Idea\ntitle: X\nactive_after: 2099-01-01\n---\nBody.\n"
		idea, err := ParseIdeaFile(content)
		if err != nil {
			t.Fatalf("ParseIdeaFile: %v", err)
		}
		if idea.Status != StatusPaused {
			t.Errorf("Status = %q, want %q", idea.Status, StatusPaused)
		}
		today := okf.NewDate(2026, 7, 31)
		if !idea.IsPaused(today) {
			t.Error("IsPaused(today) = false, want true (active_after is in the future)")
		}
	})

	t.Run("elapsed active_after is active per IsPaused", func(t *testing.T) {
		t.Parallel()

		content := "---\ntype: Ideate Idea\ntitle: X\nactive_after: 2020-01-01\n---\nBody.\n"
		idea, err := ParseIdeaFile(content)
		if err != nil {
			t.Fatalf("ParseIdeaFile: %v", err)
		}
		today := okf.NewDate(2026, 7, 31)
		if idea.IsPaused(today) {
			t.Error("IsPaused(today) = true, want false (active_after has elapsed)")
		}
	})

	t.Run("no lifecycle keys derives active", func(t *testing.T) {
		t.Parallel()

		content := "---\ntype: Ideate Idea\ntitle: X\n---\nBody.\n"
		idea, err := ParseIdeaFile(content)
		if err != nil {
			t.Fatalf("ParseIdeaFile: %v", err)
		}
		if idea.Status != StatusActive {
			t.Errorf("Status = %q, want %q", idea.Status, StatusActive)
		}
		if idea.IsArchived() {
			t.Error("IsArchived() = true, want false")
		}
	})
}

func TestParseLegacyIdeaFile(t *testing.T) {
	t.Parallel()

	pause := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	content := "---\nname: Legacy Idea\nupdated: 2026-04-01T00:00:00Z\nstatus: paused\npause_until: 2026-05-01T00:00:00Z\n---\nLegacy body.\n"

	idea, err := ParseIdeaFile(content)
	if err != nil {
		t.Fatalf("ParseIdeaFile: %v", err)
	}

	if idea.Name != "Legacy Idea" {
		t.Errorf("Name = %q, want %q", idea.Name, "Legacy Idea")
	}
	if idea.Status != StatusPaused {
		t.Errorf("Status = %q, want %q", idea.Status, StatusPaused)
	}
	if idea.PauseUntil == nil || !idea.PauseUntil.Equal(pause) {
		t.Errorf("PauseUntil = %v, want %v", idea.PauseUntil, pause)
	}
	if idea.Body != "Legacy body.\n" {
		t.Errorf("Body = %q, want %q", idea.Body, "Legacy body.\n")
	}
}

// TestUnknownFrontmatterKeysSurviveRoundTrip guards against the data-loss
// bug where conceptFromIdea rebuilt frontmatter from scratch instead of
// starting from the parsed document: any key Ideate doesn't model —
// producer extensions or unmodeled OKF core fields — was silently dropped
// on first save.
func TestUnknownFrontmatterKeysSurviveRoundTrip(t *testing.T) {
	t.Parallel()

	content := "---\ntype: Ideate Idea\ntitle: X\nphase: design\nfoo: bar\nstale_after: 2026-09-23\n---\nBody.\n"

	idea, err := ParseIdeaFile(content)
	if err != nil {
		t.Fatalf("ParseIdeaFile: %v", err)
	}

	out, err := SerializeIdeaFile(idea)
	if err != nil {
		t.Fatalf("SerializeIdeaFile: %v", err)
	}
	for _, want := range []string{"phase: design", "foo: bar", "2026-09-23"} {
		if !strings.Contains(out, want) {
			t.Errorf("serialized output missing %q; got:\n%s", want, out)
		}
	}

	// Round-trip a second time to confirm the keys keep surviving, not
	// just carried through the first stashed-raw pass.
	idea2, err := ParseIdeaFile(out)
	if err != nil {
		t.Fatalf("ParseIdeaFile (2nd pass): %v", err)
	}
	out2, err := SerializeIdeaFile(idea2)
	if err != nil {
		t.Fatalf("SerializeIdeaFile (2nd pass): %v", err)
	}
	for _, want := range []string{"phase: design", "foo: bar", "2026-09-23"} {
		if !strings.Contains(out2, want) {
			t.Errorf("second-round serialized output missing %q; got:\n%s", want, out2)
		}
	}
}

// TestPausedActiveAfterStableAcrossSaves guards against active_after
// drifting on every save: conceptFromIdea must derive a dateless pause's
// reactivation date from Created (stable), never from Updated (which
// bumps on every write).
func TestPausedActiveAfterStableAcrossSaves(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC)

	firstSave := &Idea{
		Name:    "Paused Idea",
		Created: created,
		Updated: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Status:  StatusPaused,
	}
	content1, err := SerializeIdeaFile(firstSave)
	if err != nil {
		t.Fatalf("SerializeIdeaFile (1st save): %v", err)
	}
	got1, err := ParseIdeaFile(content1)
	if err != nil {
		t.Fatalf("ParseIdeaFile (1st save): %v", err)
	}
	if got1.PauseUntil == nil {
		t.Fatal("PauseUntil is nil after 1st save, want non-nil")
	}

	// Same idea, saved again later: Created is unchanged but Updated has
	// advanced, as it would on any real re-save.
	secondSave := &Idea{
		Name:    "Paused Idea",
		Created: created,
		Updated: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		Status:  StatusPaused,
	}
	content2, err := SerializeIdeaFile(secondSave)
	if err != nil {
		t.Fatalf("SerializeIdeaFile (2nd save): %v", err)
	}
	got2, err := ParseIdeaFile(content2)
	if err != nil {
		t.Fatalf("ParseIdeaFile (2nd save): %v", err)
	}
	if got2.PauseUntil == nil {
		t.Fatal("PauseUntil is nil after 2nd save, want non-nil")
	}

	if !got1.PauseUntil.Equal(*got2.PauseUntil) {
		t.Errorf("active_after drifted across saves: %v (1st) vs %v (2nd)",
			got1.PauseUntil, got2.PauseUntil)
	}
}
