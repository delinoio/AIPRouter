# AIPRouter distribution assets

The [operations contract](../../docs/servers-aiprouter-operations-contract.md) owns these assets. Public operator guidance belongs to the documentation application. These commands describe the packaging interface for maintainers.

## Compose

Linux amd64 and arm64 are server targets. Docker Desktop on macOS and Windows is for development/evaluation. Run Compose from this directory. Copy `.env.example` to `.env`, restrict it to mode 600, and replace every placeholder. Generate each encryption key with `openssl rand -base64 32`. Keep database passwords URL-safe. Store master keys and database backups separately.

Set the public HTTPS origin and register its `/auth/callback` URL with the OIDC provider. Provider OAuth callback addresses must use the same public origin and the callback path supplied by that integration. The existing operator reverse proxy terminates TLS; examples are in `proxy/`. Do not expose health or metrics routes publicly. Configure SMTP, R2, and the exact OIDC issuer/subject operator allowlist before production use. Private provider destinations require operator approval; the mock provider is not implicitly approved in production.

```sh
docker compose build
docker compose up -d --wait postgres
docker compose run --rm --no-deps aiprouter migrate
docker compose up -d aiprouter
docker compose --profile observability up -d prometheus grafana
```

The production file publishes only loopback ports and no PostgreSQL port. `compose.fixture.yml` adds synthetic OIDC identities (`alice`, `bob`, `unverified`), an SMTP receiver, and a mock provider. The server shares the fixture network namespace so browser and server agree on the loopback issuer. The override requires Compose 2.24 or later. The SMTP receiver counts accepted messages and discards content. `/fixture/mail` reports the count. Fixture provider models `fixture-429` and `fixture-503` produce reproducible errors; `fixture-model` supports JSON and SSE chat responses. This fixture does not implement R2 or full provider compatibility. The service/adapter tests own those cases.

```sh
docker compose -f compose.yml -f compose.fixture.yml up -d --build
```

Fixtures must remain local. Use `scripts/aiprouter/compose-smoke.py` from the root for an isolated disposable installation/recovery test. It refuses an existing `.env` and removes only its own Compose project. It requires free loopback ports 8080 and 18090 and a working Docker daemon.

## Maintenance interface

Run `python3 scripts/aiprouter/maintenance.py` from the repository root with:

- `backup /absolute/path/backup.dump`: creates a mode-600 custom PostgreSQL archive, refuses overwrite, and excludes `aiprouter_temp` entirely. Encryption keys are never included.
- `restore /absolute/path/backup.dump`: stops the server, removes old temporary state, restores durable state transactionally, migrates, and invalidates all restored authority before any restart.
- `rotate`: stops the server and invokes verified re-encryption with the configured active key. Configure both old and new keys first. Remove the old active key only after successful completion; retain separate copies required for old backups.
- `upgrade`: stops the server and runs migrations using the selected image. Back up first. Rollback requires the prior image and its matching database backup; apply restore invalidation before restart.

Every mutation leaves the server stopped, including on failure. The operator explicitly runs `docker compose up -d aiprouter` only after success. Stop fixture sidecars before maintenance. Recovery requires fresh login, keys, invitations, and CLI connections. These commands assume the single-host Compose instance is the only process admitting traffic to this database; stop any separately managed instance too. Backup scheduling and retention remain operator responsibilities.

## Validation and distribution

`scripts/aiprouter/validate.sh` checks mock servers, maintenance ordering/failure handling, installer integrity, and Compose schema without a daemon. CI additionally runs Compose installation/recovery, alert timing fixtures, cross-compilation, package inspection, and deterministic mock load on Linux. None establishes live account, production, or native OS acceptance.

`scripts/aiprouter/build-release.py /new/output/path` builds five CLI targets and two Linux server targets, writes SHA256SUMS, gathers complete Go dependency licenses/notices, and assembles an npm tarball. The npm installer selects the exact platform and verifies its binary against checksums embedded in the tarball. It rejects unsupported platforms or missing/mismatched checksums. Template sources are not publishable by themselves; checksums and licenses are injected only during staging. This command does not publish. Before an actual release, verify npm scope ownership and publishing authority for `@delino/aiprouter`, review dependency advisories, validate package contents, and bind release assets to the staged checksums. Build success does not establish OS credential-store or background-service acceptance.

`scripts/aiprouter/load.py --output /tmp/aiprouter-load.json` sends a fixed synthetic workload to the mock provider. Set `--url` to test the router and provide its synthetic key through `AIPROUTER_LOAD_KEY`; never use a real user's content. Reports include revision, workload, hardware, harness resources, latency, and error counts. Supply measured server resources through `--server-resources` when measured externally. An absent server measurement is explicitly reported. This is not an SLO or a live-provider capacity claim.

`scripts/aiprouter/router-load.py --output /tmp/aiprouter-router-load.json` measures the authenticated router against the mock upstream. Supply `AIPROUTER_TEST_DATABASE_URL` for a disposable PostgreSQL server with database-creation privileges. The harness creates and removes its own database, seeds two users across four organizations, sets explicit fixture limits, and sends the fixed workload through one issued key. It samples router process CPU/RSS and records seed/limit conditions. Credentials remain in memory and child environments. It requires free loopback ports 8080, 18090, and 1025. This is the CI load path; direct fixture load alone does not satisfy router load validation.

The three alerts cover one-minute service failure, one-minute DB failure, and three consecutive cleanup failures or fifteen minutes without cleanup success. Grafana shows aggregate health/outcome series only. Prometheus evaluates alerts; operators supply their own Alertmanager routing and any capacity/error-rate policies. Enable OTLP export through the server-supported environment and an operator-owned collector; do not expose telemetry publicly or add prompt/response attributes.
