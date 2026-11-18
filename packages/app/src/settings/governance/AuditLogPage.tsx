import { useInfiniteQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { useParams } from "react-router-dom";

import type { AuditEvent, AuditEventQuery } from "@openfox/api";
import { useI18n, useT, type MessageKey } from "@openfox/i18n";
import { Badge, Button, Field, Select, TextInput } from "@openfox/ui";

import { useApi } from "../../context";
import { SettingsColumn } from "../../spaces/settings/parts";
import { useOrgPeople } from "../../tasks/queries";
import { ConfigPageHeader, EmptyState, ListSkeleton } from "../common";
import { AdminOnly } from "./AdminOnly";

// 阶段 12e · 审计日志（K-01）。仅组织所有者与管理员；按时间倒序、游标分页。

/** 与服务端一致：每页 50 条、时间跨度最多 366 天 */
export const AUDIT_PAGE_SIZE = 50;
const MAX_SPAN_MS = 366 * 24 * 60 * 60 * 1000;
const ACTION_PATTERN = /^[A-Za-z0-9_.-]{1,128}\*?$/;
const RESOURCE_KIND_PATTERN = /^[A-Za-z0-9_.-]{1,64}$/;
const ALL_ACTORS = "__all__";

interface AuditFilters {
  actorId: string;
  action: string;
  resourceKind: string;
  /** datetime-local 的值（本地时间），空串表示不限 */
  from: string;
  to: string;
}

const EMPTY_FILTERS: AuditFilters = {
  actorId: "",
  action: "",
  resourceKind: "",
  from: "",
  to: "",
};

/** datetime-local（本地时间）转 RFC 3339；非法输入返回空串 */
function toIso(local: string): string {
  if (!local) return "";
  const date = new Date(local);
  return Number.isNaN(date.getTime()) ? "" : date.toISOString();
}

/** 提交前的本地校验，与服务端规则一致；返回错误文案键 */
export function validateAuditFilters(filters: AuditFilters): MessageKey | null {
  const action = filters.action.trim();
  if (action && !ACTION_PATTERN.test(action))
    return "governance.audit.invalidAction";
  const kind = filters.resourceKind.trim();
  if (kind && !RESOURCE_KIND_PATTERN.test(kind))
    return "governance.audit.invalidResourceKind";
  const from = toIso(filters.from);
  const to = toIso(filters.to);
  if (from && to) {
    const span = new Date(to).getTime() - new Date(from).getTime();
    if (span <= 0) return "governance.audit.invalidRange";
    if (span > MAX_SPAN_MS) return "governance.audit.rangeTooLong";
  }
  return null;
}

function toQuery(filters: AuditFilters): AuditEventQuery {
  return {
    limit: AUDIT_PAGE_SIZE,
    actor_id: filters.actorId,
    action: filters.action.trim(),
    resource_kind: filters.resourceKind.trim(),
    from: toIso(filters.from),
    to: toIso(filters.to),
  };
}

export function AuditLogPage() {
  const { orgId = "" } = useParams();
  return (
    <AdminOnly orgId={orgId}>
      <AuditLog orgId={orgId} />
    </AdminOnly>
  );
}

function AuditLog({ orgId }: { orgId: string }) {
  const t = useT();
  const api = useApi();
  const [draft, setDraft] = useState<AuditFilters>(EMPTY_FILTERS);
  const [applied, setApplied] = useState<AuditFilters>(EMPTY_FILTERS);
  const [error, setError] = useState<MessageKey | null>(null);
  const people = useOrgPeople(orgId);
  const events = useInfiniteQuery({
    queryKey: ["org", orgId, "audit-events", applied] as const,
    queryFn: ({ pageParam, signal }) =>
      api.auditEvents.list(
        orgId,
        { ...toQuery(applied), cursor: pageParam },
        signal,
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  const rows = events.data?.pages.flatMap((page) => page.events) ?? [];
  const filtered = Object.values(applied).some(Boolean);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const problem = validateAuditFilters(draft);
    setError(problem);
    if (!problem) setApplied(draft);
  };
  const reset = () => {
    setDraft(EMPTY_FILTERS);
    setApplied(EMPTY_FILTERS);
    setError(null);
  };

  return (
    <SettingsColumn wide>
      <ConfigPageHeader
        title={t("governance.audit.title")}
        subtitle={t("governance.audit.subtitle")}
      />
      <form
        aria-label={t("governance.audit.filters")}
        className="flex flex-col gap-3 rounded-xl border border-line-default p-4"
        onSubmit={submit}
      >
        <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
          <Field label={t("governance.audit.actor")}>
            <Select
              value={draft.actorId || ALL_ACTORS}
              onChange={(value) =>
                setDraft({
                  ...draft,
                  actorId: value === ALL_ACTORS ? "" : value,
                })
              }
              options={[
                { value: ALL_ACTORS, label: t("governance.audit.actorAll") },
                ...(people.data ?? []).map((person) => ({
                  value: person.id,
                  label: person.name || person.email,
                })),
              ]}
            />
          </Field>
          <Field
            label={t("governance.audit.action")}
            helper={t("governance.audit.actionHelp")}
          >
            <TextInput
              value={draft.action}
              maxLength={129}
              placeholder={t("governance.audit.actionPlaceholder")}
              onChange={(event) =>
                setDraft({ ...draft, action: event.target.value })
              }
            />
          </Field>
          <Field label={t("governance.audit.resourceKind")}>
            <TextInput
              value={draft.resourceKind}
              maxLength={64}
              placeholder={t("governance.audit.resourceKindPlaceholder")}
              onChange={(event) =>
                setDraft({ ...draft, resourceKind: event.target.value })
              }
            />
          </Field>
          <Field label={t("governance.audit.from")}>
            <TextInput
              type="datetime-local"
              value={draft.from}
              onChange={(event) =>
                setDraft({ ...draft, from: event.target.value })
              }
            />
          </Field>
          <Field label={t("governance.audit.to")}>
            <TextInput
              type="datetime-local"
              value={draft.to}
              onChange={(event) =>
                setDraft({ ...draft, to: event.target.value })
              }
            />
          </Field>
        </div>
        {error && (
          <p role="alert" className="text-body-sm text-danger">
            {t(error)}
          </p>
        )}
        <div className="flex gap-2">
          <Button type="submit">{t("governance.audit.apply")}</Button>
          <Button type="button" variant="outline" onClick={reset}>
            {t("governance.audit.reset")}
          </Button>
        </div>
      </form>

      {events.isPending ? (
        <ListSkeleton rows={6} label={t("governance.audit.loading")} />
      ) : events.isError && rows.length === 0 ? (
        <EmptyState
          title={t("governance.audit.loadFailed")}
          action={
            <Button variant="outline" onClick={() => void events.refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : rows.length === 0 ? (
        <EmptyState
          title={t(
            filtered
              ? "governance.audit.emptyFiltered"
              : "governance.audit.empty",
          )}
          description={
            filtered ? t("governance.audit.emptyFilteredHint") : undefined
          }
        />
      ) : (
        <>
          <AuditTable rows={rows} />
          <div className="flex flex-col items-center gap-2">
            {events.isFetchNextPageError && (
              <p role="alert" className="text-body-sm text-danger">
                {t("governance.audit.loadMoreFailed")}
              </p>
            )}
            {events.hasNextPage ? (
              <Button
                variant="outline"
                disabled={events.isFetchingNextPage}
                onClick={() => void events.fetchNextPage()}
              >
                {t(
                  events.isFetchingNextPage
                    ? "governance.audit.loadingMore"
                    : "governance.audit.loadMore",
                )}
              </Button>
            ) : (
              <p className="text-body-sm text-fg-muted">
                {t("governance.audit.end")}
              </p>
            )}
          </div>
        </>
      )}
    </SettingsColumn>
  );
}

function AuditTable({ rows }: { rows: AuditEvent[] }) {
  const t = useT();
  const { formatDate } = useI18n();
  return (
    <div className="overflow-x-auto rounded-xl border border-line-default">
      <table className="w-full min-w-[720px] text-left text-body-sm">
        <thead className="bg-surface-muted text-fg-muted">
          <tr>
            {(["time", "actor", "action", "resource", "detail"] as const).map(
              (column) => (
                <th key={column} scope="col" className="px-3 py-2 font-medium">
                  {t(`governance.audit.columns.${column}`)}
                </th>
              ),
            )}
          </tr>
        </thead>
        <tbody>
          {rows.map((event) => (
            <tr key={event.id} className="border-t border-line-default">
              <td className="px-3 py-2 whitespace-nowrap text-fg-muted">
                <time dateTime={event.created_at}>
                  {formatDate(event.created_at, {
                    dateStyle: "medium",
                    timeStyle: "medium",
                  })}
                </time>
              </td>
              <td className="px-3 py-2">
                <span className="block truncate" title={event.actor_email}>
                  {event.actor_name}
                </span>
              </td>
              <td className="px-3 py-2">
                <code className="break-all">{event.action}</code>
                {event.decision !== "allow" && (
                  <Badge tone="danger" className="ml-2">
                    {t("governance.audit.decisionDenied")}
                  </Badge>
                )}
              </td>
              <td className="px-3 py-2">
                <span className="block">{event.resource_kind}</span>
                <span className="block break-all text-fg-muted">
                  {event.resource_id}
                </span>
              </td>
              <td className="px-3 py-2 text-fg-muted">
                {Object.entries(event.metadata)
                  .sort(([left], [right]) => left.localeCompare(right))
                  .map(([key, value]) => (
                    <span key={key} className="mr-2 inline-block break-all">
                      {key}={value}
                    </span>
                  ))}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
