# Preserve the exact existing PostgreSQL 17 Alpine ABI and data-volume layout.
FROM postgres:17-alpine@sha256:266d3c257594e195b309ae9d1805e54c788f1d7b375f56bf6c367aac6c917e7b AS build
RUN apk add --no-cache build-base
ADD https://api.github.com/repos/pgvector/pgvector/tarball/cab9da72c04353f143bb06b42ab70a403daac64a /tmp/vector.tar.gz
RUN echo 'afcd3604890ae4d11cc7ddb2a0b5a28e9f38dd8b44919a4641e5cceb64e1b95e  /tmp/vector.tar.gz' | sha256sum -c - && mkdir /tmp/vector && tar xzf /tmp/vector.tar.gz -C /tmp/vector --strip-components=1 && make -C /tmp/vector OPTFLAGS='' with_llvm=no && make -C /tmp/vector install with_llvm=no
FROM postgres:17-alpine@sha256:266d3c257594e195b309ae9d1805e54c788f1d7b375f56bf6c367aac6c917e7b
COPY --from=build /usr/local/lib/postgresql/vector.so /usr/local/lib/postgresql/
COPY --from=build /usr/local/share/postgresql/extension/vector* /usr/local/share/postgresql/extension/
