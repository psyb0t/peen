#!/bin/bash
# Exercises the action against a recording Docker CLI, without a provider call.
set -euo pipefail
SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/run.sh"
TEST_ROOT="$(mktemp -d)"
readonly SCRIPT TEST_ROOT
trap 'rm -rf "$TEST_ROOT"' EXIT
trap 'printf "action test failed at line %s\n" "$LINENO" >&2' ERR
mkdir -p "$TEST_ROOT/bin" "$TEST_ROOT/workspace/another directory" "$TEST_ROOT/temp"
cat >"$TEST_ROOT/bin/docker" <<'MOCK'
#!/bin/bash
set -euo pipefail
jq -nc --args '$ARGS.positional' -- "$@" >>"$RECORD/calls"
case "$1" in
pull) exit "${PULL_STATUS:-0}" ;;
create) exit "${CREATE_STATUS:-0}" ;;
start)
    cat >"$RECORD/prompt"
    printf 'first line\nsecond line\n'
    exit "${TURN_STATUS:-0}"
    ;;
rm) exit 0 ;;
*) exit 99 ;;
esac
MOCK
chmod +x "$TEST_ROOT/bin/docker"
export PATH="$TEST_ROOT/bin:$PATH" RUNNER_OS=Linux
export GITHUB_WORKSPACE="$TEST_ROOT/workspace" RUNNER_TEMP="$TEST_ROOT/temp"
export IN_IMAGE=psyb0t/peen:latest IN_MODEL=provider/model
export IN_UPSTREAMS='[{"name":"provider","type":"openai","apiKeyEnv":"EXAMPLE_KEY"}]'
export EXAMPLE_KEY=EXAMPLE-DO-NOT-USE
# shellcheck disable=SC2016 # Command substitution is literal hostile input.
export IN_PROMPT='prompt with $(touch unwanted) and "quotes"'

run_case() {
	local name="$1" expected="$2"
	shift 2
	export RECORD="$TEST_ROOT/$name" GITHUB_OUTPUT="$TEST_ROOT/$name/outputs"
	mkdir -p "$RECORD"
	local status=0
	env "$@" bash "$SCRIPT" >"$RECORD/stdout" 2>"$RECORD/stderr" || status=$?
	if [[ "$status" != "$expected" ]]; then
		cat "$RECORD/stderr" >&2
		printf '%s: expected exit %s, got %s\n' "$name" "$expected" "$status" >&2
		return 1
	fi
	printf 'PASS %s\n' "$name"
}

run_case text 0 IN_WORKING_DIRECTORY='another directory' IN_REASONING_EFFORT=low
[[ "$(cat "$RECORD/prompt")" == "$IN_PROMPT" ]]
jq -se --arg workspace "$GITHUB_WORKSPACE/another directory" '
  length == 4 and .[0][0] == "pull" and .[1][0] == "create" and
  (.[1] | index($workspace)) != null and
  (.[1] | index("EXAMPLE_KEY")) != null and
  (.[1] | index("--reasoning-effort")) != null and
  (.[1] | index("EXAMPLE-DO-NOT-USE")) == null and
  .[2][0] == "start" and .[3][0] == "rm" and .[3][-1] == .[2][-1]
' "$RECORD/calls" >/dev/null
[[ ! -e "$GITHUB_WORKSPACE/unwanted" ]]
grep -q '^result<<PEEN_' "$GITHUB_OUTPUT"
grep -q '^second line$' "$GITHUB_OUTPUT"
cmp "$RECORD/stdout" "$(sed -n 's/^result-file=//p' "$GITHUB_OUTPUT")"

printf 'file prompt\nwith a trailing newline\n' >"$GITHUB_WORKSPACE/prompt.txt"
run_case file 0 IN_PROMPT= IN_PROMPT_FILE=prompt.txt
cmp "$GITHUB_WORKSPACE/prompt.txt" "$RECORD/prompt"
run_case failed-turn 7 TURN_STATUS=7
grep -q '^result-file=' "$GITHUB_OUTPUT"
jq -se '.[-1][0] == "rm"' "$RECORD/calls" >/dev/null
run_case pull-failure 1 PULL_STATUS=1
jq -se 'length == 1' "$RECORD/calls" >/dev/null
run_case create-failure 1 CREATE_STATUS=1
jq -se 'length == 2' "$RECORD/calls" >/dev/null

for tc in missing-prompt both-prompts missing-file invalid-upstreams missing-key invalid-key missing-directory platform; do
	# shellcheck disable=SC2016 # The malformed name must never be evaluated.
	case "$tc" in
	missing-prompt) overrides=(IN_PROMPT=) ;;
	both-prompts) overrides=(IN_PROMPT_FILE=prompt.txt) ;;
	missing-file) overrides=(IN_PROMPT= IN_PROMPT_FILE=missing) ;;
	invalid-upstreams) overrides=(IN_UPSTREAMS=invalid) ;;
	missing-key) overrides=(EXAMPLE_KEY=) ;;
	invalid-key) overrides=('IN_UPSTREAMS=[{"apiKeyEnv":"$(touch unwanted)"}]') ;;
	missing-directory) overrides=(IN_WORKING_DIRECTORY=missing) ;;
	platform) overrides=(RUNNER_OS=Windows) ;;
	esac
	run_case "$tc" 1 "${overrides[@]}"
	[[ ! -e "$RECORD/calls" ]]
done
printf 'All action contract cases passed\n'
