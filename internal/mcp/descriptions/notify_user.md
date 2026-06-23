Surface a short message to the user via the OS notification center (macOS Notification Center on v0.1; other platforms log only). Use when the agent has nothing more to do (work finished, blocked, awaiting input) and the user may not be looking at the terminal.

Per-session rate limit: at most one notification every 5 seconds. Calls past the limit return an error result and are not displayed.

Args:
- `title` (required): one-line headline, ≤80 chars. Truncated on display.
- `body` (required): one or two sentences. Avoid markdown — the notification center renders plain text.

Returns `ok` when delivered, or an error result when rate-limited / disallowed.
