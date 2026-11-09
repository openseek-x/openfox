import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import {
  isApiError,
  type DeviceCommand,
  type DeviceDetail,
  type DeviceStorageCategory,
  type DeviceStorageCleanupResult,
} from "@openfox/api";
import { useI18n, type Translate } from "@openfox/i18n";
import {
  Button,
  Callout,
  ConfirmDialog,
  Icon,
  Tooltip,
  useToast,
} from "@openfox/ui";

import { keys } from "../context";
import { formatRelativeTime } from "../tasks/primitives/dates";
import { useDeviceCommand } from "./commands";
import { formatBytes } from "./model";
import { StorageManager } from "./StorageManager";
import { InfoCard, InfoRow } from "./parts";

/** 远程命令发起失败的文案：404 = 电脑不可见或不是所有者 */
export function commandRequestError(error: unknown, t: Translate): string {
  if (isApiError(error) && error.status === 404)
    return t("connect.remote.notFound");
  return t("connect.remote.requestFailed");
}

/** 命令以 FAILED / EXPIRED 结束时的文案；SUCCEEDED 返回 null */
export function commandSettledError(
  command: DeviceCommand,
  t: Translate,
): string | null {
  if (command.status === "FAILED")
    return t("connect.remote.failed", { code: command.error_code || "-" });
  if (command.status === "EXPIRED") return t("connect.remote.expired");
  return null;
}

function cleanupResult(
  command: DeviceCommand | undefined,
): DeviceStorageCleanupResult | null {
  if (command?.status !== "SUCCEEDED" || !command.result) return null;
  const result = command.result as Partial<DeviceStorageCleanupResult>;
  if (typeof result.freed_bytes !== "number" || !result.removed) return null;
  return result as DeviceStorageCleanupResult;
}

/** 类别的展示顺序与颜色；未知键按「其他」展示。 */
const CATEGORY_ORDER = [
  "cache",
  "task",
  "conversation",
  "automation",
  "workflow",
  "other",
] as const;
const CATEGORY_COLOR: Record<(typeof CATEGORY_ORDER)[number], string> = {
  cache: "#A1A1AA",
  task: "#3B82F6",
  conversation: "#8B5CF6",
  automation: "#22C55E",
  workflow: "#DBA400",
  other: "#D4D4D8",
};

function categoryKey(key: string): (typeof CATEGORY_ORDER)[number] {
  return (CATEGORY_ORDER as readonly string[]).includes(key)
    ? (key as (typeof CATEGORY_ORDER)[number])
    : "other";
}

/**
 * 按类型的占用条 + 图例。
 * 同一类型（含未知键）合并；占比按类别总和计算；没有细分数据时不渲染（调用方退回总量展示）。
 */
export function StorageUsage({
  categories,
  locale,
}: {
  categories: DeviceStorageCategory[];
  locale: string;
}) {
  const { t } = useI18n();
  const merged = new Map<(typeof CATEGORY_ORDER)[number], number>();
  for (const item of categories)
    merged.set(
      categoryKey(item.key),
      (merged.get(categoryKey(item.key)) ?? 0) + item.bytes,
    );
  const rows = CATEGORY_ORDER.flatMap((key) => {
    const bytes = merged.get(key) ?? 0;
    return bytes > 0 ? [{ key, bytes }] : [];
  });
  const sum = rows.reduce((total, row) => total + row.bytes, 0);
  if (sum <= 0) return null;
  return (
    <div
      className="flex flex-col gap-2 border-b border-line-soft px-4 py-3"
      data-testid="storage-usage"
    >
      <div
        role="img"
        aria-label={t("connect.storage.usageLabel")}
        className="flex h-2 w-full gap-0.5 overflow-hidden rounded-full bg-surface-muted p-0.5"
      >
        {rows.map((row) => (
          <span
            key={row.key}
            className="h-1 rounded-full"
            style={{
              width: `${(row.bytes / sum) * 100}%`,
              minWidth: 2,
              backgroundColor: CATEGORY_COLOR[row.key],
            }}
          />
        ))}
      </div>
      <ul className="flex flex-wrap gap-x-3 gap-y-1 text-caption text-fg-muted">
        {rows.map((row) => (
          <li key={row.key} className="flex items-center gap-1.5">
            <span
              aria-hidden
              className="size-2 rounded-sm"
              style={{ backgroundColor: CATEGORY_COLOR[row.key] }}
            />
            {t(`connect.storage.category.${row.key}`)}{" "}
            {formatBytes(row.bytes, locale)}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** 远程命令按钮：仅所有者可用；非所有者禁用并用 Tooltip 说明（禁用元素收不到鼠标事件，提示挂在外层）。 */
export function OwnerOnlyButton({
  isOwner,
  reason,
  children,
}: {
  isOwner: boolean;
  reason: string;
  children: React.ReactElement;
}) {
  if (isOwner) return children;
  return (
    <Tooltip content={reason}>
      <span tabIndex={0} className="inline-flex rounded-lg outline-none">
        {children}
      </span>
    </Tooltip>
  );
}

/**
 * 电脑详情 · 存储：心跳上报的统计 + 「清理」（storage_cleanup 远程命令）。
 */
export function StorageSection({
  orgId,
  device,
  isOwner,
}: {
  orgId: string;
  device: DeviceDetail;
  isOwner: boolean;
}) {
  const { t, locale } = useI18n();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [manage, setManage] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const cleanup = useDeviceCommand(orgId, device.id, "storage_cleanup", {
    onSettled: (command) => {
      const error = commandSettledError(command, t);
      if (error) {
        toast(error, { tone: "error" });
        return;
      }
      const result = cleanupResult(command);
      toast(
        t("connect.storage.done", {
          size: formatBytes(result?.freed_bytes ?? 0, locale),
        }),
      );
      // 清理后 daemon 会在下次统计时上报新的占用；先刷新一次详情
      void queryClient.invalidateQueries({
        queryKey: keys.device(orgId, device.id),
        exact: true,
      });
    },
    onError: (error) => toast(commandRequestError(error, t), { tone: "error" }),
  });
  const storage = device.storage;
  const last = cleanupResult(cleanup.command);

  const button = (
    <Button
      size="28"
      leading={<Icon name="trash-2" />}
      loading={cleanup.running}
      disabled={!isOwner || cleanup.running}
      onClick={() => setManage(true)}
    >
      {t("connect.storage.manage")}
    </Button>
  );

  return (
    <>
      {storage ? (
        <InfoCard>
          <InfoRow
            label={t("connect.storage.total")}
            description={t("connect.storage.totalDesc", {
              time: formatRelativeTime(storage.measured_at, locale),
            })}
          >
            <span className="font-medium text-fg-default">
              {formatBytes(storage.total_bytes, locale)}
            </span>
          </InfoRow>
          {storage.categories.length > 0 && (
            <StorageUsage categories={storage.categories} locale={locale} />
          )}
          <InfoRow
            label={t("connect.storage.worktrees")}
            description={t("connect.storage.worktreesDesc", {
              count: storage.worktree_count,
            })}
          >
            {formatBytes(storage.worktree_bytes, locale)}
          </InfoRow>
          <InfoRow
            label={t("connect.storage.codexHome")}
            description={t("connect.storage.codexHomeDesc")}
          >
            {formatBytes(storage.codex_home_bytes, locale)}
          </InfoRow>
          <InfoRow
            label={t("connect.storage.journal")}
            description={t("connect.storage.journalDesc")}
          >
            {formatBytes(storage.journal_bytes, locale)}
          </InfoRow>
          {storage.partial && (
            <div className="border-b border-line-soft px-4 py-2.5">
              <Callout tone="warning">{t("connect.storage.partial")}</Callout>
            </div>
          )}
          <div className="flex flex-wrap items-center justify-end gap-3 px-4 py-2.5">
            <span className="mr-auto text-caption text-fg-muted">
              {last
                ? t("connect.storage.result", {
                    size: formatBytes(last.freed_bytes, locale),
                    worktrees: last.removed.worktrees,
                    homes: last.removed.codex_homes,
                    rows: last.removed.journal_rows,
                    skipped: last.skipped_active,
                  }) +
                  (last.skipped_dirty
                    ? t("connect.storage.resultDirty", {
                        dirty: last.skipped_dirty,
                      })
                    : "")
                : t("connect.storage.footnote")}
            </span>
            <OwnerOnlyButton
              isOwner={isOwner}
              reason={t("connect.storage.ownerOnly")}
            >
              {button}
            </OwnerOnlyButton>
          </div>
        </InfoCard>
      ) : (
        <div className="flex items-center gap-3 rounded-xl border border-dashed border-line-default px-4 py-3">
          <p className="min-w-0 flex-1 text-body-sm text-fg-muted">
            {t("connect.storage.empty")}
          </p>
          <OwnerOnlyButton
            isOwner={isOwner}
            reason={t("connect.storage.ownerOnly")}
          >
            {button}
          </OwnerOnlyButton>
        </div>
      )}
      {manage && (
        <StorageManager
          orgId={orgId}
          device={device}
          onClose={() => setManage(false)}
          onLegacy={() => {
            setManage(false);
            setConfirm(true);
          }}
        />
      )}
      <ConfirmDialog
        open={confirm}
        title={t("connect.storage.confirmTitle")}
        confirmLabel={t("connect.storage.confirm")}
        onCancel={() => setConfirm(false)}
        onConfirm={() => {
          setConfirm(false);
          cleanup.start({});
        }}
      >
        <div className="flex flex-col gap-2 text-body-sm text-fg-secondary">
          <p>{t("connect.storage.confirmDesc")}</p>
          <ul className="list-disc pl-5">
            <li>{t("connect.storage.scopeWorktrees")}</li>
            <li>{t("connect.storage.scopeCodexHome")}</li>
            <li>{t("connect.storage.scopeJournal")}</li>
          </ul>
          <p>{t("connect.storage.keepDirty")}</p>
          {!device.online && (
            <Callout tone="warning">
              {t("connect.storage.confirmOffline")}
            </Callout>
          )}
        </div>
      </ConfirmDialog>
    </>
  );
}
