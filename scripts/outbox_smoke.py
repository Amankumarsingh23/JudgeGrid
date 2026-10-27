"""Live API -> durable outbox -> explicitly started relay -> real Redis; no execution."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import subprocess
import time
from urllib.request import Request, urlopen
from uuid import uuid4

parser = argparse.ArgumentParser()
parser.add_argument("--base-url", default="http://localhost:8080")
parser.add_argument("--stream", required=True)
args = parser.parse_args()
assert args.stream.startswith(("dev:", "test:")), "Use an isolated demonstration stream"


def call(method, path, body=None, key=None):
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Idempotency-Key"] = key
    req = Request(args.base_url + path, method=method, headers=headers,
                  data=json.dumps(body).encode() if body is not None else None)
    with urlopen(req, timeout=15) as response:
        return response.status, json.load(response)


def sql(query):
    return subprocess.check_output(["docker", "compose", "exec", "-T", "postgres", "psql",
                                    "-U", "judgegrid", "-d", "judgegrid", "-At", "-c", query], text=True).strip()


problem = call("GET", "/api/v1/problems/two-sum")[1]
key = "outbox-demo-" + str(uuid4())
body = {"problem_id": problem["problem_id"], "language": "python3", "source_code": "a,b=map(int,input().split());print(a+b)"}
with ThreadPoolExecutor(max_workers=20) as pool:
    responses = list(pool.map(lambda _: call("POST", "/api/v1/submissions", body, key), range(20)))
assert sum(status == 201 for status, _ in responses) == 1
assert sum(status == 200 for status, _ in responses) == 19
ids = {body["submission_id"] for _, body in responses}
assert len(ids) == 1
submission = ids.pop()
# IDs came from the API; validate before including them in local diagnostic SQL.
assert str(__import__("uuid").UUID(submission)) == submission
assert sql(f"SELECT count(*) FROM outbox_events WHERE submission_id='{submission}'") == "1"
deadline = time.monotonic() + 30
while sql(f"SELECT status FROM outbox_events WHERE submission_id='{submission}'") != "PUBLISHED":
    assert time.monotonic() < deadline, "relay did not publish before deadline"
    time.sleep(0.2)
event_id, stream_id = sql(f"SELECT id::text||'|'||stream_id FROM outbox_events WHERE submission_id='{submission}'").split("|")
message = subprocess.check_output(["docker", "compose", "exec", "-T", "redis", "redis-cli", "--json",
                                   "XRANGE", args.stream, stream_id, stream_id], text=True)
assert event_id in message and submission in message and "event_id" in message
public = call("GET", "/api/v1/submissions/" + submission)[1]
assert public["status"] == "QUEUED" and public["state_version"] == 0 and public["verdict"] is None
print(json.dumps({"submission_id": submission, "event_id": event_id, "stream_id": stream_id,
                  "status": "QUEUED", "http_created": 1, "http_replays": 19, "outbox_records": 1}))
print("PASS: live API atomic intent, relay publication and unchanged submission state")
