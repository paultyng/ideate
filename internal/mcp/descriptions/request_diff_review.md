Open a human code review of your changes. Use when the user asks to "review my diff", "look at the changes", "review this PR/branch", or otherwise wants eyes on a code diff before you continue.

Opens an Ideate diff review UI where the human can leave inline file:line comments and a summary. Returns a review_id immediately; the review proceeds asynchronously — call get_diff_review_result to wait for the human's submit.

Workflow:
1. Call request_diff_review with the repo path and git refs for the diff
2. Call get_diff_review_result(review_id) repeatedly until status is not "pending"
3. If "complete", iterate the comments array (each has path, line, side, body) and address each one. Body may include "suggestion" fenced code blocks indicating exact replacement text.
4. Optionally request another review round on the updated changes.

If the user signals they want to abandon the review (or you change your mind), call cancel_review(review_id).
