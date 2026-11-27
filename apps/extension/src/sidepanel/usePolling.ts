import { useEffect, useRef } from "react";

import type { Api, ConversationMessage } from "@openfox/api";

import {
  POLL_MIN_MS,
  nextPollDelay,
  pollOnce,
  type PollOutcome,
} from "../lib/poll";

interface PollingOptions {
  api: Api;
  orgId: string | null;
  conversationId: string | null;
  cursorRef: { current: number };
  onMessages: (messages: ConversationMessage[]) => void;
  onError: (error: unknown) => void;
}

/** 对当前会话按游标轮询新消息；面板隐藏时不再排下一次，重新可见时立即补拉。 */
export function usePolling(options: PollingOptions) {
  const { api, orgId, conversationId, cursorRef } = options;
  // 回调每次渲染都会变化，放进 ref 里读取，避免重置轮询。
  const handlers = useRef(options);
  handlers.current = options;

  useEffect(() => {
    if (!orgId || !conversationId) return;
    let stopped = false;
    let inFlight = false;
    let delay = POLL_MIN_MS;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const tick = async () => {
      if (stopped || document.hidden || inFlight) return;
      inFlight = true;
      let outcome: PollOutcome = "idle";
      let again = false;
      try {
        const result = await pollOnce(
          api,
          orgId,
          conversationId,
          cursorRef.current,
        );
        if (stopped) return;
        if (result.messages.length > 0) {
          cursorRef.current = result.cursor;
          handlers.current.onMessages(result.messages);
          outcome = "new";
          again = result.more;
        }
      } catch (error) {
        if (stopped) return;
        outcome = "error";
        handlers.current.onError(error);
      }
      inFlight = false;
      if (document.hidden) return;
      delay = nextPollDelay(delay, outcome);
      timer = setTimeout(() => void tick(), again ? 0 : delay);
    };

    const onVisibility = () => {
      clearTimeout(timer);
      if (document.hidden || inFlight) return;
      delay = POLL_MIN_MS;
      timer = setTimeout(() => void tick(), 0);
    };

    document.addEventListener("visibilitychange", onVisibility);
    timer = setTimeout(() => void tick(), delay);
    return () => {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [api, orgId, conversationId, cursorRef]);
}
