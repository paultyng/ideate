Pause an idea, marking it as temporarily on hold.

**Refuses with an error** when the idea is already paused.

The optional `until` timestamp lets you schedule when the pause
should auto-lift. Resuming before that time is always allowed via
`resume_idea`.

Args:

- `slug` (string, optional): the idea's slug. If omitted, uses the
  current session's idea.
- `until` (string, optional): ISO 8601 timestamp (RFC 3339) when
  the pause should be lifted, e.g. `2026-06-01T09:00:00Z`.

Returns plain text confirming the pause on success.
