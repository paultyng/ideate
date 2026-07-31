package model

import (
	"log/slog"
	"maps"
	"time"

	okf "github.com/paultyng/go-okf"
	"gopkg.in/yaml.v3"
)

// ideaConceptType is the OKF `type` discriminator for idea.md documents.
const ideaConceptType = "Ideate Idea"

// ideaExt is the producer-extension frontmatter carried by an idea.md
// concept beyond OKF's core keys.
type ideaExt struct {
	Created     *okf.Actor `yaml:"created,omitempty"`
	Archived    *okf.Actor `yaml:"archived,omitempty"`
	ActiveAfter *okf.Date  `yaml:"active_after,omitempty"`
}

func init() {
	okf.Register(ideaConceptType, func(c *okf.Concept) (any, error) {
		return okf.As[ideaExt](c)
	})
}

// conceptFromIdea maps an Idea onto its OKF concept representation for
// serialization. It starts from a clone of the Idea's stashed raw concept
// (see Idea.raw) — preserving frontmatter keys Ideate doesn't model, both
// producer extensions and unmodeled OKF core fields — and overlays the
// Ideate-managed fields on top. A freshly-constructed Idea (no raw) starts
// from an empty concept. Status/PauseUntil are the single source of truth
// for lifecycle: the created/archived/active_after ext keys are always
// deleted from the clone and re-derived from Status/PauseUntil/Created, so
// clearing Status/PauseUntil (as Resume/Unarchive do) leaves no stale
// archived/active_after behind.
func conceptFromIdea(idea *Idea) *okf.Concept {
	c := cloneConcept(idea.raw)

	c.Type = ideaConceptType
	c.Title = idea.Name
	c.Description = idea.Description
	c.Body = idea.Body
	if !idea.Updated.IsZero() {
		c.Generated = &okf.Actor{At: idea.Updated}
	} else {
		c.Generated = nil
	}

	if c.Extra == nil {
		c.Extra = map[string]any{}
	}

	// A cloned raw concept may still carry legacy v0.1 artifacts (Extra
	// name/updated/pause_until, core Status repurposed for our own
	// active/paused/archived vocabulary — see ideaFromConcept's
	// hasLegacyName/hasLegacyStatus detection). Every field above and
	// below is the OKF-native replacement for one of those, so strip the
	// legacy originals on every save: a resaved idea.md must never parse
	// as legacy again, or the pause/archive dates it just got would be
	// discarded next read in favor of stale legacy values.
	delete(c.Extra, "name")
	delete(c.Extra, "updated")
	delete(c.Extra, "pause_until")
	switch c.Status {
	case string(StatusActive), string(StatusPaused), string(StatusArchived):
		c.Status = ""
	}

	// Delete every lifecycle + created ext key from the clone, then
	// re-derive from the authoritative Status/PauseUntil/Created fields.
	// This is what makes a Resume/Unarchive (which clear Status/PauseUntil)
	// drop the archived/active_after keys instead of leaking a stale value
	// carried over from the stashed raw concept.
	delete(c.Extra, "created")
	delete(c.Extra, "archived")
	delete(c.Extra, "active_after")

	if !idea.Created.IsZero() {
		c.Extra["created"] = &okf.Actor{At: idea.Created}
	}

	if idea.Status == StatusArchived {
		c.Extra["archived"] = &okf.Actor{At: idea.Updated}
	}

	// active_after must never be derived from Updated: Updated bumps on
	// every save, so deriving from it would make a dateless pause's
	// reactivation date drift on each subsequent save. PauseUntil maps
	// faithfully when set; the no-date case falls back to a stable anchor
	// computed from Created, which — unlike Updated — doesn't change across
	// saves.
	if idea.Status == StatusPaused {
		if idea.PauseUntil != nil {
			d := okf.NewDate(idea.PauseUntil.Year(), idea.PauseUntil.Month(), idea.PauseUntil.Day())
			c.Extra["active_after"] = &d
		} else {
			d := pausedActiveAfterDate(idea.Created)
			c.Extra["active_after"] = &d
		}
	}

	if len(idea.Resources) > 0 {
		c.Extra["resources"] = idea.Resources
	} else {
		delete(c.Extra, "resources")
	}

	if len(c.Extra) == 0 {
		c.Extra = nil
	}

	return c
}

// cloneConcept copies src so mutating the clone's Extra map while building
// a serialized representation never mutates the Idea's stashed raw
// concept. A nil src (no prior parse) yields a fresh empty concept.
func cloneConcept(src *okf.Concept) *okf.Concept {
	if src == nil {
		return &okf.Concept{}
	}
	clone := *src
	clone.Extra = maps.Clone(src.Extra)
	return &clone
}

// pausedActiveAfterDate anchors the reactivation date for a paused idea
// that has neither ActiveAfter nor PauseUntil set: Created plus one month,
// computed once from a value (Created) that doesn't change across saves —
// never from Updated, which does.
func pausedActiveAfterDate(created time.Time) okf.Date {
	t := created.AddDate(0, 1, 0)
	return okf.NewDate(t.Year(), t.Month(), t.Day())
}

// activeAfterDate picks the date a paused legacy idea reactivates on:
// pauseUntil when set, otherwise a fallback one month past updated (the
// dateless-paused approximation applied once, at legacy-migration parse
// time, to the file's own parsed values — not re-derived on every save).
func activeAfterDate(pauseUntil *time.Time, updated time.Time) *okf.Date {
	var t time.Time
	if pauseUntil != nil {
		t = *pauseUntil
	} else {
		t = updated.AddDate(0, 1, 0)
	}
	d := okf.NewDate(t.Year(), t.Month(), t.Day())
	return &d
}

// ideaFromConcept maps a parsed OKF concept back onto an Idea, dual-reading
// legacy v0.1-shaped frontmatter (name/status/pause_until/created as a bare
// scalar) alongside the current OKF-native shape.
func ideaFromConcept(c *okf.Concept) *Idea {
	idea := &Idea{
		Name:        c.Title,
		Description: c.Description,
		Body:        c.Body,
		raw:         c,
	}
	if c.Generated != nil {
		idea.Updated = c.Generated.At
	}

	// "status" is an OKF core key (Concept.Status) with its own lifecycle
	// vocabulary (draft/stable/deprecated), but idea.md legacy documents
	// repurposed it for active/paused/archived — never written there by
	// conceptFromIdea, so its presence signals a legacy document.
	var archived *okf.Actor
	var activeAfter *okf.Date

	_, hasLegacyName := c.Extra["name"]
	hasLegacyStatus := c.Status != ""
	if hasLegacyName || hasLegacyStatus {
		archived, activeAfter = parseLegacyIdeaFields(c, idea)
	} else if ext, err := okf.As[ideaExt](c); err == nil && ext != nil {
		archived = ext.Archived
		activeAfter = ext.ActiveAfter
		if ext.Created != nil {
			idea.Created = ext.Created.At
		}
	}

	if raw, ok := c.Extra["resources"]; ok {
		if b, err := yaml.Marshal(raw); err == nil {
			var resources []Resource
			if err := yaml.Unmarshal(b, &resources); err == nil {
				idea.Resources = resources
			}
		}
	}

	// Status answers "is a pause/archive configured", presence-based on the
	// ext keys just parsed above — it does not re-check whether
	// ActiveAfter has elapsed. Auto-resurfacing (flipping paused ideas back
	// to active once ActiveAfter passes) is a later milestone, not M1;
	// today, Status stays "paused" until explicit resume regardless of the
	// date. See Idea.IsPaused for the "paused right now" question.
	switch {
	case archived != nil:
		idea.Status = StatusArchived
		idea.PauseUntil = nil
	case activeAfter != nil:
		idea.Status = StatusPaused
		t := activeAfter.Time
		idea.PauseUntil = &t
	default:
		idea.Status = StatusActive
		idea.PauseUntil = nil
	}

	return idea
}

// parseLegacyIdeaFields normalizes v0.1-shaped frontmatter (landed in
// Extra because "name"/"status"/"pause_until"/"created" aren't OKF core
// keys) onto idea, populating Name/Created and returning the archived/
// active_after lifecycle carriers so the derivation switch in
// ideaFromConcept treats legacy and current documents identically.
func parseLegacyIdeaFields(c *okf.Concept, idea *Idea) (*okf.Actor, *okf.Date) {
	if nameRaw, ok := c.Extra["name"]; ok {
		if name, ok := nameRaw.(string); ok {
			idea.Name = name
		}
	}

	if createdRaw, ok := c.Extra["created"]; ok {
		if t, ok := parseLegacyTime(createdRaw); ok {
			idea.Created = t
		}
	}

	// "updated" is a legacy v0.1 key distinct from OKF's own `generated`/
	// `timestamp` provenance keys, so it always lands in Extra and needs
	// its own mapping.
	if updatedRaw, ok := c.Extra["updated"]; ok {
		if t, ok := parseLegacyTime(updatedRaw); ok {
			idea.Updated = t
		}
	}

	legacyStatus := c.Status

	var legacyPauseUntil *time.Time
	if puRaw, ok := c.Extra["pause_until"]; ok {
		if t, ok := parseLegacyTime(puRaw); ok {
			legacyPauseUntil = &t
		}
	}

	switch Status(legacyStatus) {
	case StatusArchived:
		return &okf.Actor{At: idea.Updated}, nil
	case StatusPaused:
		return nil, activeAfterDate(legacyPauseUntil, idea.Updated)
	case StatusActive:
		// Active is the zero-lifecycle state: no ext keys to set.
		return nil, nil
	default:
		if legacyStatus != "" {
			slog.Debug("read-repaired unknown legacy status",
				slog.String("got", legacyStatus),
				slog.String("repaired_to", string(StatusActive)))
		}
		return nil, nil
	}
}

// parseLegacyTime extracts a time.Time from a generically-decoded legacy
// scalar. yaml.v3 resolves timestamp-shaped scalars (date-only or full
// RFC3339) to time.Time when unmarshaling into map[string]any, but a
// defensive string fallback keeps this tolerant of quoted values.
func parseLegacyTime(raw any) (time.Time, bool) {
	switch v := raw.(type) {
	case time.Time:
		return v, true
	case string:
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}
