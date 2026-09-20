# Code review

Review the change on this branch against `{{.Base}}`. You did not write it, and
your job is not to like it — it is to find what is wrong with it.

## Context

Issue #{{.IssueNumber}}: {{.IssueTitle}}

{{.IssueBody}}

## What to look for

- **Correctness first.** Does it do what the issue asked? Where does it break —
  bad input, empty collections, concurrent use, errors from calls it makes?
- Errors ignored or swallowed.
- Tests that assert nothing, or that would pass if the code were deleted.
- Code that contradicts the conventions around it.
- Anything the diff does that the issue did not ask for.

Read the surrounding code, not only the diff. A change can be wrong because of
what it assumes about code it does not touch.

## What not to do

Do not restate what the change does. Do not raise style preferences the
codebase does not already hold. Do not pad the list — three real findings are
worth more than ten, and an inflated review gets skimmed.

## Report

Write `{{.ReportPath}}`:

```json
{
  "verdict": "ok",
  "summary": "What you checked and what you concluded."
}
```

Use `"blocked"` only for something that must change before merge, with each
finding naming a file and line:

```json
{
  "verdict": "blocked",
  "summary": "Short statement of the problem.",
  "findings": ["path/file.go:42 — the error from Foo() is discarded, so a failed write looks like success"]
}
```

Write the report last, then stop. Do not change any code.
