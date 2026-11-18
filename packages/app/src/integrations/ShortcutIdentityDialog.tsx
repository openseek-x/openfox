import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  isApiError,
  type AccountBindingInstance,
  type AccountBindingProvider,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  Callout,
  Checkbox,
  Dialog,
  Field,
  Select,
  TextInput,
  useToast,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import { integrationErrorText } from "./queries";

const errors: Record<string, MessageKey> = {
  SHORTCUT_AUTH_FAILED: "accountBinding.shortcut.errors.auth",
  SHORTCUT_EMAIL_MISMATCH: "accountBinding.shortcut.errors.email",
  SHORTCUT_WORKSPACE_MISMATCH: "accountBinding.shortcut.errors.workspace",
  SHORTCUT_ACCOUNT_IN_USE: "accountBinding.shortcut.errors.inUse",
  SHORTCUT_RATE_LIMITED: "accountBinding.shortcut.errors.rate",
};

export function ShortcutIdentityDialog({
  orgId,
  provider,
  connectionId,
  email,
  onClose,
}: {
  orgId: string;
  provider: AccountBindingProvider;
  connectionId: string;
  email: string;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const query = useQueryClient();
  const toast = useToast();
  const instances = provider.instances ?? [];
  // Keep the version the member opened. A list refetch must not silently give
  // an old draft permission to replace a newer binding or comment grant.
  const [selected, setSelected] = useState<AccountBindingInstance | undefined>(
    () => instances.find((item) => item.connection_id === connectionId),
  );
  const [commentsEnabled, setCommentsEnabled] = useState(
    selected?.binding?.shortcut_identity?.comments_enabled ?? false,
  );
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const binding = selected?.binding;
  const closingComments =
    binding?.shortcut_identity?.comments_enabled === true && !commentsEnabled;
  const tokenValid =
    token.length > 0 &&
    token.length <= 4096 &&
    !/[\u0000-\u001f\u007f]/.test(token);
  const ready =
    Boolean(selected) &&
    instances.some((item) => item.connection_id === selected?.connection_id) &&
    (tokenValid || (closingComments && token.length === 0));
  const refresh = () => {
    void query.invalidateQueries({ queryKey: keys.accountBindings(orgId) });
    void query.invalidateQueries({ queryKey: keys.integrations(orgId) });
  };
  const save = useMutation({
    gcTime: 0,
    mutationFn: () =>
      api.accountBindings.putShortcut(orgId, {
        connection_id: selected!.connection_id,
        version: binding?.version ?? 0,
        token,
        comments_enabled: commentsEnabled,
      }),
    onSuccess: () => {
      setToken("");
      refresh();
      toast(t("accountBinding.shortcut.saved"));
      onClose();
    },
    onError: (e) => {
      const key = isApiError(e) ? errors[e.code] : undefined;
      setError(key ? t(key) : integrationErrorText(t, e));
      if (isApiError(e, "VERSION_CONFLICT")) refresh();
    },
  });
  const close = () => {
    if (save.isPending) return;
    setToken("");
    save.reset();
    onClose();
  };
  return (
    <Dialog
      open
      onClose={close}
      title={t("accountBinding.shortcut.title")}
      footer={
        <>
          <Button variant="outline" onClick={close} disabled={save.isPending}>
            {t("common.cancel")}
          </Button>
          <Button
            type="submit"
            form="shortcut-identity"
            loading={save.isPending}
            disabled={!ready || save.isPending}
          >
            {closingComments && token.length === 0
              ? t("accountBinding.shortcut.disableComments")
              : t("accountBinding.shortcut.save")}
          </Button>
        </>
      }
    >
      <form
        id="shortcut-identity"
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          if (ready && !save.isPending) save.mutate();
        }}
      >
        <Callout tone="info">
          {t("accountBinding.shortcut.explanation", { email })}
        </Callout>
        <Field label={t("accountBinding.shortcut.workspace")}>
          <Select
            aria-label={t("accountBinding.shortcut.workspace")}
            value={selected?.connection_id ?? ""}
            disabled={save.isPending}
            options={instances.map((item) => ({
              value: item.connection_id,
              label: `${item.display_name} · ${item.host}`,
            }))}
            onChange={(value) => {
              const next = instances.find(
                (item) => item.connection_id === value,
              );
              setSelected(next);
              setCommentsEnabled(
                next?.binding?.shortcut_identity?.comments_enabled ?? false,
              );
              setToken("");
              setError("");
            }}
          />
        </Field>
        {binding && (
          <p className="text-body-sm text-fg-muted">
            {t("accountBinding.shortcut.identity", {
              name: binding.display_name,
            })}
            {" · "}
            {t(
              binding.shortcut_identity?.comments_enabled
                ? "accountBinding.shortcut.commentsOn"
                : "accountBinding.shortcut.identityOnly",
            )}
          </p>
        )}
        <label className="flex items-start gap-2 text-body-sm">
          <Checkbox
            checked={commentsEnabled}
            disabled={save.isPending}
            onChange={(event) => setCommentsEnabled(event.target.checked)}
          />
          <span>{t("accountBinding.shortcut.allowComments")}</span>
        </label>
        <Callout tone={commentsEnabled ? "warning" : "info"}>
          {t(
            commentsEnabled
              ? "accountBinding.shortcut.commentsWarning"
              : "accountBinding.shortcut.identityHelp",
          )}
        </Callout>
        <Field
          label={t("accountBinding.shortcut.token")}
          helper={t(
            closingComments
              ? "accountBinding.shortcut.disableHelp"
              : "accountBinding.shortcut.tokenHelp",
          )}
        >
          <TextInput
            aria-label={t("accountBinding.shortcut.token")}
            type="password"
            autoComplete="off"
            value={token}
            maxLength={4096}
            disabled={save.isPending}
            onChange={(event) => setToken(event.target.value)}
          />
        </Field>
        {error && (
          <p role="alert" className="text-body-sm text-danger">
            {error}
          </p>
        )}
      </form>
    </Dialog>
  );
}
