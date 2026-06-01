List every idea in the workspace with its full in-flight state inlined: summary, backlog counts, running + dormant sessions, last activity. Single round-trip; the entry point for the canonical idea-centric recap and switching skills (`summarize-ideas`, `work-idea`).

State is **always** inlined — disk reads for the per-idea summary + backlog + session list are cheap enough at expected workspace sizes (O(100) ideas × O(10) sessions) that gating them on a flag would just add complexity. The only opt-in is `recent_output` because it hits the live coordinator.

Archived ideas are excluded by default; pass `exclude_archived: false` to include them.

## Args

- `exclude_archived` (boolean, default `true`): drop ideas with `status=archived`. Set to `false` to see archived ideas too (rare — mostly for audit / cleanup flows).
- `include_output_lines` (integer, default `0`): if > 0, each idea with a running session inlines `recent_output` — the last N lines of the most recent running session's vscreen. Same `strip_prompt_placeholder=true, raw=false` semantics as `get_session_output` defaults. Lets recap callers pull live activity context without N follow-up calls.

## Returns

An array of per-idea entries:

```json
[
  {
    "slug": "alpha-idea",
    "name": "Alpha Idea",
    "status": "active",
    "summary": "Add batch processing to the import pipeline.",
    "last_activity_at": "2026-05-29T12:00:00Z",
    "idea_url": "ideate://ideas/alpha-idea",
    "idea_active_session_url": "ideate://ideas/alpha-idea/active-session",
    "backlog": {
      "open": 3,
      "in_progress": 1,
      "done": 5,
      "wontfix": 1,
      "in_progress_titles": ["wire the new SQL migration"]
    },
    "sessions": {
      "running": [
        {"uuid": "...", "agent_type": "claude-code", "activity": "active", "started": "...", "session_url": "ideate://ideas/alpha-idea/sessions/..."}
      ],
      "dormant": [
        {"uuid": "...", "agent_type": "claude-code", "started": "...", "ended": "...", "session_url": "ideate://ideas/alpha-idea/sessions/..."}
      ]
    },
    "recent_output": "...last N lines, only when include_output_lines > 0..."
  }
]
```

`in_progress_titles` is capped at 5 titles per idea so a long mid-flight list doesn't bloat the payload; fall through to `list_backlog_by_slug` for the full list when needed.

URL fields are `ideate://` deep-links. Skills should emit them as the link target on every idea/session reference so the user can click to open. The `idea_active_session_url` is the "open the live session for this idea, resume if dormant, fall back to the idea page" synthetic — stable across the idea's lifetime so it's safe to emit before knowing the current session state.
