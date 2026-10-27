# AIPRouter management protocol contract

## Scope

Owns `protos/aiprouter/v1`: versioned Protobuf schemas and committed generated Go bindings.

## Runtime and Language

Protobuf with Go/Connect bindings in root module `github.com/delinoio/AIPRouter`. Generated TypeScript belongs to `packages/aiprouter-client`.

## Users and Operators

Server, console, and CLI connection-flow consumers.

## Interfaces and Contracts

Define versioned Connect RPC contracts for organization, member, key, provider, usage, and operator management, plus the CLI/browser connection flow. `management.proto` owns concrete messages and methods. `authorization.proto` declares method authorization scopes; runtime handlers enforce those scopes. Keep public inference wire protocols under the service compatibility contract. A stable public management API for external automation is excluded from the first release. Inference keys do not imply management permissions. New persisted entities use UUID v7; preserve externally required protocol ID shapes.

### Service boundaries

- `AccountService`, `OrganizationService`, `MembershipService`, `InvitationService`, and `OwnershipService` own identity and organization lifecycle.
- `KeyService`, `ProviderService`, `PoolService`, and `RoutingService` manage personal resources within a selected organization.
- `UsageService` and `AuditService` expose authorized metadata only.
- `OperatorService` exposes instance settings and aggregate health without organization inspection parameters.
- `DeviceAuthorizationService` binds CLI polling, confirmation, and compensation to a device secret. `DeviceApprovalService` requires a browser session and explicit approval.
- `ClientService` grants only model discovery and self-revocation for the authenticated inference key.

RPC paths use `/aiprouter.v1.<Service>/<Method>`. Personal requests select an organization; the authenticated session determines the user. Optional token and price fields distinguish unknown values from known zero. Decimal costs use strings. Never add prompts, responses, or stored secret values to metadata messages.

## Storage

Schemas describe lifecycle and ownership; bindings do not own persisted state.

## Security

Encode required authorization context and distinguish permission failure, expiry, cancellation, unknown usage/cost, and incomplete delivery. Server enforcement remains authoritative; generated interfaces cannot grant access to secrets or another user’s content.

## Logging

Use structured logs and request identifiers. Never log provider secrets, API keys, device secrets, raw prompts, or raw responses. Distinguish unknown usage, write failures, cancellation, limits, and upstream/gateway errors. See the [observability contract](servers-aiprouter-operations-contract.md).

## Build and Test

Run `protos/aiprouter/generate.sh` to generate Go, Connect Go, and TypeScript bindings. The script pins protoc 33.0 and the generator versions; install workspace dependencies first. Run `protos/aiprouter/generate.sh --check` to compare bytes and file sets without rewriting. Run `buf lint`, `buf format --diff --exit-code`, `go test ./protos/aiprouter/...`, and `pnpm --filter @aiprouter/client typecheck` plus its `test` command. Add protocol contract tests for authentication, error mapping, field presence, and cross-tenant isolation; use the [operations scenarios](servers-aiprouter-operations-contract.md).

## Dependencies and Integrations

- [servers-aiprouter-service-contract](servers-aiprouter-service-contract.md)
- [packages-aiprouter-client-contract](packages-aiprouter-client-contract.md)
- [cmds-aiprouter-client-contract](cmds-aiprouter-client-contract.md)
- [apps-aiprouter-console-contract](apps-aiprouter-console-contract.md)

## Change Triggers

Update this contract, the project index, and affected producer/consumer contracts together when interfaces, ownership, or lifecycle rules change. Follow the [instruction-update policy](README.md#instruction-update-policy); behavior belongs in contracts, not instruction files. Record implementation status, validation results, and unresolved limits in GitHub records or CI artifacts.

## References

- [Project index](project-aiprouter.md)
- [Repository defaults](repository-defaults.md)
- [Workflow](repository-workflow-contract.md)
- [Environment](repository-environment-contract.md)
- [Licensing](repository-license-contract.md)
- [Dependency security](repository-dependency-security-contract.md)
