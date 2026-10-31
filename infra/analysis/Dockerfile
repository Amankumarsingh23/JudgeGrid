# Trusted test/operator tooling only; never part of an API or judge image.
FROM python:3.13.15-bookworm AS python
FROM golang:1.26
COPY --from=python /usr/local/bin/python3.13 /usr/local/bin/python3.13
COPY --from=python /usr/local/lib/python3.13 /usr/local/lib/python3.13
COPY --from=python /usr/local/lib/libpython3.13.so* /usr/local/lib/
ENV LD_LIBRARY_PATH=/usr/local/lib REPO_ANALYSIS_PYTHON=/usr/local/bin/python3.13
COPY internal/analysis/helper/requirements.lock /tmp/requirements.lock
RUN --mount=type=secret,id=local_ca if [ -f /run/secrets/local_ca ]; then export PIP_CERT=/run/secrets/local_ca; fi; python3.13 -m pip install --no-cache-dir --only-binary=:all: --require-hashes -r /tmp/requirements.lock
