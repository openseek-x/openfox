import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import type { AgentConfigVersion, SpaceAgent } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button } from "@openfox/ui";

import { keys, useApi } from "../../context";
import { agentErrorMessage } from "../queries";

/** Linear comparison keeps very long instructions bounded; changed ranges are shown side by side. */
export function instructionChanges(previous: string, current: string) {
  const oldLines = previous.split("\n"),
    newLines = current.split("\n");
  let start = 0;
  while (
    start < oldLines.length &&
    start < newLines.length &&
    oldLines[start] === newLines[start]
  )
    start++;
  let oldEnd = oldLines.length,
    newEnd = newLines.length;
  while (
    oldEnd > start &&
    newEnd > start &&
    oldLines[oldEnd - 1] === newLines[newEnd - 1]
  ) {
    oldEnd--;
    newEnd--;
  }
  return {
    removed: oldLines.slice(start, oldEnd),
    added: newLines.slice(start, newEnd),
  };
}

export function InstructionsHistory({
  orgId,
  agent,
  draft,
  editable,
  busy,
  restore,
}: {
  orgId: string;
  agent: SpaceAgent;
  draft: string;
  editable: boolean;
  busy: boolean;
  restore: (value: string) => Promise<boolean>;
}) {
  const t = useT();
  const api = useApi();
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<AgentConfigVersion>();
  const queryKey = keys.agentSub(orgId, agent.id, "config-versions");
  const history = useInfiniteQuery({
    queryKey,
    enabled: open,
    initialPageParam: undefined as number | undefined,
    queryFn: ({ pageParam }) =>
      api.agents.configVersions(orgId, agent.space_id, agent.id, pageParam),
    getNextPageParam: (page) => page.next_before_version ?? undefined,
  });
  const previousVersion = useRef(agent.current_config_version);
  useEffect(() => {
    if (previousVersion.current === agent.current_config_version) return;
    previousVersion.current = agent.current_config_version;
    void client.invalidateQueries({
      queryKey: keys.agentSub(orgId, agent.id, "config-versions"),
    });
  }, [agent.current_config_version, agent.id, orgId, client]);
  const changes = selected
    ? instructionChanges(selected.instructions, draft)
    : undefined;
  return (
    <div className="flex flex-col gap-3">
      <Button
        size="28"
        variant="ghost"
        className="self-start"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        {t("agents.sections.instructions.history")}
      </Button>
      {open && (
        <div className="flex flex-col gap-3 rounded border border-line-default p-3">
          {history.isPending && <p>{t("common.loading")}</p>}
          {history.isError && (
            <div role="alert">
              <p>{agentErrorMessage(t, history.error)}</p>
              <Button size="28" onClick={() => void history.refetch()}>
                {t("common.retry")}
              </Button>
            </div>
          )}
          <div className="flex max-h-48 flex-col overflow-y-auto">
            {history.data?.pages
              .flatMap((page) => page.config_versions)
              .map((version) => (
                <button
                  type="button"
                  key={version.config_version}
                  className="rounded px-2 py-1 text-left hover:bg-surface-hover"
                  aria-pressed={
                    selected?.config_version === version.config_version
                  }
                  onClick={() => setSelected(version)}
                >
                  {t("agents.sections.instructions.version", {
                    version: version.config_version,
                  })}{" "}
                  · {new Date(version.created_at).toLocaleString()}
                </button>
              ))}
          </div>
          {history.hasNextPage && (
            <Button
              size="28"
              loading={history.isFetchingNextPage}
              onClick={() => void history.fetchNextPage()}
            >
              {t("agents.sections.instructions.more")}
            </Button>
          )}
          {selected && changes && (
            <>
              <div className="grid min-w-0 grid-cols-2 gap-3">
                <div className="min-w-0">
                  <p>{t("agents.sections.instructions.previous")}</p>
                  <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words text-body-sm">
                    {selected.instructions}
                  </pre>
                </div>
                <div className="min-w-0">
                  <p>{t("agents.sections.instructions.current")}</p>
                  <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words text-body-sm">
                    {draft}
                  </pre>
                </div>
              </div>
              <div
                aria-label={t("agents.sections.instructions.diff")}
                className="max-h-64 overflow-auto font-mono text-body-sm"
              >
                {changes.removed.map((line, i) => (
                  <div
                    key={`old-${i}`}
                    className="whitespace-pre-wrap break-words text-danger"
                  >
                    − {line}
                  </div>
                ))}
                {changes.added.map((line, i) => (
                  <div
                    key={`new-${i}`}
                    className="whitespace-pre-wrap break-words text-success"
                  >
                    + {line}
                  </div>
                ))}
                {!changes.removed.length && !changes.added.length && (
                  <p>{t("agents.sections.instructions.unchanged")}</p>
                )}
              </div>
              <p className="text-caption text-fg-muted">
                {t("agents.sections.instructions.restoreHint")}
              </p>
              <Button
                size="28"
                disabled={
                  !editable ||
                  selected.instructions === agent.config?.instructions
                }
                loading={busy}
                onClick={async () => {
                  if (await restore(selected.instructions)) {
                    await client.invalidateQueries({ queryKey });
                    setSelected(undefined);
                  }
                }}
              >
                {t("agents.sections.instructions.restore")}
              </Button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
