import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { isApiError, type WeComGroup } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Checkbox, Dialog } from "@openfox/ui";
import { useApi } from "../context";

export function WeComGroupsDialog({
  orgId,
  botId,
  onClose,
}: {
  orgId: string;
  botId: string;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const [after, setAfter] = useState("");
  const [selected, setSelected] = useState<WeComGroup | null>(null);
  const [consent, setConsent] = useState(false);
  const query = useQuery({
    queryKey: ["wecom-groups", orgId, botId, after],
    queryFn: () => api.integrations.wecomGroups(orgId, botId, after),
  });
  const patch = useMutation({
    mutationFn: ({ group, enabled }: { group: WeComGroup; enabled: boolean }) =>
      api.integrations.patchWecomGroup(orgId, botId, {
        chat_id: group.chat_id,
        expected_version: group.version,
        enabled,
      }),
    onSuccess: async () => {
      setSelected(null);
      setConsent(false);
      await query.refetch();
    },
  });
  return (
    <Dialog
      open
      onClose={patch.isPending ? undefined : onClose}
      width={560}
      title={t("integrations.wecom.groups.title")}
      description={t("integrations.wecom.groups.description")}
    >
      <div className="flex flex-col gap-4">
        <p className="text-body-sm text-text-secondary">
          {t("integrations.wecom.groups.context")}
        </p>
        {(query.isError || patch.isError) && (
          <Callout tone="danger">
            {patch.isError &&
            isApiError(patch.error) &&
            patch.error.status === 409
              ? t("integrations.wecom.groups.conflict")
              : t("integrations.wecom.groups.failed")}
          </Callout>
        )}
        <Button
          disabled={query.isFetching || patch.isPending}
          onClick={() => {
            setSelected(null);
            setConsent(false);
            patch.reset();
            void query.refetch();
          }}
        >
          {t("integrations.wecom.groups.refresh")}
        </Button>
        {query.data?.groups.length === 0 && (
          <p>{t("integrations.wecom.groups.empty")}</p>
        )}
        {query.data?.groups.map((group) => (
          <div
            key={group.chat_id}
            className="flex items-center justify-between gap-4 border-b border-line-subtle py-2"
          >
            <div className="min-w-0">
              <p className="break-all font-mono text-body-sm">
                {group.chat_id}
              </p>
              <p className="text-body-sm">
                {t(
                  group.enabled
                    ? "integrations.wecom.groups.enabled"
                    : "integrations.wecom.groups.disabled",
                )}
              </p>
            </div>
            <Button
              disabled={patch.isPending || query.isFetching}
              onClick={() => {
                patch.reset();
                setConsent(false);
                if (group.enabled) {
                  setSelected(null);
                  patch.mutate({ group, enabled: false });
                } else setSelected(group);
              }}
            >
              {t(
                group.enabled
                  ? "integrations.wecom.groups.disable"
                  : "integrations.wecom.groups.enable",
              )}
            </Button>
          </div>
        ))}
        {selected && (
          <div className="flex flex-col gap-3 rounded border border-line-strong p-3">
            <p className="break-all font-mono">{selected.chat_id}</p>
            <p className="text-body-sm">
              {t("integrations.wecom.groups.reopen")}
            </p>
            <label className="flex items-start gap-2 text-body-sm">
              <Checkbox
                checked={consent}
                disabled={patch.isPending}
                onChange={(e) => setConsent(e.target.checked)}
              />
              {t("integrations.wecom.groups.consent")}
            </label>
            <div className="flex gap-2">
              <Button
                disabled={!consent || patch.isPending || patch.isError}
                onClick={() => patch.mutate({ group: selected, enabled: true })}
              >
                {t("integrations.wecom.groups.confirm")}
              </Button>
              <Button
                disabled={patch.isPending}
                onClick={() => {
                  setSelected(null);
                  setConsent(false);
                }}
              >
                {t("integrations.wecom.groups.cancel")}
              </Button>
            </div>
          </div>
        )}
        <div className="flex gap-2">
          {after && (
            <Button
              disabled={patch.isPending}
              onClick={() => {
                setAfter("");
                setSelected(null);
                setConsent(false);
              }}
            >
              {t("integrations.wecom.groups.first")}
            </Button>
          )}
          {query.data?.next_cursor && (
            <Button
              disabled={patch.isPending}
              onClick={() => {
                setAfter(query.data!.next_cursor);
                setSelected(null);
                setConsent(false);
              }}
            >
              {t("integrations.wecom.groups.next")}
            </Button>
          )}
        </div>
      </div>
    </Dialog>
  );
}
