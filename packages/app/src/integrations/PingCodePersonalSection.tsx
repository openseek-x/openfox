import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import type { IntegrationConnection, IntegrationDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Select, TextInput } from "@openfox/ui";

import { keys, useApi } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import { DisconnectDialog } from "./DisconnectDialog";
import { integrationErrorText } from "./queries";

export function PingCodePersonalSection({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT();
  const api = useApi();
  const cache = useQueryClient();
  const parents = detail.connections.filter((item) => item.status === "ACTIVE");
  const personal = detail.personal_connections.filter(
    (item) =>
      item.status !== "DISCONNECTED" && item.account_kind === "sync_comments",
  );
  const [open, setOpen] = useState(false);
  const [parentId, setParentId] = useState("");
  const [token, setToken] = useState("");
  const [disconnecting, setDisconnecting] =
    useState<IntegrationConnection | null>(null);
  const existing = personal.find(
    (item) => item.parent_connection_id === parentId,
  );
  const save = useMutation({
    mutationFn: () =>
      api.integrations.bindPingCodePersonal(orgId, {
        connection_id: parentId,
        version: existing?.version ?? 0,
        token: token.trim(),
      }),
    onSuccess: async () => {
      setOpen(false);
      setToken("");
      await Promise.all([
        cache.invalidateQueries({ queryKey: keys.integrations(orgId) }),
        cache.invalidateQueries({ queryKey: keys.accountBindings(orgId) }),
      ]);
    },
  });
  const close = () => {
    if (save.isPending) return;
    setOpen(false);
    setToken("");
    save.reset();
  };
  return (
    <SettingsSection
      title={t("integrations.pingcodePersonal.title")}
      description={t("integrations.pingcodePersonal.description")}
      action={
        <Button
          size="32"
          disabled={!parents.length}
          onClick={() => {
            setParentId(parents[0]!.id);
            setOpen(true);
          }}
        >
          {t("integrations.pingcodePersonal.bind")}
        </Button>
      }
    >
      <SettingsCard>
        {personal.length ? (
          personal.map((item) => (
            <SettingRow
              key={item.id}
              title={item.display_name}
              description={t("integrations.pingcodePersonal.boundHelp")}
              control={
                <Button
                  size="28"
                  variant="outline"
                  onClick={() => setDisconnecting(item)}
                >
                  {t("integrations.pingcodePersonal.unbind")}
                </Button>
              }
            />
          ))
        ) : (
          <SettingRow
            title={t("integrations.pingcodePersonal.empty")}
            description={t("integrations.pingcodePersonal.emptyHelp")}
          />
        )}
      </SettingsCard>
      {open && (
        <Dialog
          open
          title={t("integrations.pingcodePersonal.bind")}
          onClose={save.isPending ? undefined : close}
          footer={
            <>
              <Button
                variant="outline"
                disabled={save.isPending}
                onClick={close}
              >
                {t("common.cancel")}
              </Button>
              <Button
                loading={save.isPending}
                disabled={!parentId || !token.trim()}
                onClick={() => save.mutate()}
              >
                {t("integrations.pingcodePersonal.bind")}
              </Button>
            </>
          }
        >
          <div className="flex flex-col gap-4">
            <p className="text-body-sm text-fg-muted">
              {t("integrations.pingcodePersonal.tokenHelp")}
            </p>
            <label className="flex flex-col gap-1.5">
              {t("integrations.pingcodePersonal.company")}
              <Select
                aria-label={t("integrations.pingcodePersonal.company")}
                value={parentId}
                onChange={setParentId}
                options={parents.map((item) => ({
                  value: item.id,
                  label: item.display_name || item.external_id,
                }))}
              />
            </label>
            <label className="flex flex-col gap-1.5">
              {t("integrations.pingcodePersonal.token")}
              <TextInput
                type="password"
                autoComplete="off"
                aria-label={t("integrations.pingcodePersonal.token")}
                value={token}
                onChange={(event) => setToken(event.target.value)}
                maxLength={4096}
              />
            </label>
            {save.error && (
              <p role="alert" className="text-body-sm text-danger">
                {integrationErrorText(t, save.error)}
              </p>
            )}
          </div>
        </Dialog>
      )}
      {disconnecting && (
        <DisconnectDialog
          orgId={orgId}
          provider={detail.provider}
          connection={disconnecting}
          onClose={() => {
            setDisconnecting(null);
            void cache.invalidateQueries({
              queryKey: keys.accountBindings(orgId),
            });
          }}
        />
      )}
    </SettingsSection>
  );
}
