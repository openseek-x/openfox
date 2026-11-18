import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { IntegrationSettingsValues } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Callout } from "@openfox/ui";
import { useApi } from "../context";
import { SettingsSection, SettingsCard } from "../spaces/settings/parts";
import { integrationErrorText } from "./queries";
export function AzureRepoSettings({
  org,
  canManage,
}: {
  org: string;
  canManage: boolean;
}) {
  const api = useApi(),
    t = useT(),
    cache = useQueryClient();
  const key = ["azure-settings", org];
  const q = useQuery({
    queryKey: key,
    queryFn: () => api.integrations.settings(org, "azure_devops"),
    retry: false,
  });
  const save = useMutation({
    mutationFn: (settings: IntegrationSettingsValues) =>
      api.integrations.putSettings(org, "azure_devops", {
        version: q.data!.version,
        settings,
      }),
    onSuccess: () => cache.invalidateQueries({ queryKey: key }),
  });
  const choices = [
    ["link_pull_requests", "linkPR"],
    ["link_commits", "linkCommits"],
    ["pr_backlink_private", "privateBacklink"],
    ["pr_backlink_public", "publicBacklink"],
  ] as const;
  return (
    <SettingsSection title={t("integrations.azure.settings")}>
      <p>{t("integrations.azure.backlinkHelp")}</p>
      {(q.isError || save.isError) && (
        <Callout tone="warning">
          {integrationErrorText(t, save.error ?? q.error)}
        </Callout>
      )}
      <SettingsCard>
        <div className="grid gap-3 p-4">
          {q.data &&
            choices.map(([field, label]) => (
              <label className="flex items-start gap-2" key={field}>
                <input
                  type="checkbox"
                  checked={q.data.settings[field]}
                  disabled={!canManage || save.isPending}
                  onChange={(e) =>
                    save.mutate({
                      ...q.data!.settings,
                      [field]: e.target.checked,
                    })
                  }
                />
                {t(`integrations.azure.${label}`)}
              </label>
            ))}
        </div>
      </SettingsCard>
    </SettingsSection>
  );
}
