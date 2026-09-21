# Workflow

What you actually run, and when.

## The short version

You start in herdr and you never leave. herdr is the room; jade is what you say
in it. There is no moment where you "switch over" — you open a workspace for the
repo you're working on, and every jade command runs inside it.

```
jade discovery "idea"     →  a conversation, ends in a note
jade plan <note>          →  a plan + proposed unit breakdown
    ┗━ YOU APPROVE                                            ← gate 1
jade plan apply           →  issues, labels, tracking issue
jade build                →  builds and reviews one unit, opens a PR
    ┗━ YOU MERGE                                              ← gate 2
jade build                →  next unit
```

Two gates. Everything between them runs without you.

## One time per machine

```sh
jade doctor    # is this laptop ready?
jade init      # generate ~/.config/jade/config.yml
```

`doctor` checks git, an authenticated `gh`, herdr, and at least one agent CLI.
Run it first on a new machine — it fails fast and legibly, which is the whole
point.

`init` asks which profile this machine is (personal, consulting, day job) and
then starts from the copy of that profile in [`profiles/`](../profiles). It only
asks for what the repo cannot know — where your notes vault is, where your repos
live — and writes just that to `~/.config/jade/config.yml`. That file is
machine-local and never committed; everything it does not mention keeps coming
from the repo, so changing a shipped default changes every laptop at once.

`jade config show` prints each setting with the layer it came from — `default`,
`shipped`, `profile`, or `unit` — so a machine behaving unexpectedly can be
diagnosed in one command.

### Letting agents work without approval prompts

A builder started in a fresh pane will stop at its first permission dialog. jade
deliberately refuses to answer those on your behalf, so the unit would burn its
retries sitting at a prompt.

Give each role the flags its CLI needs, in the profile:

```yaml
profiles:
  - name: personal
    agent_args:
      builder: ["--dangerously-skip-permissions"]
```

This is a **profile** setting, not a per-unit one: it describes how much a given
machine trusts its agents, and a unit of work must not be able to widen the
permissions it runs under. It is empty by default — granting an agent unattended
write access is a decision to make per machine, not one jade should ship.

Reviewers only read, so they usually need less than builders do.

## Phase 1 — Discovery

No repo required. This is for an idea that may not survive.

```sh
jade discovery "agents should be able to hand work to each other"
```

jade resolves your profile, renders the discovery prompt with the right sink
path, opens a pane, and starts a discovery agent there. **You have the
conversation in that pane.** Brain-dump; it won't interrupt. When you say you're
done it synthesizes back, asks question rounds with recommended answers, and
writes a scoped note to your sink — an Obsidian vault on a personal profile,
`docs/discovery/` in the repo on a work profile.

Then stop, if the idea didn't earn more. Discovery ending here is a success, not
an abandoned task.

To pick one back up:

```sh
jade discovery --resume "Agent Handoff"
```

## Phase 2 — Planning, and the first gate

Now you need a repo. `cd` into it.

```sh
jade plan ~/vault/Projects/Agent\ Handoff.md
```

A planning agent (Opus, high effort — this is the thinking, pay for it) produces
**one plan**, not a PRD and a TDD: outcomes and acceptance criteria, then
approach, architecture, and risks. It then proposes a breakdown into units of
work, each sized to **one independently reviewable PR a builder can finish in
roughly one session**.

For each unit it proposes a model, an effort level, `depends_on`, and whether it
recommends a security review — flagging *why* (touches auth, PHI, dependencies,
external input, secrets, CI). The recommendation is advisory. The call is yours.

**This is gate 1.** You see the entire breakdown at once and approve or edit it
in one pass. This is where the per-unit dialogue you used to have lives now — all
of it, once, before any code exists.

```sh
jade plan apply
```

Creates the issues with their YAML blocks, applies labels, wires up `depends_on`,
and opens a tracking issue. From here GitHub holds the state, not you and not an
agent's context.

Want to edit by hand first:

```sh
jade plan --dry-run    # writes plan.yml locally
$EDITOR plan.yml
jade plan apply
```

## Phase 3 — The build loop

```sh
jade build
```

That's the whole command. It:

1. Picks the next unit whose dependencies are closed.
2. Labels it `agent:in-progress` and cuts a branch — main working tree, branch
   per unit, no worktree.
3. Opens a pane in the `agents` tab and dispatches the builder with the unit
   plan. The builder runs tests and **stops before committing**.
4. On green, labels `agent:review` and dispatches code review — plus security
   review if the unit's YAML says so. These run in parallel panes.
5. On a blocking failure, feeds it back to the builder in the same unit, up to
   `retry_limit` (default 2).
6. If retries run out: labels `agent:blocked`, comments the failure on the issue,
   flips the orchestrator pane state, and sends a desktop notification.
7. On success, opens the PR and stops.

You can watch any of it in the `agents` tab, or ignore it entirely. The pane
state in the sidebar (`working`, `blocked`, `done`, `idle`) tells you which
without reading output.

Anytime, from anywhere:

```sh
jade status    # the unit table for this repo
```

## Phase 4 — Merge, and the second gate

jade never merges. You review the PR and merge it.

**That's gate 2**, and it's the only other time the loop needs you. Then:

```sh
jade build    # next unit
```

## Where herdr fits

You don't switch to herdr. You were always in it.

One workspace per active repo, with semantic tabs:

| Tab | What lives there |
|---|---|
| `orchestrator` | Where you type jade commands. Your home. |
| `agents` | Builder and reviewer panes, created and torn down per unit. |
| `dev` | Your dev server — restart it without disturbing anything else. |
| `checks` | Test runs, builds, audits. |

jade creates the workspace and tabs if they don't exist, captures pane IDs from
herdr's command output, and never relies on which pane happens to be focused.

## A note on "one conversation surface"

The `orchestrator` tab is a **command** surface, not a chat. That's deliberate: a
long-lived conversational orchestrator holding all the state in its context is
both the most expensive agent in the system and the most fragile one, since it
loses its place on any compaction or restart. Making it thin is what buys you
restartability and cross-machine resume.

If you want the conversational feel back, install the Claude Code plugin and sit
in a Claude Code session in the `orchestrator` tab:

```sh
claude plugin marketplace add jturan/jade
claude plugin install jade@jade
```

Then "what's next?" and "build the next unit" work as English, and the skills
shell out to the same jade commands. You get the ergonomics without paying for a
stateful orchestrator.

The plugin is an adapter, not the product. Its skills hold no prompt content —
every prompt lives in the binary, so there is one copy to maintain. Delete the
plugin and nothing stops working.

## Letting units merge themselves

Some work does not need your eyes. `autonomy` is a dial, set per unit at plan
time alongside the security-review call:

| Level | Behaviour |
|---|---|
| `gated` | The default. You approve the plan and every merge. |
| `merge` | Auto-merges on a clean run; the plan gate still applies. |
| `full` | Also auto-applies the plan. `--yolo` is an alias. |

Override for a whole run with `jade build --autonomy merge`, and chain units
with `--all`.

**The guardrails matter more than the dial.** A unit will not merge itself if:

- it was flagged for security review;
- a reviewer raised blocking findings;
- the builder did not run the tests;
- **it needed a retry** — a unit that struggled has earned your eyes, whatever
  was decided before anyone knew it would struggle;
- it touches a protected path (migrations, auth, CI, secrets, deploy manifests
  by default; set `protected_paths` per profile to change that);
- the profile sets `review_strictness: strict`, which means nothing merges
  itself on this machine whatever a unit's autonomy says — the `dayjob` profile
  ships that way;
- the run has already merged `--max-auto-merges` units (default 3), so a bad
  plan cannot land nine pull requests while you are at lunch.

Every merge decision is reported, including the ones that go ahead, and an
automatic merge leaves a comment on the issue saying why. A silent auto-merge is
indistinguishable from a bug.

`jade build --explain` shows what would happen before anything is dispatched.

**Deployment is not on this dial.** Merging to main is reversible; a production
deploy often is not. jade stops at the merge.

## Stopping and resuming elsewhere

Because state lives in GitHub, there is no such thing as losing your place. Close
the laptop mid-initiative, open a different one, and:

```sh
jade status
jade build
```

It picks up exactly where the last machine left off.
