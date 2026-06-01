# Contributing

## Quickstart

```sh
task setup          # install tools
task generate:proto # regenerate protobuf
task build          # compile the app
```

## Architecture

See [CLAUDE.md](CLAUDE.md) for a full architecture walkthrough and code conventions.

## Manual test scenarios

See [TESTPLAN.md](TESTPLAN.md) for manual test scripts covering key user flows.

## Before opening a PR

`task ci` must pass. It runs proto generation, lint, build, and tests.

## Commit style

Use [Conventional Commits](https://www.conventionalcommits.org/): `type(scope): description`.
Common types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`.
