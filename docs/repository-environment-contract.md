# Repository Environment Contract

## Configuration ownership

Application services define configuration schemas, validation, local defaults and credential boundaries in their domain contracts. The [AIPRouter operations contract](servers-aiprouter-operations-contract.md) owns its service configuration and deployment boundary. Production credentials and user state remain outside the repository.

## Local state

Never commit `.infisical.json`, `.dev-environment/`, real `.env` files or user, production, release and signing credentials. Service-local `.env.example` files may contain names, validation guidance, placeholders and safe loopback defaults only. Do not add a root environment example.

## Process boundaries

Use explicit argument arrays and `shell: false` for child processes. Document any required platform shim. Forward termination signals and wait for owned child processes to exit. Documentation servers must declare a fixed loopback port and fail on conflicts.

## Local development

Root `pnpm dev` starts the Go API, React console, and public documentation server together. It requires the pinned Node.js/pnpm versions, the Go toolchain, installed workspace dependencies, and configured PostgreSQL, OIDC, SMTP, and R2 services. It does not start deployment or observability containers, migrate databases, or seed user state. Prepare a disposable development database with the existing server `migrate` command before starting admission.

The launcher reads optional `servers/aiprouter/.env` with Node.js environment-file parsing. Exported environment variables take precedence. File values are passed only to the API child; the launcher does not add them to frontend environments. Use the configuration names and validation requirements in the [operations contract](servers-aiprouter-operations-contract.md); the service-local example contains placeholders, not working credentials. Keep populated files uncommitted and owner-readable only.

Use `AIPROUTER_LISTEN_ADDR=127.0.0.1:8080` and `AIPROUTER_PUBLIC_URL=http://127.0.0.1:4173`. Register `http://127.0.0.1:4173/auth/callback` with the development OIDC provider. The console forwards `/auth` and Connect RPC traffic to the API at port 8080. The existing synthetic OIDC fixture accepts only the API-origin callback at port 8080 and cannot replace an OIDC provider registered for port 4173 without changes. Console and documentation listeners bind to loopback ports 4173 and 4174 and fail on conflicts. API overrides must remain compatible with the console proxy.

The launcher runs the API with `go run`; restart it after Go source changes. Web servers retain their normal development reload. Child logs inherit the terminal, and launcher lifecycle events use structured JSON without environment values. On a child exit or spawn failure, stop the other children and return a failure code. Forward SIGINT/SIGTERM to owned process groups, wait up to 35 seconds, and force termination after the deadline. Windows uses `taskkill /T /F` to stop process trees because POSIX group signals are unavailable.
