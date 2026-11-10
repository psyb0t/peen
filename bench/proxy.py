"""Serve one provider's chat endpoint and fixed models on a Unix socket.

The benchmark task containers have no network. Their relay reaches this proxy
through the socket, and the proxy alone holds the provider key, read from the
repository's .env mounted read-only. It forwards only chat completions for the
allowed models to the configured base URL and refuses everything else.

Every model request is logged as one JSON record with its trial, status,
duration, sizes and the provider's token usage, and an upstream error body is
logged truncated, so a run can be reconstructed without recording the model
traffic itself. The key is never logged.

A trial can carry a fault schedule. The relay in the task container sends the
trial name in the X-Bench-Trial header, and the proxy reads
``faults/<trial>.json`` from the bench state directory, which the adapter
writes before Peen starts. A schedule answers chosen requests with an error
status, stalls them, cuts or truncates the stream, corrupts tool-call
arguments, drops the connection, or simulates an outage. It can also replay
scripted replies instead of calling the provider, for overhead measurements
that must not vary with the model. ``docs/benchmarks.md`` documents the file.
"""

import datetime
import http.server
import json
import logging
import logging.handlers
import os
import pathlib
import re
import socketserver
import sys
import threading
import time
import urllib.error
import urllib.request

ENV_STATE_DIR = "BENCH_STATE_DIR"
ENV_ENV_FILE = "BENCH_ENV_FILE"
ENV_KEY_NAME = "BENCH_KEY_ENV"
ENV_BASE_URL = "BENCH_BASE_URL"
ENV_MODELS = "BENCH_MODELS"
DEFAULT_ENV_FILE = "/repo.env"
DEFAULT_KEY_NAME = "ZAI_TOKEN"
DEFAULT_BASE_URL = "https://api.z.ai/api/coding/paas/v4"
DEFAULT_MODELS = "glm-5.3,glm-5.3-flash"
SOCKET_DIR = "provider"
SOCKET_NAME = "provider.sock"
FAULTS_DIR = "faults"
FAULT_FILE_SUFFIX = ".json"
LOG_NAME = "proxy.log"
LOG_MAX_BYTES = 50_000_000
LOG_BACKUPS = 5
MAX_BODY_BYTES = 32 * 1024 * 1024
MAX_FAULT_FILE_BYTES = 4 * 1024 * 1024
TIMEOUT_SECONDS = 1900
STREAM_CHUNK_BYTES = 4096
ERROR_BODY_LOG_BYTES = 2000
SOCKET_MODE = 0o666
PATH_MODELS = "/v1/models"
PATH_CHAT = "/v1/chat/completions"
UPSTREAM_PREFIX = "/v1"
CONTENT_TYPE_JSON = "application/json"
CONTENT_TYPE_SSE = "text/event-stream"
HEADER_CONTENT_TYPE = "Content-Type"
HEADER_CONTENT_LENGTH = "Content-Length"
HEADER_AUTHORIZATION = "Authorization"
HEADER_RETRY_AFTER = "Retry-After"
HEADER_TRIAL = "X-Bench-Trial"
BEARER_PREFIX = "Bearer "
STATUS_BAD_REQUEST = 400
STATUS_FORBIDDEN = 403
STATUS_TOO_LARGE = 413
STATUS_SERVICE_UNAVAILABLE = 503
STATUS_NO_RESPONSE = 0
REASON_PATH = "path"
REASON_BODY_SIZE = "body_size"
REASON_INVALID_JSON = "invalid_json"
REASON_MODEL = "model"
REASON_FAULT_FILE = "fault_file"
SSE_DATA_PREFIX = b"data:"
SSE_DONE = b"[DONE]"
SSE_EVENT_END = b"\n\n"
SSE_DONE_EVENT = SSE_DATA_PREFIX + b" " + SSE_DONE + SSE_EVENT_END
TRIAL_PATTERN = re.compile(r"^[A-Za-z0-9._-]{1,200}$")

FAULT_STATUS = "status"
FAULT_STALL = "stall"
FAULT_CUT_STREAM = "cutStream"
FAULT_TRUNCATE = "truncate"
FAULT_CORRUPT_TOOL_ARGUMENTS = "corruptToolArguments"
FAULT_DROP = "drop"
FAULT_OUTAGE = "outage"
FAULT_ACTIONS = frozenset(
    (
        FAULT_STATUS,
        FAULT_STALL,
        FAULT_CUT_STREAM,
        FAULT_TRUNCATE,
        FAULT_CORRUPT_TOOL_ARGUMENTS,
        FAULT_DROP,
        FAULT_OUTAGE,
    )
)
SELECTOR_REQUEST = "request"
SELECTOR_REQUESTS = "requests"
SELECTOR_EVERY = "every"
SELECTORS = (SELECTOR_REQUEST, SELECTOR_REQUESTS, SELECTOR_EVERY)
# A fault's stage limits it to Peen's compaction requests, which carry no
# tools, or to its turn requests, which do. The main and compaction models
# can be the same model, so the model filter alone cannot tell them apart.
STAGE_COMPACTION = "compaction"
STAGE_TURN = "turn"
STAGES = frozenset((STAGE_COMPACTION, STAGE_TURN))
FAULT_STATUS_MIN = 400
FAULT_STATUS_MAX = 599
FAULT_MAX_SECONDS = 3600
FAULT_MAX_COUNT = 1_000_000
FINISH_LENGTH = "length"
FINISH_STOP = "stop"
FINISH_TOOL_CALLS = "tool_calls"
CORRUPT_ARGUMENTS_PREFIX = '{"bench-corrupted'
FAULT_MESSAGE = "injected by the bench proxy"
OUTAGE_MESSAGE = "bench proxy outage"
FAULT_FILE_MESSAGE = "bad bench fault file"
FAULT_ERROR_TYPE = "bench_fault"
REPLAY_ID_PREFIX = "bench-replay-"
TRUNCATE_ID = "bench-truncate"
REPLAY_INDEX_TOKEN = "{i}"
REPLAY_COMPLETION_TOKENS = 16
REPLAY_BYTES_PER_TOKEN = 4
OBJECT_CHUNK = "chat.completion.chunk"
OBJECT_COMPLETION = "chat.completion"
ROLE_ASSISTANT = "assistant"
TOOL_TYPE_FUNCTION = "function"
RECORD_FIELDS = (
    "reason",
    "method",
    "path",
    "trial",
    "trial_request",
    "model",
    "messages",
    "tools",
    "stream",
    "status",
    "seconds",
    "request_bytes",
    "response_bytes",
    "prompt_tokens",
    "completion_tokens",
    "finish_reason",
    "fault",
    "replay",
    "error_body",
    "err",
    "models",
    "socket_path",
    "base_url",
)

logger = logging.getLogger(__name__)


class ProxyError(Exception):
    """The proxy cannot start."""


class FaultFileError(Exception):
    """A trial's fault file is malformed."""


class JSONFormatter(logging.Formatter):
    def format(self, record):
        entry = {
            "time": datetime.datetime.fromtimestamp(
                record.created, datetime.UTC
            ).isoformat(),
            "level": record.levelname.lower(),
            "file": record.filename,
            "line": record.lineno,
            "func": record.funcName,
            "msg": record.getMessage(),
        }
        for field in RECORD_FIELDS:
            if hasattr(record, field):
                entry[field] = getattr(record, field)
        return json.dumps(entry, sort_keys=True)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


class Settings:
    def __init__(self):
        state_dir = os.environ.get(ENV_STATE_DIR, "")
        if not state_dir:
            raise ProxyError(f"{ENV_STATE_DIR} is not set")
        self.state_dir = pathlib.Path(state_dir)
        self.env_file = pathlib.Path(os.environ.get(ENV_ENV_FILE, DEFAULT_ENV_FILE))
        self.key_name = os.environ.get(ENV_KEY_NAME, DEFAULT_KEY_NAME)
        self.base_url = os.environ.get(ENV_BASE_URL, DEFAULT_BASE_URL).rstrip("/")
        self.models = frozenset(
            name.strip()
            for name in os.environ.get(ENV_MODELS, DEFAULT_MODELS).split(",")
            if name.strip()
        )
        self.socket = self.state_dir / SOCKET_DIR / SOCKET_NAME
        self.faults_dir = self.state_dir / FAULTS_DIR
        self.log_file = self.state_dir / LOG_NAME
        self.api_key = ""

    def load_key(self):
        try:
            lines = self.env_file.read_text().splitlines()
        except OSError as err:
            raise ProxyError("read the provider environment file") from err
        prefix = self.key_name + "="
        for line in lines:
            if line.startswith(prefix):
                self.api_key = line.removeprefix(prefix).strip().strip("\"'")
                return
        raise ProxyError(f"{self.key_name} is missing from the environment file")


SETTINGS = None
OPENER = urllib.request.build_opener(NoRedirect(), urllib.request.ProxyHandler({}))


def configure_logging(log_file):
    formatter = JSONFormatter()
    handlers = [
        logging.StreamHandler(sys.stderr),
        logging.handlers.RotatingFileHandler(
            log_file,
            maxBytes=LOG_MAX_BYTES,
            backupCount=LOG_BACKUPS,
        ),
    ]
    for handler in handlers:
        handler.setFormatter(formatter)
    logging.basicConfig(level=logging.INFO, handlers=handlers)


def bounded_int(value, name, low, high):
    if not isinstance(value, int) or isinstance(value, bool):
        raise FaultFileError(f"{name} must be an integer")
    if not low <= value <= high:
        raise FaultFileError(f"{name} must be between {low} and {high}")
    return value


def parse_selector(raw, label, fault):
    selectors = [key for key in SELECTORS if key in raw]
    if len(selectors) != 1:
        raise FaultFileError(f"{label} needs exactly one of {', '.join(SELECTORS)}")
    selector = selectors[0]
    if selector != SELECTOR_REQUESTS:
        fault[selector] = bounded_int(
            raw[selector], f"{label} {selector}", 1, FAULT_MAX_COUNT
        )
        return
    requests = raw[SELECTOR_REQUESTS]
    if not isinstance(requests, list) or not requests:
        raise FaultFileError(f"{label} requests must be a non-empty list")
    fault[SELECTOR_REQUESTS] = frozenset(
        bounded_int(value, f"{label} requests", 1, FAULT_MAX_COUNT)
        for value in requests
    )


def parse_fault(raw, index):
    label = f"fault {index}"
    if not isinstance(raw, dict):
        raise FaultFileError(f"{label} must be an object")
    action = raw.get("action")
    if action not in FAULT_ACTIONS:
        raise FaultFileError(f"{label} has unknown action {action!r}")
    model = raw.get("model")
    if model is not None and not isinstance(model, str):
        raise FaultFileError(f"{label} model must be a string")
    stage = raw.get("stage")
    if stage is not None and stage not in STAGES:
        raise FaultFileError(f"{label} has unknown stage {stage!r}")
    fault = {"action": action, "model": model, "stage": stage}
    parse_selector(raw, label, fault)

    if action == FAULT_STATUS:
        fault["status"] = bounded_int(
            raw.get("status"), f"{label} status", FAULT_STATUS_MIN, FAULT_STATUS_MAX
        )
        if "retryAfter" in raw:
            fault["retryAfter"] = bounded_int(
                raw["retryAfter"], f"{label} retryAfter", 0, FAULT_MAX_SECONDS
            )
    if action in (FAULT_STALL, FAULT_OUTAGE):
        fault["seconds"] = bounded_int(
            raw.get("seconds"), f"{label} seconds", 1, FAULT_MAX_SECONDS
        )
    if action == FAULT_CUT_STREAM:
        fault["afterBytes"] = bounded_int(
            raw.get("afterBytes"), f"{label} afterBytes", 0, MAX_BODY_BYTES
        )
    if action == FAULT_TRUNCATE:
        fault["afterEvents"] = bounded_int(
            raw.get("afterEvents"), f"{label} afterEvents", 0, FAULT_MAX_COUNT
        )
    return fault


def parse_replay_step(step, index):
    label = f"replay step {index}"
    if not isinstance(step, dict):
        raise FaultFileError(f"{label} must be an object")
    repeat = bounded_int(step.get("repeat", 1), f"{label} repeat", 1, FAULT_MAX_COUNT)
    tool_calls = step.get("toolCalls") or []
    if not isinstance(tool_calls, list):
        raise FaultFileError(f"{label} toolCalls must be a list")
    for call in tool_calls:
        if not isinstance(call, dict) or not isinstance(call.get("name"), str):
            raise FaultFileError(f"{label} has a tool call without a name")
        if not isinstance(call.get("arguments", {}), dict):
            raise FaultFileError(f"{label} tool call arguments must be an object")
    content = step.get("content", "")
    if not isinstance(content, str):
        raise FaultFileError(f"{label} content must be a string")
    return [{"content": content, "toolCalls": tool_calls}] * repeat


def parse_replay(raw):
    if raw is None:
        return None
    if not isinstance(raw, dict):
        raise FaultFileError("replay must be an object")
    steps = raw.get("steps")
    if not isinstance(steps, list) or not steps:
        raise FaultFileError("replay steps must be a non-empty list")
    expanded = []
    for index, step in enumerate(steps):
        expanded.extend(parse_replay_step(step, index))
    if len(expanded) > FAULT_MAX_COUNT:
        raise FaultFileError("replay expands to too many steps")
    return {"steps": expanded}


def parse_fault_file(data):
    """Validate a trial's fault file and return its schedule.

    Raises FaultFileError when any field is missing, of the wrong type, or
    out of range, so a typo never runs a trial without its faults.
    """
    if not isinstance(data, dict):
        raise FaultFileError("the fault file must be an object")
    raw_faults = data.get("faults", [])
    if not isinstance(raw_faults, list):
        raise FaultFileError("faults must be a list")
    return {
        "faults": [parse_fault(raw, index) for index, raw in enumerate(raw_faults)],
        "replay": parse_replay(data.get("replay")),
    }


EMPTY_SCHEDULE = {"faults": [], "replay": None}


class TrialState:
    """Request counters and the parsed schedule for one trial."""

    def __init__(self, schedule):
        self.schedule = schedule
        self.total = 0
        # Request counts keyed by (model or None, stage or None), so each
        # fault counts only the requests its own filters select.
        self.counts = {}
        self.outage_until = 0.0
        self.replay_index = 0

    def count(self, model, stage):
        for key in ((None, None), (model, None), (None, stage), (model, stage)):
            self.counts[key] = self.counts.get(key, 0) + 1


def request_stage(has_tools):
    return STAGE_TURN if has_tools else STAGE_COMPACTION


def match_fault(state, model, stage):
    for fault in state.schedule["faults"]:
        if fault["model"] is not None and fault["model"] != model:
            continue
        if fault["stage"] is not None and fault["stage"] != stage:
            continue
        count = state.counts[(fault["model"], fault["stage"])]
        if fault.get(SELECTOR_REQUEST) == count:
            return fault
        if count in fault.get(SELECTOR_REQUESTS, ()):
            return fault
        if SELECTOR_EVERY in fault and count % fault[SELECTOR_EVERY] == 0:
            return fault
    return None


class Trials:
    """Per-trial schedules, loaded once per trial and counted under a lock."""

    def __init__(self, faults_dir):
        self.faults_dir = faults_dir
        self.lock = threading.Lock()
        self.states = {}

    def load(self, trial):
        path = self.faults_dir / (trial + FAULT_FILE_SUFFIX)
        try:
            size = path.stat().st_size
        except FileNotFoundError:
            return EMPTY_SCHEDULE
        if size > MAX_FAULT_FILE_BYTES:
            raise FaultFileError("the fault file is too large")
        try:
            return parse_fault_file(json.loads(path.read_text()))
        except (OSError, ValueError) as err:
            raise FaultFileError("read the fault file") from err

    def next_request(self, trial, model, now, has_tools=True):
        """Count one request; return its number, fault, and replay step."""
        stage = request_stage(has_tools)
        with self.lock:
            state = self.states.get(trial)
            if state is None:
                state = TrialState(self.load(trial))
                self.states[trial] = state
            state.total += 1
            state.count(model, stage)
            if now < state.outage_until:
                return state.total, {"action": FAULT_OUTAGE}, None
            fault = match_fault(state, model, stage)
            if fault and fault["action"] == FAULT_OUTAGE:
                state.outage_until = now + fault["seconds"]
            replay = state.schedule["replay"]
            if not replay or fault:
                return state.total, fault, None
            steps = replay["steps"]
            step = dict(steps[min(state.replay_index, len(steps) - 1)])
            step["index"] = state.replay_index
            state.replay_index += 1
            return state.total, None, step


TRIALS = None


def usage_from_event(event, fields):
    """Record usage and the finish reason from one parsed stream chunk."""
    usage = event.get("usage")
    if isinstance(usage, dict):
        fields["prompt_tokens"] = usage.get("prompt_tokens")
        fields["completion_tokens"] = usage.get("completion_tokens")
    for choice in event.get("choices") or []:
        reason = choice.get("finish_reason")
        if reason:
            fields["finish_reason"] = reason


def parse_sse_event(raw_event):
    """Return the JSON payload of one SSE event, or None for [DONE] or noise."""
    for line in raw_event.split(b"\n"):
        if not line.startswith(SSE_DATA_PREFIX):
            continue
        payload = line.removeprefix(SSE_DATA_PREFIX).strip()
        if payload == SSE_DONE:
            return None
        try:
            return json.loads(payload)
        except ValueError:
            return None
    return None


def sse_bytes(payload):
    return SSE_DATA_PREFIX + b" " + json.dumps(payload).encode() + SSE_EVENT_END


def corrupt_tool_arguments(event):
    """Prefix the first tool-call argument fragment so the whole is not JSON."""
    for choice in event.get("choices") or []:
        delta = choice.get("delta") or {}
        for call in delta.get("tool_calls") or []:
            function = call.get("function") or {}
            if "arguments" in function:
                function["arguments"] = CORRUPT_ARGUMENTS_PREFIX + function["arguments"]
                return True
    return False


def chunk_body(reply_id, model, delta, finish_reason, usage=None):
    body = {
        "id": reply_id,
        "object": OBJECT_CHUNK,
        "created": int(time.time()),
        "model": model,
        "choices": [{"index": 0, "delta": delta, "finish_reason": finish_reason}],
    }
    if usage:
        body["usage"] = usage
    return body


def replay_reply(model, step, prompt_tokens):
    """Build one scripted reply: its message delta, finish reason, and usage."""
    index = step["index"]
    reply_id = REPLAY_ID_PREFIX + str(index)
    delta = {"role": ROLE_ASSISTANT}
    if step["content"]:
        delta["content"] = step["content"].replace(REPLAY_INDEX_TOKEN, str(index))
    calls = []
    for position, call in enumerate(step["toolCalls"]):
        arguments = json.dumps(call.get("arguments") or {}).replace(
            REPLAY_INDEX_TOKEN, str(index)
        )
        calls.append(
            {
                "index": position,
                "id": f"{reply_id}-{position}",
                "type": TOOL_TYPE_FUNCTION,
                "function": {"name": call["name"], "arguments": arguments},
            }
        )
    if calls:
        delta["tool_calls"] = calls
    usage = {
        "prompt_tokens": prompt_tokens,
        "completion_tokens": REPLAY_COMPLETION_TOKENS,
        "total_tokens": prompt_tokens + REPLAY_COMPLETION_TOKENS,
    }
    return {
        "id": reply_id,
        "model": model,
        "delta": delta,
        "finish": FINISH_TOOL_CALLS if calls else FINISH_STOP,
        "usage": usage,
    }


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        return

    def refuse_path(self):
        logger.warning(
            "request refused",
            extra={"reason": REASON_PATH, "method": self.command, "path": self.path},
        )
        self.send_error(STATUS_FORBIDDEN)

    def do_GET(self):
        if self.path != PATH_MODELS:
            self.refuse_path()
            return
        body = json.dumps(
            {
                "object": "list",
                "data": [
                    {"id": model, "object": "model"}
                    for model in sorted(SETTINGS.models)
                ],
            }
        ).encode()
        self.send_body(http.HTTPStatus.OK, body)

    def do_POST(self):
        if self.path != PATH_CHAT:
            self.refuse_path()
            return
        self.forward()

    def send_body(self, status, body, extra_headers=None):
        self.send_response(status)
        self.send_header(HEADER_CONTENT_TYPE, CONTENT_TYPE_JSON)
        self.send_header(HEADER_CONTENT_LENGTH, str(len(body)))
        for name, value in (extra_headers or {}).items():
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

    def send_json_error(self, status, message, retry_after=None):
        body = json.dumps(
            {"error": {"message": message, "type": FAULT_ERROR_TYPE, "code": status}}
        ).encode()
        headers = {}
        if retry_after is not None:
            headers[HEADER_RETRY_AFTER] = str(retry_after)
        self.send_body(status, body, headers)

    def read_payload(self):
        size = int(self.headers.get(HEADER_CONTENT_LENGTH, "0"))
        if not 0 <= size <= MAX_BODY_BYTES:
            logger.warning(
                "request refused",
                extra={"reason": REASON_BODY_SIZE, "request_bytes": size},
            )
            self.send_error(STATUS_TOO_LARGE)
            return None, size
        data = self.rfile.read(size)
        try:
            return json.loads(data), size
        except ValueError as err:
            logger.warning(
                "request refused",
                extra={
                    "reason": REASON_INVALID_JSON,
                    "request_bytes": size,
                    "err": str(err),
                },
            )
            self.send_error(STATUS_BAD_REQUEST)
            return None, size

    def trial_name(self):
        trial = self.headers.get(HEADER_TRIAL, "")
        if TRIAL_PATTERN.match(trial):
            return trial
        return ""

    def apply_terminal_fault(self, fault, fields):
        """Answer a request with a fault that never reaches the provider.

        Returns True when the request is answered.
        """
        action = fault["action"]
        if action == FAULT_STATUS:
            fields["status"] = fault["status"]
            self.send_json_error(
                fault["status"], FAULT_MESSAGE, fault.get("retryAfter")
            )
            return True
        if action == FAULT_OUTAGE:
            fields["status"] = STATUS_SERVICE_UNAVAILABLE
            self.send_json_error(STATUS_SERVICE_UNAVAILABLE, OUTAGE_MESSAGE)
            return True
        if action == FAULT_STALL:
            time.sleep(fault["seconds"])
            fields["status"] = STATUS_NO_RESPONSE
            self.close_connection = True
            return True
        if action == FAULT_DROP:
            fields["status"] = STATUS_NO_RESPONSE
            self.close_connection = True
            return True
        return False

    def send_replay(self, payload, fields, step):
        prompt_tokens = fields["request_bytes"] // REPLAY_BYTES_PER_TOKEN
        reply = replay_reply(payload.get("model"), step, prompt_tokens)
        fields["replay"] = step["index"]
        fields["status"] = http.HTTPStatus.OK
        fields["finish_reason"] = reply["finish"]
        fields["prompt_tokens"] = reply["usage"]["prompt_tokens"]
        fields["completion_tokens"] = reply["usage"]["completion_tokens"]
        if not payload.get("stream"):
            body = json.dumps(
                {
                    "id": reply["id"],
                    "object": OBJECT_COMPLETION,
                    "created": int(time.time()),
                    "model": reply["model"],
                    "choices": [
                        {
                            "index": 0,
                            "message": reply["delta"],
                            "finish_reason": reply["finish"],
                        }
                    ],
                    "usage": reply["usage"],
                }
            ).encode()
            fields["response_bytes"] = len(body)
            self.send_body(http.HTTPStatus.OK, body)
            return
        chunks = (
            chunk_body(reply["id"], reply["model"], reply["delta"], None),
            chunk_body(
                reply["id"], reply["model"], {}, reply["finish"], reply["usage"]
            ),
        )
        self.send_response(http.HTTPStatus.OK)
        self.send_header(HEADER_CONTENT_TYPE, CONTENT_TYPE_SSE)
        self.end_headers()
        data = b"".join(sse_bytes(chunk) for chunk in chunks) + SSE_DONE_EVENT
        fields["response_bytes"] = len(data)
        self.wfile.write(data)
        self.wfile.flush()

    def write_out(self, data, fields):
        fields["response_bytes"] += len(data)
        self.wfile.write(data)
        self.wfile.flush()

    def truncate_now(self, event, fields):
        reply_id = (event or {}).get("id", TRUNCATE_ID)
        model = (event or {}).get("model")
        stop = chunk_body(reply_id, model, {}, FINISH_LENGTH)
        fields["finish_reason"] = FINISH_LENGTH
        self.write_out(sse_bytes(stop) + SSE_DONE_EVENT, fields)

    def relay_stream(self, response, fields, fault):
        """Copy an SSE reply event by event, applying a stream fault."""
        action = fault["action"] if fault else None
        buffer = b""
        events = 0
        is_corrupted = False
        while chunk := response.read1(STREAM_CHUNK_BYTES):
            buffer += chunk
            while SSE_EVENT_END in buffer:
                raw_event, buffer = buffer.split(SSE_EVENT_END, 1)
                event = parse_sse_event(raw_event)
                if event is not None:
                    usage_from_event(event, fields)
                if action == FAULT_TRUNCATE and events >= fault["afterEvents"]:
                    self.truncate_now(event, fields)
                    return
                out = raw_event + SSE_EVENT_END
                is_corruptible = (
                    action == FAULT_CORRUPT_TOOL_ARGUMENTS
                    and not is_corrupted
                    and event is not None
                )
                if is_corruptible and corrupt_tool_arguments(event):
                    is_corrupted = True
                    out = sse_bytes(event)
                if action == FAULT_CUT_STREAM:
                    room = fault["afterBytes"] - fields["response_bytes"]
                    if len(out) > room:
                        self.write_out(out[: max(room, 0)], fields)
                        self.close_connection = True
                        return
                self.write_out(out, fields)
                events += 1
        if buffer:
            self.write_out(buffer, fields)

    def relay_body(self, response, fields):
        body = response.read()
        fields["response_bytes"] = len(body)
        try:
            usage_from_event(json.loads(body), fields)
        except ValueError as err:
            fields["err"] = "response body is not JSON: " + str(err)
        self.wfile.write(body)

    def call_upstream(self, payload, fields, fault):
        request = urllib.request.Request(
            SETTINGS.base_url + PATH_CHAT.removeprefix(UPSTREAM_PREFIX),
            data=json.dumps(payload).encode(),
            method="POST",
            headers={
                HEADER_AUTHORIZATION: BEARER_PREFIX + SETTINGS.api_key,
                HEADER_CONTENT_TYPE: CONTENT_TYPE_JSON,
            },
        )
        try:
            with OPENER.open(request, timeout=TIMEOUT_SECONDS) as response:
                fields["status"] = response.status
                content_type = response.headers.get(HEADER_CONTENT_TYPE) or ""
                self.send_response(response.status)
                self.send_header(HEADER_CONTENT_TYPE, content_type)
                self.end_headers()
                if content_type.startswith(CONTENT_TYPE_SSE):
                    self.relay_stream(response, fields, fault)
                    return
                self.relay_body(response, fields)
        except urllib.error.HTTPError as err:
            body = err.read(ERROR_BODY_LOG_BYTES)
            fields["status"] = err.code
            fields["error_body"] = body.decode("utf-8", "replace")
            self.send_body(err.code, body)
        except OSError as err:
            fields["status"] = STATUS_NO_RESPONSE
            fields["err"] = type(err).__name__ + ": " + str(err)

    def forward(self):
        started = time.monotonic()
        payload, size = self.read_payload()
        if payload is None:
            return
        model = payload.get("model")
        if model not in SETTINGS.models:
            logger.warning(
                "request refused", extra={"reason": REASON_MODEL, "model": model}
            )
            self.send_error(STATUS_FORBIDDEN)
            return
        trial = self.trial_name()
        fields = {
            "trial": trial,
            "model": model,
            "messages": len(payload.get("messages") or []),
            "tools": len(payload.get("tools") or []),
            "stream": bool(payload.get("stream")),
            "request_bytes": size,
            "response_bytes": 0,
        }
        level = logging.INFO
        try:
            fault, step = None, None
            if trial:
                try:
                    number, fault, step = TRIALS.next_request(
                        trial,
                        model,
                        time.monotonic(),
                        has_tools=bool(payload.get("tools")),
                    )
                except FaultFileError as err:
                    logger.warning(
                        "request refused",
                        extra={
                            "reason": REASON_FAULT_FILE,
                            "trial": trial,
                            "err": f"{err}: {err.__cause__}",
                        },
                    )
                    fields["status"] = STATUS_BAD_REQUEST
                    self.send_json_error(STATUS_BAD_REQUEST, FAULT_FILE_MESSAGE)
                    return
                fields["trial_request"] = number
            if fault:
                fields["fault"] = fault["action"]
                level = logging.WARNING
                if self.apply_terminal_fault(fault, fields):
                    return
            if step:
                self.send_replay(payload, fields, step)
                return
            self.call_upstream(payload, fields, fault)
            if fields.get("status", 0) >= STATUS_BAD_REQUEST or fields.get("err"):
                level = logging.WARNING
        finally:
            fields["seconds"] = round(time.monotonic() - started, 2)
            logger.log(level, "model request finished", extra=fields)


def main():
    global SETTINGS, TRIALS
    SETTINGS = Settings()
    SETTINGS.state_dir.mkdir(parents=True, exist_ok=True)
    configure_logging(SETTINGS.log_file)
    SETTINGS.load_key()
    TRIALS = Trials(SETTINGS.faults_dir)
    SETTINGS.socket.parent.mkdir(exist_ok=True)
    if SETTINGS.socket.exists():
        raise ProxyError("provider socket already exists; stop the previous proxy")
    try:
        with Server(str(SETTINGS.socket), Handler) as server:
            os.chmod(SETTINGS.socket, SOCKET_MODE)
            logger.info(
                "provider proxy ready",
                extra={
                    "models": sorted(SETTINGS.models),
                    "socket_path": str(SETTINGS.socket),
                    "base_url": SETTINGS.base_url,
                },
            )
            server.serve_forever()
    finally:
        SETTINGS.socket.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
