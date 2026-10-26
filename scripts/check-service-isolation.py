"""Verify Compose's ordinary API/worker services never receive sandbox-controller authority."""
import json
import subprocess

config = json.loads(subprocess.check_output(["docker", "compose", "config", "--format", "json"], text=True))
for name in ("api", "worker"):
    service = config["services"][name]
    assert not service.get("privileged"), name
    assert service.get("network_mode") != "host", name
    assert service.get("pid") != "host", name
    for mount in service.get("volumes", []):
        text = json.dumps(mount).lower()
        assert "docker.sock" not in text and "docker_engine" not in text and "containerd.sock" not in text, name
    assert "DOCKER_HOST" not in service.get("environment", {}), name
print("PASS: API/Redis worker have no Docker socket, daemon endpoint, privileged mode or host PID/network namespace")
