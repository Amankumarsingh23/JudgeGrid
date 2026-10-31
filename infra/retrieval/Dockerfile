# Trusted operator/test toolchain, never an ordinary service image.
FROM judgegrid-analysis-tools
COPY internal/retrieval/helper/requirements.lock /tmp/embedding-requirements.lock
RUN --mount=type=secret,id=local_ca if [ -f /run/secrets/local_ca ]; then export PIP_CERT=/run/secrets/local_ca; fi; python3.13 -m pip install --no-cache-dir --no-deps --only-binary=:all: --require-hashes -r /tmp/embedding-requirements.lock
ENV REPO_EMBEDDING_PYTHON=/usr/local/bin/python3.13
