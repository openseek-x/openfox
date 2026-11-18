import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { lazy, Suspense, useRef, useState } from "react";
import type { DocNodeDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, useToast } from "@openfox/ui";
import { useApi } from "../context";
import { docErrorMessage, putDocNode } from "./queries";
const BlockBody = lazy(() => import("./BlockBody"));

export function DocHistoryDialog({
  orgId,
  spaceId,
  node,
  onClose,
}: {
  orgId: string;
  spaceId: string;
  node: DocNodeDetail;
  onClose: () => void;
}) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const client = useQueryClient();
  // Preserve the version the user opened. A concurrent save must conflict,
  // even when realtime has refreshed the node in the background.
  const baseVersion = useRef(node.version);
  const [selected, setSelected] = useState<number>();
  const [restoring, setRestoring] = useState(false);
  const queryKey = ["org", orgId, "docs", spaceId, node.id, "revisions"];
  const list = useInfiniteQuery({
    queryKey,
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      api.docNodes.revisions(orgId, spaceId, node.id, pageParam || undefined),
    getNextPageParam: (last) => last.next_before || undefined,
  });
  const items = list.data?.pages.flatMap((page) => page.revisions) ?? [];
  const currentRevision = items[0]?.revision;
  const revision = selected ?? currentRevision;
  const preview = useQuery({
    queryKey: [...queryKey, revision],
    queryFn: () => api.docNodes.revision(orgId, spaceId, node.id, revision!),
    enabled: revision !== undefined,
  });
  const restore = async () => {
    if (!revision || restoring) return;
    setRestoring(true);
    try {
      const saved = await api.docNodes.restore(
        orgId,
        spaceId,
        node.id,
        revision,
        baseVersion.current,
      );
      baseVersion.current = saved.version;
      putDocNode(client, orgId, spaceId, saved);
      setSelected(undefined);
      await list.refetch();
      toast(t("docs.history.restored", { version: revision }));
    } catch (error) {
      toast(docErrorMessage(t, error, "docs.history.restoreFailed"), {
        tone: "error",
      });
    } finally {
      setRestoring(false);
    }
  };
  return (
    <Dialog
      open
      onClose={restoring ? undefined : onClose}
      title={t("docs.history.title")}
      description={t("docs.history.desc")}
      width={880}
    >
      <div className="flex h-[440px] gap-4">
        <div className="w-64 shrink-0 overflow-auto border-r border-line-default pr-3">
          <div role="listbox" aria-label={t("docs.history.title")}>
            {items.map((item) => (
              <button
                type="button"
                role="option"
                aria-selected={revision === item.revision}
                key={item.revision}
                disabled={restoring}
                className="mb-2 flex w-full flex-col gap-1 rounded p-2 text-left aria-selected:bg-bg-subtle"
                onClick={() => setSelected(item.revision)}
              >
                <span>
                  {t("docs.history.version", { version: item.revision })}{" "}
                  {item.revision === currentRevision &&
                    t("docs.history.current")}
                </span>
                <span className="text-caption text-fg-muted">
                  {new Date(item.created_at).toLocaleString()} ·{" "}
                  {item.created_by_name}
                </span>
              </button>
            ))}
          </div>
          {list.hasNextPage && (
            <Button
              variant="ghost"
              disabled={list.isFetchingNextPage}
              onClick={() => void list.fetchNextPage()}
            >
              {t("docs.history.more")}
            </Button>
          )}
        </div>
        <div className="min-w-0 flex-1 overflow-auto">
          {list.isError || preview.isError ? (
            <p role="alert">{t("docs.loadFailed")}</p>
          ) : list.isPending ||
            (preview.isPending && revision !== undefined) ? (
            <p>{t("docs.nav.loading")}</p>
          ) : !items.length ? (
            <p>{t("docs.history.empty")}</p>
          ) : (
            preview.data && (
              <>
                <div className="mb-4 flex items-center justify-between gap-3">
                  <span>
                    {t("docs.history.version", { version: revision! })}
                  </span>
                  {node.can_edit && revision !== currentRevision && (
                    <Button disabled={restoring} onClick={() => void restore()}>
                      {t("docs.history.restore")}
                    </Button>
                  )}
                </div>
                <h3 className="mb-3 font-semibold">
                  {preview.data.title || t("docs.untitled")}
                </h3>
                {node.kind === "doc" ? (
                  <Suspense fallback={<p>{t("docs.nav.loading")}</p>}>
                    <BlockBody
                      key={revision}
                      orgId={orgId}
                      spaceId={spaceId}
                      nodeId={node.id}
                      initialMarkdown={preview.data.body ?? ""}
                      editable={false}
                      onChange={() => {}}
                    />
                  </Suspense>
                ) : (
                  <pre className="whitespace-pre-wrap break-words text-body">
                    {preview.data.body}
                  </pre>
                )}
              </>
            )
          )}
        </div>
      </div>
    </Dialog>
  );
}
