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
import { asanaApi, type AsanaProjectsPage } from "../integrations/asana-api";
import { FormRow } from "./parts";

// Cursor chains are scoped to the selected connection version. Never follow a
// provider URI, loop a cursor or imply that a bounded list is exhaustive.
export function nextAsanaProjectPage(
  last: AsanaProjectsPage,
  pages: AsanaProjectsPage[],
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

export function AsanaProjectPicker({
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
  const detail = useIntegrationDetail(orgId, "asana");
  const active =
    detail.data?.connections.filter(
      (c) => c.scope === "ORGANIZATION" && c.status === "ACTIVE",
    ) ?? [];
  const selected = active.find((c) => c.id === connection);
  const [direct, setDirect] = useState(false);
  const projects = useInfiniteQuery({
    queryKey: ["asana-projects", orgId, connection, selected?.version],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      asanaApi(api.http).projects(orgId, connection, pageParam, signal),
    getNextPageParam: (last, pages, _lastParam, pageParams) =>
      nextAsanaProjectPage(last, pages, pageParams),
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
        <p>{t("imports.wizard.bindFirst", { brand: "Asana" })}</p>
        <Link
          className="underline"
          to={`/o/${orgId}/settings/integrations/asana`}
        >
          {t("integrations.asana.connect")}
        </Link>
      </div>
    );
  return (
    <div className="flex flex-col gap-4">
      <FormRow label={t("integrations.asana.workspace")}>
        <Select
          aria-label={t("integrations.asana.workspace")}
          value={connection || null}
          options={active.map((c) => ({ value: c.id, label: c.display_name }))}
          placeholder={t("integrations.asana.chooseWorkspace")}
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
        {t("integrations.asana.enterGid")}
      </label>
      <FormRow
        label={t(
          direct ? "integrations.asana.projectGid" : "imports.wizard.project",
        )}
      >
        {direct ? (
          <TextInput
            aria-label={t("integrations.asana.projectGid")}
            value={project}
            maxLength={128}
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
          {t("integrations.asana.projectCount", { count: listed.length })}
        </p>
      )}
      {projects.hasNextPage && (
        <Button
          variant="outline"
          size="28"
          loading={projects.isFetchingNextPage}
          onClick={() => void projects.fetchNextPage()}
        >
          {t("integrations.asana.moreProjects")}
        </Button>
      )}
      {bounded && (
        <Callout tone="warning">{t("integrations.asana.projectLimit")}</Callout>
      )}
      {selected && projects.isSuccess && !listed.length && (
        <p>{t("imports.wizard.noProject")}</p>
      )}
      {direct && (
        <p className="text-caption text-fg-muted">
          {t("integrations.asana.projectProof")}
        </p>
      )}
      <Callout tone="info">{t("integrations.asana.importScope")}</Callout>
      {children}
    </div>
  );
}
