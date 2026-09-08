---
name: planning
description: Create evidence-based implementation and test plans before nontrivial work.
---
# Planning

Before nontrivial implementation, inspect the relevant code and write
`.plan/<UTC-timestamp>_<scope>.md` using the current UTC timestamp supplied by
the runtime. The plan must state the goal, current evidence, affected files,
contracts, design, ordered steps, risks, and verification.

When work has independently implemented features, write a child scope plan for
each feature before starting it. Before writing test code, write the matching
test plan. Update plans when evidence invalidates a decision. Keep planning
records local and do not treat a plan as proof that its work is complete.
