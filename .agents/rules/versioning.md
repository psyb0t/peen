# Versions, CHANGELOG, and commits

## Version source

The Git tag is the version. The maintainer tags releases. `make build` and the Docker build inject the tag as `main.buildVersion` and the commit as `main.buildCommit`. Do not hardcode a version anywhere else. The one static copy is `version` in `.agents/.codex-plugin/plugin.json`, which moves with each release.

## Choosing the number

Peen follows [semver](https://semver.org) and is before 1.0. Breaking changes ship in a minor release, compatible features in a minor release, and fixes alone in a patch release. Every break is labeled in the CHANGELOG with its migration.

The public contract is wider than Go symbols. A change is breaking when a user who upgrades without reading the CHANGELOG could fail:

- REST paths, parameters, schemas, status codes, and error codes, and WebSocket frames and event types;
- `PEEN_` variables, their names, defaults, and accepted values;
- the harness file formats: `AGENTS.md` and rule loading, skill and agent frontmatter, `hooks.yaml`, event handlers;
- the `pkg/peen` Go API and the generated `pkg/http/api/client`;
- CLI commands and flags, the image's entrypoint and roles, and documented directory layouts.

## CHANGELOG entries

`CHANGELOG.md` lists releases newest first. Add your change to the entry at the top for the upcoming release. If the top entry is already tagged, start a new one with the number the change needs, and the maintainer confirms the number and date when tagging.

```markdown
## v0.18.0 (2026-10-12)

One paragraph: what changed for a user, then what to do before upgrading, or "No configuration or API migration is needed."

- Breaking: removes `PEEN_X`. Peen refuses to start while it is set. Migrate by ...
- Fixes <what the user saw going wrong>, and what happens now.
- Adds <the new capability>, and how to use it.
- Changes <the visible behavior>.
- Updates <the dependency or framework> to <version>, which <user-visible reason>.
```

- One change per bullet, starting with a verb in the third person. Breaking bullets come first.
- Write what a user observes: the symptom, the trigger, the new behavior. Name the setting, endpoint, or event involved. Leave out internal type and function names unless they are public API.
- A security fix names the advisory IDs.
- Write each paragraph and bullet on one line. Follow `docs-and-prose.md`.

## Commits

- Conventional commit subjects, lowercase and imperative: `fix: keep stderr from fast commands`, `feat: add ...`, `docs: ...`, `test: ...`, `refactor: ...`, `chore: ...`. A breaking change uses `feat!:` or `fix!:`.
- The subject says what changed for a user when there is a user-visible effect.
- The body, when needed, says why, in plain sentences. No em dashes.
- Never add `Closes #N`, `Fixes #N`, or another keyword that closes an issue unless the maintainer asked for that issue to be closed.
- Text that speaks for the maintainer uses "I", never "we" or "the team".
- Servicepack update commits keep the message the update tool writes.
