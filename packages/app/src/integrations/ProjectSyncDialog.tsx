import { useState } from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import type { ImportJob } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Select, useToast } from "@openfox/ui";
import { keys, useApi } from "../context";
import { useStatuses } from "../tasks/queries";
import { importErrorText } from "../imports/queries";

export function ProjectSyncDialog({
  org,
  connection,
  brand,
  close,
}: {
  org: string;
  connection: string;
  brand: string;
  close: () => void;
}) {
  const t = useT();
  return (
    <Dialog open title={t("imports.options.syncProjects")} onClose={close}>
      <ProjectSyncList org={org} connection={connection} brand={brand} />
    </Dialog>
  );
}

export function ProjectSyncList({
  org,
  connection,
  brand,
}: {
  org: string;
  connection: string;
  brand: string;
}) {
  const api = useApi(),
    t = useT();
  const list = useInfiniteQuery({
    queryKey: [...keys.imports(org), "connection", connection],
    queryFn: ({ pageParam }) =>
      api.imports.list(org, {
        connection_id: connection,
        limit: 20,
        cursor: pageParam || undefined,
      }),
    initialPageParam: "",
    getNextPageParam: (p) => p.next_cursor || undefined,
    refetchInterval: (q) =>
      q.state.data?.pages.some((p) => p.imports.some((j) => j.sync?.checking))
        ? 1000
        : 15000,
  });
  return (
    <>
      {list.isError && <p role="alert">{importErrorText(t, list.error)}</p>}
      {list.isPending && <p role="status">{t("common.loading")}</p>}
      {list.data?.pages[0]?.total === 0 && (
        <p>{t("imports.options.syncEmpty")}</p>
      )}
      <div className="divide-y divide-line">
        {list.data?.pages
          .flatMap((p) => p.imports)
          .map(
            (job) =>
              job.sync && (
                <SyncRow
                  key={job.id}
                  org={org}
                  job={job}
                  sync={job.sync}
                  brand={brand}
                />
              ),
          )}
      </div>
      {list.hasNextPage && (
        <Button
          size="28"
          variant="ghost"
          loading={list.isFetchingNextPage}
          onClick={() => void list.fetchNextPage()}
        >
          {t("imports.records.loadMore")}
        </Button>
      )}
    </>
  );
}
type Sync = NonNullable<ImportJob["sync"]>;
function SyncRow({
  org,
  job,
  sync,
  brand,
}: {
  org: string;
  job: ImportJob;
  sync: Sync;
  brand: string;
}) {
  const api = useApi(),
    t = useT(),
    toast = useToast(),
    cache = useQueryClient();
  const [draft, setDraft] = useState<Sync["direction"]>(),
    [confirm, setConfirm] = useState(false);
  const statuses = useStatuses(org);
  const [mappingOpen, setMappingOpen] = useState(false);
  const [mappings, setMappings] = useState<Record<string, string>>({});
  const direction = draft ?? sync.direction ?? "BIDIRECTIONAL";
  const edit = useMutation({
    mutationFn: (cmd: {
      status_mappings?: Record<string, string>;
      direction?: Sync["direction"];
      state?: Sync["state"];
      check_now?: boolean;
    }) =>
      api.imports.manageSync(org, job.id, { ...cmd, version: sync.version }),
    onSuccess: (_, cmd) => {
      setDraft(undefined);
      setConfirm(false);
      if (cmd.status_mappings) setMappingOpen(false);
      if (cmd.check_now) toast(t("imports.options.syncRequested"));
    },
    onError: (e) => toast(importErrorText(t, e), { tone: "error" }),
    onSettled: () => cache.invalidateQueries({ queryKey: keys.imports(org) }),
  });
  return (
    <div className="grid gap-3 py-3">
      <div>
        <div className="text-body font-medium">
          {job.file_name || job.api?.project_key || job.source_label} →{" "}
          {job.space_name}
        </div>
        <div className="text-caption text-fg-muted">
          {t(
            sync.state === "PAUSED"
              ? "imports.options.syncPaused"
              : sync.checking
                ? "imports.options.syncChecking"
                : sync.error_code === "COMMENT_IDENTITY"
                  ? "imports.options.commentIdentity"
                  : sync.error_code === "COMMENT_DELIVERY_UNKNOWN"
                    ? "imports.options.commentUnknown"
                    : sync.error_code === "COMMENT_FALLBACK"
                      ? "imports.options.commentFallback"
                      : sync.error_code === "COMMENTS"
                        ? "imports.options.syncCommentsRequired"
                        : sync.error_code === "STATUS_MAPPING"
                          ? "imports.options.syncStatusRequired"
                          : sync.error_code === "STATUS_TRANSITION"
                            ? "imports.options.syncTransitionRequired"
                            : sync.error_code === "CONFLICT"
                              ? "imports.options.syncConflict"
                              : sync.error_code
                                ? "imports.options.syncError"
                                : "imports.options.syncActive",
          )}
          {sync.last_success &&
            " · " +
              t("imports.options.syncLast", {
                time: new Date(sync.last_success).toLocaleString(),
              })}
        </div>
      </div>
      <label className="grid gap-1 text-caption">
        {t("imports.options.syncDirection")}
        <Select
          aria-label={t("imports.options.syncDirection")}
          value={direction}
          disabled={edit.isPending}
          options={[
            {
              value: "INBOUND",
              label: t("imports.options.syncInbound", { brand }),
            },
            {
              value: "BIDIRECTIONAL",
              label: t("imports.options.syncBoth", { brand }),
            },
          ]}
          onChange={(v) =>
            setDraft(v === "INBOUND" ? "INBOUND" : "BIDIRECTIONAL")
          }
        />
      </label>
      <div className="flex flex-wrap gap-2">
        {direction !== (sync.direction ?? "BIDIRECTIONAL") && (
          <Button
            size="28"
            disabled={edit.isPending}
            onClick={() => edit.mutate({ direction })}
          >
            {t("imports.options.syncSave")}
          </Button>
        )}
        {sync.state === "ACTIVE" && (
          <Button
            size="28"
            variant="outline"
            disabled={edit.isPending || sync.checking}
            onClick={() => edit.mutate({ check_now: true })}
          >
            {t(
              sync.checking
                ? "imports.options.syncChecking"
                : "imports.options.syncCheck",
            )}
          </Button>
        )}
        <Button
          size="28"
          variant="outline"
          disabled={edit.isPending}
          onClick={() => {
            setMappings(sync.status_mappings ?? {});
            setMappingOpen(true);
          }}
        >
          {t("imports.statuses.title")}
        </Button>
        <Button
          size="28"
          variant="outline"
          disabled={edit.isPending}
          onClick={() =>
            edit.mutate({
              state: sync.state === "ACTIVE" ? "PAUSED" : "ACTIVE",
            })
          }
        >
          {t(
            sync.state === "ACTIVE"
              ? "imports.options.pauseSync"
              : "imports.options.resumeSync",
          )}
        </Button>
        <Button
          size="28"
          variant="ghost"
          disabled={edit.isPending}
          onClick={() => setConfirm(true)}
        >
          {t("imports.options.stopSync")}
        </Button>
      </div>
      {mappingOpen && (
        <Dialog
          open
          title={t("imports.statuses.title")}
          onClose={() => setMappingOpen(false)}
          footer={
            <>
              <Button variant="outline" onClick={() => setMappingOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button
                disabled={edit.isPending || Object.keys(mappings).length === 0}
                onClick={() => edit.mutate({ status_mappings: mappings })}
              >
                {t("common.save")}
              </Button>
            </>
          }
        >
          <p className="text-body-sm text-fg-muted">
            {t("imports.options.syncStatusHelp")}
          </p>
          <div className="grid gap-3 py-3">
            {Object.entries(mappings).map(([source, target]) => (
              <label
                key={source}
                className="grid grid-cols-2 items-center gap-3 text-body-sm"
              >
                <span>{source}</span>
                <Select
                  aria-label={t("imports.statuses.mapAria", { name: source })}
                  value={target}
                  options={[
                    { value: "", label: t("imports.options.syncUnmapped") },
                    ...(statuses.data ?? []).map((s) => ({
                      value: s.id,
                      label: s.name,
                    })),
                  ]}
                  onChange={(v) =>
                    setMappings((prev) => ({ ...prev, [source]: v }))
                  }
                />
              </label>
            ))}
          </div>
        </Dialog>
      )}
      {confirm && (
        <Dialog
          open
          title={t("imports.options.stopSync")}
          onClose={() => setConfirm(false)}
          footer={
            <>
              <Button variant="outline" onClick={() => setConfirm(false)}>
                {t("common.cancel")}
              </Button>
              <Button
                disabled={edit.isPending}
                onClick={() => edit.mutate({ state: "DISCONNECTED" })}
              >
                {t("imports.options.stopSync")}
              </Button>
            </>
          }
        >
          {t("imports.options.stopSyncDesc")}
        </Dialog>
      )}
    </div>
  );
}
