---
title: Agent integrations
description: Install the peen agent skill so Claude Code, Codex, or OpenClaw can set up Peen, send it work over WebSocket, and inspect its stored state.
nav_order: 15
---

# Agent integrations

The `peen` agent skill teaches an agent how to install and run Peen, send work over WebSocket, add workspace harness layers, and inspect stored state. It is documentation only. Installing it does not start a server, run a hook, or change a workspace.

## Install the skill

Install it from the `psyb0t/agents` marketplace:

```bash
claude plugin marketplace add psyb0t/agents
claude plugin install peen@psyb0t

codex plugin marketplace add psyb0t/agents
codex plugin add peen@psyb0t

openclaw skills install @psyb0t/peen
```

## What it contains

The skill lives in [`.agents/skills/peen`](https://github.com/psyb0t/peen/tree/main/.agents/skills/peen). `SKILL.md` covers the workflow, and `references/setup.md` covers setup. Both link back to this documentation for the details.

## Skills inside Peen

This page is about an outside agent operating Peen. To give the agent running inside Peen its own procedures, see [Skills](skills.md).
