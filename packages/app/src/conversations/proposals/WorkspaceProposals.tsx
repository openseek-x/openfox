import { useInfiniteQuery } from "@tanstack/react-query";
import type { WorkspaceProposal } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, cn } from "@openfox/ui";
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
  if (!items.length && !query.hasNextPage)
    return (
      <p
        className={cn(
          "text-body-sm text-fg-muted",
          embedded && "p-6 text-center",
        )}
      >
        {t(
          query.isPending
            ? "artifacts.review.loading"
            : "artifacts.proposals.empty",
        )}
      </p>
    );
  return (
    <div
      className={cn(
        "space-y-1 overflow-y-auto text-body-sm",
        embedded ? "min-h-0 flex-1 p-3" : "max-h-[60vh]",
      )}
    >
      {items.map((p) => (
        <ProposalLink key={p.id} proposal={p} onOpen={onOpen} />
      ))}
      {query.hasNextPage && (
        <Button
          size="28"
          variant="ghost"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {t("artifacts.comments.more")}
        </Button>
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
  return (
    <button
      type="button"
      className="flex w-full items-center justify-between gap-2 rounded-lg border border-transparent px-2.5 py-2 text-left hover:border-line-subtle hover:bg-surface-hover"
      onClick={() => {
        open();
        onOpen();
      }}
    >
      <span className="truncate font-medium text-fg-default">
        {p.base.name}
      </span>
      <span className="shrink-0 rounded-full bg-surface-muted px-2 py-0.5 text-caption text-fg-muted">
        {t(`artifacts.proposals.${p.status}`)}
      </span>
    </button>
  );
}
