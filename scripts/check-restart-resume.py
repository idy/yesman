#!/usr/bin/env python3
"""Restart and continue, end to end, with real app processes.

A build tagged e2e runs with an identity of its own (YESMAN_E2E_NAME), a
private home, profile and temporary directory, and tools/restart-e2e/fakepi
as pi (local, deterministic, no provider). Process A gives its lead a long
run, starts a worker and puts it to sleep as the user does, then restarts
through the window's own call twice (a double click). MyGo's native Relaunch
starts process B, which must resume the lead once with a continue note in a
new pi using --continue, leave the sleeping worker alone and replay no
original message. Scenario "quit" is the control: a plain Quit, then a
start by hand, wakes nobody.

usage: check-restart-resume.py <restart|quit> <app> <fakepi> <new workdir>

It starts and waits only for its own processes and the one pid process B
reports about itself; it never kills anything else. Writes report.json in
the workdir; exits 1 when a check fails.
"""
import json
import os
import subprocess
import sys
import time

scenario, app, fakepi, work = sys.argv[1:5]
WAIT = float(os.environ.get("RESTART_E2E_WAIT", "25"))
windows = os.name == "nt"
os.makedirs(work)
home, profile, tmp = (os.path.join(work, d) for d in ("yesman-home", "profile", "tmp"))
for d in (profile, tmp):
    os.makedirs(d)
log = os.path.join(work, "pi.log")

# Only what a desktop process needs; nothing else of the caller's
# environment, so no credentials reach the app.
keep = ["PATH", "SystemRoot", "windir", "COMSPEC", "PATHEXT", "SystemDrive", "ProgramFiles", "ProgramFiles(x86)",
        "ProgramData", "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR"]
env = {k: os.environ[k] for k in keep if k in os.environ}
env.setdefault("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
env.update({"HOME": profile, "USERPROFILE": profile, "APPDATA": os.path.join(profile, "AppData", "Roaming"),
            "LOCALAPPDATA": os.path.join(profile, "AppData", "Local"), "XDG_CONFIG_HOME": os.path.join(profile, ".config"),
            "XDG_DATA_HOME": os.path.join(profile, ".local", "share"), "XDG_CACHE_HOME": os.path.join(profile, ".cache"),
            "TMPDIR": tmp, "TMP": tmp, "TEMP": tmp,
            "YESMAN_HOME": home, "YESMAN_E2E_NAME": "yesman-restart-e2e", "YESMAN_PI": fakepi, "YESMAN_HIDDEN": "1",
            "FAKE_PI_LOG": log, "YESMAN_E2E_RESTART": "e2e-hold lead task: original user text",
            "YESMAN_E2E_SLEEPER": "e2e-hold sleeper task"})
for k in ("APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"):
    os.makedirs(env[k], exist_ok=True)
if scenario == "quit":
    env["YESMAN_E2E_RESTART_MODE"] = "quit"

report = {"scenario": scenario, "app": app, "platform": sys.platform, "checks": {}}
ok = True


def check(name, cond, detail=None):
    global ok
    report["checks"][name] = {"pass": bool(cond), "detail": detail}
    ok = ok and bool(cond)


def alive(pid):
    if windows:
        out = subprocess.run(["tasklist", "/FI", f"PID eq {pid}", "/NH", "/FO", "CSV"], capture_output=True, text=True).stdout
        return f'"{pid}"' in out
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


def run(extra=None):
    """Starts the app, our own child, and waits for it to quit."""
    p = subprocess.Popen([app], env=dict(env, **(extra or {})), stdout=out, stderr=out, stdin=subprocess.DEVNULL)
    try:
        p.wait(timeout=WAIT)
    except subprocess.TimeoutExpired:
        p.terminate()
        p.wait(timeout=10)
        return p, False
    return p, True


out = open(os.path.join(work, "app.log"), "ab")
t0 = time.time()
a, quit_in_time = run()
report["pidA"], report["A_exit"] = a.pid, a.returncode
check("A quit by itself", quit_in_time and a.returncode == 0, a.returncode)
if scenario == "quit":
    c, quit_in_time = run({"YESMAN_E2E_SETTLE": "4s"})
    report["pidC"] = c.pid
    check("C quit by itself", quit_in_time, c.returncode)

result_path = os.path.join(home, "e2e-result.json")
deadline = time.time() + WAIT
while time.time() < deadline and not os.path.exists(result_path):
    time.sleep(0.2)
time.sleep(0.5)
result = json.load(open(result_path)) if os.path.exists(result_path) else None
check("the next instance reported", result is not None)
if result:
    pid = result["pid"]
    report["pidB"] = pid
    deadline = time.time() + WAIT
    while time.time() < deadline and alive(pid):
        time.sleep(0.2)
    check("the next instance quit", not alive(pid), pid)
    check("the next instance is a new process", pid != a.pid, [a.pid, pid])
    report["restart"] = result["restart"]
    report["agents"] = [{k: i.get(k) for k in ("id", "status", "finishedAt")} for i in result["agents"]]
report["seconds"] = round(time.time() - t0, 1)

events = [json.loads(line) for line in open(log)] if os.path.exists(log) else []
report["pi"] = events
prompts = [e for e in events if e["kind"] == "prompt"]
lead = [e for e in prompts if e["agent"] == "alice/lead"]
original = [e for e in lead if e["message"].startswith("e2e-hold lead task")]
notes = [e for e in lead if e["message"].startswith("[yesman] Yes Man restarted")]
sleeper = [e for e in prompts if e["agent"] == "alice/sleeper"]
lead_starts = [e for e in events if e["kind"] == "start" and e["agent"] == "alice/lead"]
check("the original prompt was sent once, never replayed", len(original) == 1, len(original))
check("the sleeping worker was not woken", len(sleeper) == 1, len(sleeper))
rdir = os.path.join(home, "restart")
last = ((result or {}).get("restart") or {}).get("last")
if scenario == "restart":
    check("the lead got exactly one continue note", len(notes) == 1, [e["message"][:100] for e in notes])
    check("the note is a follow-up", bool(notes) and notes[0]["behavior"] == "followUp")
    check("the lead resumed in a new pi with --continue", len(lead_starts) == 2 and "--continue" in lead_starts[1]["args"]
          and lead_starts[1]["pid"] != lead_starts[0]["pid"], [s["args"][-1] for s in lead_starts])
    outcomes = {f'{o["yesman"]}/{o["name"]}': o["outcome"] for o in (last or {}).get("agents", [])}
    check("the result lists the lead as resumed and nothing else", outcomes == {"alice/lead": "resumed"}, outcomes)
    left = sorted(os.listdir(rdir)) if os.path.isdir(rdir) else None
    check("only the result is left", left == ["last.json"], left)
else:
    check("no note after a plain quit", not notes, len(notes))
    check("no agent started after a plain quit", len(lead_starts) == 1, len(lead_starts))
    check("no restart result after a plain quit", last is None, last)
    check("no restart directory after a plain quit", not os.path.exists(rdir))

report["pass"] = ok
json.dump(report, open(os.path.join(work, "report.json"), "w"), indent=2)
print(json.dumps({"pass": ok, "seconds": report["seconds"],
                  "checks": {k: v["pass"] if v["pass"] else v for k, v in report["checks"].items()}}, indent=1))
sys.exit(0 if ok else 1)
