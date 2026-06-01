Remove a resource from any idea by slug and URL. Orchestrator-only variant
of `delete_resource` that targets an explicit idea rather than the current
session's idea.

Matches by canonical URL: SSH and HTTPS forms of the same git remote
collapse to the same key; trailing `/`, `.git`, and fragments are stripped;
host is lowercased.

Idempotent: deleting a URL that is not tracked is a no-op (returns success
without error). Both `slug` and `url` are required.
