# Providers, OAuth, and routing

## Availability

A complete provider/authentication matrix and live account verification are not established here. Do not assume that a provider name in the compatibility reference means that your release supports it. Use a provider only when its release documentation supplies the authentication method, model identifiers, callback requirements, and supported protocol behavior.

## Connect personal providers

1. Select the organization where you will use the provider.
2. Choose an available integration and its documented authentication method.
3. For API-key authentication, supply your own provider key through the connection workflow.
4. For OAuth, use the integration's authorization flow and verify the destination and requested permissions. Follow its exact callback registration requirements. OIDC sign-in and provider OAuth are separate connections.
5. Check connection status with a non-sensitive request and confirm model availability.
6. Repeat setup separately in another organization if needed.

Never ask an owner, administrator, or operator to copy another member's provider credentials. Provider callbacks, OAuth client registrations, refresh behavior, and device/browser constraints are provider-specific. Exact provider instructions remain unavailable until the integration supplies them; do not invent a generic callback.

## Pools, models, and combinations

Add only your own accounts to your personal pool in the selected organization. Select the model and configure routing and combination fallbacks using that release's supported model identifiers and rules. Check streaming, tool calling, continuation, and error behavior for the chosen client/provider pair, rather than testing only a simple text response.

Retries depend on the provider, authentication method, error, and whether an upstream send may already have occurred. There is no universal three-attempt provider retry rule. Do not replay an ambiguous send blindly. Cancellation, membership removal, key revocation, or loss of database authorization must stop additional upstream sends.

Public HTTPS providers are permitted by default. A local/private HTTP model server needs operator approval. Running Claude CLI, CodeBuddy, Qoder, or another local program as the provider backend is excluded, including execution agents and remote workspace/command dispatch. Configuring such a client to use a supported remote inference interface is a separate capability.

## Limits and continuation

Required default admission limits are 120 requests per minute and 10 concurrent requests per user per organization. Operators can choose 1–6,000 requests per minute and 1–100 concurrent requests; unlimited admission is not supported. Exhaustion returns HTTP 429 with `Retry-After` without an admission queue.

Temporary continuation can expire or be evicted. Responses state has a 24-hour lifetime with up to 1,000 entries and 1 GiB per user per organization. Tool-reasoning replay has a one-hour lifetime, 64 entries, and 256 KiB; bridged search replay has a one-hour lifetime, 64 entries, and 512 KiB. These examples are not a complete provider-state inventory.

Missing state must produce an explicit error, not a silent new conversation. Start a new conversation explicitly after expiry or recovery. Provider-specific timeouts also differ: local upstreams have no stall timeout by default unless configured. Check the release's path-specific behavior before tuning proxies or retrying slow work.
