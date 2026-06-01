List every backlog item for the current idea. Returns an array of `{id, title, body?, status, created, updated, source?, assignee_session?, external_url?, depends_on?, affects?}` sorted oldest-first.

Use to triage open work, surface follow-ups to the user before exiting, or pick the next item to take on. Backlog is the idea's durable task list — separate from external trackers (GitHub Issues, Jira) and from session-local TODOs that die with the session.

`status` ∈ {`open`, `in_progress`, `done`, `wontfix`}.
