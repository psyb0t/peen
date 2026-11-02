"""Serve one provider's chat endpoint and fixed models on a Unix socket.

The benchmark task containers have no network. Their relay reaches this proxy
through the socket, and the proxy alone holds the provider key, read from the
repository's .env mounted read-only. It forwards only chat completions for the
allowed models to the configured base URL and refuses everything else.

Every model request is logged as one JSON record with its status, duration and
sizes, and an upstream error body is logged truncated, so a run can be
reconstructed without recording the model traffic itself. The key is never
logged.
"""

import datetime
import http.server
import json
import logging
import logging.handlers
import os
import pathlib
import socketserver
import sys
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
LOG_NAME = "proxy.log"
LOG_MAX_BYTES = 50_000_000
LOG_BACKUPS = 5
MAX_BODY_BYTES = 32 * 1024 * 1024
TIMEOUT_SECONDS = 1900
STREAM_CHUNK_BYTES = 4096
ERROR_BODY_LOG_BYTES = 2000
SOCKET_MODE = 0o666
PATH_MODELS = "/v1/models"
PATH_CHAT = "/v1/chat/completions"
UPSTREAM_PREFIX = "/v1"
CONTENT_TYPE_JSON = "application/json"
HEADER_CONTENT_TYPE = "Content-Type"
HEADER_CONTENT_LENGTH = "Content-Length"
HEADER_AUTHORIZATION = "Authorization"
BEARER_PREFIX = "Bearer "
STATUS_BAD_REQUEST = 400
STATUS_FORBIDDEN = 403
STATUS_TOO_LARGE = 413
REASON_PATH = "path"
REASON_BODY_SIZE = "body_size"
REASON_INVALID_JSON = "invalid_json"
REASON_MODEL = "model"
RECORD_FIELDS = (
    "reason",
    "method",
    "path",
    "model",
    "messages",
    "tools",
    "stream",
    "status",
    "seconds",
    "request_bytes",
    "response_bytes",
    "error_body",
    "err",
    "models",
    "socket_path",
    "base_url",
)

logger = logging.getLogger(__name__)


class ProxyError(Exception):
    """The proxy cannot start."""


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
        self.send_response(http.HTTPStatus.OK)
        self.send_header(HEADER_CONTENT_TYPE, CONTENT_TYPE_JSON)
        self.send_header(HEADER_CONTENT_LENGTH, str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path != PATH_CHAT:
            self.refuse_path()
            return
        self.forward()

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
        fields = {
            "model": model,
            "messages": len(payload.get("messages") or []),
            "tools": len(payload.get("tools") or []),
            "stream": bool(payload.get("stream")),
            "request_bytes": size,
            "response_bytes": 0,
        }
        request = urllib.request.Request(
            SETTINGS.base_url + PATH_CHAT.removeprefix(UPSTREAM_PREFIX),
            data=json.dumps(payload).encode(),
            method="POST",
            headers={
                HEADER_AUTHORIZATION: BEARER_PREFIX + SETTINGS.api_key,
                HEADER_CONTENT_TYPE: CONTENT_TYPE_JSON,
            },
        )
        level = logging.INFO
        try:
            with OPENER.open(request, timeout=TIMEOUT_SECONDS) as response:
                fields["status"] = response.status
                self.send_response(response.status)
                self.send_header(
                    HEADER_CONTENT_TYPE, response.headers.get(HEADER_CONTENT_TYPE)
                )
                self.end_headers()
                while chunk := response.read1(STREAM_CHUNK_BYTES):
                    fields["response_bytes"] += len(chunk)
                    self.wfile.write(chunk)
                    self.wfile.flush()
        except urllib.error.HTTPError as err:
            body = err.read(ERROR_BODY_LOG_BYTES)
            level = logging.WARNING
            fields["status"] = err.code
            fields["error_body"] = body.decode("utf-8", "replace")
            self.send_response(err.code)
            self.send_header(HEADER_CONTENT_TYPE, CONTENT_TYPE_JSON)
            self.send_header(HEADER_CONTENT_LENGTH, str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except OSError as err:
            level = logging.WARNING
            fields["status"] = 0
            fields["err"] = type(err).__name__ + ": " + str(err)
        finally:
            fields["seconds"] = round(time.monotonic() - started, 2)
            logger.log(level, "model request finished", extra=fields)


def main():
    global SETTINGS
    SETTINGS = Settings()
    SETTINGS.state_dir.mkdir(parents=True, exist_ok=True)
    configure_logging(SETTINGS.log_file)
    SETTINGS.load_key()
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
