// Substitution-collapse pass for serialized CriticMarkup output.
//
// Why: typing inside a deletion-marked region naturally produces a
// `{--prefix--}{++typed++}{--suffix--}` triple in the markdown — the
// editor leaves the surrounding deletion mark intact and only the typed
// chars get the insertion mark. That triple IS a substitution; the
// human's intent is "replace `prefix+suffix` with `typed`". Emitting it
// as one `{~~old~>new~~}` mark gives the agent a cleaner signal.
//
// Pure string transform (vs. mdast tree manipulation) because Milkdown's
// serializer runs `remark.stringify(tree)` directly and bypasses unified
// transformers — there's no clean hook for tree-level post-processing.
//
// Adjacency rule: only ZERO whitespace between the marks counts as
// "adjacent". A space, newline, or any other character means the human
// made two separate edits that happen to be near each other — not a
// substitution. This matches the "typing inside a deletion" flow exactly
// (no whitespace gets inserted between the original deletion halves and
// the new insertion).
//
// Tempered groups (`(?:(?!CLOSER)[\s\S])*?`) keep each content capture
// from swallowing other marks' delimiters. A naive `[\s\S]*?` between
// `\{--` and `--\}` will backtrack across an intermediate `--\}{\+\+`
// when the *next* `--\}` doesn't validate, fusing two unrelated marks.
// Concretely: `{--A--} text {--B--}{++C++}` would mis-collapse to
// `{~~A--} text {--B~>C~~}`. The tempered form forbids the closer
// inside the content, so the engine can't do that.

// Match the inside of a CriticMarkup mark — any chars that don't start
// the given closing token. Used as the body of each capture group.
const not = (closer: string) => `(?:(?!${closer})[\\s\\S])*?`

const DEL_BODY = not('--\\}')
const INS_BODY = not('\\+\\+\\}')

const DEL_INS_DEL = new RegExp(
  `\\{--(${DEL_BODY})--\\}\\{\\+\\+(${INS_BODY})\\+\\+\\}\\{--(${DEL_BODY})--\\}`,
  'g',
)
const DEL_INS = new RegExp(
  `\\{--(${DEL_BODY})--\\}\\{\\+\\+(${INS_BODY})\\+\\+\\}`,
  'g',
)
const INS_DEL = new RegExp(
  `\\{\\+\\+(${INS_BODY})\\+\\+\\}\\{--(${DEL_BODY})--\\}`,
  'g',
)

export function collapseToSubstitutions(md: string): string {
  // Run the triple form first so it doesn't get partially eaten by the
  // pair forms.
  let out = md.replace(DEL_INS_DEL, (_, pre, mid, suf) => `{~~${pre}${suf}~>${mid}~~}`)
  out = out.replace(DEL_INS, (_, pre, mid) => `{~~${pre}~>${mid}~~}`)
  out = out.replace(INS_DEL, (_, mid, suf) => `{~~${suf}~>${mid}~~}`)
  return out
}
