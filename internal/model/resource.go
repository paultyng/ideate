package model

import (
	"net/url"
	"strings"
)

// UpsertResource adds res to idea.Resources, deduping by canonical
// URL. When an existing resource matches, mutable fields (Label,
// Status) update in place; Type promotes if the incoming type is
// richer than the tracked type (anything > "web").
func UpsertResource(idea *Idea, res Resource) {
	key := NormalizeResourceKey(res)
	for i := range idea.Resources {
		if NormalizeResourceKey(idea.Resources[i]) != key {
			continue
		}
		// Type promotion: "web" is the catch-all WebFetch type.
		// A more specific type (github_pr, repo, notion, jira, ...)
		// overrides "web". Same-type collisions leave Type alone.
		if idea.Resources[i].Type == "web" && res.Type != "" && res.Type != "web" {
			idea.Resources[i].Type = res.Type
		}
		// Mirror the Type-promotion direction: a "web" source only
		// updates Label when the tracked resource is itself "web".
		// A richer-typed resource's Label was set by a more authoritative
		// channel (link_repo, explicit add_resource) and shouldn't be
		// clobbered by an opportunistic WebFetch page title.
		if res.Label != "" && (idea.Resources[i].Type == res.Type || res.Type != "web") {
			idea.Resources[i].Label = res.Label
		}
		if res.Status != "" {
			idea.Resources[i].Status = res.Status
		}
		return
	}
	idea.Resources = append(idea.Resources, res)
}

// NormalizeResourceKey produces a canonical identity for a Resource.
// Exported so external packages (e.g. mcp, service) can match
// resources by canonical URL without re-implementing the logic.
//
// URL normalisation: parses via net/url, lowercases host, strips
// fragment and trailing slash, strips trailing ".git" (so git-remote
// URLs match their HTTPS web equivalents), rewrites the SSH
// `git@host:path` shape to `https://host/path`. Querystring stays
// significant (filter params and Notion view IDs carry meaning).
// Falls back to "<type>:<label>" for label-only resources.
func NormalizeResourceKey(r Resource) string {
	raw := r.URL
	if strings.HasPrefix(raw, "git@") && strings.Contains(raw, ":") {
		atColon := strings.SplitN(strings.TrimPrefix(raw, "git@"), ":", 2)
		if len(atColon) == 2 {
			raw = "https://" + atColon[0] + "/" + atColon[1]
		}
	}
	if raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			u.Host = strings.ToLower(u.Host)
			u.Fragment = ""
			u.Path = strings.TrimRight(strings.TrimSuffix(u.Path, ".git"), "/")
			return u.String()
		}
	}
	return r.Type + ":" + r.Label
}
