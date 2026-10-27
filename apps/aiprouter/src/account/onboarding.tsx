// SPDX-License-Identifier: Apache-2.0
import { useState } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import {
  InvitationService,
  OrganizationService,
  type Organization,
} from "@aiprouter/client";
import { AccountError, AccountSurface } from "./shared";

export function OnboardingPage({
  onOrganizationCreated,
}: {
  onOrganizationCreated?: (organization: Organization) => void;
}) {
  const [name, setName] = useState("");
  const [created, setCreated] = useState<Organization>();
  const cache = useQueryClient();
  const invitations = useQuery(
    InvitationService.method.listMyInvitations,
    { page: { pageSize: 100 } },
    { retry: false },
  );
  const create = useMutation(OrganizationService.method.createOrganization, {
    retry: false,
  });
  if (created)
    return (
      <AccountSurface title="Organization created" subtitle={created.name}>
        <p role="status">
          Your organization is ready. Connect your personal providers next.
        </p>
        <a href={`/?organization=${encodeURIComponent(created.id)}`}>
          Open organization
        </a>
      </AccountSurface>
    );
  return (
    <AccountSurface
      title="Choose your organization"
      subtitle="Create an organization or accept an invitation to get started."
    >
      <div className="account-card">
        <h2>Your invitations</h2>
        {invitations.isPending && <p role="status">Loading invitations…</p>}
        <AccountError
          error={invitations.error}
          retry={() => {
            void invitations.refetch();
          }}
        />
        {invitations.data?.invitations.map((invitation) => (
          <p key={invitation.id}>
            <strong>{invitation.organizationName}</strong>{" "}
            <a
              href={`/invitation?invitation_id=${encodeURIComponent(invitation.id)}`}
            >
              Review invitation
            </a>
          </p>
        ))}
        <p>
          Open the invitation link from your email to review and accept it. Sign
          in with the invited email address.
        </p>
        <a href="/invitation">Enter an invitation</a>
      </div>
      <form
        className="account-card"
        onSubmit={(event) => {
          event.preventDefault();
          if (!name.trim() || create.isPending) return;
          create.mutate(
            { name: name.trim() },
            {
              onSuccess: (response) => {
                if (!response.organization) return;
                setCreated(response.organization);
                void cache.invalidateQueries();
                onOrganizationCreated?.(response.organization);
              },
            },
          );
        }}
      >
        <h2>Create organization</h2>
        <label>
          Organization name
          <input
            autoFocus
            required
            maxLength={200}
            value={name}
            onChange={(event) => setName(event.target.value)}
            disabled={create.isPending}
          />
        </label>
        <AccountError error={create.error} />
        <button
          className="account-primary"
          type="submit"
          disabled={!name.trim() || create.isPending}
        >
          {create.isPending ? "Creating…" : "Create organization"}
        </button>
      </form>
      <ol className="account-hint">
        <li>Choose organization</li>
        <li>Connect personal providers</li>
        <li>Configure models and routing</li>
        <li>Create API key</li>
        <li>Connect a client</li>
      </ol>
    </AccountSurface>
  );
}
