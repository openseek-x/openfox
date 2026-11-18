import { useInfiniteQuery } from "@tanstack/react-query";
import type { WorkspaceProposal } from "@openfox/api";
import { useT } from "@openfox/i18n";
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
}: {
  orgId: string;
  conversationId: string;
  onOpen: () => void;
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
      <p role="alert" className="mb-2 text-caption text-fg-muted">
        {t("artifacts.proposals.loadFailed")}{" "}
        <button type="button" onClick={() => void query.refetch()}>
          {t("common.retry")}
        </button>
      </p>
    );
  const items = query.data?.pages.flatMap((page) => page.proposals) ?? [];
  if (!items.length && !query.hasNextPage)
    return (
      <p className="text-body-sm text-fg-muted">
        {t(
          query.isPending
            ? "artifacts.review.loading"
            : "artifacts.proposals.empty",
        )}
      </p>
    );
  return (
    <div className="max-h-[60vh] overflow-auto text-body-sm">
      {items.map((p) => (
        <ProposalLink key={p.id} proposal={p} onOpen={onOpen} />
      ))}
      {query.hasNextPage && (
        <button
          type="button"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {t("artifacts.comments.more")}
        </button>
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
      className="flex w-full items-center justify-between gap-2 rounded-lg px-1 py-1.5 text-left hover:bg-surface-hover"
      onClick={() => {
        open();
        onOpen();
      }}
    >
      <span className="truncate">{p.base.name}</span>
      <span className="shrink-0 text-caption text-fg-muted">
        {t(`artifacts.proposals.${p.status}`)}
      </span>
    </button>
  );
}
