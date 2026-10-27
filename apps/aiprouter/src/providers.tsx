// SPDX-License-Identifier: Apache-2.0
import { useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import {
  ProviderService,
  PoolService,
  RoutingService,
  ProviderAuthKind,
  ConnectionState,
  OAuthState,
  type ProviderConnection,
  type ProviderOAuth,
  type ProviderDefinition,
} from "@aiprouter/client";
import { useApi, useRefresh, type PageProps, pageSize } from "./api";
import { useAction } from "./actions";
import { usePersonalResources } from "./resources";
import {
  PageHeader,
  Button,
  Card,
  Field,
  Dialog,
  ConfirmDialog,
  ErrorNotice,
  Loading,
  EmptyState,
  Badge,
  Notice,
  formatDate,
  Pagination,
} from "./ui";
export const connectionLabel: Record<number, string> = {
  [ConnectionState.UNSPECIFIED]: "Unknown status",
  [ConnectionState.READY]: "Connected",
  [ConnectionState.AUTHORIZATION_REQUIRED]: "Reconnect required",
  [ConnectionState.DISABLED]: "Not connected",
  [ConnectionState.COOLDOWN]: "Cooling down",
  [ConnectionState.ERROR]: "Connection failed",
};
export const authLabel: Record<number, string> = {
  [ProviderAuthKind.API_KEY]: "API key",
  [ProviderAuthKind.OAUTH]: "OAuth",
  [ProviderAuthKind.NO_AUTH]: "No authentication",
};
export function ProvidersPage({ organization }: PageProps) {
  const api = useApi();
  const action = useAction();
  const [token, setToken] = useState("");
  const [search, setSearch] = useState("");
  const [edit, setEdit] = useState<ProviderConnection | true>();
  const [remove, setRemove] = useState<ProviderConnection>();
  const [models, setModels] = useState<ProviderConnection>();
  const [oauth, setOauth] = useState<ProviderOAuth>();
  const query = useQuery(ProviderService.method.listProviderConnections, {
    organizationId: organization.id,
    page: { pageSize, pageToken: token },
  });
  const catalog = useQuery(ProviderService.method.listProviderCatalog, {});
  const { pools, routes } = usePersonalResources(organization.id);
  const records = query.data?.connections.filter((c) =>
    `${c.name} ${c.providerId}`.toLowerCase().includes(search.toLowerCase()),
  );
  return (
    <>
      <PageHeader
        title="Your providers"
        description={`Only yours in ${organization.name}.`}
        action={
          <Button variant="primary" onClick={() => setEdit(true)}>
            Connect provider
          </Button>
        }
      />
      <Notice>
        Private to you. Your providers and routing are separate in every
        organization.
      </Notice>
      <div className="actions">
        <a
          className="button"
          href={`?organization=${organization.id}&page=pools`}
        >
          Manage account pools
        </a>
      </div>
      {action.feedback}
      <Field label="Search your connections">
        <input
          type="search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </Field>
      <ErrorNotice error={query.error} retry={() => void query.refetch()} />
      {query.isPending ? (
        <Loading />
      ) : records?.length === 0 ? (
        <EmptyState
          title={
            search ? "No matching connections" : "Connect your first provider"
          }
          action={
            search ? (
              <Button onClick={() => setSearch("")}>Clear filters</Button>
            ) : undefined
          }
        >
          Only you can use these provider accounts.
        </EmptyState>
      ) : (
        <div className="grid">
          {records?.map((c) => (
            <Card key={c.id}>
              <div className="record-title">
                <span className="provider-mark" aria-hidden="true">
                  {c.providerId.slice(0, 2).toUpperCase()}
                </span>
                <div>
                  <h2>{c.name}</h2>
                  <p>
                    {catalog.data?.providers.find((p) => p.id === c.providerId)
                      ?.displayName ?? c.providerId}
                  </p>
                </div>
              </div>
              <Badge
                tone={
                  c.state === ConnectionState.READY
                    ? "success"
                    : c.state === ConnectionState.ERROR
                      ? "danger"
                      : "warning"
                }
              >
                {connectionLabel[c.state] ?? "Unknown status"}
              </Badge>
              <p>{authLabel[c.authKind]} · Personal account</p>
              <p>Added {formatDate(c.createdAt)}</p>
              {c.cooldownUntil && (
                <p>Cooling down until {formatDate(c.cooldownUntil)}</p>
              )}
              <div className="actions">
                <Button onClick={() => setEdit(c)}>Edit connection</Button>
                <Button onClick={() => setModels(c)}>View models</Button>
                {c.authKind === ProviderAuthKind.OAUTH && (
                  <Button
                    disabled={action.pending}
                    onClick={() => {
                      void action.run(
                        () =>
                          api.provider.beginProviderOAuth({
                            organizationId: organization.id,
                            providerId: c.providerId,
                            name: c.name,
                            connectionId: c.id,
                          }),
                        (r) => setOauth(r.authorization),
                        "Authorization started.",
                      );
                    }}
                  >
                    Reconnect
                  </Button>
                )}
                <Button variant="danger" onClick={() => setRemove(c)}>
                  Disconnect
                </Button>
              </div>
            </Card>
          ))}
        </div>
      )}
      <Pagination
        next={query.data?.page?.nextPageToken}
        onNext={setToken}
        onFirst={() => setToken("")}
        hasPrevious={!!token}
      />
      {edit && (
        <ConnectionEditor
          connection={edit === true ? undefined : edit}
          providers={catalog.data?.providers ?? []}
          error={action.error ?? catalog.error}
          pending={action.pending}
          onClose={() => setEdit(undefined)}
          onSave={(values) => {
            void action.run(
              async () => {
                if (edit !== true) {
                  await api.provider.updateProviderConnection({
                    organizationId: organization.id,
                    connectionId: edit.id,
                    name: values.name,
                    baseUrl: values.baseUrl,
                    apiKey: values.secret || undefined,
                  });
                  return undefined;
                }
                if (values.authKind === ProviderAuthKind.OAUTH) {
                  return (
                    await api.provider.beginProviderOAuth({
                      organizationId: organization.id,
                      providerId: values.providerId,
                      name: values.name,
                    })
                  ).authorization;
                }
                await api.provider.createProviderConnection({
                  organizationId: organization.id,
                  providerId: values.providerId,
                  name: values.name,
                  baseUrl: values.baseUrl,
                  authKind: values.authKind,
                  apiKey: values.secret || undefined,
                });
                return undefined;
              },
              (authorization) => {
                setEdit(undefined);
                setOauth(authorization);
              },
              "Connection saved.",
            );
          }}
        />
      )}
      {models && (
        <ModelsDialog
          organizationId={organization.id}
          connection={models}
          onClose={() => setModels(undefined)}
        />
      )}
      {oauth && (
        <OAuthDialog
          organizationId={organization.id}
          authorization={oauth}
          onClose={() => setOauth(undefined)}
        />
      )}
      {remove && (
        <ConfirmDialog
          title={`Disconnect ${remove.name}?`}
          confirmLabel="Disconnect provider"
          onClose={() => setRemove(undefined)}
          pending={action.pending}
          error={action.error}
          onConfirm={() => {
            void action.run(
              () =>
                api.provider.deleteProviderConnection({
                  organizationId: organization.id,
                  connectionId: remove.id,
                }),
              () => setRemove(undefined),
              "Provider disconnected.",
            );
          }}
        >
          Future requests can no longer use this connection. Your external
          provider account is not deleted. Affected pools:{" "}
          {pools.data
            ?.filter((p) => p.members.some((m) => m.connectionId === remove.id))
            .map((p) => p.name)
            .join(", ") ||
            (pools.isPending || routes.isPending
              ? "Checking dependencies…"
              : "None")}
          . Direct routes:{" "}
          {routes.data
            ?.filter((r) =>
              r.targets.some(
                (t) =>
                  t.target.case === "connectionId" &&
                  t.target.value === remove.id,
              ),
            )
            .map((r) => r.modelId)
            .join(", ") ||
            (pools.isPending || routes.isPending
              ? "Checking dependencies…"
              : "None")}
          . Routes through these pools also need review.{" "}
          {(pools.error || routes.error) &&
            "Dependencies could not be loaded. Review routing before disconnecting. "}
          <a href={`?organization=${organization.id}&page=routing`}>
            Review routing
          </a>{" "}
          before disconnecting.
        </ConfirmDialog>
      )}
    </>
  );
}
type ConnectionValues = {
  providerId: string;
  name: string;
  baseUrl: string;
  authKind: ProviderAuthKind;
  secret: string;
};
function ConnectionEditor({
  connection,
  providers,
  onClose,
  onSave,
  pending,
  error,
}: {
  connection?: ProviderConnection;
  providers: ProviderDefinition[];
  onClose: () => void;
  onSave: (v: ConnectionValues) => void;
  pending: boolean;
  error: unknown;
}) {
  const [providerId, setProvider] = useState(connection?.providerId ?? "");
  const [name, setName] = useState(connection?.name ?? "");
  const [baseUrl, setBase] = useState(connection?.baseUrl ?? "");
  const [authKind, setAuth] = useState(
    connection?.authKind ?? ProviderAuthKind.UNSPECIFIED,
  );
  const [secret, setSecret] = useState("");
  const [reveal, setReveal] = useState(false);
  const [search, setSearch] = useState("");
  const provider = providers.find((p) => p.id === providerId);
  return (
    <Dialog
      title={connection ? "Edit connection" : "Connect provider"}
      onClose={onClose}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSave({
            providerId,
            name: name.trim(),
            baseUrl: baseUrl.trim(),
            authKind,
            secret,
          });
        }}
      >
        {!connection && (
          <>
            <Field label="Find a provider">
              <input
                type="search"
                autoFocus
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
            </Field>
            <Field label="Provider">
              <select
                required
                value={providerId}
                onChange={(e) => {
                  const p = providers.find((p) => p.id === e.target.value);
                  setProvider(e.target.value);
                  setAuth(p?.authKinds[0] ?? ProviderAuthKind.UNSPECIFIED);
                }}
              >
                <option value="">Choose a supported provider</option>
                {providers
                  .filter(
                    (p) =>
                      p.id === providerId ||
                      `${p.displayName} ${p.id}`
                        .toLowerCase()
                        .includes(search.toLowerCase()),
                  )
                  .map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.displayName}
                    </option>
                  ))}
              </select>
            </Field>
            <Field label="Authentication">
              <select
                required
                value={authKind}
                onChange={(e) => setAuth(Number(e.target.value))}
              >
                {!provider && (
                  <option value={0}>Choose a provider first</option>
                )}
                {provider?.authKinds.map((k) => (
                  <option key={k} value={k}>
                    {authLabel[k]}
                  </option>
                ))}
              </select>
            </Field>
          </>
        )}
        <Field label="Account label">
          <input
            required
            autoFocus={!!connection}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        {authKind !== ProviderAuthKind.OAUTH && (
          <Field
            label="Base URL (optional)"
            hint="Leave empty to use the provider default. Private or local destinations require operator approval."
          >
            <input
              type="url"
              value={baseUrl}
              onChange={(e) => setBase(e.target.value)}
            />
          </Field>
        )}
        {authKind === ProviderAuthKind.API_KEY && (
          <>
            <Field
              label={connection ? "Replacement API key (optional)" : "API key"}
              hint="Stored secrets are never shown again."
            >
              <input
                type={reveal ? "text" : "password"}
                autoComplete="new-password"
                required={!connection}
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
              />
            </Field>
            <Button aria-pressed={reveal} onClick={() => setReveal((v) => !v)}>
              {reveal ? "Hide key" : "Reveal key"}
            </Button>
          </>
        )}
        <ErrorNotice error={error} />
        <div className="actions">
          <Button onClick={onClose}>Cancel</Button>
          <Button
            type="submit"
            variant="primary"
            disabled={pending || !providerId || !name.trim() || !authKind}
          >
            {pending
              ? "Saving…"
              : authKind === ProviderAuthKind.OAUTH && !connection
                ? "Continue with OAuth"
                : "Save connection"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
function OAuthDialog({
  organizationId,
  authorization,
  onClose,
}: {
  organizationId: string;
  authorization: ProviderOAuth;
  onClose: () => void;
}) {
  const api = useApi();
  const refresh = useRefresh();
  const action = useAction();
  const [code, setCode] = useState("");
  const [state, setState] = useState("");
  const result = useQuery(
    ProviderService.method.getProviderOAuth,
    { organizationId, authorizationId: authorization.id },
    {
      refetchInterval: (q) =>
        q.state.data?.authorization?.state === OAuthState.PENDING
          ? Math.max(5, authorization.pollIntervalSeconds ?? 5) * 1000
          : false,
    },
  );
  const auth = result.data?.authorization ?? authorization;
  const pending = auth.state === OAuthState.PENDING;
  let url: URL | undefined;
  try {
    const parsed = new URL(auth.authorizationUrl);
    if (
      parsed.protocol === "https:" ||
      (parsed.protocol === "http:" &&
        ["localhost", "127.0.0.1"].includes(parsed.hostname))
    )
      url = parsed;
  } catch {
    /* Invalid authorization destinations are not rendered as links. */
  }

  return (
    <Dialog title="Provider authorization" onClose={onClose}>
      <Badge>{OAuthState[auth.state]?.toLowerCase() ?? "Unknown status"}</Badge>
      <p>
        Expires {formatDate(auth.expiresAt)}. Closing this dialog does not
        cancel authorization.
      </p>
      {pending && (
        <>
          {url && (
            <a
              className="button primary"
              href={url.href}
              target="_blank"
              rel="noopener noreferrer"
            >
              Open provider sign-in
            </a>
          )}
          {auth.userCode && (
            <p>
              Enter this code at the provider: <strong>{auth.userCode}</strong>
            </p>
          )}
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void action.run(
                () =>
                  api.provider.completeProviderOAuth({
                    organizationId,
                    authorizationId: auth.id,
                    code,
                    state,
                  }),
                onClose,
                "Provider connected.",
              );
            }}
          >
            <p>
              If the provider returns an authorization code and state, enter
              them here.
            </p>
            <Field label="Authorization code">
              <input
                value={code}
                autoComplete="off"
                onChange={(e) => setCode(e.target.value)}
              />
            </Field>
            <Field label="Authorization state">
              <input
                value={state}
                autoComplete="off"
                onChange={(e) => setState(e.target.value)}
              />
            </Field>
            <Button
              type="submit"
              variant="primary"
              disabled={action.pending || !code || !state}
            >
              Complete authorization
            </Button>
          </form>
          <Button
            disabled={action.pending}
            onClick={() => {
              void action.run(
                () =>
                  api.provider.cancelProviderOAuth({
                    organizationId,
                    authorizationId: auth.id,
                  }),
                onClose,
                "Authorization canceled.",
              );
            }}
          >
            Cancel authorization
          </Button>
        </>
      )}
      {action.feedback}
      <ErrorNotice error={result.error} retry={() => void result.refetch()} />
      {!pending && (
        <Button
          onClick={() => {
            void refresh();
            onClose();
          }}
        >
          Done
        </Button>
      )}
    </Dialog>
  );
}

function ModelsDialog({
  organizationId,
  connection,
  onClose,
}: {
  organizationId: string;
  connection: ProviderConnection;
  onClose: () => void;
}) {
  const [search, setSearch] = useState("");
  const query = useQuery(ProviderService.method.listProviderModels, {
    organizationId,
    connectionId: connection.id,
  });
  const models = query.data?.models.filter((m) =>
    `${m.id} ${m.displayName}`.toLowerCase().includes(search.toLowerCase()),
  );
  return (
    <Dialog title={`Models from ${connection.name}`} onClose={onClose}>
      <Field label="Find a model">
        <input
          autoFocus
          type="search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </Field>
      <ErrorNotice error={query.error} />
      {query.isPending ? (
        <Loading />
      ) : models?.length === 0 ? (
        <p>No matching models.</p>
      ) : (
        <ul>
          {models?.map((m) => (
            <li key={m.id}>
              <strong>{m.displayName || m.id}</strong>
              <br />
              <code>{m.id}</code>
              <p>
                Context: {m.contextLimit?.toString() ?? "Unknown"} · Output:{" "}
                {m.outputLimit?.toString() ?? "Unknown"}
              </p>
            </li>
          ))}
        </ul>
      )}
      <p>Use a model ID when configuring a route for this connection.</p>
      <Button onClick={onClose}>Done</Button>
    </Dialog>
  );
}
