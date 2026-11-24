# Model-facing text

Everything Peen puts in front of a model is product text: the base system prompt in `internal/pkg/agent/types.go`, `internal/pkg/harness/builtin/AGENTS.md` and the built-in skills and agents, tool descriptions and parameter descriptions, injected blocks and notes, error text a tool returns, and the summarizer prompt. Write it for a model reading it cold, and keep it stable enough that a provider can cache it.

## Writing it

- One line per paragraph. A line break inside a sentence reaches the model as a stray newline.
- Plain words, no capitals for emphasis. State the rule and, when it is not obvious, why. Repeating a rule or shouting it makes newer models apply it where it does not fit.
- One home per rule. A behavior rule lives in the base prompt or the built-in `AGENTS.md`, never both. A tool description states the tool's contract: what it does, its inputs, what it returns, and when it fails. It does not repeat the general working rules.
- Describe every tool parameter: what it holds, its unit, and where the value comes from.
- Name what the model controls, not Peen's internals. Write "a duration such as 90s", not "a Go duration string", and say which tool result a revision or ID comes from.
- Describe the shape of a behavior, never a specific session, file, value, or benchmark that motivated it. Prompt text is read on every future request.
- Error text a tool returns keeps the underlying error, such as the operating system's message, and says what to change. Only the HTTP layer hides internal detail from clients.

## Blocks and notes

- Every block Peen adds to the system prompt or the conversation uses a tag the base prompt's "What Peen adds" list describes. A new tag goes into that list in the same change, and `TestDefaultSystemPromptNamesTheInjectedTags` checks the list.
- Metadata goes in attributes, escaped. A body that comes from outside Peen escapes its own closing tag, so planted text cannot end the block.
- A Peen notice is a `<note kind="...">` block. Untrusted content is a `<toolOutput>` or `<events>` block.
- JSON the model reads is encoded with `modeljson.Marshal`, which leaves `<`, `>`, and `&` readable.

## Stable prefix

- The tools and the system prompt stay byte-identical across the turns of a session. The UTC date in `<environment>` is the one value that changes, at most once a day. Nothing finer, such as the time of day, goes into the prompt. `TestRuntimeSystemPromptsStayTheSameAcrossTurns` holds this.
- A result cut to fit `PEEN_MAX_TOOL_RESULT_TOKENS` says it was cut and how to read the rest, and its metadata describes what the model received.
