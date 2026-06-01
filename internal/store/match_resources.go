package store

import (
	"context"
	"fmt"

	"github.com/paultyng/ideate/internal/model"
)

// ResourceMatch identifies where a queried URL turned up: which idea
// holds it and what shape it was filed as. Returned by
// MatchResourceURLs as the value of each input-URL key.
//
// IdeaURL and IdeaActiveSessionURL are the ideate:// deep-links the
// caller (typically a skill emitting clickable references) can use
// without reconstructing the URL from the slug.
type ResourceMatch struct {
	Slug                 string `json:"slug"`
	Name                 string `json:"name"`
	ResourceType         string `json:"resource_type,omitempty"`
	ResourceLabel        string `json:"resource_label,omitempty"`
	IdeaURL              string `json:"idea_url"`
	IdeaActiveSessionURL string `json:"idea_active_session_url"`
}

// MatchResourceURLs answers "which ideas reference any of these URLs?"
// in a single pass over the store.
//
// URLs are canonicalized via model.NormalizeResourceKey before
// matching, so SSH/HTTPS forms of the same git remote, trailing
// slashes, .git suffixes, and host case-differences all collapse to
// the same key — matches the dedupe behavior add_resource uses.
//
// Returns a map keyed by the original input URL. Each key carries the
// list of matches; URLs with no matches map to an empty slice (NOT
// nil — callers can distinguish "no input URL" from "input URL with
// zero matches" without checking the map's presence bit).
//
// Empty input → empty map, no error. Idempotent and read-only.
func (s *FSStore) MatchResourceURLs(ctx context.Context, urls []string) (map[string][]ResourceMatch, error) {
	out := make(map[string][]ResourceMatch, len(urls))
	if len(urls) == 0 {
		return out, nil
	}
	// keys[canonical] = list of original input URLs that canonicalized
	// to it. Two callers passing the same logical URL in different
	// forms both get a match in the output, keyed by their own input.
	keys := make(map[string][]string, len(urls))
	for _, raw := range urls {
		out[raw] = []ResourceMatch{}
		key := model.NormalizeResourceKey(model.Resource{URL: raw})
		if key == "" {
			continue
		}
		keys[key] = append(keys[key], raw)
	}
	if len(keys) == 0 {
		return out, nil
	}

	ideas, err := s.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing ideas: %w", err)
	}
	for _, idea := range ideas {
		for _, res := range idea.Resources {
			key := model.NormalizeResourceKey(res)
			origs, ok := keys[key]
			if !ok {
				continue
			}
			match := ResourceMatch{
				Slug:                 idea.Slug,
				Name:                 idea.Name,
				ResourceType:         res.Type,
				ResourceLabel:        res.Label,
				IdeaURL:              model.IdeaURL(idea.Slug),
				IdeaActiveSessionURL: model.IdeaActiveSessionURL(idea.Slug),
			}
			for _, orig := range origs {
				out[orig] = append(out[orig], match)
			}
		}
	}
	return out, nil
}
