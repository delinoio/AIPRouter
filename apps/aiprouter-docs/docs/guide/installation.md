# Installation

## Availability

The intended installation is a single-host Docker Compose deployment. A production-ready release, published image coordinates, and an end-to-end installation verification are not established by these guides. Use only the versioned distribution and its matching configuration instructions when they become available. Do not substitute guessed image tags or install an unrelated similarly named package.

## Prepare the host

1. Choose Linux x86_64 or ARM64 for the server. Docker Desktop on macOS and Windows is for development or evaluation.
2. Prepare Docker Compose, persistent PostgreSQL storage, and an existing HTTPS reverse proxy.
3. Choose a public HTTPS origin, such as `https://router.example.com`. This is a placeholder, not a hosted service.
4. Prepare a generic OIDC provider, SMTP delivery, a private Cloudflare R2 bucket, and separately stored versioned encryption keys.
5. Identify initial instance operators by their exact OIDC issuer and subject. Organization ownership does not grant operator access.
6. Decide backup frequency, retention, storage access, and a maintenance window. AIPRouter does not schedule backups for you.

## Configure and start

Follow [service configuration](configuration.md) for the established configuration names. Keep credentials outside version control and separate from public documentation or diagnostic attachments.

The server interface defines `aiprouter-server migrate` and `aiprouter-server serve`. These names alone are not a complete installation procedure: use the released Compose configuration to supply the database, secrets, network, console, and image versions. Do not start a partially configured service for production traffic.

Before opening the public proxy, verify database readiness, encryption-key availability, OIDC callback registration, SMTP delivery, and R2 access in an isolated evaluation deployment. Check login, organization selection, a personal provider connection, and a client request. Inspect failures without recording request content or credentials.

## Publication boundaries

The public documentation can be hosted as a static site on Cloudflare Pages. That does not deploy the AIPRouter service. No public documentation domain or hosted service is promised here.

The planned client executable is `aiprouter`, with standalone binaries and checksums and an npm installer named `@delino/aiprouter`. Package publication and download availability must be confirmed for a release before installation. See [client prerequisites](clients.md).
