"""Verify Compose's ordinary API/worker services never receive sandbox-controller authority."""
import json
import subprocess

config = json.loads(subprocess.check_output(["docker", "compose", "--profile", "outbox", "config", "--format", "json"], text=True))
for name in ("api", "worker", "outbox-relay"):
    service = config["services"][name]
    assert not service.get("privileged"), name
    assert service.get("network_mode") != "host", name
    assert service.get("pid") != "host", name
    for mount in service.get("volumes", []):
        text = json.dumps(mount).lower()
        assert "docker.sock" not in text and "docker_engine" not in text and "containerd.sock" not in text, name
    assert "DOCKER_HOST" not in service.get("environment", {}), name
print("PASS: API/Redis worker/outbox relay have no Docker socket, daemon endpoint, privileged mode or host PID/network namespace")

from pathlib import Path
runtime_copy = next(line for line in Path("Dockerfile").read_text().splitlines() if line.startswith("COPY --from=build /out/"))
assert all(binary not in runtime_copy for binary in ("judge-agent", "judge-local", "sandbox-demo"))
print("PASS: trusted controller binaries remain absent from ordinary service images")
