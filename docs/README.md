# Documentation Catalog

`docs/` is the source of truth for repository contracts. AIPRouter is registered for documentation-first onboarding; its runtime paths are planned.

## Repository contracts

- [Technology defaults](repository-defaults.md)
- [Development and CI](repository-workflow-contract.md)
- [Configuration and secrets](repository-environment-contract.md)
- [Licensing](repository-license-contract.md)
- [Dependency security](repository-dependency-security-contract.md)

## Templates

- [Project index](project-template.md)
- [Domain contract](domain-template.md)

## Project catalog

### AIPRouter

- [Project index](project-aiprouter.md)
- [servers-aiprouter-service-contract](servers-aiprouter-service-contract.md)
- [servers-aiprouter-operations-contract](servers-aiprouter-operations-contract.md)
- [cmds-aiprouter-client-contract](cmds-aiprouter-client-contract.md)
- [protos-aiprouter-management-contract](protos-aiprouter-management-contract.md)
- [packages-aiprouter-libraries-contract](packages-aiprouter-libraries-contract.md)
- [packages-aiprouter-client-contract](packages-aiprouter-client-contract.md)
- [apps-aiprouter-console-contract](apps-aiprouter-console-contract.md)
- [apps-aiprouter-docs-contract](apps-aiprouter-docs-contract.md)

## Instruction update policy

Keep feature behavior in its owning contract. Update `AGENTS.md` only when development procedures, directory ownership or development rules change. Keep validation evidence and issue or pull request traceability in GitHub records and CI logs, rather than instruction files or documentation. See [root instructions](../AGENTS.md).
