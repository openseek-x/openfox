import { DeviceSettingsEditor } from "./DeviceSettingsEditor";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import {
  isApiError,
  type Device,
  type DeviceDetail,
  type DeviceGrantScope,
  type UpdateDeviceInput,
} from "@openfox/api";
import { useI18n, useT, type Translate } from "@openfox/i18n";
import {
  Avatar,
  Button,
  Callout,
  ConfirmDialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuLabel,
  MenuSeparator,
  Skeleton,
  TextInput,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { describeError, EmptyState, useCanManage } from "../settings/common";
import { InlineEdit } from "../settings/InlineEdit";
import { formatRelativeTime } from "../tasks/primitives/dates";
import { useDeviceCommand } from "./commands";
import {
  OwnerOnlyButton,
  StorageSection,
  commandRequestError,
  commandSettledError,
} from "./ComputerStorage";
import { systemText, visibilityText } from "./model";
import {
  CliNotice,
  ComputerStatusLabel,
  InfoCard,
  InfoRow,
  OsBadge,
  Overview,
  RuntimeList,
  SectionTitle,
  StatusDot,
  formatDateTime,
} from "./parts";
import { isNotFound, useApplyDevice, useDevice, useMe } from "./queries";

const MAX_SLOTS_MIN = 1;
const MAX_SLOTS_MAX = 256;

/** 设备写操作的错误文案：版本冲突、仍有绑定 Agent 等有专门提示，其余沿用设置页通用规则。 */
function deviceError(error: unknown, t: Translate): string {
  if (isApiError(error, "VERSION_CONFLICT"))
    return t("settings.errors.versionConflict");
  if (isApiError(error, "DEVICE_HAS_ACTIVE_AGENTS"))
    return t("computers.errors.hasAgents");
  if (isApiError(error) && error.status === 404)
    return t("computers.detail.notFound");
  return describeError(error, t);
}

/** 电脑详情整页：完整管理，写操作仅所有者（从组织移除另允许组织管理员）。 */
export function ComputerDetailPage({ deviceId }: { deviceId: string }) {
  const t = useT();
  const { orgId = "" } = useParams();
  const detail = useDevice(orgId, deviceId);
  const me = useMe();
  const back = `/o/${orgId}/settings/computers`;

  const backLink = (
    <Link
      to={back}
      className="inline-flex h-7 items-center gap-1 self-start rounded-md pr-2 pl-1 text-body-sm text-fg-muted hover:bg-surface-hover hover:text-fg-default"
    >
      <Icon name="chevron-left" size={14} />
      {t("computers.detail.back")}
    </Link>
  );

  if (detail.isLoading)
    return (
      <div
        aria-busy="true"
        className="mx-auto flex max-w-[908px] flex-col gap-6 px-6 pt-6 pb-10"
      >
        {backLink}
        <Skeleton className="h-12 w-72" />
        <Skeleton className="h-20 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  if (!detail.data)
    return (
      <div className="mx-auto flex max-w-[908px] flex-col gap-6 px-6 pt-6 pb-10">
        {backLink}
        <EmptyState
          title={
            isNotFound(detail.error)
              ? t("computers.detail.notFound")
              : t("computers.loadFailed")
          }
          action={
            isNotFound(detail.error) ? undefined : (
              <Button variant="outline" onClick={() => void detail.refetch()}>
                {t("common.retry")}
              </Button>
            )
          }
        />
      </div>
    );
  return (
    <DetailBody
      orgId={orgId}
      device={detail.data}
      meId={me.data?.id}
      backLink={backLink}
      back={back}
    />
  );
}

function DetailBody({
  orgId,
  device,
  meId,
  backLink,
  back,
}: {
  orgId: string;
  device: DeviceDetail;
  meId: string | undefined;
  backLink: React.ReactNode;
  back: string;
}) {
  const { t, locale } = useI18n();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const applyDevice = useApplyDevice(orgId);
  const canManageOrg = useCanManage(orgId);
  const isOwner = meId !== undefined && device.owner_user_id === meId;
  const [renaming, setRenaming] = useState(false);
  const [dialog, setDialog] = useState<
    "removeFromOrg" | "removeMachine" | "unbind" | null
  >(null);

  const refreshDevice = () => {
    void queryClient.invalidateQueries({
      queryKey: keys.device(orgId, device.id),
    });
    void queryClient.invalidateQueries({
      queryKey: keys.devices(orgId),
      exact: true,
    });
  };

  // 重新检测 Runtime（runtime_probe）：daemon 执行后会立刻发一次心跳刷新 capabilities
  const probe = useDeviceCommand(orgId, device.id, "runtime_probe", {
    onSettled: (command) => {
      const error = commandSettledError(command, t);
      toast(error ?? t("connect.probe.done"), error ? { tone: "error" } : {});
      refreshDevice();
    },
    onError: (error) => toast(commandRequestError(error, t), { tone: "error" }),
  });
  const onWriteError = (error: unknown) => {
    toast(deviceError(error, t), { tone: "error" });
    // 版本冲突、对象不存在：拉取最新数据
    if (isApiError(error) && [404, 409].includes(error.status)) refreshDevice();
  };

  const update = useMutation({
    mutationFn: (patch: Omit<UpdateDeviceInput, "settings_version">) =>
      api.devices.update(orgId, device.id, {
        ...patch,
        settings_version: device.settings_version,
      }),
    onSuccess: (saved) => {
      applyDevice(saved);
      toast(t("computers.detail.saved"));
    },
    onError: onWriteError,
  });

  const scope = useMutation({
    mutationFn: (next: { scope: DeviceGrantScope; spaceId?: string }) =>
      api.devices.updateScope(orgId, device.id, {
        scope: next.scope,
        version: device.permission_version,
        ...(next.scope === "PERSONAL" && meId ? { user_id: meId } : {}),
        ...(next.scope === "SPACE" && next.spaceId
          ? { space_id: next.spaceId }
          : {}),
      }),
    onSuccess: (saved) => {
      applyDevice(saved);
      toast(t("computers.visibility.updated"));
    },
    onError: onWriteError,
  });

  /** 电脑离开本组织后：回到列表，清掉详情缓存，刷新列表与 Agent 相关查询 */
  const leave = () => {
    navigate(back, { replace: true });
    queryClient.removeQueries({ queryKey: keys.device(orgId, device.id) });
    void queryClient.invalidateQueries({ queryKey: keys.devices(orgId) });
    void queryClient.invalidateQueries({
      queryKey: keys.agentDirectory(orgId),
    });
    void queryClient.invalidateQueries({
      queryKey: ["org", orgId, "space-agents"],
    });
    void queryClient.invalidateQueries({ queryKey: keys.agents(orgId) });
  };

  const removeFromOrg = useMutation({
    mutationFn: () => api.devices.removeFromOrganization(orgId, device.id),
    onSuccess: (result) => {
      setDialog(null);
      toast(
        t("computers.detail.removedFromOrg", {
          agents: result.archived_agents,
          runs: result.canceled_runs,
        }),
      );
      leave();
    },
    onError: onWriteError,
  });

  const unbind = useMutation({
    mutationFn: () => api.devices.unbindAgents(orgId, device.id),
    onSuccess: (result) => {
      setDialog(null);
      toast(
        t("computers.detail.unbound", {
          agents: result.archived_agents,
          runs: result.canceled_runs,
        }),
      );
      refreshDevice();
      void queryClient.invalidateQueries({
        queryKey: keys.agentDirectory(orgId),
      });
      void queryClient.invalidateQueries({
        queryKey: ["org", orgId, "space-agents"],
      });
      void queryClient.invalidateQueries({ queryKey: keys.agents(orgId) });
    },
    onError: onWriteError,
  });

  return (
    <div className="mx-auto flex max-w-[908px] flex-col gap-8 px-6 pt-6 pb-10">
      <div className="flex flex-col gap-5">
        {backLink}
        <header className="flex items-center gap-4">
          <OsBadge os={device.operating_system} size={48} />
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            {renaming ? (
              <InlineEdit
                value={device.name}
                label={t("computers.detail.nameLabel")}
                maxLength={100}
                className="h-9 text-heading font-bold"
                onCommit={(name) => update.mutate({ name })}
                onDone={() => setRenaming(false)}
              />
            ) : (
              <div className="flex min-w-0 items-center gap-1.5">
                <h1 className="truncate text-heading font-bold text-fg-default">
                  {update.isPending && update.variables.name
                    ? update.variables.name
                    : device.name}
                </h1>
                {isOwner && (
                  <IconButton
                    label={t("computers.detail.rename")}
                    icon={<Icon name="pencil" />}
                    size="28"
                    onClick={() => setRenaming(true)}
                  />
                )}
              </div>
            )}
            <div className="flex items-center gap-2 text-body-sm text-fg-muted">
              <ComputerStatusLabel device={device} />
              <span aria-hidden>·</span>
              <span>{systemText(device)}</span>
            </div>
          </div>
          <Menu
            label={t("computers.detail.more")}
            placement="bottom-end"
            trigger={({ ref, toggle, open }) => (
              <IconButton
                ref={ref}
                label={t("computers.detail.more")}
                icon={<Icon name="ellipsis" />}
                variant="outline"
                size="28"
                aria-expanded={open}
                onClick={toggle}
              />
            )}
          >
            <MenuItem
              icon={<Icon name="refresh" />}
              disabled={!isOwner || probe.running}
              disabledReason={
                isOwner ? undefined : t("connect.probe.ownerOnly")
              }
              onSelect={() => probe.start({})}
            >
              {t("computers.detail.redetect")}
            </MenuItem>
            <MenuSeparator />
            <MenuItem
              icon={<Icon name="log-out" />}
              disabled={!isOwner && !canManageOrg}
              disabledReason={t("settings.errors.forbidden")}
              onSelect={() => setDialog("removeFromOrg")}
            >
              {t("computers.detail.removeFromOrg")}
            </MenuItem>
            <MenuItem
              icon={<Icon name="trash-2" />}
              danger
              disabled={!isOwner}
              disabledReason={t("computers.detail.ownerOnly")}
              onSelect={() => setDialog("removeMachine")}
            >
              {t("computers.detail.removeMachine")}
            </MenuItem>
          </Menu>
        </header>
        <CliNotice device={device} orgId={orgId} />
      </div>

      <section className="flex flex-col gap-2">
        <SectionTitle>{t("computers.detail.overview")}</SectionTitle>
        <Overview device={device} />
      </section>

      <section className="flex flex-col gap-2">
        <SectionTitle
          action={
            <span className="flex items-center gap-2">
              {device.runtimes_checked_at && (
                <span className="text-caption text-fg-muted">
                  {t("connect.probe.checkedAt", {
                    time: formatRelativeTime(
                      device.runtimes_checked_at,
                      locale,
                    ),
                  })}
                </span>
              )}
              <OwnerOnlyButton
                isOwner={isOwner}
                reason={t("connect.probe.ownerOnly")}
              >
                <Button
                  variant="ghost"
                  size="28"
                  leading={<Icon name="refresh" />}
                  loading={probe.running}
                  disabled={!isOwner || probe.running}
                  onClick={() => probe.start({})}
                >
                  {t("computers.detail.redetect")}
                </Button>
              </OwnerOnlyButton>
            </span>
          }
        >
          {t("computers.detail.runtimes")}
        </SectionTitle>
        <RuntimeList device={device} />
      </section>

      <section className="flex flex-col gap-2">
        <SectionTitle
          action={
            isOwner && (
              <Button
                variant="outline"
                size="28"
                leading={<Icon name="unlink" />}
                disabled={device.agent_count === 0}
                onClick={() => setDialog("unbind")}
              >
                {t("computers.detail.unbindAll")}
              </Button>
            )
          }
        >
          {t("computers.detail.agents")}
        </SectionTitle>
        {device.bound_agents.length === 0 ? (
          <p className="rounded-xl border border-dashed border-line-default px-4 py-3 text-body-sm text-fg-muted">
            {t("computers.detail.noAgents")}
          </p>
        ) : (
          <InfoCard>
            <ul aria-label={t("computers.detail.agents")}>
              {device.bound_agents.map((agent) => (
                <li
                  key={agent.id}
                  className="flex h-12 items-center gap-3 border-b border-line-soft px-4 last:border-b-0"
                >
                  <Avatar name={agent.name} kind="agent" size={24} />
                  <span className="flex min-w-0 flex-1 items-center gap-1.5">
                    <span className="truncate text-body font-medium">
                      {agent.name}
                    </span>
                    {agent.title && (
                      <span className="truncate text-body-sm text-fg-muted">
                        - {agent.title}
                      </span>
                    )}
                  </span>
                  <span className="inline-flex w-20 items-center gap-1.5 text-body-sm text-fg-secondary">
                    <StatusDot status={agent.online ? "online" : "offline"} />
                    {agent.online
                      ? t("computers.status.online")
                      : t("computers.status.offline")}
                  </span>
                  <span className="w-16 text-body-sm text-fg-secondary">
                    {agent.visibility === "SHARED"
                      ? t("computers.detail.agentShared")
                      : t("computers.visibility.private")}
                  </span>
                  <span className="flex w-40 min-w-0 items-center gap-1.5 text-body-sm text-fg-secondary">
                    {agent.owner_email && (
                      <>
                        <Avatar name={agent.owner_email} size={20} />
                        <span className="truncate">{agent.owner_email}</span>
                      </>
                    )}
                  </span>
                </li>
              ))}
            </ul>
          </InfoCard>
        )}
      </section>

      <section className="flex flex-col gap-2">
        <SectionTitle>{t("computers.detail.cli")}</SectionTitle>
        <InfoCard>
          <InfoRow label={t("computers.detail.version")}>
            {device.daemon_version || t("computers.detail.notAvailable")}
          </InfoRow>
          <InfoRow label={t("computers.detail.uuid")}>
            <code className="font-mono text-code">{device.id}</code>
          </InfoRow>
          <InfoRow
            label={t("computers.detail.startup")}
            description={t("computers.detail.startupDesc")}
          >
            {t("computers.detail.notAvailable")}
          </InfoRow>
          <InfoRow label={t("computers.detail.keepAlive")}>
            {t("computers.detail.notAvailable")}
          </InfoRow>
          <InfoRow label={t("computers.detail.logs")}>
            {t("computers.detail.notAvailable")}
          </InfoRow>
        </InfoCard>
      </section>

      <section className="flex flex-col gap-2">
        <SectionTitle>{t("computers.detail.runSettings")}</SectionTitle>
        <InfoCard>
          <InfoRow
            label={t("computers.detail.maxSlots")}
            description={t("computers.detail.maxSlotsHint")}
          >
            <MaxSlotsInput
              value={device.max_slots}
              disabled={!isOwner || update.isPending}
              onCommit={(max_slots) => update.mutate({ max_slots })}
            />
          </InfoRow>
          <InfoRow label={t("connect.devicePolicy.protection")}>
            <DeviceSettingsEditor
              section="protection"
              orgId={orgId}
              deviceId={device.id}
              disabled={!isOwner || !device.online}
            />
          </InfoRow>
        </InfoCard>
      </section>

      <section className="flex flex-col gap-2">
        <SectionTitle>{t("computers.detail.storage")}</SectionTitle>
        <StorageSection orgId={orgId} device={device} isOwner={isOwner} />
      </section>

      <section className="flex flex-col gap-2">
        <SectionTitle>{t("computers.detail.info")}</SectionTitle>
        <InfoCard>
          <InfoRow
            label={t("computers.detail.visibility")}
            description={t("computers.detail.visibilityDesc")}
          >
            <VisibilityMenu
              orgId={orgId}
              device={device}
              disabled={!isOwner || scope.isPending}
              onSelect={(next, spaceId) =>
                scope.mutate({ scope: next, spaceId })
              }
            />
          </InfoRow>
          <InfoRow label={t("computers.detail.owner")}>
            {device.owner_email ? (
              <>
                <Avatar name={device.owner_email} size={20} />
                <span className="truncate">{device.owner_email}</span>
              </>
            ) : (
              t("computers.detail.notAvailable")
            )}
          </InfoRow>
          <InfoRow label={t("computers.detail.os")}>
            {systemText(device)}
          </InfoRow>
          {device.timezone && (
            <InfoRow label={t("computers.detail.timezone")}>
              {device.timezone}
            </InfoRow>
          )}
          <InfoRow label={t("computers.detail.createdAt")}>
            {formatDateTime(device.created_at, locale)}
          </InfoRow>
        </InfoCard>
      </section>

      <ConfirmDialog
        open={dialog === "removeFromOrg"}
        danger
        title={t("computers.detail.removeFromOrgTitle")}
        description={t("computers.detail.removeFromOrgDesc")}
        confirmLabel={t("computers.detail.removeFromOrg")}
        loading={removeFromOrg.isPending}
        onCancel={() => setDialog(null)}
        onConfirm={() => removeFromOrg.mutate()}
      />
      <ConfirmDialog
        open={dialog === "unbind"}
        danger
        title={t("computers.detail.unbindTitle")}
        description={t("computers.detail.unbindDesc", {
          count: device.agent_count,
        })}
        confirmLabel={t("computers.detail.unbindAll")}
        loading={unbind.isPending}
        onCancel={() => setDialog(null)}
        onConfirm={() => unbind.mutate()}
      />
      {dialog === "removeMachine" && (
        <RemoveMachineDialog
          orgId={orgId}
          device={device}
          onClose={() => setDialog(null)}
          onRemoved={() => {
            setDialog(null);
            toast(t("computers.detail.machineRemoved"));
            leave();
          }}
        />
      )}
    </div>
  );
}

/** 移除机器：先查阻断项，有绑定 Agent 时列出名字并禁用确认。 */
function RemoveMachineDialog({
  orgId,
  device,
  onClose,
  onRemoved,
}: {
  orgId: string;
  device: Device;
  onClose: () => void;
  onRemoved: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const blockers = useQuery({
    queryKey: keys.deviceRemovalBlockers(orgId, device.id),
    queryFn: () => api.devices.removalBlockers(orgId, device.id),
    staleTime: 0,
  });
  const remove = useMutation({
    mutationFn: () => api.devices.remove(orgId, device.id),
    onSuccess: onRemoved,
    onError: (error) => {
      toast(deviceError(error, t), { tone: "error" });
      if (isApiError(error, "DEVICE_HAS_ACTIVE_AGENTS"))
        void blockers.refetch();
    },
  });
  const count = blockers.data?.count ?? 0;
  return (
    <ConfirmDialog
      open
      danger
      title={t("computers.detail.removeMachine")}
      description={t("computers.detail.removeMachineDesc")}
      confirmLabel={t("computers.detail.removeMachine")}
      confirmDisabled={!blockers.data || count > 0}
      loading={remove.isPending}
      onCancel={onClose}
      onConfirm={() => remove.mutate()}
    >
      <div className="flex flex-col gap-3">
        <p className="text-body-sm text-fg-muted">
          {t("computers.detail.removeMachineHint")}
        </p>
        {blockers.isLoading ? (
          <p aria-busy="true" className="text-body-sm text-fg-muted">
            {t("computers.detail.checkingBlockers")}
          </p>
        ) : blockers.isError ? (
          <Callout tone="danger">{deviceError(blockers.error, t)}</Callout>
        ) : count > 0 ? (
          <Callout
            tone="warning"
            title={t("computers.detail.blockers", { count })}
          >
            <p>
              {blockers.data!.agents.map((agent) => agent.name).join("、")}
              {count > blockers.data!.agents.length && "…"}
            </p>
            <p className="mt-1 text-body-sm">
              {t("computers.detail.blockersHint")}
            </p>
          </Callout>
        ) : null}
      </div>
    </ConfirmDialog>
  );
}

/** 最大并发运行任务数：失焦或回车提交；不合法时恢复原值并提示。 */
function MaxSlotsInput({
  value,
  disabled,
  onCommit,
}: {
  value: number;
  disabled: boolean;
  onCommit: (next: number) => void;
}) {
  const t = useT();
  const toast = useToast();
  const [draft, setDraft] = useState(String(value));
  useEffect(() => setDraft(String(value)), [value]);
  const commit = () => {
    const next = Number(draft.trim());
    if (
      !Number.isInteger(next) ||
      next < MAX_SLOTS_MIN ||
      next > MAX_SLOTS_MAX
    ) {
      toast(t("computers.detail.maxSlotsInvalid"), { tone: "error" });
      setDraft(String(value));
      return;
    }
    if (next !== value) onCommit(next);
  };
  return (
    <TextInput
      aria-label={t("computers.detail.maxSlots")}
      inputMode="numeric"
      value={draft}
      disabled={disabled}
      onChange={(event) => setDraft(event.target.value)}
      onBlur={commit}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          event.currentTarget.blur();
        }
      }}
      className="w-24"
    />
  );
}

/** 可见性下拉：私有 / 组织 / 空间（单选一个空间）；只有所有者可改。 */
function VisibilityMenu({
  orgId,
  device,
  disabled,
  onSelect,
}: {
  orgId: string;
  device: Device;
  disabled: boolean;
  onSelect: (scope: DeviceGrantScope, spaceId?: string) => void;
}) {
  const t = useT();
  const api = useApi();
  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
    enabled: !disabled,
  });
  const label = visibilityText(device, t);
  if (disabled)
    return (
      <span title={t("computers.detail.ownerOnlyVisibility")}>{label}</span>
    );
  return (
    <Menu
      label={t("computers.visibility.label")}
      placement="bottom-end"
      trigger={({ ref, toggle, open }) => (
        <button
          ref={ref}
          type="button"
          aria-haspopup="menu"
          aria-expanded={open}
          aria-label={`${t("computers.visibility.label")}：${label}`}
          onClick={toggle}
          className="flex h-8 min-w-32 items-center justify-between gap-2 rounded-lg border border-line-default bg-surface-card px-2.5 text-body text-fg-default hover:bg-surface-hover"
        >
          <span className="truncate">{label}</span>
          <Icon name="chevron-down" size={14} className="text-fg-muted" />
        </button>
      )}
    >
      <MenuItem
        selected={device.grant_scope === "PERSONAL"}
        description={t("computers.visibility.privateDesc")}
        onSelect={() =>
          device.grant_scope !== "PERSONAL" && onSelect("PERSONAL")
        }
      >
        {t("computers.visibility.private")}
      </MenuItem>
      <MenuItem
        selected={device.grant_scope === "ORGANIZATION"}
        description={t("computers.visibility.organizationDesc")}
        onSelect={() =>
          device.grant_scope !== "ORGANIZATION" && onSelect("ORGANIZATION")
        }
      >
        {t("computers.visibility.organization")}
      </MenuItem>
      <MenuSeparator />
      <MenuLabel>
        {t("computers.visibility.space")} ·{" "}
        {t("computers.visibility.spaceDesc")}
      </MenuLabel>
      {(spaces.data ?? []).length === 0 ? (
        <MenuItem disabled>{t("computers.visibility.noSpaces")}</MenuItem>
      ) : (
        (spaces.data ?? []).map((space) => {
          const current =
            device.grant_scope === "SPACE" &&
            device.grant_space_id === space.id;
          return (
            <MenuItem
              key={space.id}
              selected={current}
              icon={<Avatar name={space.name} kind="agent" size={16} />}
              onSelect={() => !current && onSelect("SPACE", space.id)}
            >
              {space.name}
            </MenuItem>
          );
        })
      )}
    </Menu>
  );
}
