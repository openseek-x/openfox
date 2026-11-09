import { useState } from "react";

import {
  isApiError,
  type IntegrationConnection,
  type IntegrationProvider,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, TextInput, useToast } from "@openfox/ui";

import { confirmPhrase } from "./model";
import { integrationErrorText, useDisconnectConnection } from "./queries";

export function DisconnectDialog({
  orgId,
  provider,
  connection,
  onClose,
  body,
}: {
  orgId: string;
  provider: IntegrationProvider;
  connection: IntegrationConnection;
  onClose: () => void;
  /** 组织连接的确认说明，缺省用通用文案 */
  body?: string;
}) {
  const t = useT();
  const toast = useToast();
  const disconnect = useDisconnectConnection(orgId);
  const [text, setText] = useState("");
  const isOrg = connection.scope === "ORGANIZATION";
  const phrase = confirmPhrase(provider.name);
  const ready = !isOrg || text.trim() === phrase;
  const submit = () =>
    disconnect.mutate(
      {
        connectionId: connection.id,
        version: connection.version,
        ...(isOrg ? { confirmPhrase: text.trim() } : {}),
      },
      {
        onSuccess: () => {
          toast(t("integrations.disconnected"));
          onClose();
        },
        onError: (error) => {
          toast(integrationErrorText(t, error), { tone: "error" });
          // 版本冲突时详情已被刷新，关闭弹窗让用户基于最新数据重试
          if (isApiError(error, "VERSION_CONFLICT")) onClose();
        },
      },
    );
  return (
    <Dialog
      open
      onClose={disconnect.isPending ? undefined : onClose}
      title={
        isOrg
          ? t("integrations.disconnectTitle", { name: provider.name })
          : t("integrations.disconnectPersonalTitle", { name: provider.name })
      }
      description={
        isOrg
          ? (body ?? t("integrations.disconnectBody", { name: provider.name }))
          : t("integrations.disconnectPersonalBody", { name: provider.name })
      }
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={disconnect.isPending}
            data-autofocus
          >
            {t("common.cancel")}
          </Button>
          <Button
            variant="danger"
            onClick={submit}
            loading={disconnect.isPending}
            disabled={!ready}
          >
            {t("integrations.disconnect")}
          </Button>
        </>
      }
    >
      {isOrg && (
        <label className="flex flex-col gap-1.5 text-body-sm text-fg-secondary">
          {t("integrations.confirmLabel", { phrase })}
          <TextInput
            aria-label={t("integrations.confirmInput")}
            value={text}
            placeholder={phrase}
            onChange={(event) => setText(event.target.value)}
          />
        </label>
      )}
    </Dialog>
  );
}
