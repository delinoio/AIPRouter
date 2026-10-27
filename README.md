# AIPRouter

AIPRouter is a self-hosted AI provider router for organizations. Each user manages their own provider credentials, account pools, and model routing within each organization, with usage and estimated cost reporting.

The repository includes a Go server and client configuration CLI, a React web console, public documentation, and Docker Compose deployment assets. Compatibility targets [opencodex](https://github.com/lidge-jun/opencodex) revision `88aaa754566d119b6b8fc898f8fb47602387d82e`, subject to the documented differences and verification limits.

## Capabilities

- Organization membership, invitations, roles, and OIDC sign-in.
- Personal provider connections, OAuth, account pools, and model routing isolated by organization and user.
- Responses, Chat Completions, Anthropic Messages, model discovery, hosted search, and temporary image output.
- Organization-scoped inference keys, usage reporting, and operator controls.
- The `aiprouter` CLI for browser device approval, OS credential storage, named connection profiles, client settings previews, restoration, and HTTP loopback relays.
- Single-host Compose deployment with PostgreSQL, required Cloudflare R2 media storage, and optional observability assets.

AIPRouter does not provide hosted SaaS, service billing, shared organization credentials, automatic settings import, or local program execution as a provider backend. See [compatibility and verification](apps/aiprouter-docs/docs/guide/compatibility.md) for supported surfaces and remaining limits. Build and fixture results do not establish live provider, production deployment, or native OS acceptance.

## Get started

Start with [installation](apps/aiprouter-docs/docs/guide/installation.md) and [configuration](apps/aiprouter-docs/docs/guide/configuration.md). The server targets Linux x86_64 and ARM64 behind your existing HTTPS reverse proxy. Prepare an OIDC provider, SMTP, PostgreSQL, R2, and separate encryption-key backups before deployment. Check release and image availability in the installation guide rather than assuming published artifacts exist.

After signing in, create an organization or accept an invitation, connect your personal providers, configure routing, and issue an API key or approve a client connection.

- [Organizations, invitations, and keys](apps/aiprouter-docs/docs/guide/organizations.md)
- [Providers and routing](apps/aiprouter-docs/docs/guide/providers.md)
- [Client configuration and relay](apps/aiprouter-docs/docs/guide/clients.md)
- [Upgrades, backup, rollback, and restore](apps/aiprouter-docs/docs/guide/recovery.md)
- [Observability and troubleshooting](apps/aiprouter-docs/docs/guide/operations.md)
- [Manual migration](apps/aiprouter-docs/docs/guide/migration.md)

## Development

Use Node.js 24, pnpm 10.26.2, and the Go toolchain selected in `go.mod`. Read the [documentation catalog](docs/README.md), [project index](docs/project-aiprouter.md), and [repository instructions](AGENTS.md) before changing the repository. Component contracts define ownership and required behavior.

```sh
pnpm install --frozen-lockfile
pnpm test
pnpm format:check
```

Start all application development servers from the repository root:

```sh
pnpm dev
```

Turborepo runs the workspace development tasks together: the Go API on `http://127.0.0.1:8080`, the console on `http://127.0.0.1:4173`, and public docs on `http://127.0.0.1:4174`. Configure the API first as described in the [local development contract](docs/repository-environment-contract.md#local-development). PostgreSQL, OIDC, SMTP, and R2 must already be available. The command does not provision infrastructure or migrate databases. Press `Ctrl+C` to stop all three servers; a server failure also stops the group. Restart the command after Go source changes; the web servers provide their usual development reload.

Run the checks for the affected components:

```sh
go test ./...
go vet ./...
pnpm --dir "packages/aiprouter-client" test
pnpm --dir "apps/aiprouter" test
pnpm --dir "apps/aiprouter-docs" test
```

Database integration tests require a disposable PostgreSQL instance configured through `AIPROUTER_TEST_DATABASE_URL`; do not point them at user or production data. See the [workflow contract](docs/repository-workflow-contract.md) for validation requirements and the [distribution guide](packaging/aiprouter/README.md) for Compose fixtures, maintenance, and package builds.

The [public documentation source](apps/aiprouter-docs/docs/index.md) provides user and operator guidance. Internal behavior contracts remain in `docs/`.

## License

Repository-owned content is licensed under [Apache-2.0](LICENSE). Imported code, skills, fonts, and other components retain their original licenses and notices.
