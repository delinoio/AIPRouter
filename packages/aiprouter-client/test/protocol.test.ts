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
