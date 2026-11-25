#!/usr/bin/env python3
"""A minimal MCP server on stdio (spec 2025-06-18), standard library only.

Try it with the mock backend: add to ~/.config/agenttui/config.toml

    [mcp_servers.notes]
    command = "python3"
    args = ["/path/to/examples/mcp_server.py"]

then `/mcp`, and prompts such as `mcp call notes.add {"a": 2, "b": 3}` or
`mcp call notes.greet` (which asks for your name with an elicitation form).

Protocol: one JSON-RPC message per line on stdin/stdout; logs go to stderr.
Requests run on worker threads, so a tool can wait for the client's answer
to its own request (elicitation/create) while the reader keeps reading.
"""
import json
import sys
import threading
import itertools

out_lock = threading.Lock()
pending = {}  # our request id -> [event, response]
pending_lock = threading.Lock()
ids = itertools.count(1)
client_caps = {}


def log(*a):
    print("notes:", *a, file=sys.stderr, flush=True)


def write(msg):
    msg["jsonrpc"] = "2.0"
    with out_lock:
        sys.stdout.write(json.dumps(msg, separators=(",", ":")) + "\n")
        sys.stdout.flush()


def request(method, params):
    """Send a request to the client and wait for its response."""
    rid = f"s{next(ids)}"
    slot = [threading.Event(), None]
    with pending_lock:
        pending[rid] = slot
    write({"id": rid, "method": method, "params": params})
    slot[0].wait()
    resp = slot[1]
    if "error" in resp:
        raise RuntimeError(resp["error"].get("message", "error"))
    return resp["result"]


def text(s, is_error=False):
    r = {"content": [{"type": "text", "text": s}]}
    if is_error:
        r["isError"] = True
    return r


TOOLS = [
    {"name": "add", "description": "Add two numbers",
     "inputSchema": {"type": "object", "properties": {"a": {"type": "number"}, "b": {"type": "number"}},
                     "required": ["a", "b"]},
     "annotations": {"readOnlyHint": True}},
    {"name": "greet", "description": "Greet you by name (asks for it)",
     "inputSchema": {"type": "object", "properties": {}}},
]


def call_tool(name, args):
    if name == "add":
        try:
            return text(str(args["a"] + args["b"]))
        except (KeyError, TypeError) as e:
            return text(f"bad arguments: {e}", True)
    if name == "greet":
        if "elicitation" not in client_caps:
            return text("this client can't ask you for your name", True)
        r = request("elicitation/create", {
            "message": "What's your name?",
            "requestedSchema": {"type": "object", "properties": {
                "name": {"type": "string", "title": "Name"}}, "required": ["name"]},
        })
        if r.get("action") != "accept":
            return text(f"No greeting: you chose {r.get('action')}.")
        return text(f"Hello, {r['content']['name']}!")
    raise KeyError(name)


def handle(msg):
    method, params, rid = msg.get("method"), msg.get("params") or {}, msg.get("id")
    try:
        if method == "initialize":
            client_caps.update(params.get("capabilities") or {})
            v = params.get("protocolVersion")
            result = {"protocolVersion": v if v in ("2025-06-18", "2025-03-26") else "2025-06-18",
                      "capabilities": {"tools": {}},
                      "serverInfo": {"name": "notes", "version": "0.1.0"}}
        elif method == "ping":
            result = {}
        elif method == "tools/list":
            result = {"tools": TOOLS}
        elif method == "tools/call":
            if params.get("name") not in {t["name"] for t in TOOLS}:
                write({"id": rid, "error": {"code": -32602, "message": f"Unknown tool: {params.get('name')}"}})
                return
            result = call_tool(params["name"], params.get("arguments") or {})
        else:
            write({"id": rid, "error": {"code": -32601, "message": f"method not found: {method}"}})
            return
        write({"id": rid, "result": result})
    except Exception as e:  # never leave a request unanswered
        log("error:", repr(e))
        write({"id": rid, "error": {"code": -32603, "message": str(e)}})


def main():
    for line in sys.stdin:
        if not line.strip():
            continue
        try:
            msg = json.loads(line)
        except ValueError:
            write({"id": None, "error": {"code": -32700, "message": "parse error"}})
            continue
        if "method" in msg and "id" in msg:
            threading.Thread(target=handle, args=(msg,), daemon=True).start()
        elif "method" in msg:
            log("notification", msg["method"])
        else:  # a response to one of our requests
            with pending_lock:
                slot = pending.pop(msg.get("id"), None)
            if slot:
                slot[1] = msg
                slot[0].set()
    log("stdin closed, exiting")


if __name__ == "__main__":
    main()
