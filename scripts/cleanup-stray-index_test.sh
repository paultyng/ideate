#!/usr/bin/env bash
# Fixture test for cleanup-stray-index.sh. Touches only temp dirs.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
root="$tmp/ideas"
export HOME="$tmp/home"
mkdir -p "$HOME"

gen=$'# Notes\n\n* [A](a.md) - desc\n* [B](b.md)\n'
mk() { mkdir -p "$root/$(dirname "$1")"; printf '%s' "${2:-$gen}" >"$root/$1"; }

moved=(
	idea-a/.reviews/index.md
	idea-a/.worktrees/x/index.md
	idea-a/.worktrees/x/sub/index.md
	idea-a/.fm/index.md
	idea-b/.claude/untracked/index.md
)
# checkout is a real worktree of idea-b, created below
worktree_moved=(
	idea-a/checkout/index.md
	idea-a/checkout/sub/index.md
)
stay=(
	idea-a/index.md
	idea-a/notes/index.md
	idea-a/.old/index.md
	idea-a/.prose/index.md
	idea-a/repos/foo/.hidden/index.md
	idea-b/.claude/index.md
	idea-a/checkout/.claude/index.md
	idea-c/.stale/index.md
	idea-c/.stale/sub/index.md
	.backups/okf-x/index.md
)

for f in "${moved[@]}"; do mk "$f"; done
mk idea-a/.fm/index.md $'---\nokf_version: "0.2"\n---\n\n# Ideas\n\n* [A](a.md)\n'
mkdir -p "$root/idea-c/.stale"
printf 'gitdir: /nowhere\n' >"$root/idea-c/.stale/.git"
for f in "${stay[@]}"; do
	case "$f" in idea-a/checkout/*) continue ;; esac # checked out below
	mk "$f"
done
mk idea-a/.prose/index.md $'# Notes\n\nSome hand-written prose.\n* [A](a.md)\n'
touch -t 202001010000 "$root/idea-a/.old/index.md"

git -C "$root/idea-b" init -q
git -C "$root/idea-b" add .claude/index.md
git -C "$root/idea-b" -c commit.gpgsign=false -c user.email=t@t -c user.name=t commit -q -m init
git -C "$root/idea-b" worktree add -q --detach "$root/idea-a/checkout"
for f in "${worktree_moved[@]}"; do mk "$f"; done
# v0.1.7 overwrote the tracked file with different generated content
printf '# Other\n\n* [Z](z.md)\n' >"$root/idea-b/.claude/index.md"

fail=0
check() { if ! "$@"; then echo "FAIL: $*"; fail=1; fi; }

"$here/cleanup-stray-index.sh" "$root" >"$tmp/dry.out"
all_moved=("${moved[@]}" "${worktree_moved[@]}")
for f in "${all_moved[@]}" "${stay[@]}"; do check test -f "$root/$f"; done
check grep -q "stray index.md files: ${#all_moved[@]} " "$tmp/dry.out"
check grep -q "restore -- index.md" "$tmp/dry.out"
check grep -q "idea-b/.claude' restore" "$tmp/dry.out"

"$here/cleanup-stray-index.sh" "$root" --apply >"$tmp/apply.out"
dest="$(sed -n 's/^moved [0-9]* files to //p' "$tmp/apply.out")"
check test -n "$dest"
for f in "${all_moved[@]}"; do
	check test ! -e "$root/$f"
	check test -f "$dest/$f"
done
for f in "${stay[@]}"; do check test -f "$root/$f"; done

[ "$fail" -eq 0 ] && echo PASS
exit "$fail"
