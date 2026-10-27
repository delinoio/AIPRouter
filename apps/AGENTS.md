# apps working instructions

- Follow the root instruction-update policy and the nearest parent instructions.
- This file covers `apps/` and its descendants unless a more specific instruction file applies.
- Read the owning contracts below before changing behavior, including affected cross-domain consumers.

## Development and validation

- Follow root `AGENTS.md` and project-specific docs before adding or changing app code.
- Use Rstest for the AIPRouter console test suite and keep its configuration in `apps/aiprouter/rstest.config.ts`; follow the [console test contract](../docs/apps-aiprouter-console-contract.md#build-and-test).

- Keep repository and domain rules in the appropriate `AGENTS.md` files.

- Write all source and comments in English.

## Owning contracts

- [Repository defaults](../docs/repository-defaults.md)
- [Repository workflow](../docs/repository-workflow-contract.md)
- Register each new project and its domain contracts in [the documentation catalog](../docs/README.md) before implementation.

## AIPRouter ownership

- Read the [AIPRouter project index](../docs/project-aiprouter.md) before work on its paths.
- Read [apps-aiprouter-console-contract](../docs/apps-aiprouter-console-contract.md) for the paths it owns.
- Read [apps-aiprouter-docs-contract](../docs/apps-aiprouter-docs-contract.md) for the paths it owns.
