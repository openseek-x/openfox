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
import { zohoApi, type ZohoProjectsPage } from "@openfox/api";
import { FormRow } from "./parts";

// Cursor chains are scoped to the selected connection version. Never follow a
// provider URI, loop a cursor or imply that a bounded list is exhaustive.
export function nextZohoProjectPage(
  last: ZohoProjectsPage,
  pages: ZohoProjectsPage[],
  pageParams: number[],
): number | undefined {
  if (
    !last.next_index ||
    pages.length >= 50 ||
    pages.reduce((n, p) => n + p.projects.length, 0) >= 5000 ||
    pageParams.includes(last.next_index)
  )
    return undefined;
  return last.next_index;
}

export function ZohoProjectPicker({
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
  const detail = useIntegrationDetail(orgId, "zoho_bugtracker");
  const active =
    detail.data?.connections.filter(
      (c) => c.scope === "ORGANIZATION" && c.status === "ACTIVE",
    ) ?? [];
  const selected = active.find((c) => c.id === connection);
  const [direct, setDirect] = useState(false);
  const projects = useInfiniteQuery({
    queryKey: ["zoho-projects", orgId, connection, selected?.version],
    initialPageParam: 1,
    queryFn: ({ pageParam, signal }) =>
      zohoApi(api.http).projects(orgId, connection, pageParam, signal),
    getNextPageParam: (last, pages, _lastParam, pageParams) =>
      nextZohoProjectPage(last, pages, pageParams),
    enabled: !!selected && !!detail.data?.provider.configured,
    retry: false,
    gcTime: 0,
  });
  const pages = projects.data?.pages ?? [];
  const listed = [
    ...new Map(pages.flatMap((p) => p.projects).map((p) => [p.id, p])).values(),
  ];
  const bounded = !!pages.at(-1)?.next_index && !projects.hasNextPage;
  if (detail.isError)
    return (
      <Callout tone="danger">{integrationErrorText(t, detail.error)}</Callout>
    );
  if (detail.isPending) return <p>{t("common.loading")}</p>;
  if (!active.length)
    return (
      <div className="grid gap-3">
        <p>{t("imports.wizard.bindFirst", { brand: "Zoho BugTracker" })}</p>
        <Link
          className="underline"
          to={`/o/${orgId}/settings/integrations/zoho_bugtracker`}
        >
          {t("integrations.zoho.connect")}
        </Link>
      </div>
    );
  return (
    <div className="flex flex-col gap-4">
      <FormRow label={t("integrations.zoho.portal")}>
        <Select
          aria-label={t("integrations.zoho.portal")}
          value={connection || null}
          options={active.map((c) => ({ value: c.id, label: c.display_name }))}
          placeholder={t("integrations.zoho.choosePortal")}
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
        {t("integrations.zoho.enterID")}
      </label>
      <FormRow
        label={t(
          direct ? "integrations.zoho.projectID" : "imports.wizard.project",
        )}
      >
        {direct ? (
          <TextInput
            aria-label={t("integrations.zoho.projectID")}
            value={project}
            maxLength={64}
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
          {t("integrations.zoho.projectCount", { count: listed.length })}
        </p>
      )}
      {projects.hasNextPage && (
        <Button
          variant="outline"
          size="28"
          loading={projects.isFetchingNextPage}
          onClick={() => void projects.fetchNextPage()}
        >
          {t("integrations.zoho.moreProjects")}
        </Button>
      )}
      {bounded && (
        <Callout tone="warning">{t("integrations.zoho.projectLimit")}</Callout>
      )}
      {selected && projects.isSuccess && !listed.length && (
        <p>{t("imports.wizard.noProject")}</p>
      )}
      {direct && (
        <p className="text-caption text-fg-muted">
          {t("integrations.zoho.projectID")}
        </p>
      )}
      <Callout tone="info">{t("integrations.zoho.scope")}</Callout>
      {children}
    </div>
  );
}
