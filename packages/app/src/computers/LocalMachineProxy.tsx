import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Field, TextInput } from "@openfox/ui";
import { desktopBridge, type DesktopMachineProxy } from "../desktop";
import { isLocalDevice } from "./localRegistration";
import { useLocalStatus, useServerCenterUrl } from "./queries";
import { InfoRow } from "./parts";

export function LocalMachineProxy({
  orgId,
  deviceId,
  isOwner,
}: {
  orgId: string;
  deviceId: string;
  isOwner: boolean;
}) {
  const t = useT(),
    bridge = desktopBridge(),
    client = useQueryClient();
  const local = useLocalStatus(),
    center = useServerCenterUrl(orgId, Boolean(bridge));
  const enabled = Boolean(
    isOwner &&
    bridge?.platform === "darwin" &&
    bridge.getMachineProxy &&
    bridge.saveMachineProxy &&
    local.data?.registration?.registered &&
    local.data.registration.organization_id === orgId &&
    isLocalDevice(local.data, orgId, deviceId, center.data?.center_url),
  );
  const target = { orgId, deviceId, centerUrl: center.data?.center_url ?? "" };
  const key = ["desktop", "machine-proxy", orgId, deviceId, target.centerUrl];
  const query = useQuery({
    queryKey: key,
    queryFn: () => bridge!.getMachineProxy!(target),
    enabled,
    retry: false,
  });
  const [draft, setDraft] = useState<DesktopMachineProxy | null>(null);
  const [endpoint, setEndpoint] = useState({
    mode: "proxy" as const,
    protocol: "http" as "http" | "https",
    host: "",
    port: 7890,
  });
  const [error, setError] = useState("");
  const save = useMutation({
    mutationFn: async () => {
      if (!enabled || !draft) throw Error("unavailable");
      return bridge!.saveMachineProxy!({
        ...target,
        expected_revision: draft.revision,
        scope_token: draft.scope_token,
        mode: draft.mode,
        ...(draft.mode === "CUSTOM" ? { effective: endpoint } : {}),
      });
    },
    onSuccess: (value) => {
      client.setQueryData(key, value);
      setDraft(null);
    },
    onError: () => {
      setError(t("computers.machineProxy.saveFailed"));
      void query.refetch();
    },
  });
  if (!enabled) return null;
  const view = query.data;
  const state =
    query.isError || view?.problem || view?.runtime?.problem
      ? "failed"
      : !view
        ? "loading"
        : view.mode === "DISABLED"
          ? "disabledHint"
          : view.source_state === "unsupported" ||
              view.source_state === "outdated" ||
              view.sync_error
            ? "stale"
            : !view.runtime || view.runtime.loaded_revision !== view.revision
              ? "pending"
              : view.runtime.connection_revision !== undefined &&
                  view.runtime.connection_revision !== view.revision
                ? "reconnect"
                : "synced";
  const invalid =
    draft?.mode === "CUSTOM" &&
    (!endpoint.host ||
      !Number.isInteger(endpoint.port) ||
      endpoint.port < 1 ||
      endpoint.port > 65535);
  return (
    <>
      <InfoRow
        label={t("computers.machineProxy.title")}
        description={t(`computers.machineProxy.${state}`)}
      >
        {view ? (
          <Button
            size="28"
            variant="outline"
            onClick={() => {
              setDraft(view);
              setError("");
              setEndpoint(
                view.effective?.mode === "proxy"
                  ? view.effective
                  : { mode: "proxy", protocol: "http", host: "", port: 7890 },
              );
            }}
          >
            {t("computers.machineProxy.edit")}
          </Button>
        ) : (
          <Button
            size="28"
            variant="outline"
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            {t("computers.machineProxy.retry")}
          </Button>
        )}
      </InfoRow>
      {draft && (
        <Dialog
          open
          width={560}
          title={t("computers.machineProxy.title")}
          description={t("computers.machineProxy.scope")}
          onClose={save.isPending ? undefined : () => setDraft(null)}
          footer={
            <>
              <Button
                variant="outline"
                disabled={save.isPending}
                onClick={() => setDraft(null)}
              >
                {t("common.cancel")}
              </Button>
              <Button
                disabled={
                  save.isPending ||
                  Boolean(invalid) ||
                  (draft.mode === "FOLLOW_DESKTOP" && !view?.desktop_supported)
                }
                onClick={() => save.mutate()}
              >
                {t("common.save")}
              </Button>
            </>
          }
        >
          <fieldset disabled={save.isPending} className="flex flex-col gap-3">
            {(["DISABLED", "FOLLOW_DESKTOP", "CUSTOM"] as const).map((mode) => (
              <label key={mode} className="flex items-center gap-2 text-body">
                <input
                  type="radio"
                  name="machine-proxy-mode"
                  checked={draft.mode === mode}
                  disabled={
                    mode === "FOLLOW_DESKTOP" && !view?.desktop_supported
                  }
                  onChange={() => {
                    setDraft({ ...draft, mode });
                    setError("");
                  }}
                />
                {t(`computers.machineProxy.${mode}`)}
              </label>
            ))}
            {!view?.desktop_supported && (
              <p role="status" className="text-caption text-fg-muted">
                {t("computers.machineProxy.unsupported")}
              </p>
            )}
            {draft.mode === "CUSTOM" && (
              <>
                <Field label={t("shell.proxyProtocol")}>
                  <select
                    aria-label={t("shell.proxyProtocol")}
                    value={endpoint.protocol}
                    onChange={(event) =>
                      setEndpoint({
                        ...endpoint,
                        protocol: event.target.value as "http" | "https",
                      })
                    }
                  >
                    <option value="http">HTTP</option>
                    <option value="https">HTTPS</option>
                  </select>
                </Field>
                <Field label={t("shell.proxyHost")}>
                  <TextInput
                    value={endpoint.host}
                    onChange={(event) =>
                      setEndpoint({ ...endpoint, host: event.target.value })
                    }
                    aria-label={t("shell.proxyHost")}
                  />
                </Field>
                <Field label={t("shell.proxyPort")}>
                  <TextInput
                    type="number"
                    min={1}
                    max={65535}
                    value={String(endpoint.port)}
                    onChange={(event) =>
                      setEndpoint({
                        ...endpoint,
                        port: Number(event.target.value),
                      })
                    }
                    aria-label={t("shell.proxyPort")}
                  />
                </Field>
              </>
            )}
            {view?.mode === "FOLLOW_DESKTOP" && view.effective && (
              <p className="text-caption text-fg-muted">
                {t("computers.machineProxy.lastSaved")}:{" "}
                {view.effective.mode === "proxy"
                  ? `${view.effective.protocol}://${view.effective.host}:${view.effective.port}`
                  : t("computers.machineProxy.followDirect")}
              </p>
            )}
            <p className="text-caption text-fg-muted">
              {t("computers.machineProxy.lifecycle")}
            </p>
            <p className="text-caption text-fg-muted">
              {t("computers.machineProxy.native")}
            </p>
            {error && (
              <p role="alert" className="text-caption text-red-600">
                {error}
              </p>
            )}
          </fieldset>
        </Dialog>
      )}
    </>
  );
}
