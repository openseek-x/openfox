import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useT } from "@openfox/i18n";
import { Callout, Select } from "@openfox/ui";
import { useApi } from "../context";
import {
  integrationErrorText,
  useIntegrationDetail,
} from "../integrations/queries";
import { FormRow } from "./parts";
import type { ReactNode } from "react";

export function ProjectPicker({
  orgId,
  brand,
  connection,
  project,
  onConnection,
  onProject,
  children,
}: {
  orgId: string;
  brand: string;
  connection: string;
  project: string;
  onConnection: (v: string) => void;
  onProject: (v: string) => void;
  children: ReactNode;
}) {
  const api = useApi(),
    t = useT(),
    provider = brand.toLowerCase();
  const detail = useIntegrationDetail(orgId, provider);
  const active =
    detail.data?.connections.filter(
      (c) => c.scope === "ORGANIZATION" && c.status === "ACTIVE",
    ) ?? [];
  const valid = active.some((c) => c.id === connection);
  const projects = useQuery({
    queryKey: [
      "provider-import-projects",
      orgId,
      provider,
      connection,
      active.find((c) => c.id === connection)?.version,
    ],
    queryFn: () => api.imports.projects(orgId, provider, connection),
    enabled: valid,
    retry: false,
  });
  if (detail.isError)
    return (
      <Callout tone="danger">{integrationErrorText(t, detail.error)}</Callout>
    );
  if (detail.isPending) return <p>{t("common.loading")}</p>;
  if (!active.length)
    return (
      <div className="flex flex-col items-start gap-3 rounded-xl border border-line-default px-5 py-4">
        <p className="text-fg-muted">
          {t("imports.wizard.bindFirst", { brand })}
        </p>
        <Link
          className="inline-flex h-8 items-center rounded-md bg-primary px-3 text-body font-medium text-fg-inverse hover:bg-primary/90"
          to={`/o/${orgId}/settings/integrations/${provider}`}
        >
          {t("shell.bindProvider", { brand })}
        </Link>
      </div>
    );
  return (
    <div className="flex flex-col gap-4">
      <FormRow label={t("imports.wizard.instance")}>
        <Select
          aria-label={t("imports.wizard.instance")}
          value={connection || null}
          options={active.map((c) => ({ value: c.id, label: c.display_name }))}
          onChange={onConnection}
          placeholder={t("imports.wizard.chooseInstance")}
        />
      </FormRow>
      <FormRow label={t("imports.wizard.project")}>
        <Select
          aria-label={t("imports.wizard.project")}
          value={project || null}
          options={(projects.data ?? []).map((p) => ({
            value: p.id,
            label: p.name,
          }))}
          onChange={onProject}
          placeholder={t("imports.wizard.chooseProject")}
          disabled={!valid || !projects.data?.length}
        />
      </FormRow>
      {projects.isError && (
        <Callout tone="danger">
          {integrationErrorText(t, projects.error)}
        </Callout>
      )}
      {valid && projects.data?.length === 0 && (
        <p>{t("imports.wizard.noProject")}</p>
      )}
      {children}
    </div>
  );
}
