"""Run the Peen binary unchanged inside a networkless Harbor task container.

Setup copies the static Peen binary and the model relay into the task
container, starts the relay as root, and proves the container has no network,
no Docker socket and no host home directory. The run sends the task
instruction through ``peen exec`` as an unprivileged user with debug logging,
then copies Peen's frames, logs and SQLite state into the trial's log
directory.

Harbor passes the job's agent ``kwargs`` to the constructor, so a job sets the
context window, compaction model, round limit and run timeout without a code
change.
"""

import json
import os
import pathlib
import shlex

from harbor.agents.base import BaseAgent

ENV_BIN_DIR = "BENCH_BIN_DIR"
PEEN_BINARY_NAME = "peen"
RELAY_BINARY_NAME = "relay"
VERSION_FILE_NAME = "peen.version"
CONTAINER_PEEN = "/usr/local/bin/peen"
CONTAINER_RELAY = "/usr/local/bin/peen-relay"
PEEN_DIR = "/peen"
PROVIDER_SOCKET = "/provider/provider.sock"
MODEL_URL = "http://127.0.0.1:8765/v1"
AGENT_USER = "10001"
ROOT_USER = "root"
DEFAULT_WORKSPACE = "/app"
DEFAULT_RUN_TIMEOUT_SECONDS = 1860
DEFAULT_CONTEXT_WINDOW_TOKENS = "1000000"
DEFAULT_COMPACTION_MODEL = "zai/glm-5.3-flash"
DEFAULT_COMPACTION_MODE = "summarize"
DEFAULT_MAX_TOOL_ROUNDS = "64"
PROVIDER_NAME = "zai"
PROVIDER_TYPE = "zai-coding"
SHELL_PROBE = (
    "set -e; "
    "test $(tail -n +2 /proc/net/route | wc -l) -eq 0; "
    "test ! -e /var/run/docker.sock; "
    "test ! -e /home/bw; "
    'printf \'{"network_routes":0,"docker_socket":false,"host_home":false,"uid":%s}\' "$(id -u)"'
)


class SetupError(RuntimeError):
    """The task container could not be prepared for Peen."""


class RunError(RuntimeError):
    """Peen exited with a failure inside the task container."""


def bin_dir():
    return pathlib.Path(os.environ.get(ENV_BIN_DIR, pathlib.Path(__file__).parent))


class PeenAgent(BaseAgent):
    def __init__(
        self,
        *args,
        workspace=DEFAULT_WORKSPACE,
        run_timeout_sec=None,
        context_window_tokens=None,
        compaction_model=None,
        compaction_mode=None,
        max_tool_rounds=None,
        **kwargs,
    ):
        super().__init__(*args, **kwargs)
        self.workspace = workspace
        self.run_timeout_sec = int(run_timeout_sec or DEFAULT_RUN_TIMEOUT_SECONDS)
        self.context_window_tokens = str(
            context_window_tokens or DEFAULT_CONTEXT_WINDOW_TOKENS
        )
        self.compaction_model = compaction_model or DEFAULT_COMPACTION_MODEL
        self.compaction_mode = compaction_mode or DEFAULT_COMPACTION_MODE
        self.max_tool_rounds = str(max_tool_rounds or DEFAULT_MAX_TOOL_ROUNDS)

    @staticmethod
    def name():
        return "peen"

    def version(self):
        try:
            return (bin_dir() / VERSION_FILE_NAME).read_text().strip()
        except OSError as err:
            raise SetupError("read the built Peen version") from err

    async def setup(self, environment):
        await environment.upload_file(bin_dir() / PEEN_BINARY_NAME, CONTAINER_PEEN)
        await environment.upload_file(
            bin_dir() / RELAY_BINARY_NAME, CONTAINER_RELAY
        )
        prepare = await environment.exec(
            f"chmod 755 {CONTAINER_PEEN} {CONTAINER_RELAY} && "
            f"mkdir -p {PEEN_DIR} && chmod 777 {PEEN_DIR} && "
            f"chmod -R a+rwX {shlex.quote(self.workspace)} && "
            f"(BENCH_SOCKET={PROVIDER_SOCKET} nohup {CONTAINER_RELAY} "
            f">{PEEN_DIR}/relay.log 2>&1 &)",
            user=ROOT_USER,
        )
        if prepare.return_code:
            raise SetupError(f"prepare sandbox: {prepare.stderr or prepare.stdout}")
        directories = await environment.exec(
            f"mkdir -p {PEEN_DIR}/config {PEEN_DIR}/state {PEEN_DIR}/home",
            user=AGENT_USER,
        )
        if directories.return_code:
            raise SetupError("create agent directories")
        probe = await environment.exec(SHELL_PROBE, user=AGENT_USER)
        (self.logs_dir / "sandbox-check.json").write_text(probe.stdout or "")
        if probe.return_code:
            raise SetupError(f"sandbox isolation check: {probe.stdout}{probe.stderr}")

    def settings(self):
        upstreams = [
            {"name": PROVIDER_NAME, "type": PROVIDER_TYPE, "baseUrl": MODEL_URL},
        ]
        return {
            "PEEN_CONFIG_DIR": f"{PEEN_DIR}/config",
            "PEEN_STATE_DIR": f"{PEEN_DIR}/state",
            "PEEN_WORKER_SOCKET_DIR": f"{PEEN_DIR}/sockets",
            "HOME": f"{PEEN_DIR}/home",
            "USER": "appuser",
            "PEEN_WORKSPACE_ROOT": self.workspace,
            "PEEN_DEFAULT_AGENT_SPACE": "control",
            "PEEN_UPSTREAMS": json.dumps(upstreams),
            "PEEN_DEFAULT_MODEL": self.model_name,
            "PEEN_COMPACTION_MODEL": self.compaction_model,
            "PEEN_COMPACTION_MODE": self.compaction_mode,
            "PEEN_MAX_CONTEXT_TOKENS": self.context_window_tokens,
            "PEEN_MAX_TOOL_ROUNDS": self.max_tool_rounds,
            "LOG_LEVEL": "debug",
            "LOG_FORMAT": "json",
            "LOG_ADD_SOURCE": "true",
            "PEEN_HTTP_LISTEN_ADDRESS": "127.0.0.1:8080",
        }

    async def run(self, instruction, environment, context):
        command = (
            f"printf %s {shlex.quote(instruction)} | {CONTAINER_PEEN} exec "
            f"--workspace {shlex.quote(self.workspace)} --agent-space control "
            f"--prompt - > {PEEN_DIR}/frames.jsonl 2> {PEEN_DIR}/runtime.log"
        )
        result = None
        try:
            result = await environment.exec(
                command,
                env=self.settings(),
                user=AGENT_USER,
                timeout_sec=self.run_timeout_sec,
            )
        finally:
            await environment.download_dir(PEEN_DIR, self.logs_dir / "peen")
        context.metadata = {
            "exit_code": result.return_code,
            "network": "none",
            "peen_version": self.version(),
        }
        if result.return_code:
            raise RunError(f"peen exec exited {result.return_code}; see runtime.log")
