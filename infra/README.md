Local infrastructure is defined in the root docker-compose.yml.

`repository-ingestion.compose.yml` is an explicit `repository-storage` profile for private source bytes. It uses a separate MinIO instance, credentials, loopback port 19100 and persistent volume from judge bundles/cache. See [repository ingestion](../docs/repository-ingestion.md). No source credentials or Git/Docker authority are added to ordinary services.
