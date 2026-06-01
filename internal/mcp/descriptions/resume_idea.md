Resume a paused idea, returning it to active status.

**Refuses with an error** when the idea is not currently paused.

Args:

- `slug` (string, optional): the idea's slug. If omitted, uses the
  current session's idea.

Returns plain text confirming the resume on success.
