---
name: jade-plan
description: Turn a discovery note into a plan and a breakdown into units of work, then file the issues. Use when the user wants to plan an initiative, break work into issues, or invokes /jade-plan.
requires:
  bins: ["jade"]
---

# jade plan

Planning is a human gate. Three steps, and the middle one is the user's.

```bash
jade plan <path-to-discovery-note>   # a planning agent writes plan.yml
jade plan show                       # the gate: review the breakdown
jade plan apply                      # create the issues
```

After `jade plan show`, present the breakdown and **stop**. The user approves or
edits before anything is filed. Security-review recommendations appear here with
their reasons — surface those explicitly, since the reason is the part the user
can disagree with.

`jade plan apply --dry-run` shows what would be created without creating it.

To edit before filing, the plan is a plain YAML file: open `plan.yml`, change
it, then apply.
