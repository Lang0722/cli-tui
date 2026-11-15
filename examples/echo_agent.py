#!/usr/bin/env python3
"""Minimal agent loop speaking the agenttui JSONL protocol over stdio.

Run:  agenttui -- python3 examples/echo_agent.py

stdin  <- ops    {"type":"user_input","text":"..."}
stdout -> events {"id":"1","msg":{"type":"agent_message_delta","delta":"..."}}

Never print anything else to stdout; use stderr for logs (agenttui -log FILE).
Replace `run_turn` with your real model/tool loop.

What it shows:
- one turn at a time: input that arrives mid-turn is queued and run next
- streaming deltas, reasoning titles, token counts
- an approval round trip ("run <cmd>" asks before running it)
- user shell commands ("!cmd" in the UI arrives as shell_command)
- interrupt: the running command is killed and the turn ends with turn_aborted
"""
import itertools
import json
import os
import queue
import signal
import subprocess
import sys
import threading
import time

out_lock = threading.Lock()
state_lock = threading.Lock()
approvals: dict[str, queue.Queue] = {}
cancel = threading.Event()
inputs: queue.Queue = queue.Queue()  # user_input texts, run one turn at a time
procs: set[subprocess.Popen] = set()  # running commands, killed on interrupt
ids = itertools.count(1)
history: list[str] = []  # user prompts, for rewind


def log(*args):
    print(*args, file=sys.stderr, flush=True)


def emit(msg_type, **fields):
    line = json.dumps({"id": "0", "msg": {"type": msg_type, **fields}})
    with out_lock:
        sys.stdout.write(line + "\n")
        sys.stdout.flush()


def run_command(call_id, script, source=None):
    """Runs script, streaming its output; killed by interrupt."""
    argv = ["bash", "-lc", script]
    extra = {"source": source} if source else {}
    emit("exec_command_begin", call_id=call_id, command=argv, cwd=os.getcwd(), **extra)
    start = time.time()
    proc = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, text=True, start_new_session=True)
    with state_lock:
        procs.add(proc)
    output = []
    try:
        for chunk in proc.stdout:
            output.append(chunk)
            emit("exec_command_output_delta", call_id=call_id, stream="stdout", chunk=chunk)
        code = proc.wait()
        if code < 0:
            code = 128 - code  # killed by a signal: report it like a shell would
    finally:
        with state_lock:
            procs.discard(proc)
    emit("exec_command_end", call_id=call_id, exit_code=code, aggregated_output="".join(output),
         duration_ms=int((time.time() - start) * 1000))
    return code


def kill_all():
    with state_lock:
        running = list(procs)
    for p in running:
        try:
            os.killpg(p.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass


def run_turn(text):
    emit("task_started", context_window=128000)
    emit("agent_reasoning_delta", delta="**Thinking about the request**")
    time.sleep(0.3)

    if text.startswith("run "):
        # Ask before running a command.
        script = text[4:].strip()
        call_id = f"c{next(ids)}"
        with state_lock:
            approvals[call_id] = q = queue.Queue()
        emit("exec_approval_request", call_id=call_id, command=["bash", "-lc", script],
             reason="You asked me to run this")
        decision = q.get()
        if decision == "abort" or cancel.is_set():
            emit("turn_aborted", reason="interrupted")
            return
        if decision != "denied":
            run_command(call_id, script)
            if cancel.is_set():
                emit("turn_aborted", reason="interrupted")
                return

    for word in f"You said: **{text}**".split(" "):
        if cancel.is_set():
            emit("turn_aborted", reason="interrupted")
            return
        emit("agent_message_delta", delta=word + " ")
        time.sleep(0.05)
    emit("agent_message", message=f"You said: **{text}** ")
    emit("token_count", input_tokens=900, output_tokens=100, total_tokens=1000, context_window=128000)
    emit("task_complete")


def turn_worker():
    while True:
        text = inputs.get()
        cancel.clear()
        try:
            run_turn(text)
        except Exception as e:  # never leave the UI stuck in "Working"
            log("turn failed:", repr(e))
            emit("error", message=f"turn failed: {e}")
            emit("task_complete")


def handle(op):
    kind = op.get("type")
    if kind == "user_input":
        history.append(op.get("text", ""))
        inputs.put(op.get("text", ""))
    elif kind == "interrupt":
        cancel.set()
        with state_lock:
            pending = list(approvals.values())
            approvals.clear()
        for q in pending:
            q.put("abort")
        kill_all()
        while not inputs.empty():  # drop input queued behind the interrupted turn
            inputs.get_nowait()
    elif kind in ("exec_approval", "patch_approval"):
        with state_lock:
            q = approvals.pop(op.get("call_id"), None)
        if q:
            q.put(op.get("decision"))
    elif kind == "shell_command":
        call_id = f"sh{next(ids)}"
        threading.Thread(target=run_command, args=(call_id, op.get("command", ""), "user_shell"),
                         daemon=True).start()
    elif kind == "rewind":
        del history[op.get("user_message_index", len(history)):]
    elif kind == "list_sessions":
        emit("session_list", sessions=[])
    elif kind == "resume_session":
        emit("error", message="the echo agent does not save sessions")
    elif kind == "selection_response":
        pass
    elif kind == "slash_command":
        emit("background_event", message=f"/{op.get('name')} is not implemented")
    elif kind == "shutdown":
        kill_all()
        emit("shutdown_complete")
        return False
    else:
        log("ignoring op:", kind)
    return True


def main():
    threading.Thread(target=turn_worker, daemon=True).start()
    emit("session_configured", model="echo-1", cwd=os.getcwd(), agent_name="Echo", version="0.1.0")
    for line in sys.stdin:
        if not line.strip():
            continue
        try:
            if not handle(json.loads(line)):
                return
        except Exception as e:
            log("bad op:", repr(e), line[:200])
    kill_all()  # stdin closed: the UI is gone


if __name__ == "__main__":
    main()
