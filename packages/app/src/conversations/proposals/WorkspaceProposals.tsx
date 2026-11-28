import { useInfiniteQuery } from "@tanstack/react-query";
import type { WorkspaceProposal } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, cn } from "@openfox/ui";
import { useApi, keys } from "../../context";
import { useOpenArtifactList, proposalTabKey } from "../artifacts/params";
export const proposalsKey = (org: string, conversation: string) => [
  ...keys.conversation(org, conversation),
  "proposals",
];
export const proposalKey = (org: string, conversation: string, id: string) => [
  ...proposalsKey(org, conversation),
  id,
];
export function WorkspaceProposals({
  orgId,
  conversationId,
  onOpen,
  embedded = false,
}: {
  orgId: string;
  conversationId: string;
  onOpen: () => void;
  /** 嵌入右侧工作区标签页时占满剩余高度，不限制 60vh。 */
  embedded?: boolean;
}) {
  const api = useApi(),
    t = useT();
  const query = useInfiniteQuery({
    queryKey: proposalsKey(orgId, conversationId),
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      api.workspaceProposals.list(orgId, conversationId, pageParam, signal),
    getNextPageParam: (page) => page.next_cursor || undefined,
    refetchInterval: 5000,
    retry: false,
  });
  if (query.isError)
    return (
      <div
        role="alert"
        className={cn(
          "flex items-center justify-between gap-2 text-caption text-fg-muted",
          embedded ? "p-3" : "mb-2",
        )}
      >
        <span className="text-danger">
          {t("artifacts.proposals.loadFailed")}
        </span>
        <Button
          size="28"
          variant="outline"
          onClick={() => void query.refetch()}
        >
          {t("common.retry")}
        </Button>
      </div>
    );
  const items = query.data?.pages.flatMap((page) => page.proposals) ?? [];
  if (!items.length && !query.hasNextPage) {
    const label = t(
      query.isPending
        ? "artifacts.review.loading"
        : "artifacts.proposals.empty",
    );
    if (embedded)
      return (
        <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2.5 bg-surface-canvas/40 p-6 text-center">
          <span className="flex h-10 w-10 items-center justify-center rounded-xl border border-line-subtle bg-surface-card text-fg-muted shadow-card">
            <Icon name="file-text" size={16} />
          </span>
          <p className="text-body-sm text-fg-muted">{label}</p>
        </div>
      );
    return <p className="text-body-sm text-fg-muted">{label}</p>;
  }
  return (
    <div
      className={cn(
        "space-y-2 overflow-y-auto text-body-sm",
        embedded ? "min-h-0 flex-1 bg-surface-canvas/40 p-3" : "max-h-[60vh]",
      )}
    >
      {items.map((p) => (
        <ProposalLink key={p.id} proposal={p} onOpen={onOpen} />
      ))}
      {query.hasNextPage && (
        <div className="pt-1 text-center">
          <Button
            size="28"
            variant="ghost"
            disabled={query.isFetchingNextPage}
            onClick={() => void query.fetchNextPage()}
          >
            {t("artifacts.comments.more")}
          </Button>
        </div>
      )}
    </div>
  );
}
function ProposalLink({
  proposal: p,
  onOpen,
}: {
  proposal: WorkspaceProposal;
  onOpen: () => void;
}) {
  const t = useT(),
    open = useOpenArtifactList(proposalTabKey(p.id));
  const pending = p.status === "PENDING";
  return (
    <button
      type="button"
      className="group flex w-full items-center justify-between gap-2.5 rounded-xl border border-line-subtle bg-surface-card px-3 py-2.5 text-left shadow-card transition-colors hover:border-line-strong hover:bg-surface-hover"
      onClick={() => {
        open();
        onOpen();
      }}
    >
      <span className="flex min-w-0 items-center gap-2.5">
        <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-surface-muted text-fg-muted">
          <Icon name="file-text" size={14} />
        </span>
        <span className="truncate font-medium text-fg-default">
          {p.base.name}
        </span>
      </span>
      <span className="flex shrink-0 items-center gap-1.5">
        <span
          className={cn(
            "rounded-full px-2 py-0.5 text-caption",
            pending
              ? "bg-accent-subtle font-medium text-accent"
              : "bg-surface-muted text-fg-muted",
          )}
        >
          {t(`artifacts.proposals.${p.status}`)}
        </span>
        <Icon
          name="chevron-right"
          size={13}
          className="text-fg-muted opacity-50 transition-opacity group-hover:opacity-100"
        />
      </span>
    </button>
  );
}
