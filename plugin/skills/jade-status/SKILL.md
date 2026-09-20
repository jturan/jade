---
name: jade-status
description: Show the unit-of-work table for the current repo — what is ready, in flight, blocked, or done. Use when the user asks what jade is working on, what is next, where things stand, or invokes /jade-status.
requires:
  bins: ["jade"]
---

# jade status

Run:

```bash
jade status
```

Report what it prints. A `*` marks units that are dispatchable now — their
dependencies are closed.

State lives in GitHub, so this is the same answer on any machine. If the user
asks "what was I doing?", this is the command that answers it.

Pass `--repo owner/name` to inspect a repo other than the working directory.
