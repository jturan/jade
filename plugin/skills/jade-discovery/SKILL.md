---
name: jade-discovery
description: Start a discovery session to talk an idea through until it is scoped. Use when the user has a new idea, wants to brain-dump, wants a thought partner on something not yet defined, or invokes /jade-discovery.
requires:
  bins: ["jade"]
---

# jade discovery

```bash
jade discovery "<the idea, in the user's words>"
```

This opens a discovery agent in its own herdr pane and hands the user the
conversation. It takes focus deliberately — the user talks to that pane, not to
this session.

Tell them where the pane is and stop. Do not conduct the discovery here; that
would duplicate the session and split the note.

Notes land in the active profile's sink: an Obsidian vault on a personal
profile, `docs/discovery/` in the repo on a work profile.

To continue an earlier session: `jade discovery --resume "<note name>"`.

Discovery ending without a plan is a good outcome, not an abandoned task.
