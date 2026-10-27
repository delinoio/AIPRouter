# Compatibility and verification

## What compatibility means

The compatibility target is opencodex revision `88aaa754566d119b6b8fc898f8fb47602387d82e`. Later upstream changes are not automatically included. This is a reference target, not a claim that AIPRouter has implemented or tested every route, provider, or client.

| Surface                  | Required coverage                                           | Status communicated by this guide                                     |
| ------------------------ | ----------------------------------------------------------- | --------------------------------------------------------------------- |
| Responses                | HTTP, WebSocket, compaction, streaming, tools, continuation | Complete compatibility verification unavailable                       |
| Chat Completions         | Requests, responses, streams, errors, model selection       | Complete compatibility verification unavailable                       |
| Anthropic Messages       | Messages and token counting                                 | Complete compatibility verification unavailable                       |
| Models and hosted search | Discovery and provider-specific behavior                    | Complete compatibility verification unavailable                       |
| Images                   | Generation, editing, temporary artifact retrieval           | Complete compatibility verification unavailable                       |
| Providers                | API keys, OAuth, pools, routing, retry/timeout exceptions   | Per-provider live acceptance not established                          |
| Client configuration     | Formats, paths, platform constraints, restoration, relay    | Configuration handlers implemented; native acceptance not established |

This table is an overview, not an exhaustive inventory or list of supported providers. A release needs a complete interface/provider/authentication/client matrix with evidence for each included path and an explicit exclusion for each omitted path.

## Implemented workflows and remaining limits

The console implements account and organization workflows, provider/pool/routing editors, keys, usage views, and operator controls. The CLI implements browser device approval, protected credentials, named profiles, client configuration/restoration, and loopback service control. [Client guidance](clients.md) lists the current handlers, including native Codex catalog/cache configuration and Claude gateway/Desktop profiles.

The Compose packaging includes the console and API together, PostgreSQL, optional monitoring, proxy examples, and offline maintenance. Automated fixture evidence exists for parts of these workflows. It does not establish production image execution, live identity/provider accounts, native credential stores, or real application compatibility. A final source-revision acceptance assessment remains separate.

Provider OAuth and refresh, advanced pool/routing scheduling, and usage filtering/aggregation are still being completed. Keep unavailable errors visible until the corresponding service implementation and tests establish support. Optional [native TLS compatibility](native-compatibility.md) has separate build gates, consent, and cleanup requirements; the native command/runtime tests do not establish real OS trust or installed-application acceptance. Account-UI trial activation remains unavailable pending production account-attestation confirmation.

## Explicit differences

AIPRouter isolates personal credentials and routing by both organization and user. It requires OIDC, scoped inference keys, bounded encrypted temporary state, and R2 image expiry. Revocation, removal, cancellation, and database outages stop additional sends. Historical cost estimates retain the price used for the request. Local program execution as a provider and associated execution-agent features are excluded.

HTTP local model servers with operator approval, client configuration, and loopback relays remain in scope. User export and automatic import of settings, credentials, and usage are excluded.

## Interpret verification evidence

- A documentation build checks publication output. It does not verify login, inference, restore, or deployment.
- A binary or container build checks packaging for a target. It does not prove native credential storage or background-service behavior.
- Mock and contract tests check controlled responses and required rules. They do not prove a real account, OAuth consent, current provider API behavior, or a production proxy.
- Integration and deployment fixtures check their stated environment. Record its versions and limitations.
- Live provider/account, actual native OS, and real deployment checks require separate evidence. None is claimed by these guides.

Release evidence should identify the tested version, commands, results, and unperformed checks without secrets or raw user content. Required automated gates include security, isolation, recovery, protocol/integration, packaging, and reproducible load tests. Live acceptance is separate from those first-release gates.

## Performance

There is no fixed throughput, latency, availability, or SLO guarantee. Interpret load results only with the workload, provider mocks or accounts, hardware/resources, concurrency, latency distribution, and errors used for that run. A mocked load result does not measure live provider capacity.
