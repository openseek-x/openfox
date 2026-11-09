import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import type { WeComBindingCode, WeComPersonalView } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Button, ConfirmDialog, useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import { useFeishuBots, useWeComPersonal } from "./queries";

type Binding = WeComPersonalView["bindings"][number];

export function WeComPersonalSection({
  orgId,
  title,
  description,
}: {
  orgId: string;
  title: string;
  description: string;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [code, setCode] = useState<WeComBindingCode | null>(null);
  const [removing, setRemoving] = useState<Binding | null>(null);
  const bots = useFeishuBots(orgId);
  const personal = useWeComPersonal(orgId);
  useEffect(() => {
    if (!code) return;
    const timer = window.setInterval(() => void personal.refetch(), 5000);
    return () => window.clearInterval(timer);
  }, [code, personal.refetch]);
  const allBots = useMemo(() => {
    const found = new Map<string, { id: string; name: string }>();
    for (const bot of bots.data?.agents ?? [])
      found.set(bot.id, { id: bot.id, name: bot.bot_name || bot.agent.name });
    for (const row of bots.data?.assistants ?? []) {
      if (!row.bot) continue;
      found.set(row.bot.id, {
        id: row.bot.id,
        name: row.bot.bot_name || row.space_name,
      });
    }
    return [...found.values()];
  }, [bots.data]);
  const bindings = personal.data?.bindings ?? [];
  useEffect(() => {
    if (code && bindings.some((binding) => binding.bot_id === code.bot_id))
      setCode(null);
  }, [bindings, code]);
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) });
  const createCode = useMutation({
    mutationFn: (botId: string) =>
      api.integrations.createWeComBindingCode(orgId, botId),
    onSuccess: setCode,
    onError: () => toast(t("integrations.wecom.codeFailed"), { tone: "error" }),
  });
  const remove = useMutation({
    mutationFn: (botId: string) =>
      api.integrations.removeWeComPersonal(orgId, botId),
    onSuccess: () => {
      setRemoving(null);
      void refresh();
    },
    onError: () =>
      toast(t("integrations.wecom.unbindFailed"), { tone: "error" }),
  });
  const target = useMutation({
    mutationFn: (botId: string | null) =>
      api.integrations.setWeComNotificationTarget(orgId, botId),
    onSuccess: () => void refresh(),
    onError: () =>
      toast(t("integrations.wecom.targetFailed"), { tone: "error" }),
  });
  return (
    <SettingsSection title={title} description={description}>
      {personal.isError || bots.isError ? (
        <p role="alert">{t("integrations.loadFailed")}</p>
      ) : (
        <SettingsCard>
          {allBots.length === 0 && (
            <SettingRow
              title={t("integrations.wecom.noBots")}
              description={t("integrations.wecom.noBotsHelp")}
            />
          )}
          {allBots.map((bot) => {
            const binding = bindings.find((item) => item.bot_id === bot.id);
            return (
              <SettingRow
                key={bot.id}
                title={bot.name}
                description={
                  binding
                    ? t("integrations.wecom.boundAs", {
                        user: binding.external_id,
                      })
                    : t("integrations.wecom.unbound")
                }
                control={
                  binding ? (
                    <span className="flex items-center gap-2">
                      {binding.notification_target && (
                        <Badge tone="success">
                          {t("integrations.wecom.target")}
                        </Badge>
                      )}
                      <Button
                        size="28"
                        variant="outline"
                        disabled={target.isPending}
                        onClick={() =>
                          target.mutate(
                            binding.notification_target ? null : bot.id,
                          )
                        }
                      >
                        {t(
                          binding.notification_target
                            ? "integrations.wecom.disableTarget"
                            : "integrations.wecom.enableTarget",
                        )}
                      </Button>
                      <Button
                        size="28"
                        variant="outline"
                        onClick={() => setRemoving(binding)}
                      >
                        {t("integrations.wecom.unbind")}
                      </Button>
                    </span>
                  ) : (
                    <Button
                      size="28"
                      disabled={createCode.isPending}
                      onClick={() => createCode.mutate(bot.id)}
                    >
                      {t("integrations.wecom.bind")}
                    </Button>
                  )
                }
              />
            );
          })}
        </SettingsCard>
      )}
      {code && (
        <div className="rounded-xl border border-line-default bg-surface-card p-4">
          <p className="text-body-sm text-fg-muted">
            {t("integrations.wecom.codeHelp")}
          </p>
          <code className="mt-2 block select-all text-title font-semibold">
            {code.code}
          </code>
          <p className="mt-2 text-caption text-fg-muted">
            {t("integrations.wecom.codeExpires", {
              time: new Date(code.expires_at).toLocaleString(),
            })}
          </p>
        </div>
      )}
      {removing && (
        <ConfirmDialog
          open
          danger
          title={t("integrations.wecom.unbind")}
          description={t("integrations.wecom.unbindConfirm")}
          confirmLabel={t("integrations.wecom.unbind")}
          loading={remove.isPending}
          onCancel={() => setRemoving(null)}
          onConfirm={() => remove.mutate(removing.bot_id)}
        />
      )}
    </SettingsSection>
  );
}
