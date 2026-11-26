#!/usr/bin/env bash
# Moves stray index.md files written by v0.1.7 (into dot-dirs and git checkouts
# inside ideas) to ~/Ideate-backups. Dry-run by default; never deletes.
#
# Usage: cleanup-stray-index.sh <ideas-dir> [--apply]
#
# A file is stray only if ALL hold:
#   - it sits under a dot-segment, or under a git checkout at depth >= 2 (the
#     paths store.newBundleExclude now skips)
#   - mtime >= SINCE (first v0.1.7 migration)
#   - content matches go-okf Index.Bytes (index.go): optional `okf_version`
#     frontmatter, then only `# heading` and `* [title](link)[ - desc]` lines
#   - untracked: `git ls-files --error-unmatch` exits exactly 1, or no repo
#     encloses it; any other git failure skips the file and is reported
# .backups/, repos/ and sessions/ are never descended into.
set -euo pipefail

SINCE="${SINCE:-2026-11-26T13:48:00Z}"

apply=0
root=""
for a in "$@"; do
	case "$a" in
	--apply) apply=1 ;;
	-*) echo "unknown flag: $a" >&2; exit 2 ;;
	*) root="$a" ;;
	esac
done
if [ -z "$root" ] || [ ! -d "$root" ]; then
	echo "usage: $0 <ideas-dir> [--apply]" >&2
	exit 2
fi
root="$(cd "$root" && pwd -P)"

if cutoff=$(date -u -d "$SINCE" +%s 2>/dev/null); then :; else
	cutoff=$(date -j -u -f "%Y-%m-%dT%H:%M:%SZ" "$SINCE" +%s)
fi
mtime() { stat -f %m "$1" 2>/dev/null || stat -c %Y "$1"; }

is_generated() {
	awk '
		NR == 1 && $0 == "---" { fm = 1; next }
		fm == 1 { if ($0 == "---") fm = 2; else if ($0 !~ /^okf_version: /) bad = 1; next }
		/^$/ { next }
		/^# / { if (!seen) seen = 1; next }
		/^\* \[[^]]*\]\([^)]*\)( - .*)?$/ { if (!seen) bad = 1; n++; next }
		{ bad = 1 }
		END { exit (bad || !seen) }
	' "$1"
}

in_excluded_path() {
	local rel="$1" dir seg depth=0 acc=""
	dir="$(dirname "$rel")"
	[ "$dir" = "." ] && return 1
	local IFS=/
	for seg in $dir; do
		case "$seg" in .*) return 0 ;; esac
		depth=$((depth + 1))
		acc="${acc:+$acc/}$seg"
		if [ "$depth" -ge 2 ] && [ -e "$root/$acc/.git" ]; then return 0; fi
	done
	return 1
}

# Echoes keep|tracked|skip; skip reasons go to stderr.
untracked_status() {
	local f="$1" d out rc
	d="$(dirname "$f")"
	if ! out=$(git -C "$d" rev-parse --git-dir 2>&1); then
		case "$out" in
		*"not a git repository"*) echo keep ;;
		*) echo "git rev-parse failed for $f: $out" >&2; echo skip ;;
		esac
		return
	fi
	rc=0
	git -C "$d" ls-files --error-unmatch -- "$(basename "$f")" >/dev/null 2>&1 || rc=$?
	case "$rc" in
	0) echo tracked ;;
	1) echo keep ;;
	*) echo "git ls-files exited $rc for $f" >&2; echo skip ;;
	esac
}

ts="$(date -u +%Y%m%dT%H%M%SZ)"
list="$(mktemp "${TMPDIR:-/tmp}/stray-index-$ts.XXXXXX")"
skipped=0

while IFS= read -r -d '' f; do
	rel="${f#"$root"/}"
	[ "$(mtime "$f")" -ge "$cutoff" ] || continue
	is_generated "$f" || continue
	in_excluded_path "$rel" || continue
	case "$(untracked_status "$f")" in
	keep) printf '%s\n' "$rel" >>"$list" ;;
	skip) skipped=$((skipped + 1)) ;;
	esac
done < <(find "$root" \( -name .backups -o -name repos -o -name sessions -o -name .git \) -prune -o -type f -name index.md -print0)

total=$(wc -l <"$list" | tr -d ' ')
echo "stray index.md files: $total (skipped on git errors: $skipped)"
if [ "$total" -gt 0 ]; then
	cut -d/ -f1 "$list" | sort | uniq -c | sort -rn
fi
echo "list: $list"

if [ "$apply" -eq 1 ] && [ "$total" -gt 0 ]; then
	dest="$HOME/Ideate-backups/stray-index-$ts"
	while IFS= read -r rel; do
		mkdir -p "$dest/$(dirname "$rel")"
		mv -n -- "$root/$rel" "$dest/$rel"
	done <"$list"
	echo "moved $total files to $dest"
elif [ "$apply" -eq 0 ]; then
	echo "dry run; pass --apply to move them"
fi
