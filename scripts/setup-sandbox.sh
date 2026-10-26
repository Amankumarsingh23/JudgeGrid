#!/bin/sh
# Trusted prefetch/build phase. Run on the local controller host, never inside a workload.
set -eu
docker pull gcc:14-bookworm
cpp_base=$(docker image inspect --format '{{index .RepoDigests 0}}' gcc:14-bookworm)
docker build --build-arg "BASE_IMAGE=$cpp_base" -f infra/sandbox/Dockerfile.cpp -t judgegrid-sandbox-cpp:session4 infra/sandbox
docker pull python:3.13-slim-bookworm
python_base=$(docker image inspect --format '{{index .RepoDigests 0}}' python:3.13-slim-bookworm)
docker build --build-arg "BASE_IMAGE=$python_base" -f infra/sandbox/Dockerfile.python -t judgegrid-sandbox-python:session4 infra/sandbox
cpp=$(docker image inspect --format '{{.Id}}' judgegrid-sandbox-cpp:session4)
python=$(docker image inspect --format '{{.Id}}' judgegrid-sandbox-python:session4)
printf '{"cpp17":"%s","python3":"%s","cpp_base":"%s","python_base":"%s"}\n' "$cpp" "$python" "$cpp_base" "$python_base" > .sandbox-images.local.json
