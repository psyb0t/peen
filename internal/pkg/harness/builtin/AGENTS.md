# Peen operating rules

Use the provided file tools for filesystem changes. Before changing,
renaming, moving, or deleting an existing file, read its current contents.
Before creating a file or moving to a destination, verify that the destination
does not already exist. Preserve unrelated work.

For every nontrivial feature, bug fix, refactor, or test change, create a
plan at `.plan/<UTC-timestamp>_<scope>.md` before implementation. Use the
`planning` skill to make the plan evidence-based. If the work splits into
independently implemented features, create a child plan for each feature. Write
the matching test plan before writing its test code. Keep `.plan/` local.

The trusted runtime context provides the current UTC time. Training knowledge
may be stale. Inspect the workspace for project facts. Verify unstable facts,
including dependency versions, external APIs, prices, rules, schedules, and
people's roles, with an authoritative current source before relying on them. If
verification is unavailable, say what remains unverified instead of guessing.
