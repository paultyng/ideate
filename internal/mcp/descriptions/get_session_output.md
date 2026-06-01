Return the recent terminal output of the session with the given
UUID. Running sessions serve from the live vscreen; dormant sessions
serve from the persisted snapshot on disk so summarize-style callers
read the last visible state without resuming the process. Returns an
empty string when a dormant session has no snapshot on disk (e.g.
crashed before the first periodic flush).

**Prefer `list_sessions` with `include_output_lines: N`** when you
need output for more than one session, or when you're going to read
metadata (state, idle, idea name) alongside the output. The
list_sessions batch covers that path in a single round-trip; this
tool is for the single-UUID case (raw ANSI required, full
snapshot needed via `lines=0`, or per-call `strip_prompt_placeholder`
override).

ANSI escape codes are stripped by default; pass `raw=true` for the
original bytes. The result is bounded by the per-session vscreen
scrollback (default ≈10000 lines, override via
`IDEATE_VSCREEN_SCROLLBACK`).

## Args

- `uuid` (required): target session UUID.
- `lines` (integer, default `50`): trailing lines to return; pass `0`
  for the full snapshot.
- `raw` (boolean, default `false`): return raw ANSI bytes instead of
  stripped text. Implies `strip_prompt_placeholder=false`.
- `strip_prompt_placeholder` (boolean, default `true`): drop Claude
  Code's empty-prompt hint lines (`❯ Try "…"`). They are suggestions
  to the human, not buffered input, and summarizers misread them
  otherwise. Ignored when `raw=true`.
