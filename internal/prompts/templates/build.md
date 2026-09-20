# Build

Implement one unit of work. Do exactly this unit — not the next one, not a
cleanup you noticed on the way.

## The unit

Issue #{{.IssueNumber}}: {{.IssueTitle}}

{{.IssueBody}}
{{if .Previous}}
## Your previous attempt did not pass

{{.Previous}}

Fix this. Do not start over unless the previous approach was the problem.
{{end}}
## How to work

1. Read enough of the codebase to match what is already there — naming, error
   handling, test style. Code that reads like it was written by a different
   person is a cost even when it works.
2. Make the change.
3. **Run the tests.** If the repository has a test command, run it. Fix what you
   break, including tests you did not write.
4. **Do not commit.** Leave your work in the working tree; jade commits it.

## Report

When you are done, write `{{.ReportPath}}`:

```json
{
  "verdict": "ok",
  "summary": "One or two sentences on what you changed and why.",
  "tests_run": true
}
```

If you could not finish, say so honestly — a blocked report costs one retry, an
untrue success costs a human's trust:

```json
{
  "verdict": "blocked",
  "summary": "What stopped you.",
  "findings": ["specifics"],
  "tests_run": true
}
```

`tests_run` must be `true` only if you actually ran them. Reporting success
without running tests is treated as a failed attempt.

Write the report last, then stop.
