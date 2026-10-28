# Private object storage and version publication

Existing local operation uses the private ignored bundle directory. Set `BUNDLE_BACKEND=s3` explicitly to use S3. There is no fallback on an S3 error. The private bytes remain outside application images and Git.

The optional overlay is `infra/object-store.compose.yml`; the default Compose file has no object-store credentials. Build `judgegrid-minio:session9` with its pinned source Dockerfile, then use an ignored operator environment file containing generated `MINIO_ROOT_USER` and `MINIO_ROOT_PASSWORD`:

```sh
docker compose --env-file bin/local-object-store.env -f docker-compose.yml -f infra/local-judge.compose.yml -f infra/object-store.compose.yml up --build -d --wait minio
go build -o judge-admin ./cmd/judge-admin
```

Keep that file private and ignored. The development API listens on loopback port 19000; there is no exposed MinIO console. Named volume `object-data` preserves bytes across normal restart. `docker compose down` without `-v` preserves it. The development MinIO image is a pinned archived community source build, not a maintained production storage recommendation. See [ADR 0009](adr/0009-immutable-objects-cache-and-rejudge-jobs.md).

On the trusted host, configure APP_ENV=development, DEV_AUTH_ENABLED=true, DEV_PRINCIPAL to the authorized scope, and DATABASE_URL. Object configuration is S3_ENDPOINT (host:port), S3_BUNDLE_BUCKET, S3_CACHE_BUCKET, S3_ACCESS_KEY, S3_SECRET_KEY and S3_TLS. TLS is enabled by default; explicit `S3_TLS=false` is for this isolated local service. Never add these credentials to API, ordinary worker, relay or workload environments. Provision bucket readers/builders with narrower credentials for deployment; the local initialization demonstration uses a generated operator account.

```sh
./judge-admin --allow-development-administration --action init-storage
# Upload existing version-one demo bundles without rebinding their identities:
./judge-local --allow-local-judging --seed-tests
# Stage, upload, verify and atomically activate a new immutable version:
./judge-admin --allow-development-administration --action publish --key revision-2 --file bin/private-publication.json
./judge-admin --allow-development-administration --action staged
```

The publication file contains `problem_id`, `statement`, `time_limit_ms`, `memory_limit_kb` and `bundle`. The bundle uses schema 1, comparator exact/tokens, and 1–16 ordered cases with bounded base64-encoded input/expected bytes. Treat this file as private hidden-test material. The CLI bounds it to 3 MiB. Limits are 50–30000ms and 32768–524288 KiB. The supported runtime set remains fixed C++17/Python; no arbitrary runtime configuration is accepted.

A durable STAGED record precedes upload. On interrupted upload or activation, replay the same key and original file. Different contents with the same key conflict. Unknown objects and staged objects are retained; `staged` is a bounded read-only reconciliation inventory, not a delete command. Never infer garbage from age or an object-key prefix. Object/database dual writes are recoverable, not atomic. A missing/corrupt object prevents execution or produces an infrastructure failure with no fabricated verdict.

Conditional create and bucket versioning prevent ordinary duplicate publishers overwriting bytes. Every load verifies canonical digest/schema, and the evaluator verifies the entire sealed database reference. Versioning is not protection against a privileged storage administrator; changed bytes still fail digest checks. Hash addressing does not replace private access control.

The separate `object-audit` action inventories at most 64 latest keys per configured bucket and compares them with authoritative bundle/publication/cache metadata. Its decisions are known-reference-retain or unknown-retain. It is always read-only, retains historical versions, and refuses an oversized inventory instead of attempting deletion:

```sh
./judge-admin --allow-development-administration --action object-audit
```
