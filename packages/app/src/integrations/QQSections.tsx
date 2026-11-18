import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  newIdempotencyKey,
  type QQBot,
  type QQBindingSession,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  Dialog,
  Field,
  Select,
  TextInput,
} from "@openfox/ui";
import { useApi } from "../context";
import { useDirectory, useOrgRole } from "../agents/queries";
import {
  SettingsCard,
  SettingsSection,
  SettingRow,
} from "../spaces/settings/parts";
import { qqKey, useQQRefresh, useQQWrite } from "./qq-hooks";
import { qqError, qqState } from "./qq-model";
import { QQBindingDialog, QQConfirmAction } from "./QQBindingDialogs";
import { QQManageDialog } from "./QQManageDialog";

export function QQSections({
  orgId,
  agentId,
  personalOnly = false,
}: {
  orgId: string;
  agentId?: string;
  personalOnly?: boolean;
}) {
  const api = useApi(),
    t = useT(),
    refresh = useQQRefresh(orgId);
  const query = useQuery({
    queryKey: qqKey(orgId),
    queryFn: ({ signal }) => api.qq.configuration(orgId, signal),
    retry: false,
    refetchInterval: 5000,
  });
  const [create, setCreate] = useState(false),
    [manage, setManage] = useState<string>(),
    [binding, setBinding] = useState<QQBindingSession>();
  const write = useQQWrite(refresh);
  if (query.isPending) return <p>{t("common.loading")}</p>;
  if (query.isError)
    return (
      <Callout tone="danger">
        {t("integrations.qq.loadFailed")}
        <Button onClick={() => void query.refetch()}>
          {t("integrations.qq.reload")}
        </Button>
      </Callout>
    );
  const visibleBots = query.data.bots.filter(
    (bot) => !agentId || bot.agent_id === agentId,
  );
  const visibleBotIDs = new Set(visibleBots.map((bot) => bot.id));
  const config = {
    ...query.data,
    bots: visibleBots,
    bindings: query.data.bindings.filter(
      (v) => !agentId || visibleBotIDs.has(v.bot_id),
    ),
    binding_sessions: query.data.binding_sessions.filter(
      (v) => !agentId || visibleBotIDs.has(v.bot_id),
    ),
  };
  const startPrivate = (bot: QQBot) =>
    void write.run(
      (s) =>
        api.qq.startBinding(
          orgId,
          bot.id,
          {
            version: bot.version,
            purpose: "PRIVATE_CODE",
            idempotency_key: newIdempotencyKey(),
          },
          s,
        ),
      setBinding,
    );
  return (
    <div className="flex flex-col gap-5">
      <Callout tone="info">{t("integrations.qq.scope")}</Callout>
      {!config.configured && (
        <Callout tone="warning">{t("integrations.qq.notConfigured")}</Callout>
      )}
      {!personalOnly && !config.qr_configured && (
        <Callout tone="warning">{t("integrations.qq.qrNotConfigured")}</Callout>
      )}
      {!!write.error && (
        <Callout tone="danger">{qqError(t, write.error)}</Callout>
      )}
      <SettingsSection
        title={t(
          personalOnly
            ? "integrations.qq.privateBind"
            : "integrations.qq.create",
        )}
        description={personalOnly ? undefined : t("integrations.qq.slot")}
      >
        <div className="flex gap-2">
          {!personalOnly && (
            <Button
              disabled={!config.configured || !config.qr_configured}
              onClick={() => setCreate(true)}
            >
              {t("integrations.qq.create")}
            </Button>
          )}
          <Button variant="outline" onClick={refresh}>
            {t("integrations.qq.reload")}
          </Button>
        </div>
        {config.bots.length === 0 && <p>{t("integrations.qq.empty")}</p>}
        {config.bots.map((bot) => (
          <SettingsCard key={bot.id}>
            <SettingRow
              title={bot.name}
              description={bot.app_id || undefined}
              control={
                <>
                  <Badge>{qqState(t, bot.state)}</Badge>
                  <div className="flex flex-wrap gap-2">
                    {!personalOnly && bot.can_manage && (
                      <Button
                        variant="outline"
                        onClick={() => setManage(bot.id)}
                      >
                        {t("integrations.qq.manage")}
                      </Button>
                    )}
                    <Button
                      variant="outline"
                      disabled={
                        bot.state !== "ACTIVE" || write.busy || write.blocked
                      }
                      onClick={() => startPrivate(bot)}
                    >
                      {t("integrations.qq.privateBind")}
                    </Button>
                  </div>
                </>
              }
            />
          </SettingsCard>
        ))}
      </SettingsSection>
      <SettingsSection title={t("integrations.qq.personal")}>
        {config.bindings.length === 0 && (
          <p>{t("integrations.qq.bindingsEmpty")}</p>
        )}
        {config.bindings.map((v) => (
          <SettingsCard key={v.id}>
            <SettingRow
              title={config.bots.find((b) => b.id === v.bot_id)?.name ?? "QQ"}
              description={t("integrations.qq.source", {
                scope: v.scope,
                chat: v.chat_id,
                subject: v.subject_id,
              })}
              control={
                <QQConfirmAction
                  label={t("integrations.qq.revoke")}
                  description={t("integrations.qq.revokeHint")}
                  refresh={refresh}
                  execute={(_, s) => api.qq.revoke(orgId, v.id, v.version, s)}
                />
              }
            />
          </SettingsCard>
        ))}
      </SettingsSection>
      {config.binding_sessions.length > 0 && (
        <SettingsSection title={t("integrations.qq.invites")}>
          {config.binding_sessions.map((v) => (
            <SettingsCard key={v.id}>
              <SettingRow
                title={config.bots.find((b) => b.id === v.bot_id)?.name ?? "QQ"}
                description={qqState(t, v.status)}
                control={
                  <Button variant="outline" onClick={() => setBinding(v)}>
                    {t("integrations.qq.bindingTitle")}
                  </Button>
                }
              />
            </SettingsCard>
          ))}
        </SettingsSection>
      )}
      {!personalOnly && create && (
        <QQCreateDialog
          orgId={orgId}
          agentId={agentId}
          onClose={() => setCreate(false)}
          onCreated={(b) => {
            setCreate(false);
            setManage(b.id);
          }}
          refresh={refresh}
        />
      )}
      {!personalOnly && manage && (
        <QQManageDialog
          key={manage}
          orgId={orgId}
          botID={manage}
          qrConfigured={config.qr_configured}
          onClose={() => setManage(undefined)}
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
    </div>
  );
}
function QQCreateDialog({
  orgId,
  agentId,
  onClose,
  onCreated,
  refresh,
}: {
  orgId: string;
  agentId?: string;
  onClose: () => void;
  onCreated: (v: QQBot) => void;
  refresh: () => void;
}) {
  const t = useT(),
    api = useApi(),
    directory = useDirectory(orgId),
    role = useOrgRole(orgId),
    write = useQQWrite(refresh);
  const [target, setTarget] = useState<string | null>(agentId ?? null),
    [name, setName] = useState(""),
    [key] = useState(newIdempotencyKey);
  const options = (directory.data?.entries ?? [])
    .filter(
      (a) =>
        a.type === "agent" &&
        (!agentId || a.id === agentId) &&
        !a.archived_at &&
        (a.mine || role === "OWNER" || role === "ADMIN"),
    )
    .map((a) => ({ value: a.id, label: a.name }));
  const valid =
    options.some((a) => a.value === target) &&
    name.trim().length > 0 &&
    Array.from(name.trim()).length <= 200;
  return (
    <Dialog
      open
      title={t("integrations.qq.create")}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={!valid || write.busy || write.blocked}
            onClick={() =>
              void write.run(
                (s) =>
                  api.qq.create(
                    orgId,
                    {
                      agent_id: target!,
                      name: name.trim(),
                      idempotency_key: key,
                    },
                    s,
                  ),
                onCreated,
              )
            }
          >
            {t("common.create")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <p>{t("integrations.qq.slot")}</p>
        <Field label={t("integrations.qq.agent")}>
          <Select
            aria-label={t("integrations.qq.agent")}
            value={target}
            onChange={setTarget}
            options={options}
          />
        </Field>
        <Field label={t("integrations.qq.name")}>
          <TextInput
            aria-label={t("integrations.qq.name")}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        {!!write.error && (
          <Callout tone="danger">{qqError(t, write.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
