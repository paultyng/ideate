Return the running session matching the given UUID with derived
activity fields. **Prefer `list_sessions`** for any flow that examines
more than one session — list_sessions inlines exactly these fields per
entry and accepts `include_output_lines` to also fold in the screen
tail, so the orchestrator gets all of recap-style data in a single
round-trip. Reach for `get_session` only when you already have one
specific UUID (e.g. the user named a single session by slug) and don't
need the rest of the workspace.

Returns:
- `uuid`, `idea_slug`, `idea_name`, `agent_type`, `status`, `activity`,
  `state`, `started`, `working_dir`
- `last_activity_at` (RFC3339; bumped on every session-activity hook)
- `idle_seconds` (seconds since the last activity bump)
- `state` (`active`|`awaiting`|`idle`; classified from `activity` so
  skills and the filter DSL share one vocabulary)
- `idle_bucket` (`<1m`|`Nm`|`Nh`|`Nd`; the compact format the
  canonical skills render)

Errors if the UUID is unknown or the session is not running.
