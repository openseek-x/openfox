import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { SpaceAgent } from "@openfox/api";
import { useToast } from "@openfox/ui";
import { useT } from "@openfox/i18n";
import { useDeviceCommand } from "../../computers/commands";
import { useMe } from "../queries";
import { useApi } from "../../context";
import type { CreateAgentToolInput } from "./agentToolsModel";

type Config = {
  name: string;
  transport: "http" | "stdio";
  scope?: "PUBLIC" | "DEVICE_OWNER";
  catalog_id?: string;
  url?: string;
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  headers?: Record<string, string>;
  auth_mode?: "none" | "api_key" | "oauth";
  auth_header?: string;
  credential?: string;
};
type Remote = {
  id: string;
  status: string;
  version: number;
  config: Config;
  tools?: { name: string; read_only?: boolean }[];
  resources?: { uri: string; name: string }[];
};
type Inventory = {
  online: boolean;
  device_id: string;
  runtime: string;
  status: string;
  servers: {
    name: string;
    transport: string;
    source: string;
    enabled: boolean;
    selected: boolean;
  }[];
};
type InventoryState =
  "idle" | "loading" | "ready" | "unavailable" | "unsupported" | "failed";
const fromRemote = (v: Remote): CreateAgentToolInput => ({
  id: v.id,
  catalogId: v.config.catalog_id ?? "",
  name: v.config.name,
  transport: v.config.transport,
  scope: v.config.scope,
  url: v.config.url,
  command: v.config.command,
  args: v.config.args,
  env: v.config.env,
  headers: v.config.headers,
  authMode: v.config.auth_mode ?? "none",
  authHeader: v.config.auth_header,
  credential: v.config.credential,
});
const toRequest = (p: CreateAgentToolInput, version: number) => ({
  expected_version: version,
  name: p.name,
  catalog_id: p.catalogId,
  transport: p.transport,
  scope: p.scope,
  url: p.url,
  command: p.command,
  args: p.args,
  env: p.env,
  headers: p.headers,
  auth_mode: p.authMode,
  auth_header: p.authHeader,
  credential: p.credential,
});
export function useAgentToolConnections(orgId: string, agent: SpaceAgent) {
  const { http } = useApi(),
    qc = useQueryClient();
  const user = useMe().data?.id,
    toast = useToast(),
    t = useT();
  const key = ["agent-tools", orgId, user, agent.space_id, agent.id],
    ik = [
      ...key,
      "inventory",
      agent.config?.computer_id,
      agent.config?.runtime,
    ];
  const path =
    "/v1/organizations/" +
    encodeURIComponent(orgId) +
    "/spaces/" +
    encodeURIComponent(agent.space_id) +
    "/agents/" +
    encodeURIComponent(agent.id) +
    "/mcp-servers";
  const list = useQuery({
    queryKey: key,
    enabled: !!user,
    queryFn: () =>
      http.request<{
        servers: Remote[];
        can_edit: boolean;
        can_use_local: boolean;
        oauth_redirect_uri?: string;
      }>(path),
    retry: false,
  });
  const canUseDevice = list.data?.can_use_local === true;
  const inv = useQuery({
    queryKey: ik,
    enabled: canUseDevice,
    queryFn: () => http.request<Inventory>(path + "/inventory"),
    retry: false,
    staleTime: 0,
  });
  const [probeFailed, setProbeFailed] = useState(false);
  const probe = useDeviceCommand(
    orgId,
    inv.data?.device_id ?? "",
    "runtime_probe",
    {
      onSettled: (cmd) => {
        setProbeFailed(cmd.status !== "SUCCEEDED");
        void inv.refetch();
      },
      onError: () => setProbeFailed(true),
    },
  );
  const probed = useRef("");
  useEffect(() => {
    const id = inv.data?.device_id;
    if (
      canUseDevice &&
      inv.data?.online &&
      id &&
      probed.current !== path + id
    ) {
      probed.current = path + id;
      probe.start();
    }
  }, [canUseDevice, inv.data?.device_id, inv.data?.online, path, probe.start]);
  const [pending, setPending] = useState<{
    id: string;
    op: string;
    end: number;
    popup: Window;
  } | null>(null);
  useEffect(() => {
    if (!pending) return;
    let stop = false,
      timer: number;
    const poll = async () => {
      try {
        const { status } = await http.request<{ status: string }>(
          pending.op + "/status",
        );
        if (stop) return;
        if (status === "connected") {
          pending.popup.close();
          setPending(null);
          void qc.invalidateQueries({ queryKey: key, exact: true });
          return;
        }
        if (
          !["pending", "exchanging"].includes(status) ||
          Date.now() > pending.end ||
          pending.popup.closed
        ) {
          setPending(null);
          toast(t("agents.tools.oauthIncomplete"), { tone: "error" });
          return;
        }
      } catch {
        if (stop) return;
        if (Date.now() > pending.end) {
          setPending(null);
          return;
        }
      }
      if (!stop) timer = window.setTimeout(poll, 1800);
    };
    timer = window.setTimeout(poll, 1000);
    return () => {
      stop = true;
      window.clearTimeout(timer);
    };
  }, [pending, http, qc, path, user, toast, t]);
  const rows = list.data?.servers ?? [];
  const connections = rows.map((v) => ({
    id: v.id,
    name: v.config.name,
    catalogId: v.config.catalog_id ?? "",
    transport: v.config.transport,
    config: fromRemote(v),
    status: v.status,
    localOnly: v.config.scope === "DEVICE_OWNER",
    detail: "",
    readTools: (v.tools ?? []).filter((t) => t.read_only).map((t) => t.name),
    writeTools: (v.tools ?? []).filter((t) => !t.read_only).map((t) => t.name),
    resources: (v.resources ?? []).map((r) => r.name || r.uri),
  }));
  const update = async () => {
    await qc.invalidateQueries({ queryKey: key, exact: true });
  };
  const save = async (p: CreateAgentToolInput) => {
    const id = p.id ?? crypto.randomUUID(),
      row = rows.find((r) => r.id === id);
    const saved = await http.request<Remote>(path + "/" + id, {
      method: "PUT",
      body: toRequest(p, row?.version ?? 0),
      timeoutMs: 35000,
    });
    await update();
    return saved;
  };
  const submit = async (p: CreateAgentToolInput) => {
    await save(p);
  };
  const refresh = async (id: string) => {
    const row = rows.find((r) => r.id === id);
    if (row) await save(fromRemote(row));
  };
  const disconnect = async (id: string) => {
    const row = rows.find((r) => r.id === id);
    if (!row) return;
    await http.request(path + "/" + id, {
      method: "DELETE",
      query: { version: row.version },
    });
    await update();
  };
  const refreshInventory = () => {
    if (!canUseDevice || !inv.data?.device_id || probe.running) return;
    setProbeFailed(false);
    probe.start();
  };
  const setInherited =
    canUseDevice && list.data?.can_edit
      ? async (name: string, enabled: boolean) => {
          const v = await http.request<Inventory>(
            path + "/inherited/" + encodeURIComponent(name),
            {
              method: "PUT",
              body: {
                enabled,
                device_id: inv.data?.device_id,
                runtime: inv.data?.runtime,
              },
            },
          );
          qc.setQueryData(ik, v);
        }
      : undefined;
  const inventoryState: InventoryState =
    probeFailed || inv.isError || list.isError
      ? "failed"
      : probe.running || list.isLoading || inv.isFetching
        ? "loading"
        : !canUseDevice || inv.data?.online === false
          ? "unavailable"
          : inv.data?.status === "missing" || inv.data?.status === "ok"
            ? "ready"
            : inv.data?.status === "unsupported"
              ? "unsupported"
              : inv.data?.status === "invalid" ||
                  inv.data?.status === "too_large"
                ? "failed"
                : "unavailable";
  const startOAuth = async (p: CreateAgentToolInput) => {
    const popup = window.open(
      http.url("/v1/mcp/oauth/wait"),
      "openfox-mcp-oauth",
      "popup,width=640,height=760",
    );
    if (!popup) throw new Error(t("agents.tools.oauthPopupBlocked"));
    popup.opener = null;
    try {
      const saved = await save(p),
        op = path + "/" + saved.id + "/oauth";
      const result = await http.request<{ authorize_url: string }>(
        op + "/start",
        {
          method: "POST",
          body: {
            client_id: p.oauthClientId,
            client_secret: p.oauthClientSecret,
          },
          timeoutMs: 30000,
        },
      );
      const url = new URL(result.authorize_url);
      if (url.protocol !== "https:" || url.username || url.password)
        throw new Error("OAuth 授权地址无效");
      popup.location.replace(url.href);
      setPending({ id: saved.id, op, end: Date.now() + 600000, popup });
    } catch (error) {
      popup.close();
      throw error;
    }
  };
  return {
    connections,
    canManage: list.data?.can_edit === true,
    canUseDevice,
    inventoryState,
    inventory: (canUseDevice ? (inv.data?.servers ?? []) : []).map((row) => ({
      ...row,
      source: row.source === "USER_CONFIG" ? "User config" : row.source,
      active: row.selected,
    })),
    refreshInventory,
    setInherited,
    submit,
    disconnect,
    refresh,
    supportedTransports: (canUseDevice ? ["http", "stdio"] : ["http"]) as (
      "http" | "stdio"
    )[],
    supportsOAuth: true,
    oauthRedirectUri: list.data?.oauth_redirect_uri,
    startOAuth,
  };
}
