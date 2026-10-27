// SPDX-License-Identifier: Apache-2.0
import { useCallback, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import {
  AccountService,
  OrganizationService,
  type GetSessionResponse,
} from "@aiprouter/client";
import { DirtyContext, useDirtyForm, type PageProps } from "../api";
import { PageHeader } from "../ui";
import {
  AdminAuthority,
  ErrorNotice,
  Loading,
  isAdmin,
  permissionMessage,
} from "./common";
import { Invitations, Members, Settings } from "./organization";
import { Audit, OrganizationUsage } from "./reporting";
import { Health, InstanceConfiguration, Prices } from "./operator";
import "./admin.css";

export function AdminPage(props: PageProps & { section: string }) {
  return (
    <OrganizationBoundary
      key={`${props.session.userId}:${props.organization.id}`}
      {...props}
    />
  );
}
function OrganizationBoundary({
  organization,
  section,
}: PageProps & { section: string }) {
  const [denied, setDenied] = useState(false);
  const query = useQuery(
    OrganizationService.method.getOrganization,
    { organizationId: organization.id },
    {
      staleTime: 0,
      refetchOnMount: "always",
      refetchInterval: 15000,
      retry: false,
    },
  );
  const current = query.data?.organization;
  if (query.isPending || !query.isFetchedAfterMount) return <Loading />;
  if (query.error)
    return (
      <ErrorNotice error={query.error} retry={() => void query.refetch()} />
    );
  if (denied || !current || !isAdmin(current.role))
    return (
      <div className="admin-workspace">
        <p role="alert">{permissionMessage}</p>
        <a href={`/?organization_id=${encodeURIComponent(organization.id)}`}>
          Back to your workspace
        </a>
      </div>
    );
  return (
    <AdminAuthority.Provider
      value={{
        organizationId: current.id,
        role: current.role,
        onDenied: () => setDenied(true),
      }}
    >
      <div className="admin-workspace" key={`${section}:${current.role}`}>
        <p className="admin-scope">Organization: {current.name}</p>
        {section === "members" ? (
          <Members />
        ) : section === "invitations" ? (
          <Invitations />
        ) : section === "organization-usage" ? (
          <OrganizationUsage />
        ) : section === "audit" ? (
          <Audit />
        ) : section === "settings" ? (
          <Settings organization={current} />
        ) : (
          <PageHeader title="Organization page unavailable" />
        )}
      </div>
    </AdminAuthority.Provider>
  );
}
const operatorSections = [
  ["health", "Health"],
  ["limits", "Request limits"],
  ["retention", "Retention"],
  ["destinations", "Destinations"],
  ["prices", "Price overrides"],
  ["audit", "Audit"],
] as const;
type OperatorSection = (typeof operatorSections)[number][0];
export function OperatorPage({ session }: { session: GetSessionResponse }) {
  return (
    <div className="admin-operator-frame">
      <OperatorBoundary key={session.userId} />
    </div>
  );
}
function OperatorBoundary() {
  const [section, setSection] = useState<OperatorSection>("health");
  const [denied, setDenied] = useState(false);
  const [dirty, setDirty] = useState(false);
  const reportDirty = useDirtyForm();
  const onDirty = useCallback(
    (value: boolean) => {
      setDirty(value);
      reportDirty(value);
    },
    [reportDirty],
  );
  const session = useQuery(
    AccountService.method.getSession,
    {},
    {
      staleTime: 0,
      refetchOnMount: "always",
      refetchInterval: 15000,
      retry: false,
    },
  );
  if (session.isPending || !session.isFetchedAfterMount) return <Loading />;
  if (session.error)
    return (
      <ErrorNotice error={session.error} retry={() => void session.refetch()} />
    );
  if (denied || !session.data?.isInstanceOperator)
    return (
      <div className="admin-workspace">
        <p role="alert">{permissionMessage}</p>
        <a href="/">Back to organizations</a>
      </div>
    );
  return (
    <AdminAuthority.Provider
      value={{ organizationId: "", role: 0, onDenied: () => setDenied(true) }}
    >
      <div className="admin-workspace">
        <header className="admin-instance-heading">
          <strong>Instance console</strong>
          <a href="/">Back to organizations</a>
        </header>
        <p className="admin-banner">
          Whole-instance health and aggregates only.
        </p>
        <nav className="admin-tabs" aria-label="Instance console">
          {operatorSections.map(([key, title]) => (
            <button
              key={key}
              aria-current={section === key ? "page" : undefined}
              onClick={() => {
                if (
                  section !== key &&
                  dirty &&
                  !window.confirm(
                    "Leave this page? Unsaved changes will be lost.",
                  )
                )
                  return;
                setSection(key);
              }}
            >
              {title}
            </button>
          ))}
        </nav>
        <DirtyContext.Provider value={onDirty}>
          <div key={section}>
            {section === "health" ? (
              <Health />
            ) : section === "audit" ? (
              <Audit operator />
            ) : section === "prices" ? (
              <Prices />
            ) : (
              <InstanceConfiguration section={section} />
            )}
          </div>
        </DirtyContext.Provider>
      </div>
    </AdminAuthority.Provider>
  );
}
