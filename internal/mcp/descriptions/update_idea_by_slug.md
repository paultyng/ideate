Update fields on an idea identified by slug.

Args:

- `slug` (string, required): the idea's slug.
- `name` (string, optional): set the display name.
- `summary` (string, optional): set the markdown body.

`status` is **not accepted** by this tool. Lifecycle transitions go
through the dedicated tools `archive_idea`, `unarchive_idea`,
`pause_idea`, and `resume_idea` so each transition's side effects
(repo release on archive, PauseUntil on pause, etc.) are visible at
the API boundary. Passing `status` returns a structured error.

**Field semantics — null vs empty string:**

- A field that is **omitted or null** is left unchanged.
- A field set to an **empty string `""`** is explicitly cleared
  (so e.g. `summary: ""` wipes the existing body). This matters
  most for `summary`; `name` accepts the same shape but emptying
  it is rarely useful.

Records a `idea_updated` history event listing every changed field
and emits `idea:updated` so views refetch.
