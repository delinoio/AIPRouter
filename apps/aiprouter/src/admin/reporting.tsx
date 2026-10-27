// SPDX-License-Identifier: Apache-2.0
import { useContext, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  AuditAction,
  AuditService,
  OperatorService,
  UsageService,
  RequestOutcome,
  PriceSource,
  type AuditRecord,
  type UsageSummary,
  type UsageRecord,
} from "@aiprouter/client";
import { Card, PageHeader } from "../ui";
import {
  AdminAuthority,
  Dialog,
  Empty,
  ErrorNotice,
  Loading,
  Pager,
  date,
  label,
  money,
} from "./common";
function initialDates() {
  const end = new Date();
  const start = new Date(end.getTime() - 7 * 86400000);
  return {
    start: start.toISOString().slice(0, 10),
    end: end.toISOString().slice(0, 10),
  };
}
export function timeRange(start: string, end: string) {
  return {
    start: timestampFromDate(new Date(`${start}T00:00:00Z`)),
    end: timestampFromDate(new Date(`${end}T23:59:59.999Z`)),
  };
}
export function Summary({ summary }: { summary?: UsageSummary }) {
  return (
    <Card>
      <h2>Usage summary</h2>
      {summary ? (
        <>
          <div className="admin-metrics">
            <div>
              <span>Requests</span>
              <strong>{String(summary.requestCount)}</strong>
            </div>
            <div>
              <span>Known estimated cost</span>
              <strong>
                {summary.knownCosts.length
                  ? summary.knownCosts.map(money).join(" · ")
                  : "Unknown"}
              </strong>
            </div>
            <div>
              <span>Confirmed input tokens</span>
              <strong>
                {summary.knownUsage?.inputTokens?.toString() ?? "Unknown"}
              </strong>
            </div>
            <div>
              <span>Confirmed output tokens</span>
              <strong>
                {summary.knownUsage?.outputTokens?.toString() ?? "Unknown"}
              </strong>
            </div>
          </div>
          <p>
            {String(summary.missingCostCount)} requests with unknown cost ·{" "}
            {String(summary.missingUsageCount)} missing usage ·{" "}
            {String(summary.missingPriceCount)} missing price
          </p>
          <p>
            Failed requests: {String(summary.failedRequestCount)}. Recorded
            metadata may be incomplete if usage recording failed.
          </p>
        </>
      ) : (
        <Empty>Usage summary is unavailable.</Empty>
      )}
      <p>
        Estimates, not billing. Currency is shown with each known cost. Price
        changes apply to new requests only.
      </p>
    </Card>
  );
}
export function OrganizationUsage() {
  const { organizationId } = useContext(AdminAuthority);
  const [dates, setDates] = useState(initialDates);
  const [userId, setUserId] = useState("");
  const [modelId, setModelId] = useState("");
  const [outcome, setOutcome] = useState("");
  const [token, setToken] = useState("");
  const [detail, setDetail] = useState<UsageRecord>();
  const valid = !!dates.start && !!dates.end && dates.start <= dates.end;
  const filters = {
    organizationId,
    userId,
    modelId: modelId || undefined,
    timeRange: valid ? timeRange(dates.start, dates.end) : undefined,
  };
  const query = useQuery(
    UsageService.method.listUsage,
    { ...filters, page: { pageSize: 100, pageToken: token } },
    { enabled: valid },
  );
  const summary = useQuery(UsageService.method.getUsageSummary, filters, {
    enabled: valid,
  });
  const records =
    query.data?.records.filter(
      (record) => !outcome || String(record.outcome) === outcome,
    ) ?? [];
  function reset() {
    setDates(initialDates());
    setUserId("");
    setModelId("");
    setOutcome("");
    setToken("");
  }
  return (
    <>
      <PageHeader
        title="Organization usage"
        description="Per-member request metadata. No credentials, prompts or response bodies."
      />
      <Card>
        <details className="admin-filters" open>
          <summary>Filters</summary>
          <div className="admin-form-grid">
            <label>
              From (UTC)
              <input
                type="date"
                value={dates.start}
                onChange={(e) => {
                  setDates({ ...dates, start: e.target.value });
                  setToken("");
                }}
              />
            </label>
            <label>
              Through (UTC)
              <input
                type="date"
                value={dates.end}
                onChange={(e) => {
                  setDates({ ...dates, end: e.target.value });
                  setToken("");
                }}
              />
            </label>
            <label>
              Member ID
              <input
                value={userId}
                onChange={(e) => {
                  setUserId(e.target.value);
                  setToken("");
                }}
                placeholder="All members"
              />
            </label>
            <label>
              Model
              <input
                value={modelId}
                onChange={(e) => {
                  setModelId(e.target.value);
                  setToken("");
                }}
                placeholder="All models"
              />
            </label>
            <label>
              Outcome on this page
              <select
                value={outcome}
                onChange={(e) => setOutcome(e.target.value)}
              >
                <option value="">All outcomes</option>
                {Object.values(RequestOutcome)
                  .filter((v) => typeof v === "number")
                  .map((v) => (
                    <option value={v} key={v}>
                      {label(RequestOutcome[v as number])}
                    </option>
                  ))}
              </select>
            </label>
          </div>
          <button onClick={reset}>Clear filters</button>
        </details>
        {!valid && (
          <p role="alert">Choose an end date on or after the start date.</p>
        )}
      </Card>
      {valid && (
        <>
          {summary.isPending ? (
            <Loading />
          ) : summary.error ? (
            <ErrorNotice
              error={summary.error}
              retry={() => void summary.refetch()}
            />
          ) : (
            <Summary summary={summary.data?.summary} />
          )}
          <Card>
            <h2>Request metadata</h2>
            {query.isPending ? (
              <Loading />
            ) : query.error ? (
              <ErrorNotice
                error={query.error}
                retry={() => void query.refetch()}
              />
            ) : (
              <>
                <div className="admin-records">
                  {records.map((record) => (
                    <article className="admin-record" key={record.id}>
                      <div>
                        <h3>{record.modelId}</h3>
                        <p>{date(record.occurredAt)}</p>
                        <p>
                          Member: {record.userId || "Deleted user"} ·{" "}
                          {record.outcome === RequestOutcome.UNSPECIFIED
                            ? "Unknown outcome"
                            : label(RequestOutcome[record.outcome])}
                        </p>
                        <p>
                          Input:{" "}
                          {record.usage?.inputTokens?.toString() ?? "Unknown"} ·
                          Output:{" "}
                          {record.usage?.outputTokens?.toString() ?? "Unknown"}
                        </p>
                        <p>Estimated cost: {money(record.estimatedCost)}</p>
                        <p>Request ID: {record.requestId || "Unknown"}</p>
                      </div>
                      <button onClick={() => setDetail(record)}>
                        View estimate details
                      </button>
                    </article>
                  ))}
                </div>
                {!records.length && (
                  <Empty>
                    No requests match these filters. Clear filters to try again.
                  </Empty>
                )}
                <Pager
                  token={token}
                  next={query.data?.page?.nextPageToken}
                  onChange={setToken}
                />
              </>
            )}
          </Card>
        </>
      )}
      {detail && (
        <Dialog
          title="Request estimate"
          onClose={() => setDetail(undefined)}
          initial="cancel"
        >
          <p>Request ID: {detail.requestId}</p>
          <p>Estimated cost: {money(detail.estimatedCost)}</p>
          <p>
            Price provenance:{" "}
            {detail.appliedPrice
              ? label(PriceSource[detail.appliedPrice.source])
              : "Unknown"}
          </p>
          <p>Revision: {detail.appliedPrice?.sourceRevision || "Unknown"}</p>
          <p>Service tier: {detail.appliedPrice?.serviceTier || "Unknown"}</p>
          <p>
            This estimate was fixed when the request was processed. Later price
            updates do not change it.
          </p>
          <p>Error code: {detail.errorCode || "None reported"}</p>
          <button data-cancel onClick={() => setDetail(undefined)}>
            Close
          </button>
        </Dialog>
      )}
    </>
  );
}
export function Audit({ operator = false }: { operator?: boolean }) {
  const { organizationId } = useContext(AdminAuthority);
  const [token, setToken] = useState("");
  const [actor, setActor] = useState("");
  const [action, setAction] = useState("");
  const [dates, setDates] = useState({ start: "", end: "" });
  const valid =
    (!dates.start && !dates.end) ||
    (!!dates.start && !!dates.end && dates.start <= dates.end);
  const range =
    dates.start && valid ? timeRange(dates.start, dates.end) : undefined;
  const org = useQuery(
    AuditService.method.listOrganizationAudit,
    {
      organizationId,
      page: { pageSize: 100, pageToken: token },
      timeRange: range,
    },
    { enabled: !operator && valid },
  );
  const instance = useQuery(
    OperatorService.method.listInstanceAudit,
    { page: { pageSize: 100, pageToken: token }, timeRange: range },
    { enabled: operator && valid },
  );
  const query = operator ? instance : org;
  const records =
    query.data?.records.filter(
      (record) =>
        (!actor ||
          (record.actorUserId || "Deleted user")
            .toLowerCase()
            .includes(actor.toLowerCase())) &&
        (!action || String(record.action) === action),
    ) ?? [];
  return (
    <>
      <PageHeader
        title={operator ? "Instance audit" : "Organization audit"}
        description="Permission, lifecycle and configuration events. Safe metadata only."
      />
      <Card>
        <details open className="admin-filters">
          <summary>Filters</summary>
          <div className="admin-form-grid">
            <label>
              Actor ID on this page
              <input value={actor} onChange={(e) => setActor(e.target.value)} />
            </label>
            <label>
              Action on this page
              <select
                value={action}
                onChange={(e) => setAction(e.target.value)}
              >
                <option value="">All actions</option>
                {Object.values(AuditAction)
                  .filter((v) => typeof v === "number")
                  .map((v) => (
                    <option key={v} value={v}>
                      {label(AuditAction[v as number])}
                    </option>
                  ))}
              </select>
            </label>
            <label>
              From (UTC)
              <input
                type="date"
                value={dates.start}
                onChange={(e) => {
                  setDates({ ...dates, start: e.target.value });
                  setToken("");
                }}
              />
            </label>
            <label>
              Through (UTC)
              <input
                type="date"
                value={dates.end}
                onChange={(e) => {
                  setDates({ ...dates, end: e.target.value });
                  setToken("");
                }}
              />
            </label>
          </div>
          <button
            onClick={() => {
              setActor("");
              setAction("");
              setDates({ start: "", end: "" });
              setToken("");
            }}
          >
            Clear filters
          </button>
        </details>
        {!valid ? (
          <p role="alert">
            Choose both dates with the end on or after the start.
          </p>
        ) : query.isPending ? (
          <Loading />
        ) : query.error ? (
          <ErrorNotice error={query.error} retry={() => void query.refetch()} />
        ) : (
          <>
            <div className="admin-records">
              {records.map((record) => (
                <AuditRow key={record.id} record={record} />
              ))}
            </div>
            {!records.length && (
              <Empty>
                No audit records match. Clear filters or check another page.
              </Empty>
            )}
            <Pager
              token={token}
              next={query.data?.page?.nextPageToken}
              onChange={setToken}
            />
          </>
        )}
      </Card>
    </>
  );
}
function AuditRow({ record }: { record: AuditRecord }) {
  return (
    <article className="admin-record">
      <div>
        <h2>
          {record.action === AuditAction.UNSPECIFIED
            ? "Unknown action"
            : label(AuditAction[record.action])}
        </h2>
        <p>{date(record.occurredAt)}</p>
        <p>Actor: {record.actorUserId || "Deleted user"}</p>
        <p>Target: {record.resourceId || "Unknown"}</p>
        <p>Outcome: Not reported</p>
        {record.requestId && <p>Request ID: {record.requestId}</p>}
      </div>
    </article>
  );
}
