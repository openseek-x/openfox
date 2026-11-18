import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { isApiError, type SpaceDetail } from "@openfox/api";

import { keys } from "../context";
import { readLocal, writeLocal } from "../storage";
import { useView } from "../views/queries";
import { useLayoutParam } from "./layoutParam";
import {
  defaultDisplay,
  displayStorageKey,
  normalizeDisplay,
  savedDisplay,
  saveDisplay,
  type DisplaySettings,
} from "./model";

export const defaultViewModeKey = (org: string, space: string) =>
  `${displayStorageKey(org, `space:${space}`)}:inherit-default`;

// Mounted per organization/space. The existing local display is never migrated
// or assigned to an account; only new explicit choices write the local marker.
export function useSpaceDefaultView(
  org: string,
  spaceId: string,
  space: SpaceDetail | undefined,
) {
  const cache = useQueryClient();
  const page = `space:${spaceId}`;
  const [local, setLocal] = useState(() => savedDisplay(org, page, "space"));
  const [marked, setMarked] = useState(
    () => readLocal(defaultViewModeKey(org, spaceId)) === "true",
  );
  const follow = marked || local === null;
  const sourceId =
    follow && space?.features.views
      ? (space.default_view_id ?? undefined)
      : undefined;
  const source = useView(org, sourceId);
  const missing = isApiError(source.error) && source.error.status === 404;
  const valid =
    source.data?.space_id === spaceId && source.data.visibility === "space";
  const view = sourceId && valid && !source.isError ? source.data : undefined;
  useEffect(() => {
    if (sourceId && (missing || (source.data && !valid))) {
      void cache.invalidateQueries({ queryKey: keys.spaces(org) });
      void cache.invalidateQueries({ queryKey: keys.space(org, spaceId) });
    }
  }, [
    cache,
    org,
    spaceId,
    sourceId,
    missing,
    valid,
    source.errorUpdatedAt,
    source.data,
  ]);
  const layout = useLayoutParam();
  const base =
    local ?? normalizeDisplay(view?.display ?? {}, defaultDisplay("space"));
  const value = layout.layout ? { ...base, layout: layout.layout } : base;
  const store = (display: DisplaySettings, inherit: boolean) => {
    setLocal(display);
    setMarked(inherit);
    saveDisplay(org, page, display);
    writeLocal(defaultViewModeKey(org, spaceId), inherit ? "true" : null);
  };
  const reset = () => {
    setLocal(null);
    setMarked(false);
    saveDisplay(org, page, null);
    writeLocal(defaultViewModeKey(org, spaceId), null);
    layout.setLayout(null);
  };
  return {
    view,
    follow,
    loading: Boolean(sourceId) && source.isPending,
    error: sourceId && source.isError && !missing ? source.error : null,
    retry: () => source.refetch(),
    onQueryError: (error: unknown) => {
      if (view && isApiError(error) && error.status === 404)
        void source.refetch();
    },
    reset,
    leave: () => store(value, false),
    control: {
      value,
      onChange: (display: DisplaySettings) =>
        store(display, marked || Boolean(view && follow)),
      onReset: reset,
    },
  };
}
