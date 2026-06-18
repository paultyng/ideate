Type text into another agent session's terminal AND submit it (the receiving agent processes it as a turn). Dormant targets auto-resume before the write lands — the response includes `resumed: true` when this happened so the caller knows a process restart was paid for. On resume, the tool waits for the target's TUI to come up before writing (`/help for commands` / `❯ ` / `mcp connected:` marker); if the agent isn't ready within the timeout (10s default, override via `IDEATE_AGENT_READY_TIMEOUT_MS`) the call fails with an `agent not ready` error and the caller should retry.

The text is automatically prefixed with `[Input from Orchestrating Agent]` on its own line so the receiving agent (and any human reading the transcript later) can immediately distinguish it from human input — DO NOT add your own preamble.

**Default: fire-and-forget.** No reply tool is advertised; the receiving session runs without routing anything back to the orchestrator. The user navigates into the session themselves to drive it. This matches the common case: an orchestrator hand-off, not a dialogue.

Pass `include_reply_hint=true` only for **interactive orchestration** where you actually want a structured reply back via `reply_to_orchestrator`. Setting this when you don't intend to relay the reply to the user just adds noise to the target's transcript.

Pass `submit=false` to leave the input in the target's prompt buffer without sending — useful for staging a draft the human will edit before submitting. The orchestrator cannot send to its own session — that's a no-op-error guard. Use list_sessions to find target UUIDs first.
