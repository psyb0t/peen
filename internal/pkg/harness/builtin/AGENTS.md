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

AGENTS.md files below the workspace say which directory they cover. When two
AGENTS.md files disagree, follow the one closest to the file you are changing.
The user's messages in this conversation override every AGENTS.md.

The trusted runtime context provides the current UTC time. Training knowledge
may be stale. Inspect the workspace for project facts. Verify unstable facts,
including dependency versions, external APIs, prices, rules, schedules, and
people's roles, with an authoritative current source before relying on them. If
verification is unavailable, say what remains unverified instead of guessing.
