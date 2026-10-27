# Configure services

## Configuration availability

The following names are supplied by the server integration interface. Their presence does not establish live integration acceptance. Use a release's matching configuration template; do not infer additional flags, callback routes, or environment variables.

| Purpose                | Configuration                                                                                                             |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| Internal HTTP listener | `AIPROUTER_LISTEN_ADDR` (default `127.0.0.1:8080`)                                                                        |
| Public HTTPS origin    | `AIPROUTER_PUBLIC_URL`                                                                                                    |
| PostgreSQL connection  | `AIPROUTER_DATABASE_URL`                                                                                                  |
| OIDC                   | `AIPROUTER_OIDC_ISSUER`, `AIPROUTER_OIDC_CLIENT_ID`, `AIPROUTER_OIDC_CLIENT_SECRET`                                       |
| Operator identities    | `AIPROUTER_OPERATORS` (JSON array with `issuer` and `subject`)                                                            |
| Encryption             | `AIPROUTER_ENCRYPTION_KEYS` (JSON mapping key versions to base64-encoded 32-byte keys), `AIPROUTER_ENCRYPTION_ACTIVE_KEY` |
| SMTP                   | `AIPROUTER_SMTP_ADDR`, `AIPROUTER_SMTP_FROM`, `AIPROUTER_SMTP_USERNAME`, `AIPROUTER_SMTP_PASSWORD`                        |
| R2                     | `AIPROUTER_R2_ENDPOINT`, `AIPROUTER_R2_BUCKET`, `AIPROUTER_R2_ACCESS_KEY_ID`, `AIPROUTER_R2_SECRET_ACCESS_KEY`            |

Do not paste real values into support requests. Encryption keys must be generated and stored securely, not copied from an example. Back them up separately from the database.

## OIDC and operators

1. Register an application with the generic OIDC provider selected by the operator.
2. Register the exact public callback: for the example origin, `https://router.example.com/auth/callback`.
3. Configure the issuer, client ID, and client secret using the names above.
4. Ensure accounts have an email and `email_verified=true`.
5. Configure the operator allowlist using exact issuer/subject pairs.

Users are identified by issuer plus subject. Matching email addresses do not merge identities. There is no email/password login. Required session behavior is reauthentication after 24 hours of inactivity or seven days after login, whichever comes first; logout invalidates that session immediately.

Operator additions and removals require deployment configuration changes. Operators manage instance settings and health/aggregates; this role does not grant member impersonation or access to another user's provider secrets or request content.

## SMTP

Configure the SMTP server, sender, and required authentication. Verify delivery to a controlled mailbox before enabling invitations. The exact SMTP transport-security settings must come from the release configuration; no additional TLS environment switch is specified here.

An invitation can fail even if login works. Check recipient acceptance and transient/permanent SMTP errors separately. Read [invitation retries](organizations.md) before resending: a manual resend invalidates the earlier link.

## HTTPS and callbacks

The existing reverse proxy terminates public TLS. The service speaks internal HTTP. Keep PostgreSQL and metrics private. Configure proxy support for streaming and WebSocket upgrades, and preserve the intended public host and scheme. Do not expose the internal HTTP listener directly to the internet.

Conceptually, `https://router.example.com` routes to the private service listener; `/auth/callback` returns to the same public origin. This is an address example, not a complete proxy configuration. A container-to-container deployment needs the listener and proxy network selected by its Compose configuration, rather than a host-loopback address copied blindly.

Register provider OAuth callbacks only when the provider integration supplies the exact callback and client requirements. The OIDC callback is not a substitute for provider OAuth callbacks. Production proxy examples and provider callback details remain unavailable until established by the matching deployment/integration package.

## Required R2 storage

1. Create a private R2 bucket for temporary generated images.
2. Supply its endpoint, bucket, and access credentials through the configuration above.
3. Give the service the object access needed to upload, retrieve, and delete its artifacts.
4. Verify object creation and deletion, and signed download expiry, with non-sensitive evaluation data.

Do not make the bucket public to repair a failed signed download. Required limits are 200 files per user per organization, 50 MiB per image, and 100 MiB decoded image data per response. Files expire after 24 hours; oldest-file eviction can remove them earlier.

A signed URL is a bearer capability. Anyone holding it can download until it expires, for at most five minutes and never beyond artifact expiry. Authorization is checked when the URL is issued, not again for each download. Deleting the object makes an existing URL unusable. Do not put signed URLs in public logs or tickets.

## Provider destination policy

Public HTTPS destinations are the default. Private/local HTTP model servers require explicit operator approval. No concrete destination-allowlist configuration name is established here. Do not disable destination checks to work around a connection failure.
