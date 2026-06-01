PROACTIVELY use this tool whenever the session encounters an external
artifact relevant to the current idea — without waiting for the user to
ask. Add a resource the moment you:

- create or comment on a GitHub PR
- reference a Jira ticket
- create, read, or comment on a Notion doc
- fetch a vendor doc or dashboard URL via WebFetch
- link a repo
- touch a feature flag, deploy job, monitor, or any other tracked artifact

Common `type` values (not validated; pick the most specific that fits):
`github_pr`, `jira`, `notion`, `repo`, `feature_flag`, `deploy`, `datadog`,
`slack`, `web` (catch-all for WebFetch / general links).

Dedupes by canonical URL: SSH/HTTPS forms of the same git remote collapse,
trailing `/` and `.git` are stripped, fragments dropped, host lowercased.
Querystring stays significant (Notion view IDs, filter params change what
the URL points at). Re-adding the same URL updates label/status/type in
place — a `web` resource is promoted to a richer type if you later add it
again with `type=github_pr` (or similar). No separate `update_resource`
tool exists; this is the single upsert path.
