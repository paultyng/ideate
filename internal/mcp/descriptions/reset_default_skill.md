**Destructive.** Replace a default Ideate orchestrator skill's on-disk
directory with the canonical version shipped in the binary. Any user
edits or extra files in the skill directory are deleted.

Pass `name` to reset a single skill (matches a name from
`list_default_skills`). Omit `name` to reset every default skill.

Use when:
- The user explicitly asks to restore a skill ("reset the work-idea skill",
  "wipe my edits to summarize-ideas").
- `list_default_skills` reports `modified` and the user wants the canonical back.

Do not call without the user's explicit instruction; this is a destructive
action that erases their edits. The user-facing harness should be the gate,
and this tool description is the rationale shown in the approval prompt.
