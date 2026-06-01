package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/paultyng/ideate/internal/model"
)

func TestMatchResourceURLs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store := NewFSStore(dir, filepath.Join(dir, "reviews"), "", "")

	// alpha carries a GitHub PR (https) and a Notion page.
	alpha := &model.Idea{Name: "Alpha"}
	if err := store.Create(ctx, alpha); err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	if err := store.AddResource(ctx, alpha.Slug, model.Resource{
		Type:  "github_pr",
		URL:   "https://github.com/owner/repo/pull/42",
		Label: "Service PR",
	}); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	if err := store.AddResource(ctx, alpha.Slug, model.Resource{
		Type:  "notion",
		URL:   "https://www.notion.so/acme/Design-abc",
		Label: "Design doc",
	}); err != nil {
		t.Fatalf("add notion: %v", err)
	}

	// beta carries the SAME PR — cross-idea match is the point of
	// the tool (the user wants "where else does this PR appear?").
	// Stored without a trailing slash to exercise normalization.
	beta := &model.Idea{Name: "Beta"}
	if err := store.Create(ctx, beta); err != nil {
		t.Fatalf("create beta: %v", err)
	}
	if err := store.AddResource(ctx, beta.Slug, model.Resource{
		Type:  "github_pr",
		URL:   "https://github.com/owner/repo/pull/42",
		Label: "Same PR, plain form",
	}); err != nil {
		t.Fatalf("add beta: %v", err)
	}

	// gamma carries a repo URL as SSH; ensure the SSH/HTTPS form
	// canonicalization still works for the repo-URL case the
	// normalizer documents.
	gamma := &model.Idea{Name: "Gamma"}
	if err := store.Create(ctx, gamma); err != nil {
		t.Fatalf("create gamma: %v", err)
	}
	if err := store.AddResource(ctx, gamma.Slug, model.Resource{
		Type:  "repo",
		URL:   "git@github.com:owner/repo.git",
		Label: "Canonical clone",
	}); err != nil {
		t.Fatalf("add gamma: %v", err)
	}

	// Query mixes the PR URL (with a trailing slash to exercise
	// canonicalization), the repo URL in HTTPS form (canonicalize-
	// matches the SSH variant), an unrelated URL, and an empty
	// string.
	out, err := store.MatchResourceURLs(ctx, []string{
		"https://github.com/owner/repo/pull/42/",
		"https://github.com/owner/repo",
		"https://example.com/never-added",
		"",
	})
	if err != nil {
		t.Fatalf("MatchResourceURLs: %v", err)
	}

	// PR URL: alpha (HTTPS) + beta (also HTTPS); trailing slash on
	// the input should still resolve.
	prMatches := out["https://github.com/owner/repo/pull/42/"]
	if len(prMatches) != 2 {
		t.Fatalf("PR matches = %+v, want 2 (alpha + beta)", prMatches)
	}
	slugs := map[string]bool{prMatches[0].Slug: true, prMatches[1].Slug: true}
	if !slugs[alpha.Slug] || !slugs[beta.Slug] {
		t.Errorf("PR matches missing alpha or beta: %+v", prMatches)
	}

	// Repo URL: SSH form on disk, HTTPS form on the input wire — the
	// canonicalizer's documented contract.
	repoMatches := out["https://github.com/owner/repo"]
	if len(repoMatches) != 1 || repoMatches[0].Slug != gamma.Slug {
		t.Errorf("repo matches = %+v, want [gamma]", repoMatches)
	}

	// Unrelated URL — empty array, not absent. Callers distinguish
	// "no match" from "URL was not in the input set" without checking
	// the presence bit.
	if got := out["https://example.com/never-added"]; got == nil || len(got) != 0 {
		t.Errorf("unrelated URL should map to empty slice, got %+v", got)
	}

	// Empty-string input: same empty-slice contract.
	if got, ok := out[""]; !ok || len(got) != 0 {
		t.Errorf(`empty-string input should map to empty slice, got %+v ok=%v`, got, ok)
	}
}

func TestMatchResourceURLs_EmptyInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store := NewFSStore(dir, filepath.Join(dir, "reviews"), "", "")
	out, err := store.MatchResourceURLs(ctx, nil)
	if err != nil {
		t.Fatalf("MatchResourceURLs(nil): %v", err)
	}
	if len(out) != 0 {
		t.Errorf("empty input → empty map, got %+v", out)
	}
}
