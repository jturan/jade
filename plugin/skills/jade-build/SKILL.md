---
name: jade-build
description: Build the next ready unit of work — dispatch a builder, review it, and open a pull request. Use when the user says to build the next unit, work the next issue, keep going on the backlog, or invokes /jade-build.
requires:
  bins: ["jade"]
---

# jade build

Before dispatching anything, show the user what will run:

```bash
jade build --explain
```

That prints the unit, its models, whether a security review will run, and
whether it could merge itself. Confirm before proceeding unless the user
already said to go.

Then:

```bash
jade build
```

The run is unattended between the two gates. It ends in one of:

- **a pull request** — the user merges it; that gate is theirs, not yours
- **`agent:blocked`** — retries were exhausted; the failure is commented on the
  issue

Useful flags: `--unit N` to pick a specific ready issue, `--all` to chain units,
`--autonomy merge` to let clean units merge themselves.

Do not merge the pull request on the user's behalf unless they explicitly ask.
jade deliberately never merges.
