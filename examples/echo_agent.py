#!/usr/bin/env python3
"""Minimal agent loop speaking the agenttui JSONL protocol over stdio.

Run:  agenttui -- python3 examples/echo_agent.py

stdin  <- ops    {"type":"user_input","text":"..."}
stdout -> events {"id":"1","msg":{"type":"agent_message_delta","delta":"..."}}

Never print anything else to stdout; use stderr for logs (agenttui -log FILE).
Replace `run_turn` with your real model/tool loop.
"""
import json
import os
import queue
import subprocess
import sys
import threading
import time

out_lock = threading.Lock()
approvals: dict[str, queue.Queue] = {}
cancel = threading.Event()


def emit(msg_type, **fields):
    line = json.dumps({"id": "0", "msg": {"type": msg_type, **fields}})
    with out_lock:
        sys.stdout.write(line + "\n")
        sys.stdout.flush()


def run_command(call_id, script):
    emit("exec_command_begin", call_id=call_id, command=["bash", "-lc", script], cwd=os.getcwd())
    start = time.time()
    proc = subprocess.run(["bash", "-lc", script], capture_output=True, text=True)
    output = proc.stdout + proc.stderr
    emit("exec_command_end", call_id=call_id, exit_code=proc.returncode,
         aggregated_output=output, duration_ms=int((time.time() - start) * 1000))
    return output


def run_turn(text):
    emit("task_started", context_window=128000)
    emit("agent_reasoning_delta", delta="**Thinking about the request**")
    time.sleep(0.3)

    if text.startswith("!"):
        # Ask before running a shell command.
        script = text[1:].strip()
        approvals["c1"] = q = queue.Queue()
        emit("exec_approval_request", call_id="c1", command=["bash", "-lc", script],
             reason="You asked me to run this")
        decision = q.get()
        if decision == "abort" or cancel.is_set():
            emit("turn_aborted", reason="interrupted")
            return
        if decision != "denied":
            run_command("c1", script)

    for word in f"You said: **{text}**".split(" "):
        if cancel.is_set():
            emit("turn_aborted", reason="interrupted")
            return
        emit("agent_message_delta", delta=word + " ")
        time.sleep(0.05)
    emit("agent_message", message="")
    emit("token_count", input_tokens=900, output_tokens=100, total_tokens=1000, context_window=128000)
    emit("task_complete")


def main():
    emit("session_configured", model="echo-1", cwd=os.getcwd(), agent_name="Echo", version="0.1.0")
    for line in sys.stdin:
        op = json.loads(line)
        kind = op.get("type")
        if kind == "user_input":
            cancel.clear()
            threading.Thread(target=run_turn, args=(op["text"],), daemon=True).start()
        elif kind == "interrupt":
            cancel.set()
            for q in list(approvals.values()):
                q.put("abort")
            approvals.clear()
        elif kind in ("exec_approval", "patch_approval"):
            q = approvals.pop(op["call_id"], None)
            if q:
                q.put(op["decision"])
        elif kind == "slash_command":
            emit("background_event", message=f"/{op['name']} is not implemented")
        elif kind == "shutdown":
            emit("shutdown_complete")
            return


if __name__ == "__main__":
    main()
