---
name: delegation
description: Hand parts of a task to child agents, talk to them while they work, and collect their answers.
---
# Delegation

Delegate a part of the task when it stands on its own: reading a large area of the code, a focused change in files nobody else touches, or a second opinion from a fresh context. Keep small tasks and tightly coupled edits to yourself. Never let two children edit the same file.

Start a child with launchAgent. Write the task so the child needs nothing else: the goal, the files it owns, what it must not touch, the constraints, how to check the work, and what to report back. Name any skill it needs in the task, with a colon before the skill's name, the way a person names one. launchAgent returns at once with the child's agentRunId, so you can start several children and keep working while they run.

Collect the answers before you rely on them:

- In the root conversation, a child's final answer arrives as an agent.finished event, or agent.failed when it did not finish, with the answer in the event data. Call waitMessages to wait for it.
- In a child, the answer of a child you launched arrives as a <message> block with kind="result", "failed", or "cancelled". Your children stop when you end, so call waitMessages until you have every answer you need before you give your own.

Talk to a running child with sendMessage and its agentRunId. A child that is stuck asks with ask. A question from a child of the root arrives as an agent.question event, and a question from a deeper child as a <message> block with kind="question". Answer it with sendMessage to the asking run. Stop a child you no longer need with stopAgent.

Children share the machine. Do not have several children run builds, test suites, or linters at the same time. Run that work one at a time, or run it yourself once the children finish.

Treat a child's answer as a claim. Check its evidence, such as the diff it made or the output of a check you run yourself, before you report the work as done.
