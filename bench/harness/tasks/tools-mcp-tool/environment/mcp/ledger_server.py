#!/usr/bin/env python3
"""A minimal MCP server over stdio for the benchmark: one ledger tool.

It speaks newline-delimited JSON-RPC, answers the legacy initialize
handshake, lists its one tool, and serves tools/call. Anything else with an
id gets a method-not-found error, which is also how it declines the newer
server/discover probe.
"""

import json
import logging
import sys

logger = logging.getLogger(__name__)

PROTOCOLS = ["2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"]
JSONRPC_VERSION = "2.0"
CODE_METHOD_NOT_FOUND = -32601
BALANCES = {"ACCT-204": "1337.50", "ACCT-117": "88.00", "ACCT-331": "4021.75"}
TOOL = {
    "name": "accountBalance",
    "description": "Look up the current balance of a ledger account by its account id.",
    "inputSchema": {
        "type": "object",
        "properties": {
            "account": {"type": "string", "description": "The account id, such as ACCT-100."}
        },
        "required": ["account"],
    },
}


def send(message):
    sys.stdout.write(json.dumps(message) + "\n")
    sys.stdout.flush()


def call(params):
    account = (params.get("arguments") or {}).get("account", "")
    amount = BALANCES.get(account)
    if params.get("name") != TOOL["name"] or amount is None:
        return {"content": [{"type": "text", "text": f"unknown account {account}"}], "isError": True}
    return {"content": [{"type": "text", "text": f"account {account} balance: {amount} EUR"}]}


def initialize(params):
    asked = params.get("protocolVersion")
    return {
        "protocolVersion": asked if asked in PROTOCOLS else PROTOCOLS[0],
        "capabilities": {"tools": {}},
        "serverInfo": {"name": "ledger", "version": "1.0.0"},
    }


def handle(message):
    identifier, method = message["id"], message.get("method")
    params = message.get("params") or {}
    if method == "initialize":
        return {"jsonrpc": JSONRPC_VERSION, "id": identifier, "result": initialize(params)}
    if method == "ping":
        return {"jsonrpc": JSONRPC_VERSION, "id": identifier, "result": {}}
    if method == "tools/list":
        return {"jsonrpc": JSONRPC_VERSION, "id": identifier, "result": {"tools": [TOOL]}}
    if method == "tools/call":
        return {"jsonrpc": JSONRPC_VERSION, "id": identifier, "result": call(params)}
    return {
        "jsonrpc": JSONRPC_VERSION,
        "id": identifier,
        "error": {"code": CODE_METHOD_NOT_FOUND, "message": "method not found"},
    }


def main():
    logging.basicConfig(stream=sys.stderr, level=logging.INFO)
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            message = json.loads(line)
        except ValueError:
            logger.warning("skipping a line that is not JSON")
            continue
        if not isinstance(message, dict) or "id" not in message:
            continue
        send(handle(message))


main()
