import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useT } from "@openfox/i18n";
import { Button, ConfirmDialog, Dialog, Select, useToast } from "@openfox/ui";

import {
  desktopBridge,
  desktopLocalStatusKey,
  type DesktopLocalTarget,
} from "../desktop";
import { isLocalDevice } from "./localRegistration";
import { InfoRow } from "./parts";
import { useLocalStatus, useServerCenterUrl } from "./queries";

export function LocalServiceDetails({
  orgId,
  deviceId,
  isOwner,
}: {
  orgId: string;
  deviceId: string;
  isOwner: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const client = useQueryClient();
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
  const busy = startup.isPending || action.isPending;
  const running = ["RUNNING", "STARTING", "RECONCILE_REQUIRED"].includes(
    local.data?.daemon.state ?? "",
  );
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
      <InfoRow label={t("computers.detail.logs")}>
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
