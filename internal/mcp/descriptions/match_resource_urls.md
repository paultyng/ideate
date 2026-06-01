Bulk lookup: which ideas (if any) reference each of these URLs?

Takes an array of URLs and returns a map keyed by the original input URL. Each value is an array of matches; URLs with no matches map to an empty array (not absent), so callers can distinguish "no match" from "URL was not in the input set" without inspecting the map's presence bit.

URLs are canonicalized server-side via the same logic `add_resource` uses for deduplication: SSH (`git@host:path`) and HTTPS forms collapse, trailing `/` and `.git` are stripped, fragments dropped, host lowercased. Querystring stays significant. Pass URLs in whatever form you have — you don't need to normalize first.

Use when:

- Mid-session you encounter a URL (a PR, a doc, a flag) and want to know if it's already tracked elsewhere before adding a duplicate.
- The orchestrator is recapping work and wants cross-references — "this PR shows up on idea-A and idea-B".
- A meta tool needs to enrich a list of external refs with their idea bindings without N round-trips.

## Args

- `urls` (array of strings, required): the URLs to look up. Empty array returns an empty map.

## Returns

```json
{
  "https://github.com/owner/repo/pull/42": [
    {"slug": "alpha", "name": "Alpha Idea", "resource_type": "github_pr", "resource_label": "Service PR", "idea_url": "ideate://ideas/alpha", "idea_active_session_url": "ideate://ideas/alpha/active-session"},
    {"slug": "beta", "name": "Beta Idea", "resource_type": "github_pr", "resource_label": "Same PR, ssh form", "idea_url": "ideate://ideas/beta", "idea_active_session_url": "ideate://ideas/beta/active-session"}
  ],
  "https://example.com/unrelated": []
}
```

`idea_url` and `idea_active_session_url` are `ideate://` deep-links — skills should emit them as the link target on cross-reference output so the user can click through to the matched idea or jump straight into its live session.
