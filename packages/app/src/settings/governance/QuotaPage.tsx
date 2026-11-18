import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { useParams } from "react-router-dom";

import { isApiError, type RuntimeConfig } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Field, TextInput, useToast } from "@openfox/ui";

import { useApi } from "../../context";
import { SettingsColumn, SettingsSection } from "../../spaces/settings/parts";
import { ConfigPageHeader, EmptyState, ListSkeleton } from "../common";
import { AdminOnly } from "./AdminOnly";
import { runtimeConfigKey, runtimeUsageKey, useRuntimeUsage } from "./queries";

// 阶段 12e · 资源配额（NF-05）。仅组织所有者与管理员；范围与服务端一致，不在前端放宽。

export const QUEUE_MAX_RANGE = { min: 1, max: 10000 } as const;
export const ACTIVE_MAX_RANGE = { min: 1, max: 100 } as const;

/** 解析输入框里的整数；空、小数、越界都返回 null */
export function parseLimit(
  raw: string,
  range: { min: number; max: number },
): number | null {
  if (!/^\d+$/.test(raw.trim())) return null;
  const value = Number(raw.trim());
  return value >= range.min && value <= range.max ? value : null;
}

export function QuotaPage() {
  const { orgId = "" } = useParams();
  return (
    <AdminOnly orgId={orgId}>
      <Quota orgId={orgId} />
    </AdminOnly>
  );
}

function Quota({ orgId }: { orgId: string }) {
  const t = useT();
  const api = useApi();
  const config = useQuery({
    queryKey: runtimeConfigKey(orgId),
    queryFn: ({ signal }) => api.runtimeConfig.get(orgId, signal),
  });
  return (
    <SettingsColumn>
      <ConfigPageHeader
        title={t("governance.quota.title")}
        subtitle={t("governance.quota.subtitle")}
      />
      {config.isPending ? (
        <ListSkeleton rows={3} label={t("governance.quota.loading")} />
      ) : config.isError ? (
        <EmptyState
          title={t("governance.quota.loadFailed")}
          action={
            <Button variant="outline" onClick={() => void config.refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : (
        <>
          <UsageSection orgId={orgId} />
          <LimitsForm orgId={orgId} config={config.data} />
        </>
      )}
    </SettingsColumn>
  );
}

function UsageSection({ orgId }: { orgId: string }) {
  const t = useT();
  const usage = useRuntimeUsage(orgId);
  return (
    <SettingsSection title={t("governance.quota.usageTitle")}>
      {usage.data ? (
        <div className="flex flex-col gap-3">
          <dl className="grid grid-cols-2 gap-3">
            <UsageTile
              label={t("governance.quota.queued")}
              value={t("governance.quota.usageOf", {
                current: usage.data.queued,
                max: usage.data.queue_max ?? "–",
              })}
            />
            <UsageTile
              label={t("governance.quota.active")}
              value={t("governance.quota.usageOf", {
                current: usage.data.active ?? "–",
                max: usage.data.effective_active_max ?? "–",
              })}
            />
          </dl>
          {usage.data.queue_full && (
            <Callout tone="warning">{t("governance.quota.queueFull")}</Callout>
          )}
        </div>
      ) : (
        <p className="px-1 text-body-sm text-fg-muted">
          {usage.isError ? t("governance.quota.usageUnavailable") : "…"}
        </p>
      )}
    </SettingsSection>
  );
}

function UsageTile({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-1 rounded-xl border border-line-default p-4">
      <dt className="text-body-sm text-fg-muted">{label}</dt>
      <dd className="text-heading font-semibold">{value}</dd>
    </div>
  );
}

function LimitsForm({
  orgId,
  config,
}: {
  orgId: string;
  config: RuntimeConfig;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const usage = useRuntimeUsage(orgId);
  const [queueMax, setQueueMax] = useState(String(config.policy.queue_max));
  const [activeMax, setActiveMax] = useState(String(config.policy.active_max));
  const [conflict, setConflict] = useState(false);
  // 配置版本变化（保存成功，或冲突后刷新到别人的修改）时，把输入框同步为最新值
  useEffect(() => {
    setQueueMax(String(config.policy.queue_max));
    setActiveMax(String(config.policy.active_max));
  }, [
    config.config_version,
    config.policy.queue_max,
    config.policy.active_max,
  ]);

  const queueValue = parseLimit(queueMax, QUEUE_MAX_RANGE);
  const activeValue = parseLimit(activeMax, ACTIVE_MAX_RANGE);
  const dirty =
    queueValue !== config.policy.queue_max ||
    activeValue !== config.policy.active_max;
  const capacity = usage.data?.capacity_max;
  // 平台槽位是活动运行的硬上限：不允许把活动上限改成超过它的值（保存了也不会生效）。
  // 已保存的旧值若本来就超过平台槽位，不拦截对队列上限的修改，只提示生效值。
  const aboveCapacity =
    capacity !== undefined &&
    activeValue !== null &&
    activeValue > capacity &&
    activeValue !== config.policy.active_max;

  const save = useMutation({
    mutationFn: (limits: { queue_max: number; active_max: number }) =>
      api.runtimeConfig.update(orgId, {
        // 带上读取时的版本号：两位管理员同时修改时后提交的一方会得到 409，而不是互相覆盖
        expected_version: config.config_version,
        policy: { ...config.policy, ...limits },
      }),
    onSuccess: (next) => {
      setConflict(false);
      queryClient.setQueryData(runtimeConfigKey(orgId), next);
      void queryClient.invalidateQueries({ queryKey: runtimeUsageKey(orgId) });
      toast(t("governance.quota.saved"), { tone: "success" });
    },
    onError: (error) => {
      if (isApiError(error, "VERSION_CONFLICT")) {
        setConflict(true);
        void queryClient.invalidateQueries({
          queryKey: runtimeConfigKey(orgId),
        });
        return;
      }
      toast(t("governance.quota.saveFailed"), { tone: "error" });
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (queueValue === null || activeValue === null || !dirty || aboveCapacity)
      return;
    save.mutate({ queue_max: queueValue, active_max: activeValue });
  };

  return (
    <SettingsSection title={t("governance.quota.limitsTitle")}>
      <form className="flex flex-col gap-4" onSubmit={submit} noValidate>
        {conflict && (
          <Callout tone="warning">{t("governance.quota.conflict")}</Callout>
        )}
        <Field
          label={t("governance.quota.queueMax")}
          helper={t("governance.quota.queueMaxHelp", QUEUE_MAX_RANGE)}
          error={
            queueValue === null
              ? t("governance.quota.outOfRange", QUEUE_MAX_RANGE)
              : undefined
          }
        >
          <TextInput
            inputMode="numeric"
            value={queueMax}
            invalid={queueValue === null}
            onChange={(event) => setQueueMax(event.target.value)}
          />
        </Field>
        <Field
          label={t("governance.quota.activeMax")}
          helper={t("governance.quota.activeMaxHelp", ACTIVE_MAX_RANGE)}
          error={
            activeValue === null
              ? t("governance.quota.outOfRange", ACTIVE_MAX_RANGE)
              : aboveCapacity
                ? t("governance.quota.aboveCapacity", { capacity })
                : undefined
          }
        >
          <TextInput
            inputMode="numeric"
            value={activeMax}
            invalid={activeValue === null || aboveCapacity}
            onChange={(event) => setActiveMax(event.target.value)}
          />
        </Field>
        {usage.data?.effective_active_max !== undefined && (
          <p className="text-body-sm text-fg-muted">
            {t("governance.quota.effective", {
              value: usage.data.effective_active_max,
            })}
          </p>
        )}
        {capacity !== undefined &&
          activeValue !== null &&
          activeValue > capacity &&
          !aboveCapacity && (
            <Callout tone="warning">
              {t("governance.quota.capped", { capacity })}
            </Callout>
          )}
        <p className="text-body-sm text-fg-muted">
          {t("governance.quota.shrinkNote")}
        </p>
        <div>
          <Button
            type="submit"
            disabled={
              !dirty ||
              queueValue === null ||
              activeValue === null ||
              aboveCapacity ||
              save.isPending
            }
          >
            {t(
              save.isPending
                ? "governance.quota.saving"
                : "governance.quota.save",
            )}
          </Button>
        </div>
      </form>
    </SettingsSection>
  );
}
