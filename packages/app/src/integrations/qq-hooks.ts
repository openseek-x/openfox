import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { isApiError } from "@openfox/api";
import { keys } from "../context";
export const qqKey = (org: string) => [...keys.integrations(org), "qq"];
export function useQQRefresh(org: string) {
  const cache = useQueryClient();
  return () => {
    void cache.invalidateQueries({ queryKey: keys.integrations(org) });
  };
}
// Credentials, callback URLs and binding codes never enter mutation caches.
export function useQQWrite(refresh: () => void) {
  const current = useRef<AbortController | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(),
    [blocked, setBlocked] = useState(false);
  useEffect(
    () => () => {
      current.current?.abort();
      current.current = null;
    },
    [],
  );
  async function run<T>(
    request: (signal: AbortSignal) => Promise<T>,
    done: (value: T) => void,
  ) {
    if (current.current || blocked) return;
    const c = new AbortController();
    current.current = c;
    setBusy(true);
    setError(undefined);
    try {
      const value = await request(c.signal);
      if (current.current === c && !c.signal.aborted) {
        done(value);
        refresh();
      }
    } catch (e) {
      if (current.current === c && !c.signal.aborted) {
        setError(e);
        if (
          !isApiError(e) ||
          e.uncertain ||
          [403, 404, 409].includes(e.status) ||
          e.code === "INVALID_RESPONSE"
        )
          setBlocked(true);
        refresh();
      }
    } finally {
      if (current.current === c) {
        current.current = null;
        setBusy(false);
      }
    }
  }
  return { run, busy, error, blocked };
}
