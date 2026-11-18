import { useQuery } from "@tanstack/react-query";
import { useT } from "@openfox/i18n";
import { Callout, Select, Button } from "@openfox/ui";
import { useApi } from "../context";
import { azureApi, azureUUID } from "../integrations/azure-api";
import { integrationErrorText } from "../integrations/queries";
export function AzureImportOptions({
  org,
  connection,
  project,
  history,
  priority,
  onHistory,
  onPriority,
}: {
  org: string;
  connection: string;
  project: string;
  history: boolean;
  priority: string;
  onHistory: (v: boolean) => void;
  onPriority: (v: string) => void;
}) {
  const api = useApi(),
    t = useT();
  const meta = useQuery({
    queryKey: ["azure-metadata", org, connection, project],
    queryFn: () => azureApi(api.http).metadata(org, connection, project),
    enabled: !!connection && azureUUID(project),
    retry: false,
  });
  return (
    <div className="grid gap-3">
      <label className="flex items-start gap-2">
        <input
          type="checkbox"
          checked={history}
          onChange={(e) => onHistory(e.target.checked)}
        />
        {t("integrations.azure.history")}
      </label>
      <p>{t("integrations.azure.historyHelp")}</p>
      {meta.isError && (
        <Callout tone="warning">
          {integrationErrorText(t, meta.error)}
          <Button variant="outline" onClick={() => void meta.refetch()}>
            {t("common.retry")}
          </Button>
        </Callout>
      )}
      {meta.isFetching && <p role="status">{t("common.loading")}</p>}
      {meta.data && (
        <>
          <label>
            {t("integrations.azure.priorityField")}
            <Select
              aria-label={t("integrations.azure.priorityField")}
              value={priority || "none"}
              options={[
                { value: "none", label: t("integrations.azure.noPriority") },
                ...meta.data.metadata.fields
                  .filter(
                    (f) =>
                      !f.isIdentity &&
                      ["integer", "double", "string"].includes(f.type),
                  )
                  .map((f) => ({
                    value: f.referenceName,
                    label: `${f.name} · ${f.referenceName}`,
                  })),
              ]}
              onChange={(v) => onPriority(v === "none" ? "" : v)}
            />
          </label>
          <details>
            <summary>{t("integrations.azure.metadata")}</summary>
            <ul className="list-disc pl-5">
              {meta.data.metadata.types.map((v) => (
                <li key={v.type.referenceName}>
                  {v.type.name}: {v.states.map((s) => s.name).join(" · ")}
                </li>
              ))}
            </ul>
          </details>
        </>
      )}
    </div>
  );
}
