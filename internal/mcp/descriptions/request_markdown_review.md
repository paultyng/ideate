Open a human review of a markdown file. Use when the user asks to "review this plan", "look at this doc", "review the writeup", "give feedback on the markdown", or otherwise wants prose feedback before you commit to changes.

Opens an Ideate markdown review UI — a WYSIWYG editor seeded with the file's current content where the human can edit the prose directly and leave inline CriticMarkup feedback. Returns a review_id immediately; the review proceeds asynchronously — call get_markdown_review_result to wait for the human's submit.

Workflow:
1. Save the file to disk (the review snapshots it at request time).
2. Call request_markdown_review with the absolute path.
3. Call get_markdown_review_result(review_id) repeatedly until status is not "pending".
4. Process the result — see the get_markdown_review_result description for the mark types and how to combine them with direct prose edits to produce the next version of the file.

If the user signals they want to abandon the review (or you change your mind), call cancel_review(review_id).
