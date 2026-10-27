# Repository Environment Contract

## Configuration ownership

Application services define configuration schemas, validation, local defaults and credential boundaries in their domain contracts. The [AIPRouter operations contract](servers-aiprouter-operations-contract.md) owns its service configuration and deployment boundary. Production credentials and user state remain outside the repository.

## Local state

Never commit `.infisical.json`, `.dev-environment/`, real `.env` files or user, production, release and signing credentials. Service-local `.env.example` files may contain names, validation guidance, placeholders and safe loopback defaults only. Do not add a root environment example.

## Process boundaries

Use explicit argument arrays and `shell: false` for child processes. Document any required platform shim. Forward termination signals and wait for owned child processes to exit. Documentation servers must declare a fixed loopback port and fail on conflicts.
