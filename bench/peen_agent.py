"""Run the Peen binary unchanged inside a networkless Harbor task container.

Setup copies the static Peen binary, the model relay and the scenario driver
into the task container, starts the relay as root, and proves the container
has no network, no Docker socket and no host home directory. The run sends
the task instruction through ``peen exec`` as an unprivileged user with debug
logging, then copies Peen's frames, logs and SQLite state into the trial's log
directory.

A harness task may ship a ``/scenario`` directory in its image:

- ``scenario.json`` switches the trial to the scenario driver, which starts
  the server and runs the scripted steps instead of one ``peen exec`` turn;
- ``faults.json`` is the trial's provider fault schedule, which setup copies
  to the bench state directory where the proxy reads it;
- ``peen-env.json`` holds ``PEEN_`` settings for this task only;
- ``config/`` is copied into Peen's configuration directory.

Setup reads and removes ``/scenario`` before Peen starts, so the agent never
sees it. Harbor passes the job's agent ``kwargs`` to the constructor, so a job
sets the context window, compaction model, round limit, run timeout,
auto-evolve and any other ``PEEN_`` settings without a code change.
"""

import json
import os
import pathlib
import re
import shlex

from harbor.agents.base import BaseAgent
from harbor.agents.installed.base import NonZeroAgentExitCodeError

ENV_BIN_DIR = "BENCH_BIN_DIR"
ENV_STATE_DIR = "BENCH_STATE_DIR"
PEEN_BINARY_NAME = "peen"
RELAY_BINARY_NAME = "relay"
DRIVER_BINARY_NAME = "driver"
VERSION_FILE_NAME = "peen.version"
LIB_DIR_NAME = "harness/lib"
CONTAINER_PEEN = "/usr/local/bin/peen"
CONTAINER_RELAY = "/usr/local/bin/peen-relay"
CONTAINER_DRIVER = "/usr/local/bin/peen-driver"
PEEN_DIR = "/peen"
PEEN_LIB_DIR = f"{PEEN_DIR}/lib"
PEEN_CONFIG_DIR = f"{PEEN_DIR}/config"
DRIVER_DIR = f"{PEEN_DIR}/driver"
DRIVER_SCENARIO = f"{DRIVER_DIR}/scenario.json"
SCENARIO_DIR = "/scenario"
SCENARIO_FILE = f"{SCENARIO_DIR}/scenario.json"
FAULTS_FILE = f"{SCENARIO_DIR}/faults.json"
PEEN_ENV_FILE = f"{SCENARIO_DIR}/peen-env.json"
SCENARIO_CONFIG_DIR = f"{SCENARIO_DIR}/config"
FAULTS_DIR = "faults"
FAULT_FILE_SUFFIX = ".json"
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
PEEN_SETTING_PATTERN = re.compile(r"^PEEN_[A-Z0-9_]+$")
# Settings a task or job may not change: they wire Peen to the sandbox, the
# relay, and the directories the adapter downloads.
FIXED_SETTINGS = frozenset(
    (
        "PEEN_CONFIG_DIR",
        "PEEN_STATE_DIR",
        "PEEN_WORKER_SOCKET_DIR",
        "PEEN_WORKSPACE_ROOT",
        "PEEN_UPSTREAMS",
        "PEEN_HTTP_LISTEN_ADDRESS",
        "PEEN_DEFAULT_AGENT_SPACE",
    )
)
MISSING_FILE = "__missing__"
SHELL_PROBE = (
    "set -e; "
    "test $(tail -n +2 /proc/net/route | wc -l) -eq 0; "
    "test ! -e /var/run/docker.sock; "
    "test ! -e /home/bw; "
    'printf \'{"network_routes":0,"docker_socket":false,"host_home":false,"uid":%s}\' "$(id -u)"'
)


class SetupError(RuntimeError):
    """The task container could not be prepared for Peen."""


class RunError(NonZeroAgentExitCodeError):
    """Peen or the driver exited with a failure inside the task container.

    It subclasses Harbor's non-zero exit error so the grader still runs: a
    failed turn is a result to grade, not a broken trial.
    """


def bin_dir():
    return pathlib.Path(os.environ.get(ENV_BIN_DIR, pathlib.Path(__file__).parent))


def source_dir():
    return pathlib.Path(__file__).parent


def checked_settings(raw, origin):
    """Validate PEEN_ settings from a task or a job and return them as strings."""
    if not isinstance(raw, dict):
        raise SetupError(f"{origin} must be a JSON object")
    settings = {}
    for name, value in raw.items():
        if not PEEN_SETTING_PATTERN.match(name):
            raise SetupError(f"{origin} sets {name!r}, which is not a PEEN_ setting")
        if name in FIXED_SETTINGS:
            raise SetupError(f"{origin} may not set {name}")
        if not isinstance(value, (str, int, bool)):
            raise SetupError(f"{origin} {name} must be a string, number or boolean")
        settings[name] = str(value).lower() if isinstance(value, bool) else str(value)
    return settings


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
        auto_evolve=False,
        peen_settings=None,
        **kwargs,
    ):
        super().__init__(*args, **kwargs)
        self.auto_evolve = bool(auto_evolve)
        self.workspace = workspace
        self.run_timeout_sec = int(run_timeout_sec or DEFAULT_RUN_TIMEOUT_SECONDS)
        self.context_window_tokens = str(
            context_window_tokens or DEFAULT_CONTEXT_WINDOW_TOKENS
        )
        self.compaction_model = compaction_model or DEFAULT_COMPACTION_MODEL
        self.compaction_mode = compaction_mode or DEFAULT_COMPACTION_MODE
        self.max_tool_rounds = str(max_tool_rounds or DEFAULT_MAX_TOOL_ROUNDS)
        self.job_settings = checked_settings(peen_settings or {}, "the job")
        self.task_settings = {}
        self.has_scenario = False

    @staticmethod
    def name():
        return "peen"

    def version(self):
        try:
            return (bin_dir() / VERSION_FILE_NAME).read_text().strip()
        except OSError as err:
            raise SetupError("read the built Peen version") from err

    def trial_name(self):
        return self.logs_dir.parent.name

    async def read_container_file(self, environment, path):
        result = await environment.exec(
            f"cat {shlex.quote(path)} 2>/dev/null || printf {MISSING_FILE}",
            user=ROOT_USER,
        )
        if result.return_code:
            raise SetupError(f"read {path}: {result.stderr}")
        if result.stdout == MISSING_FILE:
            return None
        return result.stdout

    async def take_scenario(self, environment):
        """Read the task's /scenario files, place them, and remove /scenario."""
        faults = await self.read_container_file(environment, FAULTS_FILE)
        if faults is not None:
            state_dir = os.environ.get(ENV_STATE_DIR, "")
            if not state_dir:
                raise SetupError(f"the task has faults but {ENV_STATE_DIR} is unset")
            try:
                json.loads(faults)
            except ValueError as err:
                raise SetupError("faults.json is not JSON") from err
            target = pathlib.Path(state_dir) / FAULTS_DIR
            target.mkdir(parents=True, exist_ok=True)
            (target / (self.trial_name() + FAULT_FILE_SUFFIX)).write_text(faults)

        peen_env = await self.read_container_file(environment, PEEN_ENV_FILE)
        if peen_env is not None:
            try:
                self.task_settings = checked_settings(
                    json.loads(peen_env), "peen-env.json"
                )
            except ValueError as err:
                raise SetupError("peen-env.json is not JSON") from err

        scenario = await self.read_container_file(environment, SCENARIO_FILE)
        self.has_scenario = scenario is not None

        place = await environment.exec(
            f"mkdir -p {DRIVER_DIR} {PEEN_CONFIG_DIR} && "
            f"if [ -f {SCENARIO_FILE} ]; then "
            f"cp {SCENARIO_FILE} {DRIVER_SCENARIO}; fi && "
            f"if [ -d {SCENARIO_CONFIG_DIR} ]; then "
            f"cp -R {SCENARIO_CONFIG_DIR}/. {PEEN_CONFIG_DIR}/; fi && "
            f"chown -R {AGENT_USER}:{AGENT_USER} {PEEN_DIR} && "
            f"rm -rf {SCENARIO_DIR}",
            user=ROOT_USER,
        )
        if place.return_code:
            raise SetupError(f"place the scenario: {place.stderr or place.stdout}")

    async def upload_tools(self, environment):
        await environment.upload_file(bin_dir() / PEEN_BINARY_NAME, CONTAINER_PEEN)
        await environment.upload_file(bin_dir() / RELAY_BINARY_NAME, CONTAINER_RELAY)
        driver = bin_dir() / DRIVER_BINARY_NAME
        if driver.exists():
            await environment.upload_file(driver, CONTAINER_DRIVER)
        lib = source_dir() / LIB_DIR_NAME
        if lib.is_dir():
            await environment.upload_dir(lib, PEEN_LIB_DIR)

    async def setup(self, environment):
        await self.upload_tools(environment)
        trial = shlex.quote(self.trial_name())
        prepare = await environment.exec(
            f"chmod 755 {CONTAINER_PEEN} {CONTAINER_RELAY} && "
            f"(test ! -e {CONTAINER_DRIVER} || chmod 755 {CONTAINER_DRIVER}) && "
            f"mkdir -p {PEEN_DIR} && chmod 777 {PEEN_DIR} && "
            f"chmod -R a+rwX {shlex.quote(self.workspace)} && "
            f"(BENCH_SOCKET={PROVIDER_SOCKET} BENCH_TRIAL={trial} "
            f"nohup {CONTAINER_RELAY} >{PEEN_DIR}/relay.log 2>&1 &)",
            user=ROOT_USER,
        )
        if prepare.return_code:
            raise SetupError(f"prepare sandbox: {prepare.stderr or prepare.stdout}")
        await self.take_scenario(environment)
        directories = await environment.exec(
            f"mkdir -p {PEEN_CONFIG_DIR} {PEEN_DIR}/state {PEEN_DIR}/home",
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
        settings = {
            "PEEN_CONFIG_DIR": PEEN_CONFIG_DIR,
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
            "PEEN_AUTO_EVOLVE": "true" if self.auto_evolve else "false",
        }
        # The task's own settings win over the job's, because the task's
        # behavior depends on them; the job's win over the defaults.
        settings.update(self.job_settings)
        settings.update(self.task_settings)
        return settings

    def command(self, instruction):
        if self.has_scenario:
            return (
                f"{CONTAINER_DRIVER} --scenario {DRIVER_SCENARIO} "
                f"--out {PEEN_DIR} --peen {CONTAINER_PEEN} "
                f"2> {PEEN_DIR}/driver.log"
            )
        return (
            f"printf %s {shlex.quote(instruction)} | {CONTAINER_PEEN} exec "
            f"--workspace {shlex.quote(self.workspace)} --agent-space control "
            f"--prompt - > {PEEN_DIR}/frames.jsonl 2> {PEEN_DIR}/runtime.log"
        )

    async def run(self, instruction, environment, context):
        result = None
        try:
            result = await environment.exec(
                self.command(instruction),
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
            "scenario": self.has_scenario,
            "settings": sorted({**self.job_settings, **self.task_settings}),
        }
        if result.return_code:
            raise RunError(f"peen exited {result.return_code}; see runtime.log")
