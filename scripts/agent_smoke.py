"""Explicit trusted-development API/outbox/Redis/agent demonstration; agent must already be running."""
import argparse
import json
import time
from urllib.request import Request, urlopen
from uuid import uuid4

parser = argparse.ArgumentParser()
parser.add_argument("--base-url", default="http://localhost:8080")
args = parser.parse_args()


def call(method, path, body=None):
    req = Request(args.base_url + path, method=method,
                  data=json.dumps(body).encode() if body else None,
                  headers={"Content-Type": "application/json", "Idempotency-Key": "agent-smoke-" + str(uuid4())})
    with urlopen(req, timeout=15) as response:
        return json.load(response)


problem = call("GET", "/api/v1/problems/two-sum")
for name, language, source, expected in [
    ("cppAC", "cpp17", "#include <iostream>\nint main(){long long a,b;std::cin>>a>>b;std::cout<<a+b;}", "AC"),
    ("cppWA", "cpp17", "#include <iostream>\nint main(){std::cout<<999;}", "WA"),
    ("cppCE", "cpp17", "int main( {", "CE"),
    ("pythonAC", "python3", "a,b=map(int,input().split());print(a+b)", "AC"),
    ("pythonRE", "python3", "raise RuntimeError('private')", "RE"),
    ("pythonTLE", "python3", "while True: pass", "TLE"),
    ("unsupported", "go", "package main\nfunc main(){}", None),
]:
    sub = call("POST", "/api/v1/submissions", {"problem_id": problem["problem_id"], "language": language, "source_code": source})
    sid = sub["submission_id"]
    deadline = time.monotonic() + 90
    while True:
        result = call("GET", "/api/v1/submissions/" + sid)
        if result["status"] in ("COMPLETED", "FAILED"):
            break
        assert time.monotonic() < deadline, "automatic judging timeout"
        time.sleep(0.25)
    assert result["verdict"] == expected, result
    assert result["status"] == ("FAILED" if expected is None else "COMPLETED"), result
    assert not any(key in json.dumps(result) for key in ["bundle_sha", "expected_sha", "stdout_sha", "case-a"])
    print(json.dumps({"fixture": name, "submission_id": sid, "status": result["status"], "verdict": result["verdict"]}), flush=True)
print("PASS: six real automatic verdicts and explicit unsupported-language failure")
