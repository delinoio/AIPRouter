// SPDX-License-Identifier: Apache-2.0
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { it, expect, vi } from "vitest";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { QueryClient } from "@tanstack/react-query";
import {
  PoolService,
  ProviderService,
  RoutingService,
  PoolSchema,
  PoolStrategy,
  ProviderAuthKind,
  ConnectionState,
  OrganizationSchema,
  GetSessionResponseSchema,
  NativePoolStrategy,
  NativeQuotaWindow,
  type Pool,
} from "@aiprouter/client";
import { ApiProvider } from "../src/api";
import { PoolsPage } from "../src/routing";
function mount(
  pool: Pool | undefined,
  provider: string,
  mutation: (request: unknown) => object,
) {
  const transport = createRouterTransport((r) => {
    r.service(PoolService, {
      listPools: () => ({ pools: pool ? [pool] : [] }),
      createPool: mutation,
      updatePool: mutation,
    });
    r.service(ProviderService, {
      listProviderConnections: () => ({
        connections: [
          {
            id: "one",
            name: "First",
            providerId: provider,
            authKind: ProviderAuthKind.OAUTH,
            state: ConnectionState.READY,
          },
          {
            id: "two",
            name: "Second",
            providerId: provider,
            authKind: ProviderAuthKind.OAUTH,
            state: ConnectionState.READY,
          },
          {
            id: "other",
            name: "Other provider",
            providerId: provider === "openai" ? "anthropic" : "openai",
            authKind: ProviderAuthKind.OAUTH,
            state: ConnectionState.READY,
          },
        ],
      }),
    });
    r.service(RoutingService, { listRoutes: () => ({}) });
  });
  render(
    <ApiProvider
      rpcTransport={transport}
      queryClient={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <PoolsPage
        organization={create(OrganizationSchema, { id: "org", name: "Studio" })}
        session={create(GetSessionResponseSchema, { userId: "person" })}
      />
    </ApiProvider>,
  );
}
async function start(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: "Create pool" }));
  await user.type(screen.getByLabelText("Pool name"), "Native pool");
  await user.selectOptions(
    await screen.findByLabelText("Add a connection"),
    "one",
  );
  await user.click(screen.getByText("Native OAuth pool settings"));
  await user.click(
    screen.getByLabelText("Configure pool-level native settings"),
  );
}
it.each(["", "false"])(
  "creates Codex options preserving enabled %s and member zero/false values",
  async (enabled) => {
    const user = userEvent.setup(),
      mutation = vi.fn((_r: unknown) => ({}));
    mount(undefined, "openai", mutation);
    await start(user);
    await user.selectOptions(screen.getByLabelText("Native pooling"), enabled);
    await user.selectOptions(
      screen.getByLabelText("Native selection strategy"),
      String(NativePoolStrategy.RESET_FIRST),
    );
    await user.type(screen.getByLabelText("Pool switching threshold (%)"), "0");
    await user.selectOptions(screen.getByLabelText("Cache affinity"), "false");
    await user.type(screen.getByLabelText("Excluded plans"), "Free\nTrial");
    await user.click(
      screen.getByLabelText("Override native options for First"),
    );
    await user.type(
      screen.getByLabelText("Switching threshold for First (%)"),
      "0",
    );
    await user.type(screen.getByLabelText("Native priority for First"), "-100");
    await user.click(screen.getByLabelText("Use remaining quota for First"));
    await user.click(screen.getByLabelText("Allow credits for First"));
    await user.click(screen.getByRole("button", { name: "Save pool" }));
    const request = mutation.mock.calls[0]?.[0] as {
      nativeOptions: { enabled?: boolean; stickyLimit?: number };
    };
    expect(request).toMatchObject({
      organizationId: "org",
      nativeOptions: {
        strategy: NativePoolStrategy.RESET_FIRST,
        autoSwitchThreshold: 0,
        cacheAffinity: false,
        excludedPlans: ["Free", "Trial"],
      },
      members: [
        {
          nativeOptions: {
            autoSwitchThreshold: 0,
            priority: -100,
            useRemainingQuota: true,
            allowCredits: true,
          },
        },
      ],
    });
    expect(request.nativeOptions.enabled).toBe(
      enabled === "" ? undefined : false,
    );
    expect(request.nativeOptions.stickyLimit).toBeUndefined();
  },
);
const saved = create(PoolSchema, {
  id: "pool",
  name: "Saved native",
  strategy: PoolStrategy.PRIORITY,
  affinityEnabled: true,
  members: [
    {
      connectionId: "one",
      priority: 0,
      weight: 1,
      nativeOptions: {
        autoSwitchThreshold: 0,
        priority: 100,
        useRemainingQuota: false,
      },
    },
    { connectionId: "two", priority: 1, weight: 1 },
  ],
  nativeOptions: {
    quotaWindow: NativeQuotaWindow.MAX_UTILIZATION,
    nativeMessages: false,
    stickyLimit: 2,
    routes: [
      {
        name: "Claude",
        match: "claude-*",
        connectionIds: ["two", "one"],
        fallback: false,
      },
    ],
  },
});
it("preserves imported absent enablement, false native messages and rule/member order on edit", async () => {
  const mutation = vi.fn((_r: unknown) => ({}));
  mount(saved, "anthropic2", mutation);
  await userEvent.click(
    await screen.findByRole("button", { name: "Edit pool" }),
  );
  await userEvent.click(screen.getByRole("button", { name: "Save pool" }));
  const request = mutation.mock.calls[0]?.[0] as {
    nativeOptions: { enabled?: boolean; cacheAffinity?: boolean };
    members: { nativeOptions?: unknown }[];
  };
  expect(request).toMatchObject({
    poolId: "pool",
    affinityEnabled: true,
    nativeOptions: saved.nativeOptions,
    members: [
      { nativeOptions: saved.members[0].nativeOptions },
      { connectionId: "two" },
    ],
  });
  expect(request.nativeOptions.enabled).toBeUndefined();
  expect(request.nativeOptions.cacheAffinity).toBeUndefined();
  expect(request.members[1].nativeOptions).toBeUndefined();
});
it("clears native pool and member options without changing generic affinity", async () => {
  const user = userEvent.setup(),
    mutation = vi.fn((_r: unknown) => ({}));
  mount(saved, "anthropic", mutation);
  await user.click(await screen.findByRole("button", { name: "Edit pool" }));
  await user.click(screen.getByText("Native OAuth pool settings"));
  await user.click(
    screen.getByRole("button", { name: "Clear all native settings" }),
  );
  await user.click(screen.getByRole("button", { name: "Save pool" }));
  const request = mutation.mock.calls[0]?.[0] as {
    nativeOptions?: unknown;
    affinityEnabled: boolean;
    members: { nativeOptions?: unknown }[];
  };
  expect(request.nativeOptions).toBeUndefined();
  expect(request.members.every((m) => m.nativeOptions === undefined)).toBe(
    true,
  );
  expect(request.affinityEnabled).toBe(true);
});
it("creates ordered Anthropic model rules and rejects duplicate patterns", async () => {
  const user = userEvent.setup(),
    mutation = vi.fn((_r: unknown) => ({}));
  mount(undefined, "anthropic", mutation);
  await start(user);
  await user.selectOptions(
    screen.getByLabelText("Native quota window"),
    String(NativeQuotaWindow.WEEKLY),
  );
  await user.selectOptions(screen.getByLabelText("Native messages"), "false");
  await user.click(screen.getByRole("button", { name: "Add model rule" }));
  await user.type(screen.getByLabelText("Rule name 1"), "Coding");
  await user.type(screen.getByLabelText("Model pattern 1"), "claude-*");
  await user.selectOptions(
    screen.getByLabelText("Add rule connection 1"),
    "one",
  );
  await user.click(screen.getByRole("button", { name: "Add model rule" }));
  await user.type(screen.getByLabelText("Rule name 2"), "Duplicate");
  await user.type(screen.getByLabelText("Model pattern 2"), "claude-*");
  await user.selectOptions(
    screen.getByLabelText("Add rule connection 2"),
    "one",
  );
  expect(
    screen.getByRole("button", { name: "Save pool" }).hasAttribute("disabled"),
  ).toBe(true);
  await user.click(screen.getByRole("button", { name: "Remove model rule 2" }));
  await user.click(screen.getByRole("button", { name: "Save pool" }));
  expect(mutation.mock.calls[0]?.[0]).toMatchObject({
    nativeOptions: {
      nativeMessages: false,
      quotaWindow: NativeQuotaWindow.WEEKLY,
      routes: [
        {
          name: "Coding",
          match: "claude-*",
          connectionIds: ["one"],
          fallback: false,
        },
      ],
    },
  });
});
it("blocks mixed-provider native settings until explicitly cleared", async () => {
  const user = userEvent.setup(),
    mutation = vi.fn((_r: unknown) => ({}));
  mount(undefined, "openai", mutation);
  await start(user);
  await user.selectOptions(screen.getByLabelText("Add a connection"), "other");
  expect(
    screen.getByRole("button", { name: "Save pool" }).hasAttribute("disabled"),
  ).toBe(true);
  expect(mutation).not.toHaveBeenCalled();
  await user.click(
    screen.getByRole("button", { name: "Clear all native settings" }),
  );
  expect(
    screen.getByRole("button", { name: "Save pool" }).hasAttribute("disabled"),
  ).toBe(false);
});
