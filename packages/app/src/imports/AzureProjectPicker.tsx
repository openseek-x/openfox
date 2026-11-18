import { useState, type ReactNode } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useT } from "@openfox/i18n";
import { Button, Callout, Select, TextInput } from "@openfox/ui";
import { useApi } from "../context";
import {
  integrationErrorText,
  useIntegrationDetail,
} from "../integrations/queries";
import { azureApi, type AzureProjectsPage } from "../integrations/azure-api";
import { FormRow } from "./parts";

// Cursor chains are scoped to the selected connection version. Never follow a
// provider URI, loop a cursor or imply that a bounded list is exhaustive.
export function nextAzureProjectPage(
  last: AzureProjectsPage,
  pages: AzureProjectsPage[],
  pageParams: string[],
): string | undefined {
  if (
    !last.next_offset ||
    pages.length >= 50 ||
    pages.reduce((n, p) => n + p.projects.length, 0) >= 5000 ||
    pageParams.includes(last.next_offset)
  )
    return undefined;
  return last.next_offset;
}

export function AzureProjectPicker({
  orgId,
  connection,
  project,
  onConnection,
  onProject,
  children,
}: {
  orgId: string;
  connection: string;
  project: string;
  onConnection: (value: string) => void;
  onProject: (value: string) => void;
  children: ReactNode;
}) {
  const api = useApi(),
    t = useT();
  const detail = useIntegrationDetail(orgId, "azure_devops");
  const active =
    detail.data?.connections.filter(
      (c) => c.scope === "ORGANIZATION" && c.status === "ACTIVE",
    ) ?? [];
  const selected = active.find((c) => c.id === connection);
  const [direct, setDirect] = useState(false);
  const projects = useInfiniteQuery({
    queryKey: ["azure-projects", orgId, connection, selected?.version],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      azureApi(api.http).projects(orgId, connection, pageParam, signal),
    getNextPageParam: (last, pages, _lastParam, pageParams) =>
      nextAzureProjectPage(last, pages, pageParams),
    enabled: !!selected,
    retry: false,
  });
  const pages = projects.data?.pages ?? [];
  const listed = [
    ...new Map(pages.flatMap((p) => p.projects).map((p) => [p.id, p])).values(),
  ];
  const bounded = !!pages.at(-1)?.next_offset && !projects.hasNextPage;
  if (detail.isError)
    return (
      <Callout tone="danger">{integrationErrorText(t, detail.error)}</Callout>
    );
  if (detail.isPending) return <p>{t("common.loading")}</p>;
  if (!active.length)
    return (
      <div className="grid gap-3">
        <p>{t("imports.wizard.bindFirst", { brand: "Azure DevOps" })}</p>
        <Link
          className="underline"
          to={`/o/${orgId}/settings/integrations/azure_devops`}
        >
          {t("integrations.azure.connect")}
        </Link>
      </div>
    );
  return (
    <div className="flex flex-col gap-4">
      <FormRow label={t("integrations.azure.workspace")}>
        <Select
          aria-label={t("integrations.azure.workspace")}
          value={connection || null}
          options={active.map((c) => ({ value: c.id, label: c.display_name }))}
          placeholder={t("integrations.azure.chooseWorkspace")}
          onChange={(id) => {
            onConnection(id);
            onProject("");
            setDirect(false);
          }}
        />
      </FormRow>
      <label className="flex items-center gap-2 text-body-sm">
        <input
          type="checkbox"
          checked={direct}
          disabled={!selected}
          onChange={(e) => {
            setDirect(e.target.checked);
            onProject("");
          }}
        />
        {t("integrations.azure.manualProject")}
      </label>
      <FormRow
        label={t(
          direct ? "integrations.azure.projectId" : "imports.wizard.project",
        )}
      >
        {direct ? (
          <TextInput
            aria-label={t("integrations.azure.projectId")}
            value={project}
            maxLength={36}
            disabled={!selected}
            onChange={(e) => onProject(e.target.value.trim())}
            autoComplete="off"
          />
        ) : (
          <Select
            aria-label={t("imports.wizard.project")}
            value={project || null}
            options={listed.map((p) => ({
              value: p.id,
              label: p.name || p.id,
            }))}
            disabled={!selected || !listed.length}
            placeholder={t("imports.wizard.chooseProject")}
            onChange={onProject}
          />
        )}
      </FormRow>
      {selected && projects.isPending && (
        <p role="status">{t("common.loading")}</p>
      )}
      {projects.isError && (
        <Callout tone="warning">
          <p>{integrationErrorText(t, projects.error)}</p>
          <Button
            size="28"
            variant="outline"
            onClick={() =>
              projects.isFetchNextPageError
                ? void projects.fetchNextPage()
                : void projects.refetch()
            }
          >
            {t("common.retry")}
          </Button>
        </Callout>
      )}
      {!!listed.length && (
        <p className="text-caption text-fg-muted">
          {`${listed.length} Azure projects`}
        </p>
      )}
      {projects.hasNextPage && (
        <Button
          variant="outline"
          size="28"
          loading={projects.isFetchingNextPage}
          onClick={() => void projects.fetchNextPage()}
        >
          {t("imports.records.loadMore")}
        </Button>
      )}
      {bounded && (
        <Callout tone="warning">{t("integrations.azure.pageLimit")}</Callout>
      )}
      {selected && projects.isSuccess && !listed.length && (
        <p>{t("imports.wizard.noProject")}</p>
      )}
      {direct && (
        <p className="text-caption text-fg-muted">
          {t("integrations.azure.metadataRequired")}
        </p>
      )}
      <Callout tone="info">{t("integrations.azure.importScope")}</Callout>
      {children}
    </div>
  );
}
