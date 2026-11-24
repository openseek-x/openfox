import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useI18n, useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Dialog,
  Select,
  Switch,
  useToast,
} from "@openfox/ui";

import {
  desktopBridge,
  desktopLocalStatusKey,
  type DesktopLocalTarget,
} from "../desktop";
import { isLocalDevice } from "./localRegistration";
import { formatDateTime, InfoCard, InfoRow, SectionTitle } from "./parts";
import {
  useTaskSleepPreference,
  useLocalStatus,
  useServerCenterUrl,
} from "./queries";

export function useLocalService(
  orgId: string,
  deviceId: string,
  isOwner: boolean,
) {
  const bridge = desktopBridge();
  const local = useLocalStatus();
  const center = useServerCenterUrl(orgId, Boolean(bridge));
  const matches = isLocalDevice(
    local.data,
    orgId,
    deviceId,
    center.data?.center_url,
  );
  const supported = Boolean(
    bridge?.getLocalService &&
    bridge.setLocalStartup &&
    bridge.readLocalLogs &&
    bridge.localDaemonAction,
  );
  const enabled =
    supported && bridge?.platform === "darwin" && matches && isOwner;
  const target: DesktopLocalTarget = {
    orgId,
    deviceId,
    centerUrl: center.data?.center_url ?? "",
  };
  const serviceKey = [
    "desktop",
    "local-service",
    orgId,
    deviceId,
    target.centerUrl,
  ];
  const service = useQuery({
    queryKey: serviceKey,
    queryFn: () => bridge!.getLocalService!(target),
    enabled,
    retry: false,
  });
  const reason = !bridge
    ? "desktopOnly"
    : bridge.platform !== "darwin"
      ? "macOnly"
      : !supported
        ? "upgrade"
        : !matches
          ? "notLocal"
          : !isOwner
            ? "ownerOnly"
            : "failed";
  const ready =
    enabled &&
    service.data?.supported &&
    service.data.installed &&
    !service.isError;
  return {
    enabled,
    ready,
    reason,
    running: ["RUNNING", "STARTING", "RECONCILE_REQUIRED"].includes(
      local.data?.daemon.state ?? "",
    ),
    local,
    service,
    serviceKey,
    bridge,
    target,
  } as const;
}

type LocalService = ReturnType<typeof useLocalService>;

export function LocalServiceDetails({ state }: { state: LocalService }) {
  const { enabled, ready, reason, local, service, serviceKey, bridge, target } =
    state;
  const { t, locale } = useI18n();
  const toast = useToast();
  const client = useQueryClient();
  const [confirmStop, setConfirmStop] = useState(false);
  const [logs, setLogs] = useState(false);
  const startup = useMutation({
    mutationFn: async (mode: "login" | "manual") => {
      if (!enabled) throw Error("unavailable");
      const result = await bridge!.setLocalStartup!({ ...target, mode });
      if (result.startup !== mode) throw Error("startup did not change");
      return result;
    },
    onSuccess: (result) => client.setQueryData(serviceKey, result),
    onError: () => {
      toast(t("computers.localService.failed"), { tone: "error" });
      void service.refetch();
    },
  });
  const action = useMutation({
    mutationFn: (value: "start" | "stop" | "restart") => {
      if (!enabled) throw Error("unavailable");
      return bridge!.localDaemonAction!({ ...target, action: value });
    },
    onSuccess: (result) => {
      client.setQueryData(desktopLocalStatusKey, result);
      setConfirmStop(false);
    },
    onError: () => {
      toast(t("computers.local.actionFailed"), { tone: "error" });
      void local.refetch();
    },
  });
  const keep = useMutation({
    mutationFn: (enabled: boolean) =>
      bridge!.setLocalKeepRunning!({ ...target, enabled }),
    onSuccess: (result) => client.setQueryData(serviceKey, result),
    onError: () => toast(t("computers.localService.failed"), { tone: "error" }),
  });
  const busy = startup.isPending || action.isPending || keep.isPending;
  const running = state.running;
  return (
    <>
      <InfoRow
        label={t("computers.detail.startup")}
        description={t("computers.localService.startupHint")}
      >
        {ready ? (
          <Select
            role="combobox"
            aria-label={t("computers.detail.startup")}
            value={service.data!.startup}
            disabled={busy}
            options={[
              { value: "login", label: t("computers.localService.login") },
              { value: "manual", label: t("computers.localService.manual") },
            ]}
            onChange={(value) => startup.mutate(value as "login" | "manual")}
          />
        ) : (
          <span>
            {enabled && service.isPending
              ? t("common.loading")
              : enabled && service.data && !service.data.installed
                ? t("computers.localService.notInstalled")
                : t(`computers.localService.${reason}`)}
          </span>
        )}
      </InfoRow>
      <InfoRow
        label={t("computers.detail.keepAlive")}
        description={t("computers.localService.keepAliveHint")}
      >
        {ready ? (
          <div className="flex flex-wrap items-center gap-2">
            <span>
              {running
                ? t("computers.localService.running")
                : t("computers.localService.notRunning")}
            </span>
            <Button
              size="28"
              variant="outline"
              disabled={busy}
              onClick={() => action.mutate(running ? "restart" : "start")}
            >
              {t(running ? "computers.local.restart" : "computers.local.start")}
            </Button>
            <Button
              size="28"
              variant="danger-outline"
              disabled={busy}
              onClick={() => setConfirmStop(true)}
            >
              {t("computers.local.stop")}
            </Button>
          </div>
        ) : (
          t(`computers.localService.${reason}`)
        )}
      </InfoRow>
      {ready &&
        bridge?.setLocalKeepRunning &&
        typeof service.data?.keep_service_running === "boolean" && (
          <InfoRow
            label={t("computers.localService.keepOnQuit")}
            description={t("computers.localService.keepOnQuitHint")}
          >
            <Switch
              label={t("computers.localService.keepOnQuit")}
              checked={service.data.keep_service_running}
              disabled={busy}
              onCheckedChange={(value) => keep.mutate(value)}
            />
          </InfoRow>
        )}
      {ready && (
        <InfoRow label={t("computers.localService.startedAt")}>
          {running && local.data?.daemon.started_at
            ? formatDateTime(local.data.daemon.started_at, locale)
            : t("computers.detail.notAvailable")}
        </InfoRow>
      )}
      <InfoRow
        label={t("computers.detail.logs")}
        description={ready ? service.data?.log_path : undefined}
      >
        {enabled ? (
          <Button size="28" variant="outline" onClick={() => setLogs(true)}>
            {t("computers.localService.viewLogs")}
          </Button>
        ) : (
          t(`computers.localService.${reason}`)
        )}
      </InfoRow>
      <ConfirmDialog
        open={confirmStop && Boolean(ready)}
        danger
        title={t("computers.local.stopTitle")}
        description={t("computers.local.stopDesc")}
        confirmLabel={t("computers.local.stop")}
        loading={action.isPending}
        onCancel={() => setConfirmStop(false)}
        onConfirm={() => action.mutate("stop")}
      />
      {logs && enabled && (
        <LocalLogs target={target} onClose={() => setLogs(false)} />
      )}
    </>
  );
}

export function LocalStorageLocation({ state }: { state: LocalService }) {
  const t = useT();
  const toast = useToast();
  const client = useQueryClient();
  const move = useMutation({
    mutationFn: () => state.bridge!.moveLocalStorage!(state.target),
    onSuccess: (result) => {
      if (result) {
        client.setQueryData(state.serviceKey, result);
        toast(t("computers.localService.moved"));
      }
    },
    onError: (error) => {
      const message = String(error);
      const reason = message.includes("storage_busy")
        ? "moveBusy"
        : message.includes("storage_different_volume")
          ? "moveVolume"
          : "moveFailed";
      toast(t(`computers.localService.${reason}`), { tone: "error" });
      void state.service.refetch();
    },
  });
  const open = useMutation({
    mutationFn: () => state.bridge!.openLocalStorage!(state.target),
    onError: () =>
      toast(t("computers.localService.openFailed"), { tone: "error" }),
  });
  if (!state.ready || !state.service.data?.storage_path) return null;
  return (
    <InfoCard>
      <InfoRow
        label={t("computers.localService.storageLocation")}
        description={
          <code className="break-all">{state.service.data.storage_path}</code>
        }
      >
        {state.bridge?.openLocalStorage && (
          <Button
            size="28"
            variant="outline"
            disabled={open.isPending}
            onClick={() => open.mutate()}
          >
            {t("computers.localService.openStorage")}
          </Button>
        )}
        {state.bridge?.moveLocalStorage && (
          <Button
            size="28"
            variant="outline"
            disabled={move.isPending || state.running}
            onClick={() => move.mutate()}
          >
            {t("computers.localService.moveStorage")}
          </Button>
        )}
      </InfoRow>
      {state.bridge?.moveLocalStorage && state.running && (
        <p className="px-4 py-2 text-caption text-fg-muted">
          {t("computers.localService.moveStopFirst")}
        </p>
      )}
    </InfoCard>
  );
}

export function LocalPowerSettings({ state }: { state: LocalService }) {
  const { t, locale } = useI18n();
  const toast = useToast();
  const client = useQueryClient();
  const taskSleep = useTaskSleepPreference();
  const taskPreference = state.local.data?.prevent_sleep;
  const [minutes, setMinutes] = useState(60);
  const until = state.service.data?.awake_until;
  const { refetch } = state.service;
  useEffect(() => {
    if (!state.ready || !until) return;
    const timer = setTimeout(
      () => void refetch(),
      Math.max(0, until - Date.now()) + 50,
    );
    return () => clearTimeout(timer);
  }, [until, state.ready, refetch]);
  const awake = useMutation({
    mutationFn: (duration: number) =>
      state.bridge!.setLocalAwake!({ ...state.target, minutes: duration }),
    onSuccess: (result) => client.setQueryData(state.serviceKey, result),
    onError: () => toast(t("connect.sleep.failed"), { tone: "error" }),
  });
  if (!state.ready || !state.bridge?.setLocalAwake || until === undefined)
    return null;
  const active = until > Date.now();
  return (
    <section className="flex flex-col gap-2">
      <SectionTitle>{t("connect.sleep.label")}</SectionTitle>
      <InfoCard>
        {taskPreference && state.bridge?.setPreventSleep && (
          <InfoRow label={t("connect.sleep.desc")}>
            <Switch
              label={t("connect.sleep.desc")}
              checked={taskPreference.enabled}
              disabled={taskSleep.isPending}
              onCheckedChange={(value) => taskSleep.mutate(value)}
            />
          </InfoRow>
        )}
        <InfoRow
          label={t("computers.localService.timedAwake")}
          description={t("computers.localService.timedAwakeHint")}
        >
          {active ? (
            <span>
              {t("computers.localService.until", {
                time: new Intl.DateTimeFormat(locale, {
                  hour: "numeric",
                  minute: "2-digit",
                }).format(until),
              })}
            </span>
          ) : (
            <Select
              aria-label={t("computers.localService.duration")}
              value={String(minutes)}
              disabled={awake.isPending}
              options={[60, 120, 240, 480].map((value) => ({
                value: String(value),
                label: t("computers.localService.hours", { count: value / 60 }),
              }))}
              onChange={(value) => setMinutes(Number(value))}
            />
          )}
          <Switch
            label={t("computers.localService.timedAwake")}
            checked={active}
            disabled={awake.isPending}
            onCheckedChange={(next) => awake.mutate(next ? minutes : 0)}
          />
        </InfoRow>
      </InfoCard>
    </section>
  );
}

function LocalLogs({
  target,
  onClose,
}: {
  target: DesktopLocalTarget;
  onClose: () => void;
}) {
  const t = useT();
  const [cursor, setCursor] = useState<string>();
  const logs = useQuery({
    queryKey: [
      "desktop",
      "local-logs",
      target.orgId,
      target.deviceId,
      target.centerUrl,
      cursor,
    ],
    queryFn: () => desktopBridge()!.readLocalLogs!({ ...target, cursor }),
    retry: false,
    gcTime: 0,
  });
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("computers.detail.logs")}
      width={760}
      footer={
        <>
          <Button
            variant="outline"
            disabled={logs.isFetching}
            onClick={() => {
              if (cursor) setCursor(undefined);
              else void logs.refetch();
            }}
          >
            {t("computers.localService.latest")}
          </Button>
          <Button
            variant="outline"
            disabled={logs.isFetching || !logs.data?.next_cursor}
            onClick={() => setCursor(logs.data!.next_cursor!)}
          >
            {t("computers.localService.older")}
          </Button>
          <Button onClick={onClose}>{t("common.close")}</Button>
        </>
      }
    >
      <p className="mb-3 text-body-sm text-fg-muted">
        {t("computers.localService.redacted")}
      </p>
      {logs.data?.reset && (
        <p role="status">{t("computers.localService.rotated")}</p>
      )}
      {logs.isError ? (
        <p role="alert">{t("computers.localService.failed")}</p>
      ) : logs.isPending ? (
        <p>{t("common.loading")}</p>
      ) : (
        <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded bg-surface-muted p-3 text-code">
          {logs.data.lines.join("\n") || t("computers.localService.emptyLogs")}
        </pre>
      )}
    </Dialog>
  );
}
