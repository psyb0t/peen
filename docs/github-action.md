# GitHub action

Use `psyb0t/peen@main` to run a prompt as a workflow step on a Linux runner with Docker. The action pulls `psyb0t/peen:latest` and runs `peen exec` in that image. It streams reply text to the job log and returns it as an output. The caller prepares the working directory and handles checkout and publication.

Add your provider key to the repository's Actions secrets as `PROVIDER_KEY`, and replace the example provider URL and model. The runner needs Bash and jq; GitHub-hosted Ubuntu runners include them.

## Work on a checkout

```yaml
name: Ask Peen
on: workflow_dispatch
permissions:
  contents: read
jobs:
  prompt:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      # Include checkout only when the prompt needs repository files.
      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6
        with:
          persist-credentials: false
      - uses: psyb0t/peen@main
        id: peen
        with:
          prompt: Explain the project and write a summary to SUMMARY.md.
          model: gateway/your-model-id
          upstreams: >-
            [{"name":"gateway","type":"openai","baseUrl":"https://gateway.example/v1","apiKeyEnv":"PROVIDER_KEY"}]
        env:
          PROVIDER_KEY: ${{ secrets.PROVIDER_KEY }}
      - name: Save the reply for later steps
        env:
          PEEN_REPLY: ${{ steps.peen.outputs.result }}
        run: printf '%s\n' "$PEEN_REPLY" > "$RUNNER_TEMP/peen-reply.txt"
```

To work outside a checkout, create a directory in an earlier step and pass its absolute path as `working-directory`. Relative paths resolve against `GITHUB_WORKSPACE`. The directory must already exist. No Git repository is required. Files the agent writes remain in that directory for later workflow steps.

## Run without checkout

This complete workflow gives Peen an empty directory and asks it to write a file. The next step reads the generated file directly from the runner.

```yaml
name: Generate a document
on: workflow_dispatch
permissions: {}
jobs:
  generate:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - name: Prepare a directory
        run: mkdir -p "$RUNNER_TEMP/peen-work"
      - uses: psyb0t/peen@main
        with:
          working-directory: ${{ runner.temp }}/peen-work
          prompt: Write a short guide to table-driven tests in GUIDE.md.
          model: gateway/your-model-id
          upstreams: >-
            [{"name":"gateway","type":"openai","baseUrl":"https://gateway.example/v1","apiKeyEnv":"PROVIDER_KEY"}]
        env:
          PROVIDER_KEY: ${{ secrets.PROVIDER_KEY }}
      - name: Read the generated guide
        run: cat "$RUNNER_TEMP/peen-work/GUIDE.md"
```

## Inputs

| Input | Meaning |
| --- | --- |
| `prompt` | Prompt text. Set exactly one of this and `prompt-file`. |
| `prompt-file` | Nonempty prompt file, relative to `GITHUB_WORKSPACE` or absolute. Its contents go through stdin, including trailing newlines. |
| `working-directory` | Writable directory, default `.` relative to `GITHUB_WORKSPACE`. |
| `upstreams` | Required [provider configuration](configuration.md) as `PEEN_UPSTREAMS` JSON. Supply every credential named by `apiKeyEnv` through the step's `env`. |
| `model` | Required provider-qualified model. |
| `compaction-model` | Summarizer model, defaulting to `model`. |
| `reasoning-effort` | Optional reasoning level for the turn. |
| `system-prompt` | Optional additional system instructions. |
| `image` | Peen image, default `psyb0t/peen:latest`. Set a tag or digest to freeze it. The image must support `peen exec`. |

## Outputs and failures

`steps.peen.outputs.result` holds the reply text. `steps.peen.outputs.result-file` names a file under `RUNNER_TEMP` with that same stdout, which is useful for large replies or artifact uploads. A failed turn fails the action and retains any partial reply in that file. Startup failures before the turn have no reply outputs. Use `if: always()` and check that the output is nonempty when collecting a failed step's file.

Each invocation has separate configuration, home, and SQLite state under `RUNNER_TEMP`; it starts no persistent service and removes only its own container on exit. The agent runs as the runner's UID and GID, so files it writes retain runner ownership. Only the selected directory and action runtime directory are mounted. The agent can edit the mounted working directory and use the container's network. It reads the directory's normal Peen harness, including hooks. Provider credentials reach model calls by environment variable name. Transcripts and tool output remain unredacted, as described in [Security](security.md).

Set a timeout on the calling job. Pass only the provider credentials the prompt needs, and run trusted prompts and harness files. A prompt can ask the agent to make network calls or modify files; the action adds no approval step.
