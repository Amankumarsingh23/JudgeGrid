# Database migrations

`go run ./cmd/manage migrate` applies embedded, ordered SQL migrations in one transaction under a PostgreSQL advisory lock. Applied filenames and SHA-256 checksums are recorded in `schema_migrations`. `go run ./cmd/manage check` fails on missing migrations or changed checksums. Add new migrations rather than editing applied SQL.

Compose runs migrations before the API starts. To verify or seed explicitly:

```sh
docker compose run --rm migrate manage check
docker compose run --rm -e APP_ENV=development migrate manage seed
```

Seed is an explicit development-only CLI operation. It creates two problems and their first versions idempotently, preserving any newer current-version pointer. Version rows cannot be updated or deleted. Publish a new version by inserting it and updating the problem's current pointer in a transaction; there is no public mutation endpoint.

Migrations are forward-only. Back up persistent data before future schema changes. Tests use isolated schemas and never drop the application's public schema.
