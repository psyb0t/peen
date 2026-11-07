# Docs and prose

## Where documentation lives

| Place | Holds |
| --- | --- |
| `README.md` | What Peen is, install, run, the security summary, and links to the documentation site. |
| `docs/getting-started.md`, `deployment.md` | First run and production deployment. |
| `docs/command-line.md`, `control-surface.md` | The `peen` turn and session commands, and the browser control surface. |
| `docs/security.md` | What the agent can reach, credentials, transcripts, and network exposure. |
| `docs/configuration.md` | Every `PEEN_` variable. `.env.example` is its commented starting point. |
| `docs/architecture.md` | Controller, workers, one turn, durable state, terms and identifiers. |
| `docs/harness.md`, `rules.md`, `skills.md`, `agents.md`, `hooks.md`, `events.md` | The harness formats users write. |
| `docs/tools.md` | Every tool the agent can call. |
| `docs/http-api.md`, `websocket-api.md` | The REST and WebSocket contract, next to `api/api.yml` and `api/asyncapi.yaml`. |
| `docs/troubleshooting.md` | Startup refusals, `peen doctor` failures, and API error codes. |
| `docs/agent-integrations.md` | Installing the `peen` agent skill. |
| `docs/benchmarks.md` | Benchmark method, results, and the Peen defects each run found. Update it after a benchmark run. |
| `docs/development.md` | Changing Peen itself: layout, Make targets, test tiers, generated code, docs sync, and releases. Do not name it `contributing.md`, which makes GitHub show a Contributing tab. |
| `docs/embedding.md`, `embedding-options.md` | The public Go API. `pkg/peen/README.md` is a short pointer to them. |
| `.agents/skills/peen/` | The published agent skill for operating Peen. |
| `.agents/rules/` | The topic rules for changing Peen. |
| `AGENTS.md` | The entry point for agents changing Peen: links to the guide and every rule. `CLAUDE.md` links to it and `.claude` links to `.agents`. Keep it short and free of secrets and machine-local detail. |

Each fact has one home. Link to it instead of copying it. User docs describe what Peen does and how to use it. Implementation detail belongs in code comments.

## Keep them true

A change that alters behavior, configuration, the API, events, harness formats, or deployment updates the affected docs, `.env.example`, and the CHANGELOG in the same change. Commands in docs are run as written, so check that each one you add or change works. A stale command is worse than none.

Committed text describes the product. It never mentions a contributor's local machine paths, scratch directories, private tools, or session markers.

## The documentation site

GitHub Pages publishes `docs/` from `main` at <https://psyb0t.github.io/peen/>, built with Jekyll and the Just the Docs theme pinned in `docs/_config.yml`. Keep `docs/` flat, because a page's file name is its URL, and renaming one breaks every inbound link.

- Every page has `title`, `description`, and `nav_order` front matter. The description is one specific sentence of 50 to 170 characters, because it is what a search result shows. A child page sets `parent` to the parent's exact title. Titles and descriptions are unique.
- A page has one `#` heading.
- Link another page with its relative `.md` path. Link a file outside `docs/` with its full `https://github.com/psyb0t/peen/blob/main/...` URL, or `tree/main/...` for a directory, because the site does not contain it. When a page names a repository path, link the first mention.
- `README.md` links to the site with full `https://psyb0t.github.io/peen/...` URLs, because Docker Hub shows it too and relative links die there.
- Wrap a code block that holds `${{ }}` in `<!-- {% raw %} -->` and `<!-- {% endraw %} -->`, or Jekyll treats it as a template.
- Run `make docs-build` after changing `docs/`. It uses the image GitHub builds with.

## Markdown format

- Write each paragraph on one source line and let the renderer wrap it. Do not hard-wrap prose to a column. Some older docs are hard-wrapped. Leave untouched paragraphs as they are, and write new or rewritten paragraphs on one line.
- Use line breaks only where Markdown needs them: headings, lists, tables, code blocks, and blank lines between blocks.
- Do not run a formatter that reflows Markdown prose.
- Headings use sentence case. Code, paths, variables, and endpoints go in backticks.
- Check that links and anchors resolve.
