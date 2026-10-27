# Upgrade, rollback, backup, and restore

## Availability and prerequisites

This page defines the required recovery sequence. A complete executable Compose backup/restore procedure and a verified recovery result are not yet established here. Do not use a generic full-database dump as a substitute: ordinary backups must exclude temporary continuation and device-handoff data.

Before production use, obtain the release's exact backup exclusion and restore procedure, then test it on an isolated instance. Maintenance downtime is permitted. Keep the previous image version, matching database backup, and required encryption-key versions available together.

## Back up

1. Choose and record the backup schedule and retention policy. The operator owns both.
2. Arrange a consistent backup using the release's supported maintenance procedure.
3. Back up durable database data while excluding temporary continuation and device-handoff storage. Temporary raw device keys must never enter ordinary backups.
4. Back up encryption keys separately with restricted access. Retain older key versions required to decrypt older backups.
5. Record the application version and backup time without secrets or request content.
6. Test restoration in isolation and verify the invalidation requirements below before accepting the backup as recoverable.

Temporary conversations may be lost during disaster recovery. Generated images are temporary artifacts, not a permanent response archive. Existing backups expire under the operator's policy; deleting live data does not rewrite old backup copies.

## Upgrade

1. Read the target release's compatibility and migration requirements.
2. Schedule downtime and prevent new traffic.
3. Take a recoverable database backup and preserve the matching previous image and encryption keys.
4. Apply the release's database migration procedure and start the matching new image.
5. Check readiness, login, authorization, and representative client/provider behavior before reopening traffic.
6. If the upgrade fails, keep traffic closed and follow rollback.

Do not assume that starting an old image can reverse a database migration.

## Roll back

Restore both the previous image and its matching database backup. A database restore triggers the complete post-restore invalidation sequence, even when rollback follows a failed upgrade. Changes made after the backup can be lost. Notify users to reconnect and recreate affected configuration where necessary.

## Restore and invalidate before startup

1. Keep all service instances stopped and public traffic blocked.
2. Restore the selected durable database backup and configure its required encryption keys.
3. Do not restore temporary continuation or device-handoff state.
4. Invalidate **all API keys, web sessions, invitations, and device authorizations before starting any server**. The established maintenance command is `aiprouter-server restore-invalidate`, run offline with the restored database configuration. A complete released invocation/environment procedure is still required.
5. Verify invalidation completed successfully. If it fails or cannot be verified, keep the service stopped.
6. Start the matching application image, verify readiness and recovery behavior, then reopen traffic.
7. Require users to log in again, issue new keys, and reconnect CLIs. Send fresh invitations and start fresh device approvals. Old continuation state cannot be recovered; explicitly start new conversations.

A restore test must show that old keys, cookies, invitation links, and device approvals fail. A successful database import or health response alone does not prove this.

## Rotate encryption keys

During maintenance, retain the previous key for decryption and configure the new active version. The server interface names `aiprouter-server reencrypt` for re-encryption. Verify completion before removing the previous key from active configuration. Preserve separate copies of every old key needed for retained backups. Do not rotate or delete keys on the assumption that a documentation build verified recovery.
