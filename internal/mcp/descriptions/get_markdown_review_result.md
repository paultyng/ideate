Poll for the result of a previously requested markdown review. Each call long-polls up to 60s for the human's submit; on "pending", call again immediately — DO NOT sleep between calls. Do not ask the user "are you done?" — poll silently.

Returns a JSON object with:
- status: "pending" | "complete" | "cancelled"
- event: "APPROVE" | "REQUEST_CHANGES" | "COMMENT"
- body: overall review summary text — first-class feedback to act on, not just a label. Apply across the document if it suggests global changes ("tighten the prose", "use shorter sentences", etc.).
- markdown.path: path to the file under review
- markdown.original: snapshot of the file content at request time
- markdown.marked_up: human's edited version (may contain CriticMarkup marks). DO NOT write this to disk as-is — it still contains CriticMarkup syntax which would corrupt the source.
- markdown.marks: parsed CriticMarkup marks the human added — sorted by position. Doc-content literals (CriticMarkup syntax already present in original, e.g. inline-code examples in a spec) are filtered out by (type, content) multiset matching, so this list is the human's *new* edits only. Each entry has:
    type:   "insertion" | "deletion" | "substitution" | "comment"
    start:  byte offset in marked_up (delimiters included)
    length: byte length of the literal mark in marked_up
    text:   inner content for insertion/deletion/comment (omitted for substitution)
    old:    "from" payload for substitution (omitted for others)
    new:    "to" payload for substitution (omitted for others)
  Iterate marks instead of regex-scanning marked_up.

## How to apply the result

The marked_up is HUMAN INPUT, not the new file. Your job is to produce the next version of the document — the review is guidance, not a transaction.

GUIDING PRINCIPLE: honor the human's intent for each signal — not always the exact bytes.

Mark-type semantics:
- insertion ({++text++}): apply text verbatim at that point. Edit if it needs reshaping.
- deletion ({--text--}): remove text verbatim. Fix references elsewhere if the removal breaks them.
- substitution ({~~old~>new~~}): apply old→new verbatim. Same considerations as deletion+insertion.
- comment ({>>text<<}): TASK FOR YOU — read it and do the work it asks (rewrite a section, expand a paragraph, restructure). Strip the mark from the output. Most of the agent's actual work lives here.

Direct prose edits (the human typed changes outside any mark): treat as the human dictating exact replacement text. Compare markdown.original vs markdown.marked_up to find them.

Highlight ({==...==}): not part of the surface — the editor doesn't expose it. If you encounter it, strip the delimiters and treat the inner text as plain prose.

If markdown.marked_up == markdown.original AND body is empty: nothing to do, stop.

Write the resulting next-version file via Edit/Write — never write marked_up itself.
