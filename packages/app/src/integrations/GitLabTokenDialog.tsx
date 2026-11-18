import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  isApiError,
  type IntegrationConnection,
  type OAuthPurpose,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, Select, TextInput } from "@openfox/ui";
import { keys, useApi } from "../context";
import { useDevices, useMe } from "../computers/queries";
import { integrationErrorText } from "./queries";

export function GitLabTokenDialog({
  orgId,
  purpose = "organization",
  connection,
  onClose,
}: {
  orgId: string;
  purpose?: OAuthPurpose;
  connection?: IntegrationConnection;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();
  const [instance, setInstance] = useState(connection?.instance_url ?? "");
  const me = useMe();
  const devices = useDevices(orgId, purpose === "organization");
  const [proxy, setProxy] = useState("");
  const computers = (devices.data ?? []).filter(
    (device) =>
      device.online &&
      device.owner_user_id === me.data?.id &&
      device.capabilities.gitlab_proxy === true,
  );
  const [token, setToken] = useState("");
  const save = useMutation({
    mutationFn: () =>
      api.integrations.bindGitLabToken(orgId, {
        purpose,
        access_token: token.trim(),
        ...(purpose === "organization"
          ? {
              instance_url: instance.trim(),
              ...(proxy && { proxy_device_id: proxy }),
            }
          : {
              connection_id: connection?.id,
              ...(purpose === "reauthorize"
                ? { version: connection?.version }
                : {}),
            }),
      }),
    onSuccess: async () => {
      setToken("");
      await queryClient.invalidateQueries({
        queryKey: keys.integrations(orgId),
      });
      onClose();
    },
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!token.trim() || !instance.trim() || save.isPending) return;
    save.mutate();
  };
  return (
    <Dialog
      open
      title={t("integrations.gitlab.tokenTitle")}
      description={t("integrations.gitlab.tokenDescription")}
      onClose={save.isPending ? undefined : onClose}
      width={520}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={save.isPending}>
            {t("common.cancel")}
          </Button>
          <Button
            type="submit"
            form="gitlab-token-form"
            loading={save.isPending}
            disabled={!token.trim() || !instance.trim()}
          >
            {t("integrations.gitlab.tokenConnect")}
          </Button>
        </>
      }
    >
      <form
        id="gitlab-token-form"
        onSubmit={submit}
        className="flex flex-col gap-4"
      >
        {save.error && (
          <Callout tone="danger">
            {isApiError(save.error, "INTEGRATION_NEEDS_REAUTH")
              ? t("integrations.gitlab.tokenInvalid")
              : integrationErrorText(t, save.error)}
          </Callout>
        )}
        <label className="flex flex-col gap-1.5">
          {t("integrations.gitlab.tokenTitle")}
          <TextInput
            type="password"
            autoComplete="off"
            aria-label={t("integrations.gitlab.tokenTitle")}
            placeholder="glpat-xxxxxxxxxxxxxxxxxxxx"
            maxLength={1024}
            value={token}
            onChange={(e) => setToken(e.target.value)}
            disabled={save.isPending}
          />
        </label>
        <label className="flex flex-col gap-1.5">
          {t("integrations.gitlab.instanceUrl")}
          <TextInput
            type="url"
            aria-label={t("integrations.gitlab.instanceUrl")}
            placeholder="https://gitlab.your-company.com"
            maxLength={2048}
            value={instance}
            onChange={(e) => setInstance(e.target.value)}
            readOnly={purpose !== "organization"}
            disabled={save.isPending}
            required
          />
        </label>
        {purpose === "organization" && (
          <label className="flex flex-col gap-1.5">
            {t("integrations.gitlab.proxy")}
            <Select
              aria-label={t("integrations.gitlab.proxy")}
              value={proxy}
              onChange={setProxy}
              disabled={save.isPending}
              options={[
                { value: "", label: t("integrations.gitlab.noProxy") },
                ...computers.map((device) => ({
                  value: device.id,
                  label: device.name,
                })),
              ]}
            />
          </label>
        )}
      </form>
    </Dialog>
  );
}
