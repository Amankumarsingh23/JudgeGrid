"""Live API checks using only Python's standard library; requires seeded problems."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
from urllib.error import HTTPError
from urllib.request import Request, urlopen
from uuid import uuid4

parser = argparse.ArgumentParser()
parser.add_argument("--base-url", default="http://localhost:8080")
parser.add_argument("--unauthenticated", action="store_true")
args = parser.parse_args()


def call(method, path, body=None, key=None):
    headers = {"Content-Type": "application/json", "X-User-ID": "untrusted-user"}
    if key:
        headers["Idempotency-Key"] = key
    data = json.dumps(body).encode() if body is not None else None
    req = Request(args.base_url + path, data=data, headers=headers, method=method)
    try:
        response = urlopen(req, timeout=15)
    except HTTPError as error:
        response = error
    with response:
        return response.status, json.load(response)


assert call("GET", "/healthz") == (200, {"status": "ok"})
assert call("GET", "/readyz") == (200, {"postgres": "ok", "redis": "ok"})
if args.unauthenticated:
    assert call("GET", "/api/v1/submissions")[0] == 401
    print("PASS: health, readiness, submissions denied with development auth disabled")
else:
    status, problem = call("GET", "/api/v1/problems/two-sum")
    assert status == 200 and problem["problem_version"] >= 1
    assert call("GET", "/api/v1/problems/maximum")[0] == 200
    assert call("GET", "/api/v1/problems/does-not-exist")[0] == 404
    payload = {"problem_id": problem["problem_id"], "language": "cpp17", "source_code": "int main(){return 0;}"}
    key = "live-smoke-" + str(uuid4())
    with ThreadPoolExecutor(max_workers=20) as pool:
        results = list(pool.map(lambda _: call("POST", "/api/v1/submissions", payload, key), range(20)))
    codes = [status for status, _ in results]
    assert codes.count(201) == 1 and codes.count(200) == 19, codes
    ids = {body["submission_id"] for _, body in results}
    assert len(ids) == 1
    submission_id = ids.pop()
    status, submission = call("GET", "/api/v1/submissions/" + submission_id)
    assert status == 200 and submission["status"] == "QUEUED" and submission["verdict"] is None
    assert "principal_id" not in submission
    assert call("POST", "/api/v1/submissions", dict(payload, source_code="different"), key)[0] == 409
    assert call("POST", "/api/v1/submissions", payload)[0] == 400
    assert call("POST", "/api/v1/submissions", dict(payload, unknown=True), str(uuid4()))[0] == 400
    assert call("POST", "/api/v1/submissions", dict(payload, source_code="x" * 65537), str(uuid4()))[0] == 413
    assert call("GET", "/api/v1/submissions?limit=101")[0] == 400
    status, page = call("GET", "/api/v1/submissions?limit=1")
    assert status == 200 and len(page["items"]) == 1
    assert call("GET", "/api/v1/submissions/00000000-0000-4000-8000-000000000000")[0] == 404
    print("PASS: live health, problems, create/get/list, bounds, validation, conflict; 20-way HTTP race: 1 create + 19 replays, one ID")
