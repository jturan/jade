# Planning

Turn a scoped discovery note into a plan and a breakdown into units of work.

Your output is two files. Write both, then stop — do not create issues, branches
or code.

## 1. The plan: `{{.PlanPath}}`

**One document, not a PRD and a TDD.** The split exists to serve two audiences
reading at different times; here the audience is one person and an agent, at the
same time, and two documents create a sync problem. Use these sections:

- **Outcomes** — what is true when this is done.
- **Acceptance criteria** — how anyone can check that.
- **Approach** — how it gets built, and what it builds on.
- **Architecture** — the shape: components, data, interfaces, and what changes.
- **Risks** — what could go wrong, and what you are deliberately not doing.

Write what the discovery note decided. Where it left something open, say so
under Risks rather than inventing an answer.

## 2. The breakdown: `{{.PlanYAMLPath}}`

```yaml
initiative: <short name>
summary: <one line>
plan_note: {{.PlanPath}}
units:
  - id: 1
    title: <imperative, specific>
    body: |
      ## Problem
      Why this unit exists. What is wrong or missing today.

      ## Plan
      How to do it. Name real files and functions where you know them.

      ## Acceptance
      How to tell it worked.
    depends_on: []
    builder: {vendor: claude, model: sonnet, effort: medium}
    code_review: {vendor: claude, model: sonnet, effort: medium}
    security_review: false
    retry_limit: 2
```

### Sizing

Each unit is **one independently reviewable pull request**, completable by a
builder in roughly one session without running out of context. If a unit cannot
be described in a few paragraphs, split it.

Prefer units that are independently mergeable. A unit that only makes sense
alongside the next one is really one unit.

### Dependencies

`depends_on` holds **plan-local ids** from this file. They are translated to
real issue numbers when the plan is applied. Order the plan so it reads top to
bottom, and only declare a dependency that genuinely exists — a false dependency
serializes work that could have been done in any order.

### Models and effort

Defaults are: builder `sonnet`/`medium`, code review `sonnet`/`medium`. Raise to
`opus`/`high` for a unit that is cross-cutting, subtle, security-sensitive, or
touches something you cannot easily test. Say why in the body when you raise it.

### Security review

Set `security_review: true` and give a `security_review_reason` when a unit
touches authentication or authorization, personal or health data, payments,
secrets or configuration, dependency manifests, parsing of external input, or CI
and infrastructure.

This is a **recommendation**. A human decides at the approval gate, so the reason
matters more than the flag — write the reason so someone can disagree with it.

### Autonomy

Leave `autonomy` unset unless the discovery note says otherwise. Unset means
gated: a human approves the plan and every merge.

## Context

- Discovery note: `{{.DiscoveryNote}}`
- Repository: `{{.Repo}}`
- Profile: `{{.Profile}}`

Read the discovery note first. Follow what it decided, including decisions you
would have made differently — if something in it looks wrong, note it under
Risks rather than quietly planning around it.
