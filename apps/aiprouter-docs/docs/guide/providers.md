# Providers, OAuth, and routing

## Availability

The console provides Providers, Pools, and Routing pages backed by the service. A complete provider/authentication matrix and live account verification are not established here. Do not assume that a provider name in the compatibility reference means that your release supports it. Use a provider only when its release documentation supplies the authentication method, model identifiers, callback requirements, and supported protocol behavior.

## Connect personal providers

1. Select the organization where you will use the provider.
2. Open Providers, select **Connect provider**, and search the catalog. Choose the integration and authentication method.
3. For API-key authentication, supply your own provider key through the connection workflow.
4. For OAuth, use the integration's authorization flow and verify the destination and requested permissions. Follow its exact callback registration requirements. OIDC sign-in and provider OAuth are separate connections.
5. Check connection status with a non-sensitive request and confirm model availability.
6. Repeat setup separately in another organization if needed.

Never ask an owner, administrator, or operator to copy another member's provider credentials. Provider callbacks, OAuth client registrations, refresh behavior, and device/browser constraints are provider-specific. Exact provider instructions remain unavailable until the integration supplies them; do not invent a generic callback.

## Provider authorization in the console

For an OAuth integration, select **Continue with OAuth**, choose an authentication method offered for that provider, and supply any requested method-specific settings. Use **Open provider sign-in** only after verifying its destination and requested permissions. OIDC login and provider authorization remain separate flows.

For device or browser-poll methods, enter the displayed user code at the provider if requested and keep the authorization dialog open to see the result. The console respects the service's updated polling interval. For a method that requires manual completion, paste the returned authorization code, token, or full callback URL into the masked completion field. Supply authorization state if the dialog requires it. This server does not open a local callback listener; do not invent or register a generic callback route.

Closing the dialog does not cancel the flow. Use **Cancel authorization** to cancel explicitly. A completed flow creates or reconnects your scoped provider connection; expired, canceled, failed, and unavailable results are not successful connections. Completion and refresh can trigger model discovery, but missing model evidence is not proof of account entitlement.

OAuth completion, cancellation, refresh, and model discovery are implemented for the corresponding supported provider methods. Provider-specific restrictions still apply. If a connection reports **Authorization required**, reconnect through the explicit authorization workflow rather than repeatedly retrying an uncertain credential rotation. Automated provider fixtures do not establish live login, refresh, or native callback acceptance.

## Pools, models, and combinations

Add only your own accounts to your personal pool in the selected organization. Select the model and configure routing and combination fallbacks using that release's supported model identifiers and rules. Check streaming, tool calling, continuation, and error behavior for the chosen client/provider pair, rather than testing only a simple text response.

In Pools, select **Create pool**, name it, choose the strategy, and add your own connections in the intended order. In Routing, select **Create route**, enter the model alias used by your clients, choose a strategy, and add connection or pool targets with model IDs. Direct routing requires one target. Review target order and any weights before saving. Resolve unavailable targets and dependent routes before disconnecting a provider.

Pools support priority, round-robin, and least-recently-used selection. Routes support direct, fallback, round-robin, and weighted selection. These strategies now have server selection support; use positive weights for weighted targets and do not add duplicate pool members or route targets. Direct routes require exactly one target.

Keep a pool's provider and transport compatible. An incompatible pool is rejected; use separate route targets for cross-provider fallback. Paused, expired, uncertain, or cooled-down accounts can be ineligible even when listed in your settings. Bound continuation/recovery cannot silently rotate to another account. Strategy availability does not establish every provider-specific retry, quota, or probe behavior.

To opt in to generic OAuth session affinity, select **Keep generic OAuth sessions on the same account** in the pool editor. The default is off. Supported generic sessions can retain account affinity for 24 hours within their key/provider/session scope; Codex and Anthropic retain provider-specific session rules. Affinity does not override current authorization, account eligibility, or revocation. Deselect the option and save to remove the opt-in.

Model discovery can be unavailable for a custom or uncatalogued provider. The routing editor permits an explicit model ID; obtain it from the actual provider, not a guessed alias. No last-verified timestamp is available for provider connections: **Added** identifies creation, not successful verification.

Retries depend on the provider, authentication method, error, and whether an upstream send may already have occurred. There is no universal three-attempt provider retry rule. Do not replay an ambiguous send blindly. Cancellation, membership removal, key revocation, or loss of database authorization must stop additional upstream sends.

Public HTTPS providers are permitted by default. A local/private HTTP model server needs operator approval. Running Claude CLI, CodeBuddy, Qoder, or another local program as the provider backend is excluded, including execution agents and remote workspace/command dispatch. Configuring such a client to use a supported remote inference interface is a separate capability.

## Limits and continuation

Required default admission limits are 120 requests per minute and 10 concurrent requests per user per organization. Operators can choose 1–6,000 requests per minute and 1–100 concurrent requests; unlimited admission is not supported. Exhaustion returns HTTP 429 with `Retry-After` without an admission queue.

Temporary continuation can expire or be evicted. Responses state has a 24-hour lifetime with up to 1,000 entries and 1 GiB per user per organization. Tool-reasoning replay has a one-hour lifetime, 64 entries, and 256 KiB; bridged search replay has a one-hour lifetime, 64 entries, and 512 KiB. These examples are not a complete provider-state inventory.

Missing state must produce an explicit error, not a silent new conversation. Start a new conversation explicitly after expiry or recovery. Provider-specific timeouts also differ: local upstreams have no stall timeout by default unless configured. Check the release's path-specific behavior before tuning proxies or retrying slow work.
