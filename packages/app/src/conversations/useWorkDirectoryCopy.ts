import { useEffect, useRef, useState } from "react";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";
import { desktopBridge } from "../desktop";

export function useWorkDirectoryCopy(
  orgId: string,
  conversationId: string | null,
) {
  const t = useT();
  const toast = useToast();
  const [pending, setPending] = useState(false);
  const request = useRef<string | null>(null);
  useEffect(() => {
    setPending(false);
    return () => {
      if (request.current)
        void desktopBridge()
          ?.cancelWorkDirectoryCopy?.(request.current)
          .catch(() => {});
      request.current = null;
    };
  }, [orgId, conversationId]);
  const supported = Boolean(
    desktopBridge()?.copyWorkDirectory &&
    desktopBridge()?.cancelWorkDirectoryCopy,
  );
  return {
    disabled: !supported || !conversationId || pending,
    reason: !supported
      ? t("chat.assistant.path.desktopOnly")
      : !conversationId
        ? t("chat.assistant.path.noSession")
        : t("chat.assistant.path.copying"),
    async copy() {
      const bridge = desktopBridge();
      if (!conversationId || !bridge?.copyWorkDirectory || pending) return;
      const id = crypto.randomUUID();
      request.current = id;
      setPending(true);
      try {
        const result = await bridge.copyWorkDirectory({
          orgId,
          conversationId,
          requestId: id,
        });
        if (request.current !== id) return;
        if (result.ok) toast(t("chat.assistant.path.copied"));
        else if (result.reason !== "cancelled") {
          const messages = {
            timeout: "chat.assistant.path.timeout",
            offline: "chat.assistant.path.offline",
            unsupported: "chat.assistant.path.unsupported",
            workspace_unavailable: "chat.assistant.path.noDirectory",
            changed: "chat.assistant.path.changed",
            clipboard_failed: "chat.assistant.path.clipboardFailed",
            forbidden: "chat.assistant.path.forbidden",
            busy: "chat.assistant.path.busy",
          } as const;
          toast(
            t(
              messages[result.reason as keyof typeof messages] ??
                "chat.assistant.path.failed",
            ),
          );
        }
      } catch {
        if (request.current === id) toast(t("chat.assistant.path.failed"));
      } finally {
        if (request.current === id) {
          request.current = null;
          setPending(false);
        }
      }
    },
  };
}
