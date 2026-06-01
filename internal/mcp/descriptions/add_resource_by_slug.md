PROACTIVELY add a resource to the idea identified by `slug` when the
orchestrator encounters an external artifact relevant to a specific idea
without an active session.

Common `type` values (not validated): `github_pr`, `jira`, `notion`,
`repo`, `feature_flag`, `deploy`, `datadog`, `slack`, `web`.

Dedupes by canonical URL — SSH/HTTPS forms of the same git remote
collapse, trailing `/` and `.git` are stripped, fragments dropped, host
lowercased. Querystring stays significant. Re-adding the same URL updates
label/status/type in place; a `web` resource is promoted to a richer
type on a subsequent add. No separate `update_resource_by_slug` tool;
this is the single upsert path.
