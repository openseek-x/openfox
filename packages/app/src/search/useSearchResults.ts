import { useQueries, useQuery } from "@tanstack/react-query";
import { useState } from "react";

import type { SearchGroup, SearchQuery, SearchType } from "@openfox/api";
import { useApi } from "../context";
import { SEARCH_LIMIT_SINGLE } from "./model";

interface PageRequest {
  type: SearchType;
  offset: number;
}

/** The first request can span several groups. Paging one group must not replace or advance its neighbours. */
export function useSearchResults(
  orgId: string,
  viewer: readonly [string | undefined, string | undefined],
  search: SearchQuery,
  enabled: boolean,
) {
  const api = useApi();
  const scope = ["org", orgId, "search", ...viewer] as const;
  const queryKey = [...scope, search] as const;
  const first = useQuery({
    queryKey,
    queryFn: ({ signal }) => api.search.query(orgId, search, signal),
    enabled,
    staleTime: 0,
    gcTime: 30_000,
    retry: false,
  });
  const identity = JSON.stringify(queryKey);
  const [extra, setExtra] = useState<{
    identity: string;
    pages: PageRequest[];
  }>({ identity: "", pages: [] });
  const pages = enabled && extra.identity === identity ? extra.pages : [];
  const more = useQueries({
    queries: pages.map((page) => {
      const params = {
        ...search,
        types: [page.type],
        offset: page.offset,
        limit: SEARCH_LIMIT_SINGLE,
      };
      return {
        queryKey: [...scope, params],
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          api.search.query(orgId, params, signal),
        staleTime: 30_000,
        gcTime: 30_000,
        retry: false,
      };
    }),
  });
  // A previous response is no longer authoritative after a failed permission check.
  const groups = (enabled && first.isSuccess ? first.data.groups : [])
    .filter((group) => search.types?.includes(group.type))
    .map((group) => {
      const items = [...group.items];
      let total = group.total ?? items.length;
      let pending = false;
      let retry: (() => void) | undefined;
      for (const [index, page] of pages.entries()) {
        if (page.type !== group.type) continue;
        const request = more[index]!;
        const next =
          request.isSuccess &&
          request.data.groups.find(
            (candidate) => candidate.type === group.type,
          );
        if (next) {
          items.push(...next.items);
          total = next.items.length ? (next.total ?? total) : items.length;
        }
        pending ||= request.isFetching;
        if (request.isError) retry = () => void request.refetch();
      }
      return { ...group, items, total, pending, retry };
    });
  function next(group: SearchGroup) {
    const offset = group.items.length;
    setExtra((previous) => {
      const current = previous.identity === identity ? previous.pages : [];
      if (
        current.some(
          (page) => page.type === group.type && page.offset === offset,
        )
      )
        return previous;
      return { identity, pages: [...current, { type: group.type, offset }] };
    });
  }
  return { first, groups, next };
}
