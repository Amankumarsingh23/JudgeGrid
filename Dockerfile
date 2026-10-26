FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=local_ca sh -c 'if [ -f /run/secrets/local_ca ]; then export SSL_CERT_FILE=/run/secrets/local_ca; fi; go mod download'
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api && CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker && CGO_ENABLED=0 go build -trimpath -o /out/manage ./cmd/manage

FROM alpine:3.23
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/api /out/worker /out/manage /usr/local/bin/
USER 10001:10001
EXPOSE 8080
CMD ["api"]
