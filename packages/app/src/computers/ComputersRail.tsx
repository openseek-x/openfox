import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";

import type { Device } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Avatar, Button, Drawer, Icon, Popover, cn } from "@openfox/ui";

import { isDesktop } from "../desktop";
import { ConnectComputerDialog } from "./ConnectComputer";
import { localOs, localState } from "./LocalComputerCard";
import {
  computerPartition,
  computerStatus,
  railStatus,
  sortComputers,
  statusLabel,
  systemText,
  visibilityText,
} from "./model";
import {
  CliNotice,
  ComputerStatusLabel,
  InfoCard,
  InfoRow,
  OsBadge,
  Overview,
  RuntimeIcons,
  RuntimeList,
  SectionTitle,
  StatusDot,
  formatDateTime,
} from "./parts";
import {
  isNotFound,
  useDevice,
  useDevices,
  useLocalStatus,
  useMe,
} from "./queries";

/** 悬停打开 / 离开关闭的延迟。 */
export const HOVER_OPEN_MS = 250;
export const HOVER_CLOSE_MS = 80;

/**
 * 侧栏左下角「电脑」入口：状态点 + 悬停弹层「电脑列表」，点击电脑打开只读详情抽屉。
 */
export function ComputersRailButton({ orgId }: { orgId: string }) {
  const t = useT();
  const devices = useDevices(orgId);
  const status = railStatus(devices.data ?? []);
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const [drawer, setDrawer] = useState<string | null>(null);
  const [connecting, setConnecting] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const clear = () => {
    if (timer.current !== undefined) clearTimeout(timer.current);
    timer.current = undefined;
  };
  const later = (next: boolean, delay: number) => {
    clear();
    timer.current = setTimeout(() => {
      timer.current = undefined;
      setOpen(next);
    }, delay);
  };
  useEffect(() => clear, []);

  const hover = {
    onMouseEnter: () => (open ? clear() : later(true, HOVER_OPEN_MS)),
    onMouseLeave: () => (open ? later(false, HOVER_CLOSE_MS) : clear()),
  };

  return (
    <>
      <button
        ref={anchor}
        type="button"
        aria-label={`${t("shell.rail.computers")}：${t(statusLabel[status])}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        data-status={status}
        onClick={() => {
          clear();
          setOpen((value) => !value);
        }}
        {...hover}
        className={cn(
          "relative flex size-9 items-center justify-center rounded-lg text-fg-secondary hover:bg-surface-hover",
          open && "bg-surface-active",
        )}
      >
        <Icon name="monitor" size={19} />
        <StatusDot
          status={status}
          className="absolute right-[7px] bottom-[7px] size-2 ring-2 ring-surface-card"
        />
      </button>
      <Popover
        open={open}
        onOpenChange={(next) => {
          clear();
          setOpen(next);
        }}
        anchorRef={anchor}
        placement="right-end"
        offset={8}
        aria-label={t("computers.popover.title")}
        className="w-[340px] p-0"
      >
        <div {...hover}>
          <ComputerListPopover
            orgId={orgId}
            devices={devices.data ?? []}
            loading={devices.isLoading}
            onOpenDevice={(id) => {
              clear();
              setOpen(false);
              setDrawer(id);
            }}
            onNavigate={() => {
              clear();
              setOpen(false);
            }}
            onConnect={() => {
              clear();
              setOpen(false);
              setConnecting(true);
            }}
          />
        </div>
      </Popover>
      {drawer && (
        <ComputerDrawer
          orgId={orgId}
          deviceId={drawer}
          onClose={() => setDrawer(null)}
        />
      )}
      <ConnectComputerDialog
        orgId={orgId}
        open={connecting}
        onClose={() => setConnecting(false)}
      />
    </>
  );
}

/** 列表区滚动渐隐：未到顶时顶部 32px、未到底时底部 32px 渐隐 */
function useScrollFade() {
  const ref = useRef<HTMLDivElement | null>(null);
  const [edges, setEdges] = useState({ top: false, bottom: false });
  const update = () => {
    const node = ref.current;
    if (!node) return;
    const top = node.scrollTop > 0;
    const bottom = node.scrollTop + node.clientHeight < node.scrollHeight - 1;
    setEdges((prev) =>
      prev.top === top && prev.bottom === bottom ? prev : { top, bottom },
    );
  };
  useEffect(update);
  const mask =
    edges.top || edges.bottom
      ? `linear-gradient(to bottom, ${edges.top ? "transparent" : "#000"} 0, #000 32px, #000 calc(100% - 32px), ${edges.bottom ? "transparent" : "#000"} 100%)`
      : undefined;
  return {
    ref,
    onScroll: update,
    style: mask ? { maskImage: mask, WebkitMaskImage: mask } : undefined,
    edges,
  };
}

function ComputerListPopover({
  orgId,
  devices,
  loading,
  onOpenDevice,
  onNavigate,
  onConnect,
}: {
  orgId: string;
  devices: Device[];
  loading: boolean;
  onOpenDevice: (id: string) => void;
  onNavigate: () => void;
  onConnect: () => void;
}) {
  const t = useT();
  const me = useMe();
  const desktop = isDesktop();
  const fade = useScrollFade();
  const { shared, personal } = useMemo(() => {
    const sorted = sortComputers(devices, me.data?.id);
    return {
      shared: sorted.filter((item) => computerPartition(item) === "shared"),
      personal: sorted.filter((item) => computerPartition(item) === "private"),
    };
  }, [devices, me.data?.id]);
  const empty = !desktop && shared.length === 0 && personal.length === 0;

  return (
    <div className="flex max-h-[min(560px,calc(100vh-80px))] flex-col">
      <div className="flex h-11 shrink-0 items-center justify-between pr-2 pl-3">
        <h2 className="text-body font-medium">
          {t("computers.popover.title")}
        </h2>
        <Button
          variant="secondary"
          size="28"
          leading={<Icon name="plus" />}
          onClick={onConnect}
        >
          {t("computers.connect")}
        </Button>
      </div>
      <div
        ref={fade.ref}
        onScroll={fade.onScroll}
        style={fade.style}
        data-fade-top={fade.edges.top || undefined}
        data-fade-bottom={fade.edges.bottom || undefined}
        className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-3 pb-3"
      >
        {loading ? (
          <p
            aria-busy="true"
            className="py-6 text-center text-body-sm text-fg-muted"
          >
            {t("common.loading")}
          </p>
        ) : empty ? (
          <p className="py-6 text-center text-body-sm text-fg-muted">
            {t("computers.empty.popover")}
          </p>
        ) : (
          <>
            {desktop && (
              <PopoverGroup label={t("computers.sections.thisMachine")}>
                <LocalPopoverCard orgId={orgId} onNavigate={onNavigate} />
              </PopoverGroup>
            )}
            {shared.length > 0 && (
              <PopoverGroup label={t("computers.sections.shared")}>
                {shared.map((device) => (
                  <PopoverRow
                    key={device.id}
                    device={device}
                    onClick={() => onOpenDevice(device.id)}
                  />
                ))}
              </PopoverGroup>
            )}
            {personal.length > 0 && (
              <PopoverGroup label={t("computers.sections.private")}>
                {personal.map((device) => (
                  <PopoverRow
                    key={device.id}
                    device={device}
                    onClick={() => onOpenDevice(device.id)}
                  />
                ))}
              </PopoverGroup>
            )}
          </>
        )}
      </div>
    </div>
  );
}

function PopoverGroup({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <section aria-label={label} className="flex flex-col gap-1.5">
      <h3 className="text-caption text-fg-muted">{label}</h3>
      {children}
    </section>
  );
}

const rowClass =
  "flex w-full items-center gap-3 rounded-xl border border-line-default px-3 py-2.5 text-left hover:bg-surface-hover";

function PopoverRow({
  device,
  onClick,
}: {
  device: Device;
  onClick: () => void;
}) {
  return (
    <button type="button" onClick={onClick} className={rowClass}>
      <OsBadge os={device.operating_system} size={32} />
      <span className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="truncate text-body font-medium text-fg-default">
          {device.name}
        </span>
        <span className="flex items-center gap-2">
          <ComputerStatusLabel device={device} availableLabel />
          {computerStatus(device) !== "offline" &&
            device.runtimes.length > 0 && (
              <RuntimeIcons runtimes={device.runtimes} max={4} />
            )}
        </span>
      </span>
      <Icon name="chevron-right" className="text-fg-muted" />
    </button>
  );
}

/** 本机卡片：本机与远程电脑无法对应到同一条设备记录，点击进入设置页的本机管理。「防止休眠」2c 接入。 */
function LocalPopoverCard({
  orgId,
  onNavigate,
}: {
  orgId: string;
  onNavigate: () => void;
}) {
  const t = useT();
  const navigate = useNavigate();
  const local = useLocalStatus();
  const state = localState(local.data, null);
  return (
    <button
      type="button"
      className={rowClass}
      onClick={() => {
        onNavigate();
        navigate(`/o/${orgId}/settings/computers`);
      }}
    >
      <OsBadge os={localOs()} size={32} />
      <span className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="truncate text-body font-medium text-fg-default">
          {t("computers.local.name")}
          <span className="font-normal text-fg-muted">
            {" "}
            {t("computers.local.suffix")}
          </span>
        </span>
        <span className="inline-flex items-center gap-1.5 text-body-sm text-fg-secondary">
          <StatusDot
            status={
              state === "available"
                ? "online"
                : state === "starting" || state === "unreachable"
                  ? "noRuntime"
                  : "offline"
            }
          />
          {t(`computers.local.${state}`)}
        </span>
      </span>
      <Icon name="chevron-right" className="text-fg-muted" />
    </button>
  );
}

/** 电脑详情抽屉（页 18 C1–C7）：只读；离线时显示「修复并重连」（2c 开放）。电脑不可见时自动关闭。 */
export function ComputerDrawer({
  orgId,
  deviceId,
  onClose,
}: {
  orgId: string;
  deviceId: string;
  onClose: () => void;
}) {
  const { t, locale } = useI18n();
  const detail = useDevice(orgId, deviceId);
  const device = detail.data;
  const gone = isNotFound(detail.error);
  useEffect(() => {
    if (gone) onClose();
  }, [gone, onClose]);

  // 安装指南弹窗叠在抽屉上时，Esc 只关闭弹窗（两者都在 document 上监听，抽屉的监听先注册）
  const close = () => {
    if (
      document.querySelectorAll('[role="dialog"][aria-modal="true"]').length > 1
    )
      return;
    onClose();
  };

  return (
    <Drawer open onClose={close} title={t("computers.drawer.title")}>
      {!device ? (
        <div className="px-6 py-6 text-body-sm text-fg-muted" aria-busy="true">
          {detail.isError ? t("computers.loadFailed") : t("common.loading")}
        </div>
      ) : (
        <div className="flex flex-col gap-8 px-6 pt-3 pb-6">
          <header className="flex items-center gap-4">
            <OsBadge os={device.operating_system} size={48} />
            <div className="flex min-w-0 flex-col gap-1">
              <h3 className="truncate text-heading font-medium">
                {device.name}
              </h3>
              <div className="flex items-center gap-2 text-body-sm text-fg-muted">
                <ComputerStatusLabel device={device} availableLabel />
                <span>{systemText(device)}</span>
              </div>
            </div>
          </header>
          <CliNotice device={device} orgId={orgId} />
          <section className="flex flex-col gap-2">
            <SectionTitle>{t("computers.detail.overview")}</SectionTitle>
            <Overview device={device} showAgents={false} />
          </section>
          <section className="flex flex-col gap-2">
            <SectionTitle>{t("computers.detail.runtimes")}</SectionTitle>
            <RuntimeList device={device} />
          </section>
          <section className="flex flex-col gap-2">
            <SectionTitle>{t("computers.detail.info")}</SectionTitle>
            <InfoCard>
              <InfoRow label={t("computers.detail.visibility")}>
                {visibilityText(device, t)}
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
              <InfoRow label={t("computers.detail.uuid")}>
                <code className="font-mono text-code">{device.id}</code>
              </InfoRow>
              <InfoRow label={t("computers.detail.createdAt")}>
                {formatDateTime(device.created_at, locale)}
              </InfoRow>
            </InfoCard>
          </section>
        </div>
      )}
    </Drawer>
  );
}
