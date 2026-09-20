# Security review

Review the change on this branch against `{{.Base}}` for security problems.
A human decided at the plan gate that this unit warranted this review.

## Context

Issue #{{.IssueNumber}}: {{.IssueTitle}}

{{.IssueBody}}

## What to look for

- **Authentication and authorization**: missing checks, checks in the wrong
  layer, privilege that widens without saying so.
- **Sensitive data**: personal or health information in logs, error messages,
  URLs, analytics, or test fixtures. Data crossing a boundary it should not.
- **Injection and untrusted input**: SQL, shell, templates, path traversal,
  deserialization. Anything built by string concatenation from user input.
- **Secrets**: credentials, tokens, or keys in code, config, or fixtures.
- **Dependencies**: new or upgraded packages, and what they pull in.
- **Transport and storage**: what is encrypted, what is not, and what is logged.

Judge what the change makes possible, not only what it does. A function that is
safe at its single call site but unsafe by contract is a finding.

## Report

Write `{{.ReportPath}}`:

```json
{
  "verdict": "ok",
  "summary": "What you examined and why you consider it safe."
}
```

Use `"blocked"` for anything that must be fixed before merge. State the impact,
not just the pattern — "an unauthenticated caller can read another patient's
summary" is actionable in a way that "missing authorization check" is not:

```json
{
  "verdict": "blocked",
  "summary": "Short statement of the exposure.",
  "findings": ["path/file.go:42 — <what an attacker can do, and how>"]
}
```

Write the report last, then stop. Do not change any code.
