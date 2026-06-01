Remove a resource from the current session's idea by its URL.

Matches by canonical URL: SSH and HTTPS forms of the same git remote
collapse to the same key; trailing `/`, `.git`, and fragments are stripped;
host is lowercased. A resource added as `git@github.com:o/r.git` can be
removed with `https://github.com/o/r`.

Idempotent: deleting a URL that is not tracked is a no-op (returns success
without error). Only `url` is required — type and label are not needed to
identify the resource.
