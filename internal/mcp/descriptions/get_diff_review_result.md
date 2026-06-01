Poll for the result of a previously requested diff review. Each call long-polls up to 60s for the human's submit; on "pending", call again immediately — DO NOT sleep between calls.

Returns a JSON object with:
- status: "pending" (still reviewing), "complete" (submitted), or "cancelled" (dismissed)
- comments: array of inline comments (when complete), each with path, line, side, body
- body: overall review summary text
- event: "APPROVE", "REQUEST_CHANGES", or "COMMENT"

Comment bodies may contain `suggestion` fenced code blocks indicating exact replacement text for the commented lines.

Do not ask the user "are you done reviewing?" — poll silently.
