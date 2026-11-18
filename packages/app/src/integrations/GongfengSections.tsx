import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  isApiError,
  type GongfengRepository,
  type IntegrationConnection,
  type IntegrationDetail,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  ConfirmDialog,
  Dialog,
  Field,
  TextInput,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import { SettingsCard, SettingsSection } from "../spaces/settings/parts";
import { DisconnectDialog } from "./DisconnectDialog";
import {
  GongfengCreateDialog,
  GongfengEditDialog,
  GongfengReconcileDialog,
} from "./GongfengDialogs";
import { gongfengError, gongfengRepositoryURL } from "./gongfeng-model";

export function GongfengSections({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT(),
    cache = useQueryClient();
  const [bind, setBind] = useState(false);
  const refresh = () => {
    void cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
  };
  return (
    <>
      <Callout tone="info">{t("integrations.gongfeng.scope")}</Callout>
      {!detail.can_manage ? (
        <Callout tone="info">
          {t("integrations.readOnly", { name: detail.provider.name })}
        </Callout>
      ) : (
        <>
          <Button variant="outline" onClick={() => setBind(true)}>
            {t("integrations.gongfeng.bind")}
          </Button>
          {bind && (
            <TokenDialog
              orgId={orgId}
              refresh={refresh}
              onClose={() => setBind(false)}
            />
          )}
          {detail.connections.map((connection) => (
            <ConnectionPanel
              key={connection.id}
              orgId={orgId}
              detail={detail}
              connection={connection}
              refresh={refresh}
            />
          ))}
        </>
      )}
    </>
  );
}

function TokenDialog({
  orgId,
  connection,
  version,
  refresh,
  onClose,
}: {
  orgId: string;
  connection?: IntegrationConnection;
  version?: number;
  refresh: () => void;
  onClose: () => void;
}) {
  const t = useT(),
    api = useApi();
  const [token, setToken] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>();
  const valid = /^[\x21-\x7e]{1,4096}$/.test(token);
  const reconnect = connection?.status === "DISCONNECTED";
  async function save() {
    if (!valid || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      await api.integrations.bindGongfeng(orgId, {
        access_token: token,
        ...(connection
          ? {
              connection_id: connection.id,
              version: version ?? connection.version,
            }
          : {}),
      });
      setToken("");
      refresh();
      onClose();
    } catch (e) {
      setError(e);
      if (isApiError(e) && e.status === 409) refresh();
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog
      open
      title={t(
        reconnect
          ? "integrations.gongfeng.reconnect"
          : connection
            ? "integrations.gongfeng.replace"
            : "integrations.gongfeng.bind",
      )}
      description={t(
        reconnect
          ? "integrations.gongfeng.reconnectHelp"
          : "integrations.gongfeng.tokenHelp",
      )}
      onClose={busy ? undefined : onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button onClick={() => void save()} loading={busy} disabled={!valid}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <div className="grid gap-3">
        <Field label={t("integrations.gongfeng.token")}>
          <TextInput
            type="password"
            autoComplete="new-password"
            value={token}
            maxLength={4096}
            onChange={(e) => setToken(e.target.value)}
            disabled={busy}
          />
        </Field>
        {!!token && !valid && (
          <Callout tone="warning">
            {t("integrations.gongfeng.tokenInvalid")}
          </Callout>
        )}
        {error != null && (
          <Callout tone="danger">{gongfengError(t, error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}

function ConnectionPanel({
  orgId,
  detail,
  connection,
  refresh,
}: {
  orgId: string;
  detail: IntegrationDetail;
  connection: IntegrationConnection;
  refresh: () => void;
}) {
  const t = useT(),
    api = useApi();
  const [cursorStack, setCursorStack] = useState<string[]>([]),
    [workStack, setWorkStack] = useState<string[]>([]);
  const [dialog, setDialog] = useState<
      "token" | "add" | "rotate" | "disconnect" | null
    >(null),
    [saved, setSaved] = useState(false);
  const disconnected = connection.status === "DISCONNECTED";
  const prefix = [...keys.integrations(orgId), "gongfeng", connection.id];
  const config = useQuery({
    queryKey: [...prefix, "configuration", cursorStack.at(-1) ?? ""],
    queryFn: () =>
      api.integrations.gongfengConfiguration(
        orgId,
        connection.id,
        cursorStack.at(-1),
      ),
    retry: false,
  });
  const work = useQuery({
    queryKey: [...prefix, "work", workStack.at(-1) ?? ""],
    queryFn: () =>
      api.integrations.gongfengWork(orgId, connection.id, workStack.at(-1)),
    retry: false,
  });
  const version = config.data?.version ?? connection.version;
  const rotate = useMutation({
    mutationFn: () =>
      api.integrations.rotateGongfengWebhook(orgId, connection.id, version),
    onSuccess: () => {
      setDialog(null);
      setSaved(true);
      refresh();
    },
    onError: (e) => {
      if (isApiError(e) && e.status === 409) refresh();
    },
  });
  const done = () => {
    setDialog(null);
    setSaved(true);
    refresh();
  };
  return (
    <SettingsSection
      title={connection.display_name}
      description={connection.status}
      action={
        <Button variant="outline" onClick={refresh}>
          {t("integrations.gongfeng.refresh")}
        </Button>
      }
    >
      {disconnected ? (
        <>
          <Callout tone="warning">
            {t("integrations.gongfeng.disconnected")}
          </Callout>
          <Button
            variant="outline"
            disabled={!config.data}
            onClick={() => setDialog("token")}
          >
            {t("integrations.gongfeng.reconnect")}
          </Button>
        </>
      ) : (
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={() => setDialog("token")}>
            {t("integrations.gongfeng.replace")}
          </Button>
          <Button
            variant="outline"
            disabled={!config.data}
            onClick={() => setDialog("add")}
          >
            {t("integrations.gongfeng.add")}
          </Button>
          <Button
            variant="outline"
            disabled={!config.data}
            onClick={() => {
              rotate.reset();
              setDialog("rotate");
            }}
          >
            {t("integrations.gongfeng.rotate")}
          </Button>
          <Button variant="outline" onClick={() => setDialog("disconnect")}>
            {t("integrations.disconnect")}
          </Button>
        </div>
      )}
      {saved && (
        <Callout tone="info">{t("integrations.gongfeng.pending")}</Callout>
      )}
      {config.isError && (
        <Callout tone="danger">{gongfengError(t, config.error)}</Callout>
      )}
      {config.data && (
        <SettingsSection title={t("integrations.gongfeng.repositories")}>
          {!config.data.repositories.length && (
            <p>{t("integrations.gongfeng.empty")}</p>
          )}
          {config.data.repositories.map((repo) => (
            <RepositoryPanel
              key={repo.id}
              orgId={orgId}
              connection={connection}
              version={version}
              repo={repo}
              link={detail.sync_links.find((l) => l.id === repo.sync_link_id)}
              refresh={refresh}
            />
          ))}
          <Pages
            stack={cursorStack}
            setStack={setCursorStack}
            next={config.data.has_more ? config.data.next_cursor : undefined}
          />
        </SettingsSection>
      )}
      <SettingsSection
        title={t("integrations.gongfeng.work")}
        description={t("integrations.gongfeng.workHelp")}
      >
        {work.isError && (
          <Callout tone="danger">{gongfengError(t, work.error)}</Callout>
        )}
        {work.data && (
          <>
            {!work.data.work.length && (
              <p>{t("integrations.gongfeng.noWork")}</p>
            )}
            {work.data.work.map((item) => (
              <SettingsCard key={item.id}>
                <div className="grid gap-1 p-4 text-body-sm">
                  <p className="font-medium">
                    {t("integrations.gongfeng.workItem", {
                      kind: item.kind,
                      key: item.object_key,
                    })}
                  </p>
                  <p>{item.status}</p>
                  <p>
                    {t("integrations.gongfeng.generation", {
                      requested: item.requested_generation,
                      completed: item.completed_generation,
                    })}
                  </p>
                  <p>
                    {t("integrations.gongfeng.backlinkState", {
                      state: item.backlink_state,
                    })}
                  </p>
                  {item.backlink_state === "UNKNOWN" && (
                    <Callout tone="warning">
                      {t("integrations.gongfeng.unknown")}
                    </Callout>
                  )}
                  {item.error_code && <p>{item.error_code}</p>}
                  {item.next_attempt_at && (
                    <p>
                      {t("integrations.gongfeng.nextAttempt", {
                        time: item.next_attempt_at,
                      })}
                    </p>
                  )}
                </div>
              </SettingsCard>
            ))}
            <Pages
              stack={workStack}
              setStack={setWorkStack}
              next={work.data.has_more ? work.data.next_cursor : undefined}
            />
          </>
        )}
      </SettingsSection>
      {dialog === "token" && (
        <TokenDialog
          orgId={orgId}
          connection={connection}
          version={version}
          refresh={refresh}
          onClose={() => setDialog(null)}
        />
      )}
      {!disconnected && dialog === "add" && (
        <GongfengCreateDialog
          orgId={orgId}
          connection={connection}
          version={version}
          refresh={refresh}
          onDone={done}
          onClose={() => setDialog(null)}
        />
      )}
      {!disconnected && dialog === "disconnect" && (
        <DisconnectDialog
          orgId={orgId}
          provider={detail.provider}
          connection={{ ...connection, version }}
          body={t("integrations.gongfeng.disconnectHelp")}
          onClose={() => setDialog(null)}
        />
      )}
      {!disconnected && (
        <ConfirmDialog
          open={dialog === "rotate"}
          onCancel={() => setDialog(null)}
          title={t("integrations.gongfeng.rotate")}
          description={t("integrations.gongfeng.rotateHelp")}
          loading={rotate.isPending}
          onConfirm={() => rotate.mutate()}
        >
          {rotate.isError && (
            <Callout tone="danger">{gongfengError(t, rotate.error)}</Callout>
          )}
        </ConfirmDialog>
      )}
    </SettingsSection>
  );
}

function Pages({
  stack,
  setStack,
  next,
}: {
  stack: string[];
  setStack: (v: string[]) => void;
  next?: string;
}) {
  const t = useT();
  if (!stack.length && !next) return null;
  return (
    <div className="grid gap-2">
      {next && (
        <p className="text-body-sm text-fg-muted">
          {t("integrations.gongfeng.more")}
        </p>
      )}
      <div className="flex gap-2">
        <Button
          variant="outline"
          disabled={!stack.length}
          onClick={() => setStack(stack.slice(0, -1))}
        >
          {t("integrations.gongfeng.previous")}
        </Button>
        <Button
          variant="outline"
          disabled={!next}
          onClick={() => next && setStack([...stack, next])}
        >
          {t("integrations.gongfeng.next")}
        </Button>
      </div>
    </div>
  );
}

function RepositoryPanel({
  orgId,
  connection,
  version,
  repo,
  link,
  refresh,
}: {
  orgId: string;
  connection: IntegrationConnection;
  version: number;
  repo: GongfengRepository;
  link?: IntegrationDetail["sync_links"][number];
  refresh: () => void;
}) {
  const t = useT(),
    api = useApi();
  const [dialog, setDialog] = useState<"edit" | "remove" | "reconcile" | null>(
    null,
  );
  const available =
    connection.status !== "DISCONNECTED" && repo.status !== "REMOVED";
  const url = gongfengRepositoryURL(repo.path);
  const action = useMutation({
    mutationFn: (value: "remove" | "retry" | "pause" | "resume") => {
      const input = {
        connection_version: version,
        generation: repo.generation,
      };
      return value === "pause" || value === "resume"
        ? api.integrations.updateGongfengRepository(
            orgId,
            connection.id,
            repo.id,
            { ...input, paused: value === "pause" },
          )
        : api.integrations.actGongfengRepository(
            orgId,
            connection.id,
            repo.id,
            value,
            input,
          );
    },
    onSuccess: () => {
      setDialog(null);
      refresh();
    },
    onError: (e) => {
      if (isApiError(e) && e.status === 409) refresh();
    },
  });
  return (
    <SettingsCard>
      <div className="grid gap-3 p-4">
        <div className="font-medium">
          {url ? (
            <a
              href={url}
              target="_blank"
              rel="noopener noreferrer"
              className="text-fg-link"
            >
              {repo.path}
            </a>
          ) : (
            repo.path
          )}
        </div>
        {link && (
          <p className="text-body-sm">
            {link.space_name} ·{" "}
            {t(
              link.direction === "BIDIRECTIONAL"
                ? "integrations.gongfeng.bidirectional"
                : "integrations.gongfeng.inbound",
            )}
          </p>
        )}
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-body-sm">
          <dt>{t("integrations.gongfeng.status")}</dt>
          <dd>{repo.status}</dd>
          <dt>{t("integrations.gongfeng.hook")}</dt>
          <dd>{repo.hook_state}</dd>
          {repo.hook_id != null && (
            <>
              <dt>{t("integrations.gongfeng.hookID")}</dt>
              <dd>{repo.hook_id}</dd>
            </>
          )}
          {!!repo.status_reason && (
            <>
              <dt>{t("integrations.gongfeng.reason")}</dt>
              <dd>{repo.status_reason}</dd>
            </>
          )}
        </dl>
        {repo.hook_state.includes("UNKNOWN") && (
          <Callout tone="warning">{t("integrations.gongfeng.unknown")}</Callout>
        )}
        <p className="text-body-sm">
          {t("integrations.gongfeng.excluded", {
            count: repo.excluded_confidential,
          })}
        </p>
        {Object.keys(repo.progress).length > 0 && (
          <div className="grid gap-2 text-body-sm">
            <h3 className="font-medium">
              {t("integrations.gongfeng.scanTitle")}
            </h3>
            <p className="text-fg-muted">
              {t("integrations.gongfeng.scanHelp")}
            </p>
            {(["issues", "merge_requests"] as const).map((kind) => {
              const scan = repo.progress[kind];
              if (!scan) return null;
              return (
                <div key={kind} className="grid gap-1">
                  <p>{t(`integrations.gongfeng.scanStatus.${scan.status}`)}</p>
                  <p>
                    {t("integrations.gongfeng.scanDetail", {
                      kind: t(`integrations.gongfeng.${kind}`),
                      pass: scan.pass,
                      page: scan.page,
                      count: scan.count,
                      first: scan.first_count,
                    })}
                  </p>
                  {scan.history && (
                    <p>{t("integrations.gongfeng.scanHistory")}</p>
                  )}
                  {scan.from && scan.to && (
                    <p>
                      {t("integrations.gongfeng.scanWindow", {
                        from: scan.from,
                        to: scan.to,
                      })}
                    </p>
                  )}
                  {scan.error_code && <p>{scan.error_code}</p>}
                </div>
              );
            })}
          </div>
        )}
        {action.isError && (
          <Callout tone="danger">{gongfengError(t, action.error)}</Callout>
        )}
        {available && (
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={action.isPending || !link}
              onClick={() => setDialog("edit")}
            >
              {t("integrations.gongfeng.edit")}
            </Button>
            <Button
              variant="outline"
              disabled={action.isPending}
              onClick={() =>
                action.mutate(repo.status === "ACTIVE" ? "pause" : "resume")
              }
            >
              {t(
                repo.status === "ACTIVE"
                  ? "integrations.gongfeng.pause"
                  : "integrations.gongfeng.resume",
              )}
            </Button>
            <Button
              variant="outline"
              disabled={action.isPending}
              onClick={() => action.mutate("retry")}
            >
              {t("integrations.gongfeng.retry")}
            </Button>
            <Button
              variant="outline"
              disabled={action.isPending || repo.status !== "ACTIVE"}
              onClick={() => setDialog("reconcile")}
            >
              {t("integrations.gongfeng.reconcile")}
            </Button>
            <Button
              variant="outline"
              disabled={action.isPending}
              onClick={() => {
                action.reset();
                setDialog("remove");
              }}
            >
              {t("integrations.gongfeng.remove")}
            </Button>
          </div>
        )}
        {available && dialog === "edit" && link && (
          <GongfengEditDialog
            orgId={orgId}
            connection={connection}
            version={version}
            repo={repo}
            direction={
              link.direction === "BIDIRECTIONAL" ? "BIDIRECTIONAL" : "INBOUND"
            }
            refresh={refresh}
            onClose={() => setDialog(null)}
          />
        )}
        {available && dialog === "reconcile" && (
          <GongfengReconcileDialog
            orgId={orgId}
            connection={connection}
            version={version}
            repo={repo}
            refresh={refresh}
            onClose={() => setDialog(null)}
          />
        )}
        {available && (
          <ConfirmDialog
            open={dialog === "remove"}
            title={t("integrations.gongfeng.remove")}
            description={t("integrations.gongfeng.removeHelp")}
            danger
            loading={action.isPending}
            onCancel={() => setDialog(null)}
            onConfirm={() => action.mutate("remove")}
          >
            {action.isError && (
              <Callout tone="danger">{gongfengError(t, action.error)}</Callout>
            )}
          </ConfirmDialog>
        )}
      </div>
    </SettingsCard>
  );
}
