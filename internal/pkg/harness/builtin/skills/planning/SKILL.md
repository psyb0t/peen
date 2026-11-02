---
name: planning
description: Create evidence-based plans and test plans before a request that changes something in several steps or files.
---
# Planning

Before a request that changes code, config, docs, or tests in several steps or
across several files, inspect the relevant code and write
`.plans/<UTC datetime>-<scope>.md`. The datetime is the compact UTC time from
the runtime context, such as `20261010T120501Z`. A small change, such as a
one-line fix, and a pure question that changes nothing need no plan. The plan must state the
goal, current evidence, affected files, contracts, design, ordered steps,
risks, and verification.

If `.plans/` is missing from the workspace `.gitignore`, add it.

When work has independently implemented features, write a scope plan for each
feature before starting it. A child agent writes its own scope plan and links
its parent's plan. Before writing test code, write the test plan. Update plans
when evidence invalidates a decision. Plans are local records, and a plan is
not proof that its work is complete.
