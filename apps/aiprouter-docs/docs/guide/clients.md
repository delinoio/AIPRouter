# API keys and clients

## Availability

The planned executable is `aiprouter`. Published packages, exact command syntax, and a verified per-client path/format/platform matrix are not established by this guide. Do not copy guessed connection flags or overwrite client settings manually to imitate an unavailable integration.

Target CLI platforms are Linux x86_64/ARM64, macOS x86_64/ARM64, and Windows x86_64. A CLI build for an operating system does not mean every external client supports it. Confirm release availability and checksums before installing standalone binaries. The planned npm installer name is `@delino/aiprouter`; publication is not confirmed here.

## Issue an inference key

In the selected organization, use the key workflow to create a personal key with no expiry or a chosen expiry. Copy the raw key when shown; ordinary issuance shows it once. Store it securely. The key belongs to that user and organization and grants inference access, not general management access.

Revoke a lost or exposed key through the organization's key management view. Revocation/expiry blocks new requests immediately. A response already dispatched upstream may finish, but no further retries, account switches, or refreshes are permitted for it.

## Approve a CLI connection

1. Start the supported connection workflow when available. The CLI displays an approval URL and one-time code; the browser can be on another computer for SSH use.
2. Open the URL, log in, and compare the code and server address. Confirm the organization, device name, and key expiry before approving.
3. Allow the CLI to finish storing its dedicated key in the OS credential store.
4. Review the proposed client-settings changes and confirm them.
5. If a client requires a remote key in its own settings, give separate explicit confirmation for that additional storage.

Codes expire ten minutes after original issuance. Polling occurs every five seconds. Denial or expiry before approval creates no key. Interrupted delivery can be retried by the same CLI until the original expiry. Unclaimed keys are revoked at expiry.

If the OS credential store is unavailable, settings must remain unchanged; a normal credential file is not a fallback. The CLI must attempt to revoke an already-issued key. If that fails, sign in to the web console and revoke the reported outstanding key manually.

## Profiles and relay trust

Named profiles keep separate server/organization credentials. Select an active profile for each client. Switching profiles retains the prior profile's key; disconnecting a profile revokes its key.

Some clients require an HTTP relay installed as a user background service that starts at login. It binds only to loopback and has no separate local authentication: other processes on that computer can call it. Use this only under that local-computer trust policy. It does not execute client programs as providers.

Concurrent profile relays require distinct fixed ports. A conflict must fail with an actionable error, not silently select another port. Changing a port requires a preview and confirmation of affected settings. Status, stop, and removal interfaces must be supplied by the actual CLI release before this workflow is available.

## Disconnect and recover settings

Disconnect attempts server-key revocation, stops the relay, and offers restoration from protected original-settings backups. It must preserve unrelated settings and your later edits. Successful restoration deletes the recovery backup.

If your edits conflict, preserve both the edited settings and recovery backup, then reconcile them manually using the reported conflict. Do not overwrite the whole file. Key revocation and relay shutdown must still be attempted. Backups can contain original secrets: keep owner-only permissions or Windows ACLs and never attach them to support requests.

If the server is offline, local cleanup can proceed, but server revocation remains incomplete. When access returns, sign in to that server and organization and revoke the dedicated key. A restored server also requires fresh login, keys, and CLI connection; see [recovery](recovery.md).
