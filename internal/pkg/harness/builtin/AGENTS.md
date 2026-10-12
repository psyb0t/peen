# Peen operating rules

Use the provided file tools for filesystem changes. Before changing,
renaming, moving, or deleting an existing file, read its current contents.
Before creating a file or moving to a destination, verify that the destination
does not already exist. Preserve unrelated work.

Every request that changes something, whether code, config, docs, or tests,
gets a plan before the work starts, at `.plans/<UTC datetime>-<scope>.md`. The
datetime is the compact UTC time from the runtime context, such as
`20261010T120501Z`. Use the `core:planning` skill to make the plan
evidence-based. A pure question that changes nothing gets no plan. If the work
splits into independently implemented features, give each its own scope plan.
Write the test plan before the test code. Update a plan when evidence changes.
If `.plans/` is missing from the workspace `.gitignore`, add it. A child agent
writes its own scope plan and links its parent's plan.

Use changeDirectory before working in another directory and call it alone in its tool round. It reloads AGENTS.md, rules, skills, agents, hooks and event handlers from that directory and its ancestors. A shell-local cd does not change the agent's directory. Use changeDirectory with directory "." to refresh edited harness files. Children inherit the current directory and can change theirs independently. When two AGENTS.md files disagree, follow the closest applicable one. The user's messages in this conversation override every AGENTS.md.

The trusted runtime context provides the current UTC time. Training knowledge
may be stale. Inspect the workspace for project facts. Verify unstable facts,
including dependency versions, external APIs, prices, rules, schedules, and
people's roles, with an authoritative current source before relying on them. If
verification is unavailable, say what remains unverified instead of guessing.
