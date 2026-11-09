import { useState } from "react";
import {
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";

import type { ImportJob } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Callout, Icon } from "@openfox/ui";

import { SettingsColumn, SettingsSection } from "../spaces/settings/parts";
import { isOrgAdmin, useOrgRole } from "../spaces/queries";
import { ProviderIcon } from "../integrations/ProviderIcon";
import { ExportRecords } from "./ExportRecords";
import { ExportTasksDialog } from "./ExportTasksDialog";
import { ImportRecords } from "./ImportRecords";
import { ImportWizard } from "./ImportWizard";
import {
  AVAILABLE_SOURCES,
  COMING_SOON_SOURCES,
  prefillFromJob,
  type ApiPrefill,
} from "./model";
import { useImportList } from "./queries";

// 导入与导出。向导在本页内原地展开，用查询参数 source=jira 打开。

// URL 里只保留非敏感的向导状态；重新导入的预填值经 navigation state 传递
const sourceSlug = (brand: string) =>
  brand === "禅道" ? "zentao" : brand.toLowerCase();
const WIZARD_KEYS = ["method", "space", "sync"];

export function ImportExportPage() {
  const { orgId = "" } = useParams();
  const t = useT();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const role = useOrgRole(orgId);
  const list = useImportList(orgId);
  const first = list.data?.pages[0];
  const canManage = first ? first.can_manage : isOrgAdmin(role);
  const [methodOpen, setMethodOpen] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);
  // 导出对所有非访客成员开放；访客没有导出权限（服务端同样返回 403）
  const canExport = role !== undefined && role !== "GUEST";
  const source = params.get("source");
  const brand = AVAILABLE_SOURCES.find((s) => sourceSlug(s) === source);
  const wizardOpen = !!brand && canManage;
  const apiMode = brand === "Jira" && params.get("method") === "api";
  const location = useLocation();
  const prefill = apiMode
    ? ((location.state as { prefill?: ApiPrefill } | null)?.prefill ??
      undefined)
    : undefined;

  const openWizard = (
    options: {
      brand?: (typeof AVAILABLE_SOURCES)[number];
      spaceId?: string;
      api?: ApiPrefill;
      file?: boolean;
    } = {},
  ) => {
    setMethodOpen(false);
    const next = new URLSearchParams(params);
    next.set("source", sourceSlug(options.brand ?? "Jira"));
    for (const key of WIZARD_KEYS) next.delete(key);
    if (options.spaceId) next.set("space", options.spaceId);
    if (options.file) next.set("method", "file");
    if (options.api) next.set("method", "api");
    setParams(
      next,
      options.api && options.api.site
        ? { state: { prefill: options.api } }
        : undefined,
    );
  };
  const closeWizard = () => {
    const next = new URLSearchParams(params);
    next.delete("source");
    for (const key of WIZARD_KEYS) next.delete(key);
    setParams(next);
  };

  const wizard = wizardOpen && (
    <ImportWizard
      orgId={orgId}
      key={brand + params.get("method") + params.get("sync")}
      initialFile={params.get("method") === "file"}
      brand={brand}
      initialSpaceId={params.get("space") ?? undefined}
      mode={apiMode ? "api" : "csv"}
      initialContinuousSync={params.get("sync") === "1"}
      prefill={prefill}
      canManage={canManage}
      onClose={closeWizard}
    />
  );
  if (wizardOpen && brand !== "Jira") {
    return <div className="w-full px-4 pb-12 pt-4">{wizard}</div>;
  }
  return (
    <SettingsColumn wide>
      <header className="flex flex-col gap-1 px-1">
        <h1 className="text-title font-bold">{t("imports.title")}</h1>
        <p className="text-body-sm text-fg-muted">{t("imports.description")}</p>
      </header>
      {!canManage && first && (
        <Callout tone="info">{t("imports.adminOnly")}</Callout>
      )}
      {wizardOpen ? (
        wizard
      ) : (
        <>
          <SettingsSection title={t("imports.groups.import")}>
            <div className="grid grid-cols-1 gap-3">
              {AVAILABLE_SOURCES.map((name) => (
                <button
                  key={name}
                  type="button"
                  data-testid={`source-${name}`}
                  disabled={!canManage}
                  aria-expanded={methodOpen}
                  onClick={() =>
                    name === "Jira"
                      ? setMethodOpen((open) => !open)
                      : openWizard({ brand: name })
                  }
                  className="flex h-[60px] items-center gap-3 rounded-xl border border-line-default bg-surface-card px-4 text-left hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-60"
                >
                  <ProviderIcon name={name} />
                  <span className="min-w-0 flex-1 truncate text-body font-medium">
                    {name}
                  </span>
                  <Icon name="chevron-down" className="text-fg-muted" />
                </button>
              ))}
              {methodOpen && canManage && (
                <div
                  role="group"
                  aria-label={t("imports.method.title")}
                  className="flex gap-3"
                >
                  <button
                    type="button"
                    onClick={() => openWizard()}
                    className="flex flex-1 flex-col gap-0.5 rounded-xl border border-line-default bg-surface-card px-4 py-3 text-left hover:bg-surface-hover"
                  >
                    <span className="text-body font-medium">
                      {t("imports.method.csv")}
                    </span>
                    <span className="text-caption text-fg-muted">
                      {t("imports.method.csvDesc")}
                    </span>
                  </button>
                  <button
                    type="button"
                    onClick={() =>
                      openWizard({
                        api: {
                          deployment: "cloud",
                          site: "",
                          projectKey: "",
                          jql: "",
                          authKind: "api_token",
                          user: "",
                        },
                      })
                    }
                    className="flex flex-1 flex-col gap-0.5 rounded-xl border border-line-default bg-surface-card px-4 py-3 text-left hover:bg-surface-hover"
                  >
                    <span className="text-body font-medium">
                      {t("imports.method.api")}
                    </span>
                    <span className="text-caption text-fg-muted">
                      {t("imports.method.apiDesc")}
                    </span>
                  </button>
                </div>
              )}
              <button
                type="button"
                data-testid="source-GitHub"
                disabled={!canManage}
                onClick={() =>
                  navigate(
                    `/o/${orgId}/settings/integrations/github?import=existing`,
                  )
                }
                className="flex h-[60px] items-center gap-3 rounded-xl border border-line-default bg-surface-card px-4 text-left hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-60"
              >
                <ProviderIcon name="GitHub" />
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-body font-medium">GitHub</span>
                  <span className="truncate text-caption text-fg-muted">
                    {t("imports.githubSyncDesc")}
                  </span>
                </span>
                <Icon name="chevron-right" className="text-fg-muted" />
              </button>
              {COMING_SOON_SOURCES.map((name) => (
                <div
                  key={name}
                  aria-disabled="true"
                  data-testid={`source-${name}`}
                  className="flex h-[60px] items-center gap-3 rounded-xl border border-line-default bg-surface-card px-4 opacity-60"
                >
                  <ProviderIcon name={name} />
                  <span className="min-w-0 flex-1 truncate text-body font-medium">
                    {name}
                  </span>
                  <Badge tone="muted">{t("imports.comingSoon")}</Badge>
                </div>
              ))}
            </div>
          </SettingsSection>
          <SettingsSection title={t("imports.groups.export")}>
            <div className="grid grid-cols-1 gap-3">
              <button
                type="button"
                data-testid="export-tasks"
                disabled={!canExport}
                onClick={() => setExportOpen(true)}
                className="flex h-[60px] items-center gap-3 rounded-xl border border-line-default bg-surface-card px-4 text-left hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-60"
              >
                <Icon name="download" />
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-body font-medium">
                    {t("imports.export.tasks")}
                  </span>
                  <span className="truncate text-caption text-fg-muted">
                    {t("imports.export.tasksDesc")}
                  </span>
                </span>
              </button>
            </div>
          </SettingsSection>
          <ExportRecords orgId={orgId} />
          <ImportRecords
            orgId={orgId}
            canManage={canManage}
            onReimport={(job: ImportJob) =>
              job.source.endsWith("_csv") ||
              job.source.endsWith("_api") ||
              job.source === "tapd_excel"
                ? openWizard({
                    file: !job.source.endsWith("_api"),
                    brand: AVAILABLE_SOURCES.find((b) =>
                      job.source.startsWith(sourceSlug(b) + "_"),
                    ),
                    spaceId: job.space_id ?? undefined,
                  })
                : openWizard({
                    spaceId: job.space_id ?? undefined,
                    api: prefillFromJob(job),
                  })
            }
          />
        </>
      )}
      {exportOpen && (
        <ExportTasksDialog
          orgId={orgId}
          canManage={canManage}
          onClose={() => setExportOpen(false)}
        />
      )}
    </SettingsColumn>
  );
}
