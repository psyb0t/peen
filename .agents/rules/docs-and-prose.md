# Docs and prose

## Where documentation lives

| Place | Holds |
| --- | --- |
| `README.md` | What Peen is, install, run, the security summary, and links into `docs/`. |
| `docs/getting-started.md`, `deployment.md` | First run and production deployment. |
| `docs/configuration.md` | Every `PEEN_` variable. `.env.example` is its commented starting point. |
| `docs/architecture.md` | Controller, workers, one turn, durable state, terms and identifiers. |
| `docs/harness.md`, `rules.md`, `skills.md`, `agents.md`, `hooks.md`, `events.md` | The harness formats users write. |
| `docs/http-api.md` | The REST and WebSocket contract, next to `api/api.yml`. |
| `docs/development.md` | Changing Peen itself. |
| `pkg/peen/README.md` | The public Go API. |
| `.agents/skills/peen/` | The published agent skill for operating Peen. |
| `AGENTS.md`, `.agents/rules/` | Contributor instructions. |

Each fact has one home. Link to it instead of copying it. User docs describe what Peen does and how to use it. Implementation detail belongs in code comments.

## Keep them true

A change that alters behavior, configuration, the API, events, harness formats, or deployment updates the affected docs, `.env.example`, and the CHANGELOG in the same change. Commands in docs are run as written, so check that each one you add or change works. A stale command is worse than none.

Committed text describes the product. It never mentions a contributor's local machine paths, scratch directories, private tools, or session markers.

## Markdown format

- Write each paragraph on one source line and let the renderer wrap it. Do not hard-wrap prose to a column. Some older docs are hard-wrapped. Leave untouched paragraphs as they are, and write new or rewritten paragraphs on one line.
- Use line breaks only where Markdown needs them: headings, lists, tables, code blocks, and blank lines between blocks.
- Do not run a formatter that reflows Markdown prose.
- Headings use sentence case. Code, paths, variables, and endpoints go in backticks.
- Check that links and anchors resolve.

## Prose rules

These apply to every word a person reads: docs, the CHANGELOG, commit messages, code comments, API descriptions, error messages, and these rule files.

- Plain words. Not: leverage, utilize, robust, seamless, crucial, delve, enhance, foster, pivotal, showcase, underscore, landscape, realm, notably, facilitate. Use the ordinary word: use, help, important, strong.
- Active voice with a named actor: "Peen refuses to start", not "startup is refused".
- Say what the thing is or does. Cut "serves as", "in order to", "it is important to note", "not just X but Y", and lists of three padded for rhythm.
- Be specific: the variable, the status code, the number, the observable behavior.
- No promotional adjectives and no chatbot filler.
- Straight quotes. No decorative emoji. No bold on every term.
- One idea per sentence. If a reader has to reread it, split it.
