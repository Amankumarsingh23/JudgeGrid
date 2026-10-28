# MinIO community images are no longer distributed. Build a pinned upstream release.
FROM golang:1.26 AS build
RUN --mount=type=secret,id=local_ca --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
 sh -c 'if [ -f /run/secrets/local_ca ]; then export SSL_CERT_FILE=/run/secrets/local_ca; fi; go mod download github.com/minio/minio@v0.0.0-20250907161309-07c3a429bfed && cd /go/pkg/mod/github.com/minio/minio@v0.0.0-20250907161309-07c3a429bfed && CGO_ENABLED=0 go build -mod=mod -buildvcs=false -o /out/minio .'
FROM alpine:3.23
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/minio /usr/local/bin/minio
RUN mkdir /data && chown 10001:10001 /data
USER 10001:10001
ENV MINIO_BROWSER=off
ENTRYPOINT ["minio"]
CMD ["server","/data","--address",":9000"]
