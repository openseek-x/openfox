import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { isApiError } from "@openfox/api";
import { keys, useApi } from "../context";
import { slackPending } from "./slack-model";
export const slackKey = (org: string) => [
  ...keys.integrations(org),
  "slack-configuration",
];
export function useSlackConfiguration(org: string) {
  const api = useApi();
  return useQuery({
    queryKey: slackKey(org),
    queryFn: ({ signal }) => api.slack.configuration(org, signal),
    retry: false,
    refetchInterval: (q) =>
      q.state.data?.apps.some((a) => slackPending.has(a.state)) ? 5000 : false,
  });
}
export function useSlackRefresh(org: string) {
  const cache = useQueryClient();
  return () => {
    void cache.invalidateQueries({ queryKey: keys.integrations(org) });
    void cache.invalidateQueries({ queryKey: keys.accountBindings(org) });
    void cache.invalidateQueries({ queryKey: keys.notificationChannels });
  };
}
/** Secret-bearing writes never enter a mutation cache. A closed form aborts and ignores late results. */
export function useSlackWrite(refresh: () => void) {
  const active = useRef<AbortController | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(),
    [blocked, setBlocked] = useState(false);
  useEffect(
    () => () => {
      active.current?.abort();
      active.current = null;
    },
    [],
  );
  async function run<T>(
    request: (signal: AbortSignal) => Promise<T>,
    done: (value: T) => void,
  ) {
    if (active.current || blocked) return;
    const controller = new AbortController();
    active.current = controller;
    setBusy(true);
    setError(undefined);
    try {
      const result = await request(controller.signal);
      if (active.current === controller && !controller.signal.aborted) {
        done(result);
        refresh();
      }
    } catch (e) {
      if (active.current === controller && !controller.signal.aborted) {
        setError(e);
        if (
          isApiError(e) &&
          (e.status === 409 ||
            e.status === 403 ||
            e.status === 404 ||
            e.uncertain ||
            e.code === "INVALID_RESPONSE")
        )
          setBlocked(true);
        refresh();
      }
    } finally {
      if (active.current === controller) {
        active.current = null;
        setBusy(false);
      }
    }
  }
  return { run, busy, error, blocked };
}
