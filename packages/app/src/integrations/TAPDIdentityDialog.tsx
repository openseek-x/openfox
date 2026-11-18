import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { isApiError, type AccountBindingProvider } from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  Callout,
  Dialog,
  Field,
  Select,
  TextInput,
  useToast,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import { integrationErrorText } from "./queries";

const errors: Record<string, MessageKey> = {
  TAPD_AUTH_FAILED: "accountBinding.tapd.errors.auth",
  TAPD_PROJECT_UNAVAILABLE: "accountBinding.tapd.errors.project",
  TAPD_EMAIL_MISMATCH: "accountBinding.tapd.errors.email",
  TAPD_BASIC_REQUIRED: "accountBinding.tapd.errors.basic",
  TAPD_ACCOUNT_IN_USE: "accountBinding.tapd.errors.inUse",
  TAPD_PROJECT_LIMIT: "accountBinding.tapd.errors.limit",
  TAPD_RATE_LIMITED: "accountBinding.tapd.errors.rate",
};

export function TAPDIdentityDialog({
  orgId,
  provider,
  email,
  onClose,
}: {
  orgId: string;
  provider: AccountBindingProvider;
  email: string;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const query = useQueryClient();
  const toast = useToast();
  const instances = provider.instances ?? [];
  const [parent, setParent] = useState(
    instances.find((i) => i.binding)?.connection_id ??
      instances.find((i) => i.identity_supported)?.connection_id ??
      instances[0]?.connection_id ??
      "",
  );
  const [project, setProject] = useState("");
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const current = instances.find((i) => i.connection_id === parent);
  const binding = current?.binding;
  const refresh = () => {
    void query.invalidateQueries({ queryKey: keys.accountBindings(orgId) });
    void query.invalidateQueries({ queryKey: keys.integrations(orgId) });
  };
  const failed = (e: unknown) => {
    const key = isApiError(e) ? errors[e.code] : undefined;
    setError(key ? t(key) : integrationErrorText(t, e));
    if (isApiError(e, "VERSION_CONFLICT")) refresh();
  };
  const save = useMutation({
    gcTime: 0,
    mutationFn: () =>
      api.accountBindings.putTAPD(orgId, {
        connection_id: parent,
        project_id: project,
        version: binding?.version ?? 0,
        access_token: token,
      }),
    onSuccess: () => {
      setToken("");
      setProject("");
      setError("");
      refresh();
      toast(t("accountBinding.connectedToast"));
    },
    onError: failed,
  });
  const remove = useMutation({
    mutationFn: (id: string) =>
      api.accountBindings.removeTAPDProject(orgId, id, {
        binding_id: binding!.connection_id,
        version: binding!.version,
      }),
    onSuccess: () => {
      setError("");
      refresh();
    },
    onError: failed,
  });
  const busy = save.isPending || remove.isPending;
  const ready =
    current?.identity_supported &&
    /^[0-9]{1,32}$/.test(project) &&
    token.length > 0 &&
    token.length <= 4096;
  return (
    <Dialog
      open
      onClose={() => {
        setToken("");
        save.reset();
        onClose();
      }}
      title={t("accountBinding.tapd.title")}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            type="submit"
            form="tapd-identity"
            loading={save.isPending}
            disabled={!ready || busy}
          >
            {t("accountBinding.tapd.verify")}
          </Button>
        </>
      }
    >
      <form
        id="tapd-identity"
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          if (ready && !busy) save.mutate();
        }}
      >
        <Callout tone="info">
          {t("accountBinding.tapd.explanation", { email })}
        </Callout>
        <Field label={t("accountBinding.tapd.parent")}>
          <Select
            aria-label={t("accountBinding.tapd.parent")}
            value={parent}
            disabled={busy}
            options={instances.map((i) => ({
              value: i.connection_id,
              label: `${i.display_name} · ${i.host || "TAPD"}`,
            }))}
            onChange={(value) => {
              setParent(value ?? "");
              setToken("");
              setError("");
            }}
          />
        </Field>
        {!current?.identity_supported && (
          <Callout tone="warning">
            {t("accountBinding.tapd.errors.basic")}
          </Callout>
        )}
        {binding?.tapd_identity && (
          <div className="flex flex-col gap-2">
            <p>
              {t("accountBinding.tapd.identity", {
                nick: binding.tapd_identity.nick,
              })}
            </p>
            <p className="text-body-sm text-fg-muted">
              {t("accountBinding.tapd.scopes")}
            </p>
            {binding.tapd_identity.projects.map((id) => (
              <div key={id} className="flex items-center justify-between gap-3">
                <span>{id}</span>
                <Button
                  variant="outline"
                  size="32"
                  disabled={busy}
                  onClick={() => remove.mutate(id)}
                  aria-label={t("accountBinding.tapd.remove", { project: id })}
                >
                  {t("accountBinding.tapd.remove", { project: id })}
                </Button>
              </div>
            ))}
          </div>
        )}
        <Field
          label={t("accountBinding.tapd.project")}
          helper={t("accountBinding.tapd.projectHelp")}
        >
          <TextInput
            aria-label={t("accountBinding.tapd.project")}
            value={project}
            maxLength={32}
            disabled={busy}
            onChange={(e) => setProject(e.target.value)}
          />
        </Field>
        <Field
          label={t("accountBinding.tapd.token")}
          helper={t("accountBinding.tapd.tokenHelp")}
        >
          <TextInput
            aria-label={t("accountBinding.tapd.token")}
            type="password"
            autoComplete="off"
            value={token}
            maxLength={4096}
            disabled={busy}
            onChange={(e) => setToken(e.target.value)}
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
