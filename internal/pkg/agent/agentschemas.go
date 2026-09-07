package agent

// launch_agent runs a named or ad-hoc child agent on a task and returns its
// final response, sharing this turn's session, working directory, resolved
// rules, tool registry, and model.

const launchAgentDescription = `Run a child agent on a task and return
its final response.
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
The child shares this session, its working directory, the same resolved
rules and tools, and the same model. It cannot select its own credentials,
provider, or model, and runs with a bounded turn budget and a bounded
recursion depth, so it may itself call launch_agent up to that depth.
This call is synchronous: it blocks until the child finishes and returns
its final answer. If this specific run is cancelled while running, the call
still succeeds and reports that it was cancelled instead of ending your
turn.`

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
