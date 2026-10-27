# Optional native TLS compatibility

## Availability

Optional Claude first-party Desktop/model-picker compatibility and experimental Codex Desktop compatibility are included product modes. They are separate from ordinary [gateway configuration](clients.md), and are not local executable provider backends.

The runtime and CLI activation integration are still being validated. Exact supported mode/build identifiers and a verified activation procedure are not finalized in this guide. Do not treat the presence of a `native` command in a development build as successful activation. Continue to use gateway configuration until your release documents its supported native build and procedure.

## Review before activation

The activation preview must identify the exact mode and installed build, intercepted destinations and protocol paths, loopback listener addresses, certificate fingerprint, trust-store changes, and application-setting changes. Unknown platforms or builds must refuse. Do not bypass a build check or change system-wide proxy settings to make a refused mode work.

Review and confirm certificate trust separately from application mutation. Ordinary connection or client-settings consent does not authorize either. A bounded Codex account-UI trial requires its own explicit confirmation and remains separate from observe behavior. A displayed quota does not establish billing credit, provider isolation, or server authorization.

The runtime must restrict interception to the pinned client routes and destinations, preserve upstream TLS verification, and avoid forwarding native account credentials to AIPRouter. It is not a general-purpose traffic interceptor. The server retains provider selection and usage authority.

## Trust and local access

Listeners bind only to loopback and use fixed ports; conflicts must fail without choosing another port. Keep the runtime's CA private material in protected user storage. Never copy it into a ticket, commit it, log it, or transmit it. Trust changes must be limited to the exact owned certificate in the current user's trust scope. Missing trust is pending activation, not success.

Status must distinguish enabled, trusted, and ready. Application settings must not be applied using a listener that has not passed readiness and consent checks. Ordinary gateway behavior must remain available without enrolling optional trust.

## Stop, disable, and restore

Stopping a listener is not the same as removing certificate trust or restoring application settings. Disable/disconnect must identify owned resources, stop owned listeners, offer protected settings restoration, and remove only the owned trust certificate after the required confirmation. Preserve unrelated certificates, proxies, and user changes.

If restoration conflicts, retain recovery backups and reconcile the reported edits. If trust removal or listener shutdown fails, cleanup is incomplete; keep the recovery record and follow the release's explicit recovery instructions. Do not report a successful disconnect as proof that trust was removed when that step failed or was declined. Restore native settings before switching back to gateway configuration.

## Verification boundary

Temporary-home fixtures, mock trust adapters, and loopback tests do not establish actual OS trust, installed application, or live-account acceptance. No such acceptance is claimed here. The ChatGPT app-server shim is outside this workflow.
