package agent

// launchAgent starts a named or ad-hoc child agent on a task in the
// background, sharing this turn's session, working directory, resolved rules,
// tool registry, and model.

const launchAgentDescription = `Start a child agent on a task. The call
returns at once with the child's agentRunId while the child works in the
background.
Specify the child in exactly one of two ways:
- "agent": the name of an effective agent from the resolved catalogue.
  Prefer this whenever a stored agent file already covers the job. An
  unknown name is reported back with the currently effective agent names so
  you can correct it.
- "agentDefinition": an inline ad-hoc definition for a job no stored agent
  covers, with "name" used only for identification and logging, and
  "instructions" carrying the summarized context the child actually needs.
  An ad-hoc definition is never written to disk; it lives for this one run
  only and is bounded in size.
Supplying both, or neither, is a validation failure.
The task is treated like a user message. A skill the child needs is named in
the task with :name, such as ":planning" or ":core:planning", and it reaches
the child as its own message right after the task, together with the skills
it names. The child does not receive a skill you only loaded yourself. A task
that names an unknown skill fails this call and lists the available skills.
The child shares this session, its working directory, the same resolved
rules and tools, and the same model. It cannot select its own credentials,
provider, or model, and runs with a bounded turn budget and a bounded
recursion depth, so it may itself call launchAgent up to that depth.
The task reaches the child in a <message> block naming you as the sender.
When the child ends, you hear back. When you are a child agent, a child you
launched reports as a <message> block from it with kind="result" and its
final answer, or kind="failed" or kind="cancelled" when it did not finish.
A child of the root conversation reports through an agent.finished or
agent.failed session event whose data carries its final answer, and that
event starts a new turn when the root is idle. Children launched by a child
are cancelled when that child ends, so a child calls waitMessages for the
answers it needs before it gives its own final answer. Talk to a running
child with sendMessage and stop it with stopAgent.`

const launchAgentSchema = `{
  "type": "object",
  "properties": {
    "task": {
      "type": "string",
      "description": "The task the child agent should perform."
    },
    "agent": {
      "type": "string",
      "description": "Name of an effective agent from the catalogue."
    },
    "agentDefinition": {
      "type": "object",
      "description": "Ad-hoc definition for a job no stored agent covers.",
      "properties": {
        "name": {
          "type": "string",
          "description": "Identifies this ad-hoc child for logging only."
        },
        "instructions": {
          "type": "string",
          "description": "Summarized context and instructions this child needs."
        }
      },
      "required": ["name", "instructions"],
      "additionalProperties": false
    }
  },
  "required": ["task"],
  "additionalProperties": false
}`

const sendMessageDescription = `Send a message to another agent in this
session.
"to" is "root" for the session's root conversation, or the agentRunId of any
running agent in this session, such as a child you launched or your parent,
named in the run attribute of your task. The message lands in the
recipient's conversation after its current tool call, in a <message> block
naming you as the sender. A message to root reaches it as an agent.message
session event, which starts a turn when the root is idle.
Answer a child's question by sending the answer to its agentRunId.
Sending to an agent that has finished fails and says how it ended. Sending to
an ID this session never used fails as unknown.`

const sendMessageSchema = `{
  "type": "object",
  "properties": {
    "to": {
      "type": "string",
      "description": "An agentRunId, or \"root\"."
    },
    "message": {
      "type": "string",
      "description": "The message text."
    }
  },
  "required": ["to", "message"],
  "additionalProperties": false
}`

const waitMessagesDescription = `Block until a message waits for you,
then return how many wait. They reach you right after this tool result.
For a child agent, a message is anything another agent or the user sent it,
including the result of a child it launched. For the root conversation, it is
the next user message or session event, including agent.finished,
agent.failed, agent.message, and agent.question from its children.
timeout is a Go duration string such as "30s" or "10m". Empty waits until a
message arrives, for at most 30 days. When the timeout passes first, timedOut
is true and waiting is 0.
Use it after launching children whose answers you need.`

const waitMessagesSchema = `{
  "type": "object",
  "properties": {
    "timeout": {
      "type": "string",
      "description": "Go duration to wait, like \"30s\" or \"10m\"."
    }
  },
  "additionalProperties": false
}`

const stopAgentDescription = `Stop a running child agent by its
agentRunId.
The child ends as cancelled, and so do the children it launched. Stopping an
agent that already ended is not an error: its current state is reported. An
agent cannot stop itself. It gives its final answer instead.`

const stopAgentSchema = `{
  "type": "object",
  "properties": {
    "agentRunId": {
      "type": "string",
      "format": "uuid",
      "description": "The child agent run to stop."
    }
  },
  "required": ["agentRunId"],
  "additionalProperties": false
}`

const askDescription = `Ask a question and wait for the answer.
A child agent asks the agent that launched it. A child of the root asks the
root through an agent.question session event. The root conversation asks the
user, and the user's next message is the answer.
The call returns when the next message reaches you, with answered true. That
message, normally the answer, comes right after this tool result. When the
deployment's wait limit passes first, answered is false and timedOut is true.
Ask only when you cannot go on without the answer.`

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
