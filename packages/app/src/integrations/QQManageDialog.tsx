import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  newIdempotencyKey,
  type QQBot,
  type QQBindingSession,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Button, Callout, Dialog, Field, TextInput } from "@openfox/ui";
import { useApi } from "../context";
import { qqKey, useQQRefresh, useQQWrite } from "./qq-hooks";
import { qqError, qqOfficialURL, qqState } from "./qq-model";
import {
  QQAssignDialog,
  QQBindingDialog,
  QQConfirmAction,
} from "./QQBindingDialogs";
import { QRCode } from "./QRCode";
import { selectVersion } from "./qr";

export function QQManageDialog({
  orgId,
  botID,
  qrConfigured,
  onClose,
}: {
  orgId: string;
  botID: string;
  qrConfigured: boolean;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT(),
    refresh = useQQRefresh(orgId),
    write = useQQWrite(refresh);
  const query = useQuery({
    queryKey: [...qqKey(orgId), "bot", botID],
    queryFn: ({ signal }) => api.qq.detail(orgId, botID, signal),
    retry: false,
    refetchInterval: 3000,
  });
  const [qr, setQR] = useState<string>(),
    [binding, setBinding] = useState<QQBindingSession>(),
    [assign, setAssign] = useState(false),
    [secret, setSecret] = useState<"webhook" | "credentials">();
  const data = query.data,
    bot = data?.bot;
  const startQR = () => {
    if (!bot) return;
    void write.run(
      (s) =>
        api.qq.startQR(
          orgId,
          bot.id,
          { version: bot.version, idempotency_key: newIdempotencyKey() },
          s,
        ),
      (v) => setQR(v.id),
    );
  };
  return (
    <Dialog
      open
      title={t("integrations.qq.manage")}
      onClose={onClose}
      width={760}
      footer={
        <Button variant="outline" onClick={onClose}>
          {t("common.close")}
        </Button>
      }
    >
      {query.isPending ? (
        <p>{t("common.loading")}</p>
      ) : query.isError ? (
        <Callout tone="danger">{t("integrations.qq.loadFailed")}</Callout>
      ) : (
        data &&
        bot && (
          <div className="flex flex-col gap-5">
            <div className="flex items-center gap-2">
              <strong>{bot.name}</strong>
              <Badge>{qqState(t, bot.state)}</Badge>
            </div>
            {bot.app_id && <p className="break-all">AppID: {bot.app_id}</p>}
            <div className="flex flex-wrap gap-2">
              {!bot.app_id &&
                ["PREPARED", "NEEDS_REAUTH"].includes(bot.state) && (
                  <Button
                    disabled={!qrConfigured || write.busy || write.blocked}
                    onClick={startQR}
                  >
                    {t("integrations.qq.qrStart")}
                  </Button>
                )}
              {["WAITING_WEBHOOK", "ACTIVE"].includes(bot.state) && (
                <Button variant="outline" onClick={() => setSecret("webhook")}>
                  {t("integrations.qq.webhook")}
                </Button>
              )}
              {bot.app_id &&
                [
                  "ACTIVE",
                  "NEEDS_REAUTH",
                  "DISCONNECTED",
                  "WAITING_WEBHOOK",
                ].includes(bot.state) && (
                  <Button
                    variant="outline"
                    onClick={() => setSecret("credentials")}
                  >
                    {t("integrations.qq.credentials")}
                  </Button>
                )}
              {!["DISCONNECTED", "CANCELLED"].includes(bot.state) && (
                <QQConfirmAction
                  label={t("integrations.qq.disconnect")}
                  description={t("integrations.qq.disconnectHint")}
                  refresh={refresh}
                  execute={(_, s) =>
                    api.qq.disconnect(orgId, bot.id, bot.version, s)
                  }
                />
              )}
              {["DISCONNECTED", "PREPARED", "NEEDS_REAUTH"].includes(
                bot.state,
              ) && (
                <QQConfirmAction
                  label={t("integrations.qq.release")}
                  description={t("integrations.qq.releaseHint")}
                  refresh={refresh}
                  remote={
                    !!bot.app_id ||
                    data.qr_sessions.some((q) =>
                      ["DONE", "UNKNOWN"].includes(q.status),
                    )
                  }
                  execute={(checked, s) =>
                    api.qq
                      .release(
                        orgId,
                        bot.id,
                        { version: bot.version, remote_checked: checked },
                        s,
                      )
                      .then((v) => {
                        onClose();
                        return v;
                      })
                  }
                />
              )}
              <Button variant="outline" onClick={refresh}>
                {t("integrations.qq.reload")}
              </Button>
            </div>
            {bot.state === "WAITING_WEBHOOK" && (
              <Callout tone="info">{t("integrations.qq.webhookHelp")}</Callout>
            )}
            {!!write.error && (
              <Callout tone="danger">{qqError(t, write.error)}</Callout>
            )}
            {data.qr_sessions.map((v) => (
              <div
                key={v.id}
                className="flex items-center justify-between gap-3"
              >
                <span>{qqState(t, v.status)}</span>
                <Button variant="outline" onClick={() => setQR(v.id)}>
                  {t("integrations.qq.qrTitle")}
                </Button>
              </div>
            ))}
            <section className="flex flex-col gap-3">
              <h3>{t("integrations.qq.groups")}</h3>
              <p>{t("integrations.qq.groupIdentityHelp")}</p>
              <div className="flex gap-2">
                <Button
                  disabled={
                    bot.state !== "ACTIVE" || write.busy || write.blocked
                  }
                  onClick={() =>
                    void write.run(
                      (s) =>
                        api.qq.startBinding(
                          orgId,
                          bot.id,
                          {
                            version: bot.version,
                            purpose: "GROUP_DISCOVERY",
                            idempotency_key: newIdempotencyKey(),
                          },
                          s,
                        ),
                      setBinding,
                    )
                  }
                >
                  {t("integrations.qq.groupDiscovery")}
                </Button>
                <Button
                  variant="outline"
                  disabled={bot.state !== "ACTIVE"}
                  onClick={() => setAssign(true)}
                >
                  {t("integrations.qq.invite")}
                </Button>
              </div>
              {data.groups
                .filter((g) => g.enabled)
                .map((g) => (
                  <div
                    key={g.id}
                    className="flex items-center justify-between gap-3"
                  >
                    <span className="break-all">{g.chat_id}</span>
                    <QQConfirmAction
                      label={t("integrations.qq.disableGroup")}
                      description={t("integrations.qq.disableGroupHint")}
                      refresh={refresh}
                      execute={(_, s) =>
                        api.qq.disableGroup(orgId, g.id, g.version, s)
                      }
                    />
                  </div>
                ))}
              {data.bindings.map((v) => (
                <div
                  key={v.id}
                  className="flex items-center justify-between gap-3"
                >
                  <span className="break-all">
                    {v.scope} · {v.chat_id} · {v.subject_id}
                  </span>
                  <QQConfirmAction
                    label={t("integrations.qq.revoke")}
                    description={t("integrations.qq.revokeHint")}
                    refresh={refresh}
                    execute={(_, s) => api.qq.revoke(orgId, v.id, v.version, s)}
                  />
                </div>
              ))}
            </section>
            <section className="flex flex-col gap-3">
              <h3>{t("integrations.qq.diagnostics")}</h3>
              <p>{t("integrations.qq.replyHint")}</p>
              {data.replies.length === 0 && (
                <p>{t("integrations.qq.noReplies")}</p>
              )}
              {data.replies.map((v) => (
                <div
                  key={v.id}
                  className="flex flex-col gap-1 border-b border-line-default py-2"
                >
                  <span>{qqState(t, v.state)}</span>
                  <span>{new Date(v.created_at).toLocaleString()}</span>
                  <span>
                    {t("integrations.qq.expires", {
                      time: new Date(v.deadline).toLocaleString(),
                    })}
                  </span>
                  {v.state === "UNKNOWN" && (
                    <p>{t("integrations.qq.replyHint")}</p>
                  )}
                </div>
              ))}
            </section>
            {secret && (
              <QQSecretDialog
                key={`${secret}:${bot.version}`}
                orgId={orgId}
                bot={bot}
                kind={secret}
                onClose={() => setSecret(undefined)}
                refresh={refresh}
              />
            )}
            {assign && (
              <QQAssignDialog
                orgId={orgId}
                detail={data}
                refresh={refresh}
                onClose={() => setAssign(false)}
              />
            )}
          </div>
        )
      )}
      {qr && (
        <QQQRDialog
          key={qr}
          orgId={orgId}
          id={qr}
          canRecover={!!bot && ["PREPARED", "NEEDS_REAUTH"].includes(bot.state)}
          onClose={() => setQR(undefined)}
          refresh={refresh}
        />
      )}
      {binding && (
        <QQBindingDialog
          key={binding.id}
          orgId={orgId}
          initial={binding}
          onClose={() => setBinding(undefined)}
          refresh={refresh}
        />
      )}
    </Dialog>
  );
}
function QQSecretDialog({
  orgId,
  bot,
  kind,
  onClose,
  refresh,
}: {
  orgId: string;
  bot: QQBot;
  kind: "webhook" | "credentials";
  onClose: () => void;
  refresh: () => void;
}) {
  const api = useApi(),
    t = useT(),
    write = useQQWrite(refresh),
    [secret, setSecret] = useState(""),
    [url, setURL] = useState("");
  const close = () => {
    setSecret("");
    setURL("");
    onClose();
  };
  return (
    <Dialog
      open
      title={t(
        kind === "webhook"
          ? "integrations.qq.webhookTitle"
          : "integrations.qq.credentials",
      )}
      onClose={close}
      footer={
        <>
          <Button variant="outline" onClick={close}>
            {t("common.close")}
          </Button>
          {kind === "credentials" && (
            <Button
              disabled={!secret.trim() || write.busy || write.blocked}
              onClick={() => {
                const value = secret;
                setSecret("");
                void write.run(
                  (s) =>
                    api.qq.credentials(
                      orgId,
                      bot.id,
                      {
                        version: bot.version,
                        app_id: bot.app_id,
                        app_secret: value,
                      },
                      s,
                    ),
                  close,
                );
              }}
            >
              {t("common.save")}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <p>
          {t(
            kind === "webhook"
              ? "integrations.qq.webhookHelp"
              : "integrations.qq.credentialHint",
          )}
        </p>
        {kind === "webhook" ? (
          <>
            {!url && (
              <Button
                disabled={write.busy || write.blocked}
                onClick={() =>
                  void write.run(
                    (s) => api.qq.webhook(orgId, bot.id, bot.version, s),
                    (v) => setURL(v.url),
                  )
                }
              >
                {t("integrations.qq.webhook")}
              </Button>
            )}
            {url && (
              <p className="break-all select-all rounded-lg bg-bg-muted p-3">
                {url}
              </p>
            )}
          </>
        ) : (
          <>
            <p className="break-all">AppID: {bot.app_id}</p>
            <Field label={t("integrations.qq.secret")}>
              <TextInput
                type="password"
                autoComplete="off"
                aria-label={t("integrations.qq.secret")}
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
              />
            </Field>
          </>
        )}
        {!!write.error && (
          <Callout tone="danger">{qqError(t, write.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
function QQQRDialog({
  orgId,
  id,
  canRecover,
  onClose,
  refresh,
}: {
  orgId: string;
  id: string;
  canRecover: boolean;
  onClose: () => void;
  refresh: () => void;
}) {
  const api = useApi(),
    t = useT(),
    write = useQQWrite(refresh);
  const query = useQuery({
    queryKey: [...qqKey(orgId), "qr", id],
    queryFn: ({ signal }) => api.qq.qr(orgId, id, signal),
    retry: false,
    refetchInterval: (q) =>
      ["PENDING", "POLLING", "WAITING_SCAN"].includes(
        q.state.data?.status ?? "PENDING",
      )
        ? 2000
        : false,
  });
  const v = query.data,
    url =
      v?.qr_url && Date.parse(v.expires_at) > Date.now()
        ? qqOfficialURL(v.qr_url)
        : null;
  let qrFits = false;
  if (url) {
    try {
      selectVersion(new TextEncoder().encode(url).length);
      qrFits = true;
    } catch {
      /* Keep the verified official link usable when the existing encoder is full. */
    }
  }
  return (
    <Dialog
      open
      title={t("integrations.qq.qrTitle")}
      onClose={onClose}
      footer={
        <Button variant="outline" onClick={onClose}>
          {t("common.close")}
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        <p>{t("integrations.qq.qrHint")}</p>
        {query.isError && (
          <Callout tone="danger">{t("integrations.qq.loadFailed")}</Callout>
        )}
        {v && (
          <>
            <p>{qqState(t, v.status)}</p>
            {url && (
              <>
                {qrFits ? (
                  <QRCode value={url} label={t("integrations.qq.qrTitle")} />
                ) : (
                  <p>{t("integrations.qq.qrLong")}</p>
                )}
                <a href={url} target="_blank" rel="noopener noreferrer">
                  {t("integrations.qq.officialLink")}
                </a>
              </>
            )}
            {["PENDING", "POLLING"].includes(v.status) && (
              <p>{t("integrations.qq.qrWaiting")}</p>
            )}
            {["PENDING", "POLLING", "WAITING_SCAN"].includes(v.status) && (
              <QQConfirmAction
                label={t("integrations.qq.qrCancel")}
                description={t("integrations.qq.qrUnknown")}
                refresh={refresh}
                execute={(_, s) =>
                  api.qq.cancelQR(
                    orgId,
                    id,
                    { version: v.version, remote_checked: false },
                    s,
                  )
                }
              />
            )}
            {v.status === "UNKNOWN" && (
              <>
                <Callout tone="warning">
                  {t("integrations.qq.qrUnknown")}
                </Callout>
                <p>{t("integrations.qq.qrStopWait")}</p>
                <QQConfirmAction
                  label={t("integrations.qq.qrCloseUnknown")}
                  description={t("integrations.qq.qrUnknown")}
                  refresh={refresh}
                  remote
                  execute={(checked, s) =>
                    api.qq.cancelQR(
                      orgId,
                      id,
                      { version: v.version, remote_checked: checked },
                      s,
                    )
                  }
                />
              </>
            )}
            {v.status === "DONE" && v.has_candidate && canRecover && (
              <>
                <Callout tone="info">
                  {t("integrations.qq.webhookHelp")}
                </Callout>
                <Button
                  disabled={write.busy || write.blocked}
                  onClick={() =>
                    void write.run(
                      (s) => api.qq.recoverQR(orgId, id, v.version, s),
                      onClose,
                    )
                  }
                >
                  {t("integrations.qq.qrRecover")}
                </Button>
              </>
            )}
            <p>
              {t("integrations.qq.expires", {
                time: new Date(v.expires_at).toLocaleString(),
              })}
            </p>
          </>
        )}
        {!!write.error && (
          <Callout tone="danger">{qqError(t, write.error)}</Callout>
        )}
        <Button variant="outline" onClick={() => void query.refetch()}>
          {t("integrations.qq.reload")}
        </Button>
      </div>
    </Dialog>
  );
}
