package agent

// launchAgent starts a named or ad-hoc child agent on a task in the
// background, sharing this turn's session, working directory, resolved rules,
// tool registry, and model. Each paragraph is one line, as the model reads it.

//nolint:lll // Model-facing paragraphs stay on one line.
const launchAgentDescription = `Start a child agent on a task. The call returns at once with the child's agentRunId while the child works in the background on the same session, directory, rules, and model.

Delegate a part that stands on its own: reading a large area, a focused change in files nobody else touches, or a second opinion from a fresh context. Do small tasks, single-file edits, and tightly coupled steps yourself. Write the task as a complete brief, because the child sees only the task, the rules, and the files: the goal, the files it owns, the constraints, how to check the result, and what to report back. Never let two children edit the same file.

Name the child in exactly one of two ways. agent is a name from the <agents> block; prefer it when a stored agent covers the job, and an unknown name is answered with the names that exist. agentDefinition is an ad-hoc child for this one run, with a name for logs and instructions holding what the child needs; it is never saved. Giving both or neither fails.

A skill the child needs is named in the task with :name, such as :core:planning, and reaches it right after the task; the child does not get a skill you only loaded yourself, and an unknown skill name fails the call. A child runs with a bounded number of rounds and may launch children of its own up to a bounded depth.

When the child ends, you hear back. A child you launched as a child agent reports as a <message> block from it with kind="result" and its final answer, or kind="failed" or kind="cancelled". A child of the root conversation reports through an agent.finished or agent.failed session event carrying its final answer, which starts a new turn when the root is idle. Children launched by a child are cancelled when that child ends, so a child waits with waitMessages for the answers it needs before giving its own. Talk to a running child with sendMessage and stop it with stopAgent.`

//nolint:lll // Parameter descriptions stay on one line.
const launchAgentSchema = `{
  "type": "object",
  "properties": {
    "task": {
      "type": "string",
      "description": "The complete brief: goal, files the child owns, constraints, how to check the result, and what to report."
    },
    "agent": {
      "type": "string",
      "description": "A name from the <agents> block."
    },
    "agentDefinition": {
      "type": "object",
      "description": "An ad-hoc child for a job no stored agent covers.",
      "properties": {
        "name": {
          "type": "string",
          "description": "A short name for this child, used in logs and in the sender of its messages."
        },
        "instructions": {
          "type": "string",
          "description": "The context and standing instructions this child needs."
        }
      },
      "required": ["name", "instructions"],
      "additionalProperties": false
    }
  },
  "required": ["task"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const sendMessageDescription = `Send a message to another agent in this session. to is root for the session's root conversation, or the agentRunId of any running agent, such as a child you launched or your parent, named in the run attribute of your task. The message reaches the recipient after its current tool call, in a <message> block naming you as the sender; a message to root arrives as an agent.message session event, which starts a turn when the root is idle. Answer a child's question by sending the answer to its agentRunId. Sending to an agent that has ended fails and says how it ended, and an ID this session never used fails as unknown.`

//nolint:lll // Parameter descriptions stay on one line.
const sendMessageSchema = `{
  "type": "object",
  "properties": {
    "to": {
      "type": "string",
      "description": "An agentRunId from launchAgent or a run attribute, or root."
    },
    "message": {
      "type": "string",
      "description": "The message text."
    }
  },
  "required": ["to", "message"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const waitMessagesDescription = `Wait until a message is waiting for you, then return how many are; they reach you right after this tool result. For a child agent, a message is anything another agent or the user sent it, a launched child's result included. For the root conversation, it is the next user message or session event, agent.finished, agent.failed, agent.message, and agent.question from its children included. timeout is a duration such as 30s or 10m; empty waits until a message arrives, for at most 30 days. When the timeout passes first, timedOut is true and waiting is 0. Use it after launching children whose answers you need. Root work resumed with resumeExecution is not a message and starts only after your turn ends, so do not wait for it here.`

//nolint:lll // Parameter descriptions stay on one line.
const waitMessagesSchema = `{
  "type": "object",
  "properties": {
    "timeout": {
      "type": "string",
      "description": "How long to wait, such as 30s or 10m. Empty waits until a message arrives."
    }
  },
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const stopAgentDescription = `Stop a running child agent by its agentRunId. The child ends as cancelled, and so do the children it launched. Stopping an agent that already ended is not an error: the result reports its current state. An agent cannot stop itself; it gives its final answer instead.`

//nolint:lll // Parameter descriptions stay on one line.
const stopAgentSchema = `{
  "type": "object",
  "properties": {
    "agentRunId": {
      "type": "string",
      "format": "uuid",
      "description": "The agentRunId launchAgent returned for the child to stop."
    }
  },
  "required": ["agentRunId"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const askDescription = `Ask a question and wait for the answer. A child agent asks the agent that launched it; a child of the root asks the root through an agent.question session event; the root conversation asks the user, whose next message is the answer. The call returns when the next message reaches you, with answered true, and that message comes right after this tool result. When the deployment's wait limit passes first, answered is false and timedOut is true. Ask when you cannot go on without the answer, and before an action your instructions say needs approval.`

const askSchema = `{
  "type": "object",
  "properties": {
    "question": {
      "type": "string",
      "description": "The question to ask."
    }
  },
  "required": ["question"],
  "additionalProperties": false
}`
