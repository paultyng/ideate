List the canonical Ideate orchestrator skills that ship with the
app and report each one's on-disk status.

Returns a JSON array of `{name, status, path, canonical_sha256, on_disk_sha256}` entries.

`status` ∈ `{missing, up-to-date, modified}`:
- **`missing`** — the on-disk SKILL.md doesn't exist. Auto-install will fix on next app start.
- **`up-to-date`** — bytes match the canonical version shipped in the binary.
- **`modified`** — bytes differ. The user has edited it, or it's drifted from a prior canonical version.

Use this before `reset_default_skill` to see what's resettable. This tool only
lists the **default Ideate skills** — user-authored or third-party skills are
ignored.
