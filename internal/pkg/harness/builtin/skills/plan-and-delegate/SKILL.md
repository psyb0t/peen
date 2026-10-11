---
name: plan-and-delegate
description: Plan a larger change, split it into independent parts, delegate them to child agents, and verify the combined result.
---
# Plan and delegate

Use this for a change large enough to split. A small change, or one whose parts all touch the same files, is faster done yourself.

1. Read the relevant code and write the plan the planning skill describes. Name each part that can run on its own, the files it owns, the approach, and the check that proves it done. Parts never share a file. Shared files and the joining work stay with you.
2. Start one child per part with launchAgent. Each task states the goal, the owned files, what is out of scope, the approach, the project rules that apply, the check to run, and what to report. Tell the child to report what it is unsure of rather than widen its scope.
3. Wait with waitMessages. Answer questions with sendMessage. Stop a child that drifts from its part with stopAgent and start a corrected one.
4. Review each answer against its part. Read the diff and run the part's check yourself. A child's report is not proof.
5. Join the parts, then run the project's full checks once, one heavy check at a time.
6. Update the plan with what each part delivered and what remains open.
