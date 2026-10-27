# AIPRouter management protocol contract

## Scope

Owns planned `protos/aiprouter/v1`: versioned Protobuf schemas and committed generated Go bindings.

## Runtime and Language

Protobuf with Go/Connect bindings in root module `github.com/delinoio/AIPRouter`. Generated TypeScript belongs to `packages/aiprouter-client`.

## Users and Operators

Server, console, and CLI connection-flow consumers.

## Interfaces and Contracts

Define versioned Connect RPC contracts for organization, member, key, provider, usage, and operator management, plus the CLI/browser connection flow. Concrete message and method schemas are required implementation work, not established by this onboarding. Keep public inference wire protocols under the service compatibility contract. A stable public management API for external automation is excluded from the first release. Inference keys do not imply management permissions. New persisted entities use UUID v7; preserve externally required protocol ID shapes.

## Storage

Schemas describe lifecycle and ownership; bindings do not own persisted state.

## Security

Encode required authorization context and distinguish permission failure, expiry, cancellation, unknown usage/cost, and incomplete delivery. Server enforcement remains authoritative; generated interfaces cannot grant access to secrets or another user’s content.

## Logging

Use structured logs and request identifiers. Never log provider secrets, API keys, device secrets, raw prompts, or raw responses. Distinguish unknown usage, write failures, cancellation, limits, and upstream/gateway errors. See the [observability contract](servers-aiprouter-operations-contract.md).

## Build and Test

Generate reproducibly, check generated output for drift, and run Go tests and TypeScript consumer tests. Add protocol contract tests for authentication, error mapping, field presence, and cross-tenant isolation; use the [operations scenarios](servers-aiprouter-operations-contract.md).

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
