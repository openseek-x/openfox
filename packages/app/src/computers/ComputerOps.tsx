import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import {
  isApiError,
  type Device,
  type DeviceKeyRotateResult,
  type DeviceOrganization,
} from "@openfox/api";
import { useI18n, useT, type MessageKey } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  ConfirmDialog,
  Icon,
  Tooltip,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { describeError, useCanManage } from "../settings/common";
import { useDeviceCommand } from "./commands";
import {
  OwnerOnlyButton,
  commandRequestError,
  commandSettledError,
} from "./ComputerStorage";
import { effectiveMaxSlots } from "./model";
import { InfoCard, InfoRow, SectionTitle, formatDateTime } from "./parts";
import { SharedTrustConfirm } from "./SharedTrustConfirm";

// 阶段 12e · 电脑详情新增的运维界面：并发上限、配置状态、受保护目录告警、密钥轮换与允许的组织。

/** D-05：同时显示中心上限、本机上限（旧 daemon 未上报显示 —）与生效值。 */
export function ConcurrencyLimits({
  device,
}: {
  device: Pick<Device, "max_slots" | "local_max_runs" | "effective_max_slots">;
}) {
  const t = useT();
  const local =
    typeof device.local_max_runs === "number"
      ? String(device.local_max_runs)
      : t("ops.limits.unreported");
  const items = [
    { key: "center", label: t("ops.limits.center"), value: device.max_slots },
    { key: "local", label: t("ops.limits.local"), value: local },
  ];
  return (
    <div
      role="group"
      aria-label={t("ops.limits.title")}
      className="flex flex-wrap items-center justify-end gap-x-4 gap-y-1 text-body-sm"
    >
      {items.map((item) => (
        <div key={item.key} className="flex items-center gap-1.5">
          <span className="text-fg-muted">{item.label}</span>
          <span data-testid={`limit-${item.key}`}>{item.value}</span>
        </div>
      ))}
      <div className="flex items-center gap-1.5 font-medium text-fg-default">
        <span>{t("ops.limits.effective")}</span>
        <span data-testid="limit-effective">{effectiveMaxSlots(device)}</span>
      </div>
    </div>
  );
}
/** K-02：daemon 对本组织运行配置的应用状态；旧 daemon（null）不显示。 */
export function RuntimeConfigBadge({
  status,
}: {
  status: Device["runtime_config_status"];
}) {
  const t = useT();
  if (!status) return null;
  if (status.error)
    return (
      <Tooltip content={t(`ops.config.error.${status.error}`)}>
        <Badge tone="danger">{t("ops.config.failed")}</Badge>
      </Tooltip>
    );
  if (status.restart_required)
    return (
      <Tooltip content={t("ops.config.restartHint")}>
        <Badge tone="warning">{t("ops.config.restartRequired")}</Badge>
      </Tooltip>
    );
  return (
    <Badge tone="success">
      {t("ops.config.applied", { version: status.applied_version })}
    </Badge>
  );
}

/** 心跳 capabilities.protected_directory_roots：只取非空字符串。 */
export function protectedRoots(device: Pick<Device, "capabilities">): string[] {
  const raw = (device.capabilities as Record<string, unknown> | null)
    ?.protected_directory_roots;
  if (!Array.isArray(raw)) return [];
  return raw.filter(
    (item): item is string => typeof item === "string" && item !== "",
  );
}

/** 注册根命中受保护目录名单：只告警，不能作为工作目录。 */
export function ProtectedRootsNotice({
  device,
}: {
  device: Pick<Device, "capabilities">;
}) {
  const t = useT();
  const roots = protectedRoots(device);
  if (roots.length === 0) return null;
  return (
    <Callout tone="warning" title={t("ops.protectedRoots.title")}>
      <p>{t("ops.protectedRoots.desc")}</p>
      <ul className="mt-1 flex flex-col gap-0.5">
        {roots.map((root) => (
          <li key={root}>
            <code className="font-mono text-code break-all">{root}</code>
          </li>
        ))}
      </ul>
    </Callout>
  );
}

const ROTATE_ERRORS = ["KEY_ROTATION_FAILED", "KEY_ROTATION_UNSUPPORTED"];

function rotateResult(result: unknown): DeviceKeyRotateResult | null {
  const value = result as Partial<DeviceKeyRotateResult> | null | undefined;
  return value && typeof value.key_id === "string"
    ? (value as DeviceKeyRotateResult)
    : null;
}

/**
 * D-01 密钥轮换：仅电脑所有者可见；daemon 心跳没有 key_rotation 能力位时禁用并提示升级或用 CLI。
 * 点击先确认，再下发 key_rotate 远程命令并显示结果。
 */
export function KeyRotationRow({
  orgId,
  device,
  isOwner,
}: {
  orgId: string;
  device: Device;
  isOwner: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const supported =
    (device.capabilities as Record<string, unknown> | null)?.key_rotation ===
    true;
  const rotate = useDeviceCommand(orgId, device.id, "key_rotate", {
    onSettled: (command) => {
      if (command.status === "SUCCEEDED") {
        toast(
          rotateResult(command.result)?.resumed
            ? t("ops.rotate.resumed")
            : t("ops.rotate.done"),
        );
        void queryClient.invalidateQueries({
          queryKey: keys.device(orgId, device.id),
        });
        return;
      }
      const code = command.error_code ?? "";
      toast(
        command.status === "FAILED" && ROTATE_ERRORS.includes(code)
          ? t(`ops.rotate.failed.${code}` as MessageKey)
          : (commandSettledError(command, t) ?? ""),
        { tone: "error" },
      );
    },
    onError: (error) =>
      toast(
        isApiError(error, "DEVICE_CAPABILITY_UNSUPPORTED")
          ? t("ops.rotate.unsupported")
          : commandRequestError(error, t),
        { tone: "error" },
      ),
  });
  if (!isOwner) return null;
  const done =
    rotate.command?.status === "SUCCEEDED"
      ? rotateResult(rotate.command.result)
      : null;
  return (
    <InfoRow
      label={t("ops.rotate.title")}
      description={
        supported ? t("ops.rotate.desc") : t("ops.rotate.unsupported")
      }
    >
      {done && (
        <span className="truncate text-caption text-fg-muted">
          {t("ops.rotate.keyId", { id: done.key_id })}
        </span>
      )}
      <OwnerOnlyButton isOwner={supported} reason={t("ops.rotate.unsupported")}>
        <Button
          variant="outline"
          size="28"
          leading={<Icon name="key-round" />}
          loading={rotate.running}
          disabled={!supported || rotate.running}
          onClick={() => setConfirming(true)}
        >
          {t("ops.rotate.action")}
        </Button>
      </OwnerOnlyButton>
      <ConfirmDialog
        open={confirming}
        title={t("ops.rotate.confirmTitle")}
        description={t("ops.rotate.confirmDesc")}
        confirmLabel={t("ops.rotate.action")}
        onCancel={() => setConfirming(false)}
        onConfirm={() => {
          setConfirming(false);
          rotate.start({});
        }}
      />
    </InfoRow>
  );
}

type OrgAction = {
  kind: "join" | "revoke" | "remove";
  item: DeviceOrganization;
};

const GRANT_STATUS: Record<string, MessageKey> = {
  ACTIVE: "ops.orgs.status.ACTIVE",
  REVOKED: "ops.orgs.status.REVOKED",
  EXPIRED: "ops.orgs.status.EXPIRED",
};

/**
 * D-09「允许的组织」：所有者看到自己所在的全部组织，可加入（先弹信任说明）或移出；
 * 一行同时可撤销与可移出时只显示「移出组织」。
 * 非所有者只看到当前组织一行。撤销 / 移出的路径用目标组织的 id（can_* 已按查看者在该组织的角色算好）。
 */
export function DeviceOrganizationsSection({
  orgId,
  device,
  onLeftCurrent,
}: {
  orgId: string;
  device: Pick<Device, "id">;
  /** 电脑离开了当前组织（撤销或移出当前组织一行） */
  onLeftCurrent: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const queryKey = [...keys.device(orgId, device.id), "organizations"] as const;
  const list = useQuery({
    queryKey,
    queryFn: () => api.devices.organizations(orgId, device.id),
    retry: false,
  });
  const [action, setAction] = useState<OrgAction | null>(null);

  const run = useMutation({
    mutationFn: async ({ kind, item }: OrgAction) => {
      const target = item.organization_id;
      if (kind === "join")
        await api.devices.joinOrganization(target, device.id);
      else if (kind === "revoke")
        await api.devices.revokeGrant(target, device.id);
      else await api.devices.removeFromOrganization(target, device.id);
    },
    onSuccess: (_result, { kind, item }) => {
      setAction(null);
      const done: Record<OrgAction["kind"], MessageKey> = {
        join: "ops.orgs.joined",
        revoke: "ops.orgs.revoked",
        remove: "ops.orgs.removed",
      };
      toast(t(done[kind], { name: item.name }));
      if (kind !== "join" && item.current) {
        onLeftCurrent();
        return;
      }
      void queryClient.invalidateQueries({ queryKey });
      // 目标组织的电脑列表也变了
      void queryClient.invalidateQueries({
        queryKey: keys.devices(item.organization_id),
      });
    },
    onError: (error) => {
      setAction(null);
      toast(
        isApiError(error, "DEVICE_REMOVED_BY_ADMIN")
          ? t("ops.orgs.removedByAdmin")
          : isApiError(error) && error.status === 404
            ? t("computers.detail.notFound")
            : describeError(error, t),
        { tone: "error" },
      );
      void queryClient.invalidateQueries({ queryKey });
    },
  });

  if (list.isLoading) return null;
  if (!list.data)
    return (
      <p className="flex items-center gap-2 rounded-xl border border-dashed border-line-default px-4 py-3 text-body-sm text-fg-muted">
        {t("ops.orgs.loadFailed")}
        <Button variant="ghost" size="28" onClick={() => void list.refetch()}>
          {t("common.retry")}
        </Button>
      </p>
    );
  return (
    <>
      <InfoCard>
        <ul aria-label={t("ops.orgs.title")}>
          {list.data.organizations.map((item) => (
            <li
              key={item.organization_id}
              className="flex min-h-12 items-center gap-3 border-b border-line-soft px-4 py-2 last:border-b-0"
            >
              <span className="flex min-w-0 flex-1 items-center gap-2">
                <span className="truncate text-body font-medium text-fg-default">
                  {item.name}
                </span>
                {item.current && (
                  <Badge tone="muted">{t("ops.orgs.current")}</Badge>
                )}
              </span>
              <span className="text-body-sm text-fg-secondary">
                {item.rejoin_blocked && !item.can_join
                  ? t("ops.orgs.status.removedByAdmin")
                  : t(
                      (item.grant_status && GRANT_STATUS[item.grant_status]) ||
                        "ops.orgs.status.none",
                    )}
              </span>
              <span className="flex shrink-0 items-center gap-1">
                {item.can_join && (
                  <Button
                    variant="outline"
                    size="28"
                    aria-label={t("ops.orgs.joinLabel", { name: item.name })}
                    onClick={() => setAction({ kind: "join", item })}
                  >
                    {t("ops.orgs.join")}
                  </Button>
                )}
                {item.can_revoke && !item.can_remove && (
                  <Button
                    variant="ghost"
                    size="28"
                    aria-label={t("ops.orgs.revokeLabel", { name: item.name })}
                    onClick={() => setAction({ kind: "revoke", item })}
                  >
                    {t("ops.orgs.revoke")}
                  </Button>
                )}
                {item.can_remove && (
                  <Button
                    variant="ghost"
                    size="28"
                    aria-label={t("ops.orgs.removeLabel", { name: item.name })}
                    onClick={() => setAction({ kind: "remove", item })}
                  >
                    {t("ops.orgs.remove")}
                  </Button>
                )}
              </span>
            </li>
          ))}
        </ul>
      </InfoCard>
      <SharedTrustConfirm
        open={action?.kind === "join"}
        variant="addOrganization"
        loading={run.isPending}
        onCancel={() => setAction(null)}
        onConfirm={() => action && run.mutate(action)}
      />
      <ConfirmDialog
        open={action?.kind === "revoke" || action?.kind === "remove"}
        danger
        title={t(
          action?.kind === "remove"
            ? "ops.orgs.removeTitle"
            : "ops.orgs.revokeTitle",
          { name: action?.item.name ?? "" },
        )}
        description={t(
          action?.kind === "remove"
            ? "ops.orgs.removeDesc"
            : "ops.orgs.revokeDesc",
        )}
        confirmLabel={t(
          action?.kind === "remove" ? "ops.orgs.remove" : "ops.orgs.revoke",
        )}
        loading={run.isPending}
        onCancel={() => setAction(null)}
        onConfirm={() => action && run.mutate(action)}
      />
    </>
  );
}

/**
 * D-09「被管理员移除的电脑」：组织 Owner/Admin 把别人的电脑移出或撤销授权后，所有者不能自行加回；
 * 管理员在这里「重新允许」后所有者才能再加入（不会自动加入）。仅 Owner/Admin 可见，没有时不显示。
 */
export function RemovedDevicesSection({ orgId }: { orgId: string }) {
  const { t, locale } = useI18n();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const canManage = useCanManage(orgId);
  const queryKey = [...keys.devices(orgId), "removed"] as const;
  const list = useQuery({
    queryKey,
    queryFn: () => api.devices.removedDevices(orgId),
    enabled: canManage,
    retry: false,
  });
  const allow = useMutation({
    mutationFn: (deviceId: string) => api.devices.allowRejoin(orgId, deviceId),
    onSuccess: () => toast(t("ops.removed.allowed")),
    onError: (error) => toast(describeError(error, t), { tone: "error" }),
    onSettled: () => void queryClient.invalidateQueries({ queryKey }),
  });
  const devices = list.data?.devices ?? [];
  if (!canManage || devices.length === 0) return null;
  return (
    <section className="flex flex-col gap-2">
      <SectionTitle>{t("ops.removed.title")}</SectionTitle>
      <p className="text-body-sm text-fg-muted">{t("ops.removed.desc")}</p>
      <InfoCard>
        <ul aria-label={t("ops.removed.title")}>
          {devices.map((item) => (
            <li
              key={item.device_id}
              className="flex min-h-12 items-center gap-3 border-b border-line-soft px-4 py-2 last:border-b-0"
            >
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-body font-medium text-fg-default">
                  {item.name}
                </span>
                <span className="truncate text-caption text-fg-muted">
                  {item.owner_email ?? t("ops.removed.unknownOwner")}
                  {" · "}
                  {t("ops.removed.removedAt", {
                    time: formatDateTime(item.removed_at, locale),
                  })}
                </span>
              </span>
              <Button
                variant="outline"
                size="28"
                aria-label={t("ops.removed.allowLabel", { name: item.name })}
                loading={allow.isPending && allow.variables === item.device_id}
                disabled={allow.isPending}
                onClick={() => allow.mutate(item.device_id)}
              >
                {t("ops.removed.allow")}
              </Button>
            </li>
          ))}
        </ul>
      </InfoCard>
    </section>
  );
}
