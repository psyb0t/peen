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
| `docs/http-api.md` | The rules every REST operation shares and the entry to the REST reference. Each operation's own detail lives in `api/api.yml`, which the site renders as one page per operation. |
| `docs/websocket-api.md` | The WebSocket contract, next to `api/asyncapi.yaml`. |
| `docs/troubleshooting.md` | Startup refusals, `peen doctor` failures, and API error codes. |
| `docs/where-it-runs.md` | The control space and agent space combinations and what the agent can reach in each. |
| `docs/openai-api.md`, `mcp-server.md`, `cron.md`, `telegram.md` | The optional controller entry points that start turns: the OpenAI Chat Completions API, the MCP control server, scheduled turns, and the Telegram bot. |
| `docs/mcp.md` | The MCP servers a session's agent connects to. |
| `docs/github-action.md` | Running Peen from a GitHub Actions workflow through `action.yml`. |
| `docs/agent-integrations.md` | Installing the `peen` agent skill. |
| `docs/benchmarks.md` | Benchmark method, results, and the Peen defects each run found. Update it after a benchmark run. |
| `docs/development.md` | Changing Peen itself: layout, Make targets, test tiers, generated code, docs sync, and releases. Do not name it `contributing.md`, which makes GitHub show a Contributing tab. |
| `docs/embedding.md`, `embedding-options.md` | The public Go API. `pkg/peen/README.md` is a short pointer to them. |
| `.agents/skills/peen/` | The published agent skill for operating Peen. |
| `.agents/rules/` | Only the rules for every change. Every file here loads in every agent session, so keep it short. |
| `.agents/rule-details/` | Area rules for changing Peen, read before touching their area. A new area rule gets a row in the `AGENTS.md` table and a pattern in `.agents/rule-gate.json` in the same change. |
| `AGENTS.md` | The entry point for agents changing Peen: links to the guide, the rules for every change, and the table of area rules by path. `CLAUDE.md` links to it and `.claude` links to `.agents`. Keep it short and free of secrets and machine-local detail. |

Each fact has one home. Link to it instead of copying it. User docs describe what Peen does and how to use it. Implementation detail belongs in code comments.

## Keep them true

Commands in docs are run as written, so check that each one you add or change works. A stale command is worse than none.

## The documentation site

GitHub Pages publishes the site at <https://psyb0t.github.io/peen/>. Docusaurus builds it from `docs/`. [`.github/workflows/docs.yml`](https://github.com/psyb0t/peen/blob/main/.github/workflows/docs.yml) runs `make docs-build` on pushes to `main`, pull requests, and manual dispatch, and the reusable Pages workflow deploys `website/build` from `main` only. Keep `docs/` flat, because a page's file name is its URL, and renaming one breaks every inbound link.

- `docs/` holds every page. `website/` holds only the Docusaurus project: `package.json`, `pnpm-lock.yaml`, `pnpm-workspace.yaml`, `docusaurus.config.mjs`, `sidebars.mjs`, and the theme in `src/`. Do not write pages there.
- `make docs-build` runs `scripts/make/docs.sh build` in the dev image. It copies `website/` to a scratch directory, runs `scripts/make/docs_prepare.py`, installs the locked packages with `--frozen-lockfile --ignore-scripts`, builds, checks the full site links with `scripts/make/docs_check_site_links.py`, and copies the result to the gitignored `website/build`. A broken link or anchor fails the build. Run it after changing `docs/` or `api/api.yml`.
- The REST reference is generated. `docusaurus-plugin-openapi-docs` turns `api/api.yml` into one page per operation under `/api/`, grouped by tag, and `website/sidebars.mjs` nests them under the REST API page. `website/src/theme/ApiExplorer` shows each operation's `x-codeSamples` curl example in place of the plugin's in-browser request editor. Fix reference text in `api/api.yml`, never in generated pages.
- `docs_prepare.py` copies `docs/*.md` into the Docusaurus content, copies `api/api.yml` to the site's `reference/api.yml` download, builds the sidebar from `parent` and `nav_order`, and writes `llms.txt` from `docs/llms.txt` plus one line per page. It refuses a page with no front matter, an unknown front matter key, a missing `title`, `description`, or `nav_order`, a description shorter than 50 or longer than 170 characters, a title or description another page already uses, a `parent` that matches no page title, Liquid or Jekyll syntax, or anything but exactly one `#` heading. The refusal names the file.
- Pages are plain CommonMark that also reads correctly on GitHub, not MDX: no JSX, imports, or `{}` expressions. `has_children`, `seo`, `sitemap`, Liquid tags, and `{% raw %}` wrappers are gone, and the build refuses them. A GitHub Actions expression such as `${{ secrets.X }}` needs no wrapping.

Front matter:

- Every page has `title`, `description`, and `nav_order`. The description is one specific sentence of 50 to 170 characters, because it is what a search result shows. Titles and descriptions are unique.
- A child page sets `parent` to the parent's exact title. A page with children needs no marker.
- `permalink` replaces the URL the file name gives. `nav_exclude: true` leaves a page out of the sidebar, and `search_exclude: true` leaves it out of `llms.txt`. The build accepts no other key.

Pages and links:

- A page has one `#` heading.
- Link another page with its relative `.md` path. Link a file outside `docs/` with its full `https://github.com/psyb0t/peen/blob/main/...` URL, or `tree/main/...` for a directory, because the site does not contain it. When a page names a repository path, link the first mention.
- `README.md` links to the site with full `https://psyb0t.github.io/peen/...` URLs, because Docker Hub shows it too and relative links die there.
- Link a REST reference page with its full URL, such as `https://psyb0t.github.io/peen/api/list-session-feed/`, because the page exists only on the site. Its slug is the operation's `operationId` in kebab case. Docusaurus does not check full URLs, so `docs_check_site_links.py` resolves every `https://psyb0t.github.io/peen/...` link in `docs/`, `README.md`, and the published skill against the build, including its `#anchor`.

## Markdown format

- Write each paragraph on one source line and let the renderer wrap it. Do not hard-wrap prose to a column. Some older docs are hard-wrapped. Leave untouched paragraphs as they are, and write new or rewritten paragraphs on one line.
- Use line breaks only where Markdown needs them: headings, lists, tables, code blocks, and blank lines between blocks.
- Do not run a formatter that reflows Markdown prose.
- Headings use sentence case. Code, paths, variables, and endpoints go in backticks.
- Check that links and anchors resolve.
