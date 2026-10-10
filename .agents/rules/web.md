# Web control surface

`web/` is a SvelteKit app built with `@sveltejs/adapter-static` into `internal/pkg/http/server/web/dist`, which `spa.go` embeds into the binary. It is one client of the public API, with no private endpoints. pnpm is the package manager. Every web command runs in the dev container through Make, never on the host.

## Layout

| Path | Role |
| --- | --- |
| `src/routes/+page.svelte`, `+layout.ts` | The single prerendered page. Live data loads in the browser. |
| `src/lib/api/client.ts` | Typed REST client on `openapi-fetch`. |
| `src/lib/api/generated.ts` | Types generated from `api/api.yml`. Never edited. |
| `src/lib/ws/` | The WebSocket client. |
| `src/lib/chat/` | Transcript, streaming, tool cards, and Markdown rendering. |
| `src/lib/browser/log.ts` | Allowlisted browser diagnostics. |
| `src/lib/common/` | Shared constants and JSON helpers. |

Import shared code through `$lib/`. Keep logic in `.ts` modules with tests, and keep components thin. Match the Svelte syntax of the file you are editing.

## Code rules

- TypeScript with `strict: true`. No `any`: take `unknown` and narrow it.
- Call the API through `createPeenAPI` and the generated types. Do not hand-write request or response shapes that `generated.ts` already has.
- Named values live in `src/lib/common/constants.ts`. See `constants.md`.
- Never render untrusted HTML. No `{@html}` on message, tool, or event content. Markdown goes through `Markdown.svelte`, and links through `safeLinkHref`. ESLint runs `eslint-plugin-security` and `eslint-plugin-no-unsanitized`.
- Diagnostics go through `src/lib/browser/log.ts`, which emits allowlisted event names and fields to `console.debug`. Never log tokens, message text, request or response bodies, WebSocket payloads, or error text. Never call `console.*` elsewhere.
- Handle every promise. No empty `catch`, and no `.catch(() => null)` that hides a failure from the user.
- Prettier settings live in `web/.prettierrc`: tabs, double quotes, trailing commas, 88 columns.

## Commands

| Command | What it does |
| --- | --- |
| `make web-lint` | ESLint and `prettier --check`. |
| `make web-lint-fix` | Prettier writes the formatting. |
| `make web-check` | `svelte-check` and `tsc --noEmit`. |
| `make web-test` | Vitest with jsdom and `@testing-library/svelte`. |
| `make web-generate` | Regenerates `generated.ts` from `api/api.yml`. `make generate` also does this. |
| `make web-build` | Builds the static site into `internal/pkg/http/server/web/dist`. |
| `make test-browser` | Drives the embedded build in a real browser and saves a screenshot under `.testing/browser-artifacts/`. |

Tests sit next to their module as `<name>.test.ts`. Test what a user or caller sees, not component internals.

## Finishing a change

A change under `web/` is done when `make web-lint`, `make web-check`, and `make web-test` pass, `make web-build` has rebuilt `dist`, the rebuilt `dist` is committed with the source, and `make test-browser` passes. Look at the screenshot for a visible change. `make lint` fails when an embedded file is gitignored.

## Packages

Change dependencies only with `make web-pkg-add WEB_PKG=name@version`, `web-pkg-update`, `web-pkg-upgrade`, `web-pkg-remove`, or `web-pkg-lock`. Versions are exact, installs run with `--ignore-scripts`, and only `esbuild` may run its build script. Never use npm. See `dependencies.md`.
