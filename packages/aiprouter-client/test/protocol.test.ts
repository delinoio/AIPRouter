// SPDX-License-Identifier: Apache-2.0
import assert from "node:assert/strict";
import test from "node:test";
import { create, fromBinary, toBinary, toJson } from "@bufbuild/protobuf";
import { createClient, type Transport } from "@connectrpc/connect";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import {
  ClientService,
  DeviceAuthorizationService,
  OrganizationService,
  PriceRatesSchema,
  TokenUsageSchema,
} from "../src/index.js";

test("unknown usage and price remain absent across binary and JSON", () => {
  const unknown = fromBinary(
    TokenUsageSchema,
    toBinary(TokenUsageSchema, create(TokenUsageSchema)),
  );
  assert.equal(unknown.inputTokens, undefined);
  assert.deepEqual(toJson(TokenUsageSchema, unknown), {});
  const zero = fromBinary(
    TokenUsageSchema,
    toBinary(TokenUsageSchema, create(TokenUsageSchema, { inputTokens: 0n })),
  );
  assert.equal(zero.inputTokens, 0n);
  assert.deepEqual(toJson(TokenUsageSchema, zero), { inputTokens: "0" });
  assert.equal(create(PriceRatesSchema).input, undefined);
  assert.equal(create(PriceRatesSchema, { input: "0" }).input, "0");
});

test("generated descriptors are directly usable by Connect and Connect Query", async () => {
  const transport: Transport = {
    async unary(method, _signal, _timeout, _headers, input) {
      assert.equal(method.name, "BeginDeviceAuthorization");
      assert.equal((input as { deviceName: string }).deviceName, "test-device");
      return {
        stream: false,
        service: method.parent,
        method,
        header: new Headers(),
        trailer: new Headers(),
        message: create(method.output),
      };
    },
    async stream() {
      throw new Error("Unexpected stream");
    },
  };
  const client = createClient(DeviceAuthorizationService, transport);
  await client.beginDeviceAuthorization({ deviceName: "test-device" });
  const key = createConnectQueryKey({
    schema: OrganizationService.method.listOrganizations,
    input: {},
    cardinality: "finite",
  });
  assert.ok(key.length > 0);
  assert.equal(
    ClientService.method.revokeCLIKey.input.typeName,
    "aiprouter.v1.RevokeCLIKeyRequest",
  );
});

test("recipient invitation descriptors preserve selectors and lifecycle", async () => {
  const {
    InvitationService,
    AcceptInvitationRequestSchema,
    GetInvitationResponseSchema,
    LifecycleState,
    Role,
    ErrorReason,
  } = await import("../src/index.js");
  assert.equal(
    InvitationService.method.getInvitation.input.typeName,
    "aiprouter.v1.GetInvitationRequest",
  );
  assert.deepEqual(
    InvitationService.method.listMyInvitations.input.fields.map(
      (field) => field.name,
    ),
    ["page"],
  );
  const request = create(AcceptInvitationRequestSchema, {
    invitationId: "invitation",
  });
  assert.equal(request.token, "");
  assert.equal(
    fromBinary(
      AcceptInvitationRequestSchema,
      toBinary(AcceptInvitationRequestSchema, request),
    ).invitationId,
    "invitation",
  );
  for (const state of [
    LifecycleState.PENDING,
    LifecycleState.ACCEPTED,
    LifecycleState.CANCELED,
    LifecycleState.EXPIRED,
    LifecycleState.SUPERSEDED,
    LifecycleState.FAILED,
  ]) {
    const response = create(GetInvitationResponseSchema, {
      invitation: {
        id: "invitation",
        organizationName: "Organization",
        email: "recipient@example.test",
        role: Role.MEMBER,
        state,
      },
    });
    assert.deepEqual(
      fromBinary(
        GetInvitationResponseSchema,
        toBinary(GetInvitationResponseSchema, response),
      ),
      response,
    );
  }
  assert.ok(ErrorReason.INVITATION_EMAIL_MISMATCH);
});

test("usage and audit filters preserve explicit unknown independently of omission", async () => {
  const {
    ListUsageRequestSchema,
    GetUsageSummaryRequestSchema,
    ListDailyUsageRequestSchema,
    ListOrganizationAuditRequestSchema,
    ListInstanceAuditRequestSchema,
  } = await import("../src/index.js");
  for (const schema of [
    ListUsageRequestSchema,
    GetUsageSummaryRequestSchema,
    ListDailyUsageRequestSchema,
    ListOrganizationAuditRequestSchema,
    ListInstanceAuditRequestSchema,
  ]) {
    const unknown = create(schema, { outcome: 0 });
    assert.equal(fromBinary(schema, toBinary(schema, unknown)).outcome, 0);
    assert.equal(
      fromBinary(schema, toBinary(schema, create(schema))).outcome,
      undefined,
    );
  }
});

test("OAuth native callback and collection CAS descriptors are available", async () => {
  const {
    BeginProviderOAuthRequestSchema,
    ProviderOAuthMethod,
    ProviderOAuthSchema,
    ListPriceOverridesResponseSchema,
    SetPriceOverrideRequestSchema,
    DeletePriceOverrideResponseSchema,
  } = await import("../src/index.js");
  const input = create(BeginProviderOAuthRequestSchema, {
    method: ProviderOAuthMethod.NATIVE_CALLBACK,
    redirectUri: "aiprouter://callback",
    clientId: "client",
  });
  assert.deepEqual(
    fromBinary(
      BeginProviderOAuthRequestSchema,
      toBinary(BeginProviderOAuthRequestSchema, input),
    ),
    input,
  );
  assert.equal(
    create(ProviderOAuthSchema, { callbackState: "correlation" }).callbackState,
    "correlation",
  );
  assert.equal(
    create(ListPriceOverridesResponseSchema, { revision: "1" }).revision,
    "1",
  );
  assert.equal(
    create(SetPriceOverrideRequestSchema, { expectedRevision: "1" })
      .expectedRevision,
    "1",
  );
  assert.equal(
    create(DeletePriceOverrideResponseSchema, { revision: "2" }).revision,
    "2",
  );
});

test("provider catalog carries typed eligible OAuth methods without client inventory", async () => {
  const { ProviderDefinitionSchema, ProviderOAuthMethod } =
    await import("../src/index.js");
  const provider = create(ProviderDefinitionSchema, {
    id: "native-provider",
    oauthMethods: [ProviderOAuthMethod.NATIVE_CALLBACK],
  });
  assert.deepEqual(
    fromBinary(
      ProviderDefinitionSchema,
      toBinary(ProviderDefinitionSchema, provider),
    ).oauthMethods,
    [ProviderOAuthMethod.NATIVE_CALLBACK],
  );
  assert.deepEqual(create(ProviderDefinitionSchema).oauthMethods, []);
});

test("pool generic OAuth affinity requires explicit opt-in", async () => {
  const { PoolSchema, CreatePoolRequestSchema, UpdatePoolRequestSchema } =
    await import("../src/index.js");
  for (const schema of [
    PoolSchema,
    CreatePoolRequestSchema,
    UpdatePoolRequestSchema,
  ]) {
    assert.equal(create(schema).affinityEnabled, false);
    const enabled = create(schema, { affinityEnabled: true });
    assert.equal(
      fromBinary(schema, toBinary(schema, enabled)).affinityEnabled,
      true,
    );
  }
});

test("hosted helper settings preserve personal selectors and optional defaults", async () => {
  const { HostedSettingsSchema, HostedVisionBackend, HostedSettingsService } =
    await import("../src/index.js");
  const settings = create(HostedSettingsSchema, {
    search: {
      enabled: false,
      target: {
        selection: { case: "connectionId", value: "search-connection" },
      },
    },
    vision: {
      backend: HostedVisionBackend.ROUTED,
      selection: { case: "routeModel", value: "vision-alias" },
      maxDescriptionsPerTurn: 0,
    },
    images: {
      target: { selection: { case: "poolId", value: "media-pool" } },
      videoBridgeEnabled: true,
    },
  });
  const decoded = fromBinary(
    HostedSettingsSchema,
    toBinary(HostedSettingsSchema, settings),
  );
  assert.deepEqual(decoded, settings);
  assert.equal(decoded.search?.enabled, false);
  assert.equal(decoded.vision?.enabled, undefined);
  assert.equal(decoded.images?.bridgeEnabled, false);
  assert.equal(
    HostedSettingsService.method.getHostedSettings.input.typeName,
    "aiprouter.v1.GetHostedSettingsRequest",
  );
});

test("provider config preserves explicit false, replacement, and write-only header clearing", async () => {
  const {
    ProviderConfigurationSchema,
    ProviderPolicyOverridesSchema,
    ProviderConnectionSchema,
    ProviderWire,
    UpdateProviderConnectionRequestSchema,
  } = await import("../src/index.js");
  const policy = create(ProviderPolicyOverridesSchema, {
    dropResponsesReasoningItems: false,
  });
  const configuration = create(ProviderConfigurationSchema, {
    wire: ProviderWire.RESPONSES,
    modelWires: { model: ProviderWire.ANTHROPIC },
    responsesPath: "/custom/responses",
    minMaxOutputTokens: 1024n,
    policy,
  });
  assert.deepEqual(
    fromBinary(
      ProviderConfigurationSchema,
      toBinary(ProviderConfigurationSchema, configuration),
    ),
    configuration,
  );
  assert.equal(configuration.policy?.foldDeveloperRoleToSystem, undefined);
  assert.equal(configuration.policy?.dropResponsesReasoningItems, false);
  const absent = create(UpdateProviderConnectionRequestSchema);
  const clear = create(UpdateProviderConnectionRequestSchema, {
    configuration: {},
    customHeaders: {},
  });
  assert.equal(absent.configuration, undefined);
  assert.equal(absent.customHeaders, undefined);
  const decoded = fromBinary(
    UpdateProviderConnectionRequestSchema,
    toBinary(UpdateProviderConnectionRequestSchema, clear),
  );
  assert.ok(decoded.configuration);
  assert.deepEqual(decoded.customHeaders?.values, {});
  assert.equal(
    ProviderConnectionSchema.fields.some(
      (field) => field.name === "custom_headers",
    ),
    false,
  );
});

test("JEV configuration preserves absence, defaults, and target hints", async () => {
  const { UpdateRouteRequestSchema, RoutingStrategy, ProviderWire } =
    await import("../src/index.js");
  assert.equal(create(UpdateRouteRequestSchema).jevDecision, undefined);
  const route = create(UpdateRouteRequestSchema, {
    strategy: RoutingStrategy.JEV,
    jevDecision: { decisionConnectionId: "private", decisionModel: "alias" },
    targets: [
      {
        modelId: "candidate",
        reasoningEfforts: ["ultra"],
        modelProfile: "profile",
      },
    ],
  });
  const decoded = fromBinary(
    UpdateRouteRequestSchema,
    toBinary(UpdateRouteRequestSchema, route),
  );
  assert.deepEqual(decoded, route);
  assert.equal(decoded.jevDecision?.decisionTimeoutMs, 0);
  assert.equal(decoded.jevDecision?.sessionPinTtlSeconds, 0);
  assert.equal(ProviderWire.JEV_DECISION, 5);
});
