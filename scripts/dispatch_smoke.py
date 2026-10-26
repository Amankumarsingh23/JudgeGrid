"""Development-only live CLI/worker/receipt smoke check. No judging or source execution."""
import json
import os
import re
import subprocess
import time
from urllib.request import urlopen


def command(*args):
    return subprocess.check_output(["docker", "compose", *args], text=True).strip()


def sql(statement):
    return command("exec", "-T", "postgres", "psql", "-U", "judgegrid", "-d", "judgegrid", "-At", "-c", statement)


stream = os.environ.get("QUEUE_STREAM", "")
if not re.fullmatch(r"(?:dev|test):[A-Za-z0-9:_-]+", stream):
    raise SystemExit("Set an isolated dev: or test: QUEUE_STREAM and enable the development worker first")
with urlopen("http://localhost:8080/api/v1/submissions?limit=1", timeout=10) as response:
    items = json.load(response)["items"]
assert items, "Create a local submission first (scripts/smoke.py does this)"
submission_id = items[0]["submission_id"]
assert re.fullmatch(r"[0-9a-f-]{36}", submission_id)
assert items[0]["status"] == "QUEUED" and items[0]["verdict"] is None
attempt = time.time_ns() // 1_000_000
for _ in range(2):
    published = json.loads(command("exec", "-T", "worker", "dispatch", submission_id, str(attempt)))
    assert published["workload"] == "dispatch_test"
receipt_query = f"SELECT count(*) FROM development_dispatch_receipts WHERE submission_id='{submission_id}' AND attempt={attempt}"
deadline = time.monotonic() + 15
while time.monotonic() < deadline:
    pending = command("exec", "-T", "redis", "redis-cli", "--raw", "XPENDING", stream, os.environ.get("QUEUE_GROUP", "judges"))
    if sql(receipt_query) == "1" and pending.splitlines()[0] == "0":
        break
    time.sleep(0.1)
else:
    raise AssertionError("Worker did not durably handle and ACK test deliveries")
assert sql(f"SELECT status || ':' || coalesce(verdict,'none') || ':' || state_version FROM submissions WHERE id='{submission_id}'") == "QUEUED:none:0"
print("PASS: live publisher CLI + worker: 2 messages, 1 durable development receipt, PEL=0; submission remains QUEUED")
