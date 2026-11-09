import { useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { isApiError, type MarketAgentTarget } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Checkbox,
  Dialog,
  Icon,
  IconButton,
  Skeleton,
  cn,
  useToast,
} from "@openfox/ui";
import { AgentAvatar } from "../agents/AgentAvatar";
import { keys, useApi } from "../context";
import { marketErrorText } from "./queries";

function useAgentTargets(orgId: string, q: string, enabled = true) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: ["org", orgId, "market", "agent-targets", q],
    queryFn: ({ pageParam }) => api.market.agentTargets(orgId, q, pageParam),
    initialPageParam: "",
    getNextPageParam: (page) => page.next_cursor || undefined,
    enabled,
  });
}

export function MarketAgentPicker({
  orgId,
  marketId,
  name,
  onBack,
  onClose,
}: {
  orgId: string;
  marketId: string;
  name: string;
  onBack: () => void;
  onClose: () => void;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient(),
    toast = useToast();
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<Map<string, MarketAgentTarget>>(
    new Map(),
  );
  const [busy, setBusy] = useState(false);
  const q = search.trim();
  const catalog = useAgentTargets(orgId, "");
  // Search locally once the full directory is loaded, just like the reference picker.
  // An incomplete page needs a server search so results beyond the first page are eligible.
  const remoteSearch = Boolean(q && catalog.hasNextPage);
  const filtered = useAgentTargets(orgId, q, remoteSearch);
  const targets = remoteSearch ? filtered : catalog;
  const candidates = targets.data?.pages.flatMap((p) => p.items) ?? [];
  const needle = q.toLocaleLowerCase();
  const items =
    remoteSearch || !needle
      ? candidates
      : candidates.filter((a) =>
          (a.name + "\n" + a.space_name).toLocaleLowerCase().includes(needle),
        );
  function toggle(agent: MarketAgentTarget) {
    setSelected((prev) => {
      const next = new Map(prev);
      if (next.has(agent.id)) next.delete(agent.id);
      else next.set(agent.id, agent);
      return next;
    });
  }
  async function install() {
    if (busy || !selected.size) return;
    setBusy(true);
    const picked = [...selected.values()],
      succeeded: MarketAgentTarget[] = [];
    const failed: unknown[] = [];
    try {
      // Bound work when a member selects many Agents. Each target commits independently.
      for (let i = 0; i < picked.length; i += 4) {
        await Promise.all(
          picked.slice(i, i + 4).map(async (a) => {
            try {
              await api.agentSkills.installMarketSkill(
                orgId,
                a.space_id,
                a.id,
                marketId,
              );
              succeeded.push(a);
            } catch (error) {
              failed.push(error);
            }
          }),
        );
      }
      for (const space of new Set(succeeded.map((a) => a.space_id)))
        void cache.invalidateQueries({ queryKey: keys.skills(orgId, space) });
      if (failed.length)
        toast(
          isApiError(failed[0])
            ? marketErrorText(t, failed[0], "market.skill.addFailed")
            : t("market.skill.installFailed", { count: failed.length }),
          { tone: "error" },
        );
      if (succeeded.length) {
        void cache.invalidateQueries({ queryKey: keys.agentDirectory(orgId) });
        toast(
          picked.length === 1
            ? t("market.skill.installedOne", {
                skill: name,
                name: picked[0]!.name,
              })
            : t("market.skill.installedMany", {
                skill: name,
                count: succeeded.length,
              }),
          { tone: "success" },
        );
      }
      if (!failed.length) onClose();
    } finally {
      setBusy(false);
    }
  }
  const title = t("market.skill.agentTitle");
  return (
    <Dialog
      open
      title={title}
      hideHeader
      onClose={busy ? undefined : onClose}
      width={640}
      className="h-[min(38rem,calc(100dvh-2rem))] gap-0! overflow-hidden rounded-[14px]! p-0! [&>div]:flex [&>div]:min-h-0 [&>div]:flex-1 [&>div]:flex-col"
    >
      <header className="flex h-16 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <IconButton
          label={t("common.back")}
          icon={<Icon name="arrow-left" />}
          disabled={busy}
          onClick={onBack}
        />
        <div className="min-w-0 flex-1">
          <h3 className="truncate text-body font-semibold">{title}</h3>
          <p className="truncate text-caption text-fg-muted">{name}</p>
        </div>
        <IconButton
          label={t("common.close")}
          icon={<Icon name="x" />}
          disabled={busy}
          onClick={onClose}
        />
      </header>
      <div
        className="min-h-0 flex-1 overflow-y-auto px-6 py-4"
        onScroll={(e) => {
          const el = e.currentTarget;
          if (
            el.scrollHeight - el.scrollTop - el.clientHeight < 80 &&
            targets.hasNextPage &&
            !targets.isFetching &&
            !targets.isError
          )
            void targets.fetchNextPage();
        }}
      >
        <label className="relative block">
          <Icon
            name="search"
            size={14}
            className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-muted"
          />
          <input
            type="text"
            maxLength={100}
            aria-label={t("market.skill.agentSearch")}
            placeholder={t("market.skill.agentSearch")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-8 w-full rounded-full border border-line-default bg-surface-card pr-3 pl-8 text-body outline-none focus:border-accent"
          />
        </label>
        <div className="mt-3 flex flex-col gap-0.5">
          {targets.isPending ? (
            <div aria-busy="true" className="space-y-1.5">
              <Skeleton className="h-10" />
              <Skeleton className="h-10" />
            </div>
          ) : (
            items.map((a) => (
              <label
                key={a.id}
                className={cn(
                  "flex cursor-pointer items-center gap-3 rounded-lg px-2 py-2 text-body",
                  selected.has(a.id)
                    ? "bg-surface-active"
                    : "hover:bg-surface-hover",
                )}
              >
                <Checkbox
                  checked={selected.has(a.id)}
                  onChange={() => toggle(a)}
                  disabled={busy}
                  aria-label={a.name}
                  className="size-4!"
                />
                <AgentAvatar
                  name={a.name}
                  avatar={a.avatar}
                  size={24}
                  className="rounded-md!"
                />
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate">{a.name}</span>
                  <span className="truncate text-caption text-fg-muted">
                    {a.space_name}
                  </span>
                </span>
              </label>
            ))
          )}
          {targets.isError ? (
            <div role="alert" className="px-2 py-2 text-body text-fg-secondary">
              {t("market.loadFailed")}{" "}
              <Button
                variant="ghost"
                size="28"
                onClick={() =>
                  void (targets.isFetchNextPageError
                    ? targets.fetchNextPage()
                    : targets.refetch())
                }
              >
                {t("market.retry")}
              </Button>
            </div>
          ) : (
            !targets.isPending &&
            !items.length && (
              <p className="px-2 py-2 text-body text-fg-muted">
                {t(
                  q && (remoteSearch || candidates.length > 0)
                    ? "market.skill.agentNoMatch"
                    : "market.skill.agentNone",
                )}
              </p>
            )
          )}
          {targets.hasNextPage && !targets.isError && (
            <Button
              variant="ghost"
              loading={targets.isFetchingNextPage}
              onClick={() => void targets.fetchNextPage()}
            >
              {t("market.loadMore")}
            </Button>
          )}
        </div>
      </div>
      <footer className="flex shrink-0 justify-end border-t border-line-default px-6 py-3">
        <Button
          size="28"
          loading={busy}
          disabled={!selected.size}
          onClick={() => void install()}
        >
          {t("market.skill.install")}
        </Button>
      </footer>
    </Dialog>
  );
}
