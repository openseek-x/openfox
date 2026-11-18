import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { IntegrationDetail, IntegrationConnection } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, Field, Select, TextInput } from "@openfox/ui";
import { keys, useApi } from "../context";
import { SettingsSection } from "../spaces/settings/parts";
import { DisconnectDialog } from "./DisconnectDialog";
import { integrationErrorText } from "./queries";

export function YunxiaoSection({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient();
  const [mode, setMode] = useState<"personal" | "credentials" | null>(null),
    [parent, setParent] = useState(""),
    [token, setToken] = useState("");
  const [disconnect, setDisconnect] = useState<IntegrationConnection | null>(
    null,
  );
  const active = detail.connections.filter((c) => c.status === "ACTIVE");
  const own = detail.personal_connections.filter(
    (c) => c.account_kind === "identity" && c.status !== "DISCONNECTED",
  );
  const choices =
    mode === "personal"
      ? active.filter((c) => !own.some((p) => p.parent_connection_id === c.id))
      : active;
  function close() {
    if (save.isPending) return;
    setMode(null);
    setToken("");
    save.reset();
  }
  const save = useMutation({
    mutationFn: () =>
      mode === "personal"
        ? api.integrations.bindYunxiaoPersonal(orgId, {
            connection_id: parent,
            token,
          })
        : api.integrations.updateYunxiaoCredentials(orgId, parent, {
            version: active.find((c) => c.id === parent)!.version,
            token,
          }),
    onSuccess: async () => {
      setToken("");
      setMode(null);
      await Promise.all([
        cache.invalidateQueries({ queryKey: keys.integrations(orgId) }),
        cache.invalidateQueries({ queryKey: keys.accountBindings(orgId) }),
      ]);
    },
  });
  function open(next: "personal" | "credentials") {
    setMode(next);
    setParent("");
    setToken("");
    save.reset();
  }
  return (
    <SettingsSection title={t("integrations.yunxiao.identityTitle")}>
      <div className="flex flex-col gap-3">
        <Callout>{t("integrations.yunxiao.identityDescription")}</Callout>
        {own.map((c) => (
          <div key={c.id} className="flex items-center justify-between gap-3">
            <span>
              {c.display_name} ·{" "}
              {
                active.find((p) => p.id === c.parent_connection_id)
                  ?.display_name
              }
            </span>
            <Button
              size="28"
              variant="outline"
              onClick={() => setDisconnect(c)}
            >
              {t("shell.disconnectProvider")}
            </Button>
          </div>
        ))}
        <div className="flex gap-2">
          <Button
            variant="outline"
            disabled={
              !active.some(
                (c) => !own.some((p) => p.parent_connection_id === c.id),
              )
            }
            onClick={() => open("personal")}
          >
            {t("integrations.yunxiao.verifyMine")}
          </Button>
          {detail.can_manage && (
            <Button
              variant="outline"
              disabled={!active.length}
              onClick={() => open("credentials")}
            >
              {t("integrations.yunxiao.updateToken")}
            </Button>
          )}
        </div>
      </div>
      {mode && (
        <Dialog
          open
          title={t(
            mode === "personal"
              ? "integrations.yunxiao.verifyMine"
              : "integrations.yunxiao.updateToken",
          )}
          onClose={save.isPending ? undefined : close}
          footer={
            <>
              <Button
                variant="outline"
                onClick={close}
                disabled={save.isPending}
              >
                {t("common.cancel")}
              </Button>
              <Button
                disabled={!parent || !token}
                loading={save.isPending}
                onClick={() => save.mutate()}
              >
                {t("common.save")}
              </Button>
            </>
          }
        >
          <div className="flex flex-col gap-4">
            <p>
              {t(
                mode === "personal"
                  ? "integrations.yunxiao.identityDescription"
                  : "integrations.yunxiao.sameHolder",
              )}
            </p>
            <Field label={t("imports.wizard.instance")}>
              <Select
                aria-label={t("imports.wizard.instance")}
                value={parent || null}
                options={choices.map((c) => ({
                  value: c.id,
                  label: c.display_name,
                }))}
                onChange={setParent}
              />
            </Field>
            <Field label={t("shell.accessToken")}>
              <TextInput
                type="password"
                autoComplete="off"
                aria-label={t("shell.accessToken")}
                value={token}
                onChange={(e) => setToken(e.target.value)}
              />
            </Field>
            {save.isError && (
              <Callout tone="danger">
                {integrationErrorText(t, save.error)}
              </Callout>
            )}
          </div>
        </Dialog>
      )}
      {disconnect && (
        <DisconnectDialog
          orgId={orgId}
          provider={detail.provider}
          connection={disconnect}
          onClose={() => setDisconnect(null)}
        />
      )}
    </SettingsSection>
  );
}
