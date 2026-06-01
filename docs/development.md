# Development

## Setup

Prereqs: Go 1.26+, Node 20+, [go-task](https://taskfile.dev/), [buf](https://buf.build/docs/installation).

```sh
git clone https://github.com/paultyng/ideate.git
cd ideate
task setup           # install wails CLI + npm install in frontend/
task generate:proto  # generate proto code into internal/gen/
```

## Common tasks

```sh
task dev          # wails dev against an isolated, seeded .ideate-dev
task dev:user     # wails dev against your real ideas dir (dogfood)

task test         # Go tests with race detector
task test:ui      # Playwright tests against wails dev
task lint         # golangci-lint
task ci           # full CI pipeline
```

`task --list` for the full set.

`task dev` writes data to `.ideate-dev/` (isolated, reseeded). `task dev:user` uses your real OS-default config and ideas dir.

## Local ports

The hooks endpoint is on a fixed port so a session started against an earlier Ideate run keeps working after a restart. Override with `IDEATE_HOOKS_PORT`. Defaults are chosen so all three modes can run simultaneously without collisions:

| Mode | Wails dev | Hooks |
|------|-----------|-------|
| Production app | n/a | 34117 |
| `task dev:user` | 34115 | 34117 |
| `task dev` | 34116 | 34118 |
| `task test:ui` | 34116 | 34119 |

`task dev` and `task test:ui` share the same Wails dev port (34116) — the hooks port is the differentiator (34118 vs 34119), so they cannot run simultaneously.

If the chosen hooks port is already taken (typically: another Ideate already running), startup falls back to an ephemeral port and warns. New sessions still work — they get a fresh `--settings` file pointing at whatever port the listener bound.
