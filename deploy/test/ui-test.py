#!/usr/bin/env python3
"""Automatic test of the test UI (port 8081) against the test proxy (50291/50292).

Does not touch the real proxy; at the end it checks that it is still running unchanged.
Run on the server as root (after setup.sh):  python3 -I deploy/test/ui-test.py
"""
import http.cookiejar
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:8081"
PW = open("/root/modbusui-test-pw.txt").read().strip()
REAL_UNIT = "modbus-proxy.service"
TEST_UNIT = "modbus-proxy-test.service"
TEST_CONFIG = "/etc/modbus-proxy-test.yml"

jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
results = []


def call(method, path, body=None, header=True):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if header:
        req.add_header("X-Modbusui", "1")
    if data:
        req.add_header("Content-Type", "application/json")
    try:
        with opener.open(req, timeout=60) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b"null")


def check(name, ok, info=""):
    ok = bool(ok)
    results.append(ok)
    print(("OK    " if ok else "FAILED") + " " + name + (("  -> " + str(info)) if info else ""))


def show(unit):
    out = subprocess.run(["systemctl", "show", "-p", "MainPID,ActiveState,NRestarts,ActiveEnterTimestamp", unit],
                         capture_output=True, text=True).stdout
    return dict(l.split("=", 1) for l in out.strip().splitlines())


def listening(port):
    return f":{port} " in subprocess.run(["ss", "-tln"], capture_output=True, text=True).stdout


def wait_job(job_id):
    for _ in range(120):
        _, st = call("GET", "/api/job?id=" + job_id)
        if st["state"] in ("ok", "rolledback", "error"):
            return st
        time.sleep(0.5)
    return {"state": "timeout"}


def devices():
    _, d = call("GET", "/api/devices")
    return d


def save(devs, hash_):
    return call("POST", "/api/devices", {"devices": devs, "baseHash": hash_})


t_start = time.time()
real_before = show(REAL_UNIT)
print("Real proxy before:", real_before)

# login
code, s = call("GET", "/api/session")
check("Session without login", code == 200 and s["loggedIn"] is False and s["passwordSet"] is True, s)
code, _ = call("GET", "/api/devices")
check("Device list blocked without login", code == 401, code)
code, _ = call("POST", "/api/login", {"password": "wrong-wrong"})
check("Wrong password rejected", code == 401, code)
code, _ = call("POST", "/api/login", {"password": PW})
check("Login", code == 200, code)
code, _ = call("POST", "/api/service", {"action": "restart"}, header=False)
check("POST without X-Modbusui rejected (CSRF)", code == 403, code)

# read
d = devices()
check("Devices read", len(d["devices"]) == 2 and not d["readOnly"], [x["name"] for x in d["devices"]])
orig = d["devices"]
check("Ports in use detected", all(str(p) in d["reserved"] for p in (22, 8081)), d["reserved"])
code, s = call("GET", "/api/service")
check("Service status", s["proxy"]["active"] == "active" and s["applyPath"]["active"] == "active",
      (s["proxy"]["active"], s["proxy"]["uptimeSec"], s["proxy"]["rssBytes"], s["applyPath"]["active"]))
code, c = call("GET", "/api/connections")
check("Connections", code == 200 and all(x["listening"] for x in c["devices"]),
      [(x["listenPort"], x["listening"]) for x in c["devices"]])
code, si = call("GET", "/api/system")
check("System values", code == 200 and si["diskTotal"] > 0 and not si.get("journalError"),
      {k: si[k] for k in ("diskUsed", "journalBytes", "varLogBytes", "memTotal")})
check("Journal lines readable", all(not u.get("error") for u in si["lines24h"]),
      [(u["label"], u["lines"], u.get("error", "")) for u in si["lines24h"]])

# Modbus test
code, r = call("POST", "/api/modbus", {"port": 50291, "unit": 1, "fc": 3, "address": 0, "count": 2, "timeoutMs": 1500})
check("Modbus test reports the error cleanly (target does not exist)", code == 200 and not r["ok"] and r["error"], r.get("error"))
code, r = call("POST", "/api/modbus", {"port": 22, "unit": 1, "fc": 3, "address": 0, "count": 1})
check("Modbus test on a port outside the config is blocked", code == 400, r)

# save
code, r = save(orig, d["hash"])
check("Save unchanged -> no restart", code == 200 and r.get("unchanged"), r)
bad = orig + [dict(orig[0], name="Duplicate")]
code, r = save(bad, d["hash"])
check("Duplicate port/target rejected", code == 422, [e["msg"] for e in r.get("errors", [])])
code, r = save(orig, "0" * 64)
check("Outdated config detected", code == 409, r)

pid0 = show(TEST_UNIT)["MainPID"]
new = orig + [{"name": "Test device C", "host": "192.0.2.3", "port": 502, "timeout": 3, "listenPort": 50293}]
code, r = save(new, d["hash"])
st = wait_job(r["jobId"]) if code == 202 else r
check("Add device -> saved, restarted", st["state"] == "ok" and listening(50293), st.get("message"))
check("Test proxy has a new PID", show(TEST_UNIT)["MainPID"] != pid0)
check("Name written as comment", "# Test device C\n" in open(TEST_CONFIG, encoding="utf-8").read())

d2 = devices()
before_fail = open(TEST_CONFIG, "rb").read()
fail = d2["devices"] + [{"name": "FAILTEST", "host": "192.0.2.4", "port": 502, "timeout": 3, "listenPort": 50294}]
code, r = save(fail, d2["hash"])
st = wait_job(r["jobId"]) if code == 202 else r
check("Failed start -> automatic rollback", st["state"] == "rolledback", st.get("message"))
check("Config after rollback as before", open(TEST_CONFIG, "rb").read() == before_fail)
check("Test proxy running after rollback", show(TEST_UNIT)["ActiveState"] == "active" and listening(50293))

d3 = devices()
code, r = save(orig, d3["hash"])
st = wait_job(r["jobId"]) if code == 202 else r
check("Delete device again", st["state"] == "ok" and not listening(50293), st.get("message"))

# service control
for action, want_active in (("stop", False), ("start", True), ("restart", True)):
    code, r = call("POST", "/api/service", {"action": action})
    st = wait_job(r["jobId"]) if code == 202 else r
    act = show(TEST_UNIT)["ActiveState"]
    check(f"Service {action}", st["state"] == "ok" and (act == "active") == want_active, (st.get("message"), act))

code, _ = call("POST", "/api/logout")
code, _ = call("GET", "/api/devices")
check("Blocked after logout", code == 401, code)

# real proxy untouched?
real_after = show(REAL_UNIT)
check("Real proxy untouched (same PID, same start time)",
      real_after.get("MainPID") == real_before.get("MainPID")
      and real_after.get("ActiveEnterTimestamp") == real_before.get("ActiveEnterTimestamp"), real_after)

# what ended up in the journal?
since = "@%d" % int(t_start)
for unit in ("modbusui-test.service", "modbusui-test-apply.service", TEST_UNIT):
    out = subprocess.run(["journalctl", "-u", unit, "--since", since, "-o", "cat", "--no-pager", "-q"],
                         capture_output=True, text=True).stdout.strip().splitlines()
    print(f"Journal {unit}: {len(out)} lines" + ("  e.g. " + repr(out[:3]) if out else ""))

print()
print(f"{sum(results)} of {len(results)} checks passed.")
sys.exit(0 if all(results) else 1)
