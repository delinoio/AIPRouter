// SPDX-License-Identifier: Apache-2.0
import { create } from "@bufbuild/protobuf";
import {
  ProviderConfigurationSchema,
  ProviderPolicyOverridesSchema,
  ProviderCustomHeadersSchema,
  ProviderWire,
  type ProviderConnection,
  type ProviderConfiguration,
  type ProviderPolicyOverrides,
} from "@aiprouter/client";
import { Button, Field, Notice } from "./ui";
export enum ConfigurationMode {
  Preserve = "preserve",
  Replace = "replace",
  Reset = "reset",
}
export enum HeaderMode {
  Preserve = "preserve",
  Replace = "replace",
  Clear = "clear",
}
export type ConfigurationDraft = {
  mode: ConfigurationMode;
  configuration: ProviderConfiguration;
  models: { id: string; wire: ProviderWire }[];
  floor: string;
  headerMode: HeaderMode;
  headers: { name: string; value: string }[];
};
export function configurationDraft(
  connection?: ProviderConnection,
): ConfigurationDraft {
  const configuration = create(
    ProviderConfigurationSchema,
    connection?.configuration,
  );
  return {
    mode: ConfigurationMode.Preserve,
    configuration,
    models: Object.entries(configuration.modelWires).map(([id, wire]) => ({
      id,
      wire,
    })),
    floor: configuration.minMaxOutputTokens?.toString() ?? "",
    headerMode: HeaderMode.Preserve,
    headers: [],
  };
}
export function configurationValues(draft: ConfigurationDraft) {
  const { mode, headerMode } = draft;
  const modelWires: Record<string, ProviderWire> = Object.create(null);
  if (mode === ConfigurationMode.Replace)
    for (const row of draft.models) {
      const id = row.id.trim();
      if (!id || Object.hasOwn(modelWires, id))
        throw new Error("Model IDs must be nonempty and unique.");
      if (row.wire !== ProviderWire.CHAT && row.wire !== ProviderWire.RESPONSES)
        throw new Error(
          "Model overrides support only Chat completions or Responses.",
        );
      modelWires[id] = row.wire;
    }
  const values: Record<string, string> = Object.create(null);
  const names = new Set<string>();
  if (headerMode === HeaderMode.Replace)
    for (const row of draft.headers) {
      const name = row.name.trim();
      if (
        !/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(name) ||
        names.has(name.toLowerCase()) ||
        /[\r\n]/.test(row.value)
      )
        throw new Error(
          "Header names must be valid and unique; values cannot contain line breaks.",
        );
      names.add(name.toLowerCase());
      values[name] = row.value;
    }
  const floor = draft.floor.trim();
  if (
    mode === ConfigurationMode.Replace &&
    floor &&
    (!/^\d+$/.test(floor) || BigInt(floor) < 1n || BigInt(floor) > 2147483647n)
  )
    throw new Error(
      "The Azure output-token floor must be between 1 and 2147483647.",
    );
  return {
    configuration:
      mode === ConfigurationMode.Replace
        ? create(ProviderConfigurationSchema, {
            ...draft.configuration,
            modelWires,
            minMaxOutputTokens: floor ? BigInt(floor) : undefined,
          })
        : undefined,
    resetConfiguration: mode === ConfigurationMode.Reset,
    customHeaders:
      headerMode === HeaderMode.Preserve
        ? undefined
        : create(ProviderCustomHeadersSchema, { values }),
  };
}
const wires = [
  [ProviderWire.CHAT, "Chat completions"],
  [ProviderWire.RESPONSES, "Responses"],
  [ProviderWire.ANTHROPIC, "Anthropic"],
  [ProviderWire.GOOGLE, "Google"],
] as const;
const policies = [
  ["foldDeveloperRoleToSystem", "Fold developer role into system"],
  ["dropResponsesReasoningItems", "Drop Responses reasoning items"],
  ["preserveResponsesReasoningContent", "Preserve Responses reasoning content"],
  [
    "requiresAdjacentResponsesToolResults",
    "Require adjacent Responses tool results",
  ],
  [
    "requiresPairedResponsesToolResults",
    "Require paired Responses tool results",
  ],
  ["statelessResponses", "Stateless Responses"],
  ["annotateEmptyToolOutputs", "Annotate empty tool outputs"],
  ["parallelToolCalls", "Parallel tool calls"],
  ["promptCacheKey", "Prompt cache key"],
  ["chatServiceTier", "Chat service tier"],
  ["supportsServiceTier", "Service tier support"],
  ["supportsVerbosity", "Verbosity support"],
  ["supportsResponsesCustomTools", "Responses custom tools"],
  ["supportsOpenAiWebSearchToolFields", "OpenAI web search fields"],
  ["responsesItemIdRepair", "Responses item ID repair"],
  ["showThinkingSummary", "Show thinking summary"],
  ["escapeBuiltinToolNames", "Escape built-in tool names"],
] as const satisfies readonly (readonly [
  keyof ProviderPolicyOverrides,
  string,
])[];
export function ConfigurationFields({
  draft,
  onChange,
  providerId,
  connection,
}: {
  draft: ConfigurationDraft;
  onChange: (draft: ConfigurationDraft) => void;
  providerId: string;
  connection?: ProviderConnection;
}) {
  const config = draft.configuration;
  const update = (patch: Partial<ProviderConfiguration>) =>
    onChange({
      ...draft,
      mode: ConfigurationMode.Replace,
      configuration: create(ProviderConfigurationSchema, {
        ...config,
        ...patch,
      }),
    });
  return (
    <>
      <Field
        label="API format"
        hint="Provider default is Chat for custom connections. Specialized providers validate their supported formats."
      >
        <select
          value={config.wire ?? ""}
          disabled={draft.mode === ConfigurationMode.Reset}
          onChange={(e) =>
            update({
              wire: e.target.value === "" ? undefined : Number(e.target.value),
            })
          }
        >
          <option value="">Provider default</option>
          {wires.map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>
      </Field>
      <details>
        <summary>Advanced connection settings</summary>
        {connection && (
          <Field label="Configuration changes">
            <select
              value={draft.mode}
              onChange={(e) =>
                onChange({
                  ...draft,
                  mode: e.target.value as ConfigurationMode,
                })
              }
            >
              <option value={ConfigurationMode.Preserve}>
                Keep saved configuration
              </option>
              <option value={ConfigurationMode.Replace}>
                Replace configuration with these values
              </option>
              <option value={ConfigurationMode.Reset}>
                Reset configuration to provider defaults
              </option>
            </select>
          </Field>
        )}
        {draft.mode === ConfigurationMode.Reset ? (
          <Notice>
            Reset removes configuration overrides. Custom headers are kept
            unless you clear them below.
          </Notice>
        ) : (
          <>
            <p>
              Changing a setting replaces the complete configuration. Unchanged
              settings shown here are retained.
            </p>
            <Field
              label="Responses endpoint path"
              hint="Origin-relative path, for example /v1/responses. Empty uses the provider default."
            >
              <input
                value={config.responsesPath ?? ""}
                onChange={(e) =>
                  update({ responsesPath: e.target.value || undefined })
                }
              />
            </Field>
            <Field label="Chat completions endpoint path">
              <input
                value={config.chatCompletionsPath ?? ""}
                onChange={(e) =>
                  update({ chatCompletionsPath: e.target.value || undefined })
                }
              />
            </Field>
            {providerId === "azure" && (
              <Field
                label="Minimum output-token budget"
                hint="Optional Azure floor, subject to request limits."
              >
                <input
                  type="number"
                  min="1"
                  max="2147483647"
                  step="1"
                  value={draft.floor}
                  onChange={(e) =>
                    onChange({
                      ...draft,
                      mode: ConfigurationMode.Replace,
                      floor: e.target.value,
                    })
                  }
                />
              </Field>
            )}
            <h3>Model API formats</h3>
            <p>Overrides apply to exact model IDs.</p>
            {draft.models.map((row, index) => (
              <fieldset key={index}>
                <legend>Model override {index + 1}</legend>
                <Field label={`Model ID ${index + 1}`}>
                  <input
                    required
                    value={row.id}
                    onChange={(e) =>
                      onChange({
                        ...draft,
                        mode: ConfigurationMode.Replace,
                        models: draft.models.map((r, i) =>
                          i === index ? { ...r, id: e.target.value } : r,
                        ),
                      })
                    }
                  />
                </Field>
                <Field label={`Model format ${index + 1}`}>
                  <select
                    value={row.wire}
                    onChange={(e) =>
                      onChange({
                        ...draft,
                        mode: ConfigurationMode.Replace,
                        models: draft.models.map((r, i) =>
                          i === index
                            ? { ...r, wire: Number(e.target.value) }
                            : r,
                        ),
                      })
                    }
                  >
                    {wires
                      .filter(
                        ([wire]) =>
                          wire === ProviderWire.CHAT ||
                          wire === ProviderWire.RESPONSES,
                      )
                      .map(([value, label]) => (
                        <option key={value} value={value}>
                          {label}
                        </option>
                      ))}
                  </select>
                </Field>
                <Button
                  onClick={() =>
                    onChange({
                      ...draft,
                      mode: ConfigurationMode.Replace,
                      models: draft.models.filter((_, i) => i !== index),
                    })
                  }
                >
                  Remove model override {index + 1}
                </Button>
              </fieldset>
            ))}
            <Button
              onClick={() =>
                onChange({
                  ...draft,
                  mode: ConfigurationMode.Replace,
                  models: [
                    ...draft.models,
                    { id: "", wire: ProviderWire.CHAT },
                  ],
                })
              }
            >
              Add model override
            </Button>
            <details>
              <summary>Request compatibility policies</summary>
              <p>
                Inherit uses the provider default. Disabled is an explicit
                override.
              </p>
              {policies.map(([key, label]) => (
                <Field key={key} label={label}>
                  <select
                    value={
                      config.policy?.[key] === undefined
                        ? ""
                        : String(config.policy[key])
                    }
                    onChange={(e) =>
                      update({
                        policy: create(ProviderPolicyOverridesSchema, {
                          ...config.policy,
                          [key]:
                            e.target.value === ""
                              ? undefined
                              : e.target.value === "true",
                        }),
                      })
                    }
                  >
                    <option value="">Inherit</option>
                    <option value="true">Enabled</option>
                    <option value="false">Disabled</option>
                  </select>
                </Field>
              ))}
            </details>
          </>
        )}
        <h3>Custom headers</h3>
        {!!connection?.customHeaderNames.length && (
          <p>
            Saved names: {connection.customHeaderNames.join(", ")}. Values are
            never returned.
          </p>
        )}
        <Field label="Header changes">
          <select
            value={draft.headerMode}
            onChange={(e) =>
              onChange({ ...draft, headerMode: e.target.value as HeaderMode })
            }
          >
            <option value={HeaderMode.Preserve}>Keep saved headers</option>
            <option value={HeaderMode.Replace}>Replace all headers</option>
            <option value={HeaderMode.Clear}>Clear all headers</option>
          </select>
        </Field>
        {draft.headerMode === HeaderMode.Replace && (
          <>
            <p>
              Enter every header to retain. Authentication and transport headers
              are restricted.
            </p>
            {draft.headers.map((row, index) => (
              <fieldset key={index}>
                <legend>Header {index + 1}</legend>
                <Field label={`Header name ${index + 1}`}>
                  <input
                    required
                    autoComplete="off"
                    value={row.name}
                    onChange={(e) =>
                      onChange({
                        ...draft,
                        headers: draft.headers.map((r, i) =>
                          i === index ? { ...r, name: e.target.value } : r,
                        ),
                      })
                    }
                  />
                </Field>
                <Field label={`Header value ${index + 1}`}>
                  <input
                    type="password"
                    autoComplete="new-password"
                    value={row.value}
                    onChange={(e) =>
                      onChange({
                        ...draft,
                        headers: draft.headers.map((r, i) =>
                          i === index ? { ...r, value: e.target.value } : r,
                        ),
                      })
                    }
                  />
                </Field>
                <Button
                  onClick={() =>
                    onChange({
                      ...draft,
                      headers: draft.headers.filter((_, i) => i !== index),
                    })
                  }
                >
                  Remove header {index + 1}
                </Button>
              </fieldset>
            ))}
            <Button
              disabled={draft.headers.length >= 64}
              onClick={() =>
                onChange({
                  ...draft,
                  headers: [...draft.headers, { name: "", value: "" }],
                })
              }
            >
              Add header
            </Button>
          </>
        )}
      </details>
    </>
  );
}
