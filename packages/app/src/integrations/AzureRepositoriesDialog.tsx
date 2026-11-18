import { useState } from "react";
import { useInfiniteQuery, useMutation, useQuery } from "@tanstack/react-query";
import type { IntegrationConnection } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, Select, TextInput } from "@openfox/ui";
import { useApi } from "../context";
import {
  azureApi,
  nextAzurePage,
  azureUUID,
  type AzureRepository,
  type AzureHooksConfig,
  type AzureBacklink,
} from "./azure-api";
import { integrationErrorText } from "./queries";
export function AzureRepositoriesDialog({
  org,
  connection,
  close,
}: {
  org: string;
  connection: IntegrationConnection;
  close: () => void;
}) {
  const api = useApi(),
    t = useT(),
    client = azureApi(api.http);
  const active = connection.status === "ACTIVE";
  const [project, setProject] = useState("");
  const [draft, setDraft] = useState<{
    base: AzureHooksConfig;
    repos: AzureRepository[];
  }>();
  const [cursors, setCursors] = useState({ work: "", backlinks: "" });
  const [resend, setResend] = useState<AzureBacklink | null>(null),
    [confirmed, setConfirmed] = useState(false);
  const config = useQuery({
    queryKey: ["azure-hooks", org, connection.id],
    queryFn: () => client.config(org, connection.id),
    retry: false,
    refetchInterval: 5000,
  });
  const projects = useInfiniteQuery({
    queryKey: ["azure-projects", org, connection.id, connection.version],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      client.projects(org, connection.id, pageParam, signal),
    getNextPageParam: (last, pages, _last, params) =>
      nextAzurePage(last, pages, params),
    enabled: active,
    retry: false,
  });
  const listed = projects.data?.pages.flatMap((p) => p.projects) ?? [];
  const repos = useQuery({
    queryKey: [
      "azure-repos-available",
      org,
      connection.id,
      connection.version,
      project,
    ],
    queryFn: () => client.availableRepositories(org, connection.id, project),
    enabled: active && azureUUID(project),
    retry: false,
  });
  const work = useQuery({
    queryKey: ["azure-work", org, connection.id, cursors],
    queryFn: () =>
      client.work(org, connection.id, cursors.work, cursors.backlinks),
    retry: false,
    refetchInterval: 5000,
  });
  const selected = draft?.repos ?? config.data?.repositories ?? [];
  const save = useMutation({
    mutationFn: () =>
      client.saveRepositories(
        org,
        connection.id,
        draft?.base ?? config.data!,
        selected,
      ),
    onSuccess: async () => {
      setDraft(undefined);
      await config.refetch();
    },
  });
  const repair = useMutation({
    mutationFn: (id: string) =>
      client.repair(
        org,
        connection.id,
        config.data!.hooks.find((h) => h.id === id)!,
      ),
    onSuccess: () => config.refetch(),
  });
  const retry = useMutation({
    mutationFn: ({
      item,
      action,
    }: {
      item: AzureBacklink;
      action: "reconcile" | "resend";
    }) =>
      client.retryBacklink(
        org,
        connection.id,
        item.id,
        config.data!,
        action,
        action === "resend" ? "SEND_AGAIN_MAY_DUPLICATE" : undefined,
      ),
    onSuccess: async () => {
      setResend(null);
      setConfirmed(false);
      await work.refetch();
    },
  });
  const change = (r: AzureRepository, checked: boolean) => {
    if (!config.data) return;
    const others = selected.filter(
      (v) =>
        v.repository_id !== r.repository_id || v.project_id !== r.project_id,
    );
    setDraft({
      base: draft?.base ?? config.data,
      repos: checked ? [...others, r] : others,
    });
  };
  const errors = [
    config.error,
    projects.error,
    repos.error,
    work.error,
    save.error,
    repair.error,
    retry.error,
  ].filter(Boolean);
  return (
    <Dialog
      open
      title={t("integrations.azure.repositories")}
      onClose={close}
      footer={
        <>
          <Button variant="outline" onClick={close}>
            {t("common.close")}
          </Button>
          {active && (
            <Button
              disabled={
                !config.data || save.isPending || selected.length > 20 || !draft
              }
              loading={save.isPending}
              onClick={() => save.mutate()}
            >
              {t("common.save")}
            </Button>
          )}
        </>
      }
    >
      <div className="grid gap-4">
        {!active && <Callout>{t("integrations.azure.cleanup")}</Callout>}
        {errors.map((e, i) => (
          <Callout tone="warning" key={i}>
            {integrationErrorText(t, e)}
          </Callout>
        ))}
        {save.isError && (
          <Button
            variant="outline"
            onClick={() => {
              setDraft(undefined);
              save.reset();
              void config.refetch();
            }}
          >
            {t("integrations.azure.reload")}
          </Button>
        )}
        {active && (
          <>
            <p>{t("integrations.azure.repositoryHelp")}</p>
            <label>
              {t("imports.wizard.project")}
              <Select
                aria-label={t("imports.wizard.project")}
                value={project || null}
                options={listed.map((p) => ({ value: p.id, label: p.name }))}
                onChange={setProject}
              />
            </label>
            {projects.hasNextPage && (
              <Button
                variant="outline"
                disabled={projects.isFetchingNextPage}
                onClick={() => void projects.fetchNextPage()}
              >
                {t("imports.records.loadMore")}
              </Button>
            )}
            <label>
              {t("integrations.azure.projectId")}
              <TextInput
                aria-label={t("integrations.azure.projectId")}
                value={project}
                maxLength={36}
                onChange={(e) => setProject(e.target.value.trim())}
              />
            </label>
            <fieldset className="grid gap-2">
              <legend>{t("integrations.azure.repositorySelect")}</legend>
              {(repos.data?.repositories ?? []).map((r) => {
                const checked = selected.some(
                  (v) => v.project_id === project && v.repository_id === r.id,
                );
                return (
                  <label key={r.id} className="flex items-start gap-2">
                    <input
                      type="checkbox"
                      checked={checked}
                      disabled={
                        save.isPending || (!checked && selected.length >= 20)
                      }
                      onChange={(e) =>
                        change(
                          {
                            project_id: project,
                            project_name: r.project.name,
                            repository_id: r.id,
                            repository_name: r.name,
                          },
                          e.target.checked,
                        )
                      }
                    />
                    {r.name} · {r.id}
                  </label>
                );
              })}
            </fieldset>
            <p>
              {t("integrations.azure.selectedCount", {
                count: selected.length,
              })}
            </p>
            <ul className="grid gap-1">
              {selected.map((r) => (
                <li
                  key={`${r.project_id}/${r.repository_id}`}
                  className="flex items-center justify-between gap-2"
                >
                  <span>
                    {r.project_name} / {r.repository_name}
                  </span>
                  <Button
                    size="28"
                    variant="ghost"
                    disabled={save.isPending}
                    onClick={() => change(r, false)}
                  >
                    {t("integrations.azure.remove")}
                  </Button>
                </li>
              ))}
            </ul>
          </>
        )}
        <h3 className="font-semibold">{t("integrations.azure.hooks")}</h3>
        {config.data?.hooks.length === 0 && (
          <p>{t("integrations.azure.noHooks")}</p>
        )}
        {config.data?.hooks.map((h) => (
          <div
            key={h.id}
            className="grid gap-1 rounded border border-line-default p-3"
          >
            <p>
              {h.event_type} · {h.state}
            </p>
            <p className="break-all text-caption">
              {h.project_id}
              {h.repository_id && ` / ${h.repository_id}`}
            </p>
            <p className="break-all text-caption">
              Hook {h.id} / {h.remote_generation ?? "?"}
            </p>
            {h.last_error && <p role="status">{h.last_error}</p>}
            {h.manual_cleanup && (
              <Callout tone="warning">
                {t("integrations.azure.manualCleanup")}
                {h.subscription_id && (
                  <p className="break-all">{h.subscription_id}</p>
                )}
              </Callout>
            )}
            {active && h.desired && h.state !== "ACTIVE" && (
              <Button
                size="28"
                variant="outline"
                disabled={repair.isPending}
                onClick={() => repair.mutate(h.id)}
              >
                {t("integrations.azure.repair")}
              </Button>
            )}
          </div>
        ))}
        <h3 className="font-semibold">{t("integrations.azure.work")}</h3>
        {work.data && !work.data.work.length && !work.data.backlinks.length && (
          <p>{t("integrations.azure.noWork")}</p>
        )}
        {work.data?.work.map((w) => (
          <div key={w.id} className="grid gap-1 border-b border-line-soft py-2">
            <p>
              {w.kind === "WORK_ITEM" ? "Azure Boards" : w.kind} #{w.object_id}{" "}
              · {w.state}
            </p>
            <p className="break-all text-caption">
              {w.project_id}
              {w.repository_id && ` / ${w.repository_id}`}
            </p>
            {w.last_error && <p role="status">{w.last_error}</p>}
          </div>
        ))}
        {work.data?.backlinks.map((b) => (
          <div key={b.id} className="grid gap-2 border-b border-line-soft py-2">
            <p>
              PR #{b.pr_number} · {b.state} {b.last_error}
            </p>
            {b.state === "UNKNOWN" && <p>{t("integrations.azure.unknown")}</p>}
            {b.thread_id && (
              <p>
                {b.thread_id}:{b.comment_id}
              </p>
            )}
            {active && ["UNKNOWN", "FAILED", "SKIPPED"].includes(b.state) && (
              <div className="flex gap-2">
                <Button
                  size="28"
                  variant="outline"
                  disabled={retry.isPending || !config.data}
                  onClick={() => retry.mutate({ item: b, action: "reconcile" })}
                >
                  {t("integrations.azure.reconcile")}
                </Button>
                <Button
                  size="28"
                  variant="outline"
                  onClick={() => {
                    setResend(b);
                    setConfirmed(false);
                  }}
                >
                  {t("integrations.azure.resend")}
                </Button>
              </div>
            )}
          </div>
        ))}
        {(work.data?.next_work_after || work.data?.next_backlink_after) && (
          <Button
            variant="outline"
            onClick={() =>
              setCursors({
                work: work.data?.next_work_after || cursors.work,
                backlinks: work.data?.next_backlink_after || cursors.backlinks,
              })
            }
          >
            {t("imports.records.loadMore")}
          </Button>
        )}
        {resend && (
          <Callout tone="warning">
            <p>{t("integrations.azure.resendWarning")}</p>
            <label className="my-3 flex gap-2">
              <input
                type="checkbox"
                checked={confirmed}
                onChange={(e) => setConfirmed(e.target.checked)}
              />
              {t("integrations.azure.resendConsent")}
            </label>
            <Button
              disabled={!confirmed || retry.isPending || !config.data}
              onClick={() => retry.mutate({ item: resend, action: "resend" })}
            >
              {t("integrations.azure.resend")}
            </Button>
            <Button variant="ghost" onClick={() => setResend(null)}>
              {t("common.cancel")}
            </Button>
          </Callout>
        )}
      </div>
    </Dialog>
  );
}
