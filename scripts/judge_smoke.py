"""Explicit local judge demonstration. Requires development API, private DB URL and trusted judge-local binary."""
import argparse
import json
import subprocess
from pathlib import Path
from urllib.request import Request, urlopen
from uuid import uuid4

parser = argparse.ArgumentParser()
parser.add_argument("--judge-binary", required=True)
parser.add_argument("--base-url", default="http://localhost:8080")
args = parser.parse_args()


def call(method, path, body=None):
    request = Request(args.base_url + path, method=method,
                      data=json.dumps(body).encode() if body is not None else None,
                      headers={"Content-Type": "application/json", "Idempotency-Key": "judge-demo-" + str(uuid4())})
    with urlopen(request, timeout=15) as response:
        return json.load(response)


def judge(submission_id=None):
    command = [str(Path(args.judge_binary).resolve()), "--allow-local-judging"]
    command += ["--submission", submission_id] if submission_id else ["--seed-tests"]
    result = subprocess.run(command, check=True, capture_output=True, text=True, timeout=180)
    return json.loads(result.stdout)


judge()
problem = call("GET", "/api/v1/problems/two-sum")
for name, language, source, verdict, count in [
    ("cpp", "cpp17", "#include <iostream>\nint main(){long long a,b;std::cin>>a>>b;std::cout<<a+b;}", "AC", 6),
    ("python", "python3", "a,b=map(int,input().split());print(a+b)", "AC", 6),
    ("wrong", "python3", "print(999)", "WA", 1),
    ("compile", "cpp17", "int main( {", "CE", 0),
    ("runtime", "python3", "raise RuntimeError('private diagnostic')", "RE", 1),
    ("timeout", "python3", "while True: pass", "TLE", 1),
    ("memory", "python3", "x=bytearray(512*1024*1024)", "MLE", 1),
    ("stdout", "python3", "import os\nwhile True: os.write(1,b'x'*8192)", "OLE", 1),
    ("stderr", "python3", "import os\nwhile True: os.write(2,b'x'*8192)", "OLE", 1),
]:
    sub = call("POST", "/api/v1/submissions", {"problem_id": problem["problem_id"], "language": language, "source_code": source})
    result = judge(sub["submission_id"])
    evaluation = result["evaluation"]
    assert evaluation["status"] == "COMPLETED" and evaluation["verdict"] == verdict, result
    assert evaluation["tests_run"] == count, result
    if name == "cpp":
        assert result["controller_operations"]["Compilations"] == 1, result
        assert result["controller_operations"]["RuntimeStarts"] == 6, result
    replay = judge(sub["submission_id"])
    assert replay["evaluation"]["replayed"] and replay["evaluation"]["evaluation_run_id"] == evaluation["evaluation_run_id"]
    assert replay["controller_operations"]["RuntimeStarts"] == 0
    public = call("GET", "/api/v1/submissions/" + sub["submission_id"])
    assert public["status"] == "COMPLETED" and public["verdict"] == verdict
    assert not any(secret in json.dumps(public) for secret in ["bundle_sha", "expected_sha", "case-a", "stdout_sha"])
    print(json.dumps({"fixture": name, "evaluation": evaluation, "controller_operations": result["controller_operations"]}))
print("PASS: nine real CLI evaluations, seven verdicts, replay and public API checks")
