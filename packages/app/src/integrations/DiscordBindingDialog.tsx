import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { DiscordBindingSession } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Checkbox, Dialog } from "@openfox/ui";
import { useApi } from "../context";
import { discordError, discordKey, useDiscordWrite } from "./discord-hooks";
export function DiscordBindingDialog({
  orgId,
  botId,
  onClose,
  refresh,
}: {
  orgId: string;
  botId: string;
  onClose: () => void;
  refresh: () => void;
}) {
  const api = useApi(),
    t = useT(),
    write = useDiscordWrite(refresh),
    [session, setSession] = useState<DiscordBindingSession>(),
    [code, setCode] = useState(""),
    [consent, setConsent] = useState(false),
    [expired, setExpired] = useState(false);
  const poll = useQuery({
    queryKey: [...discordKey(orgId), "binding", session?.id],
    enabled: !!session,
    queryFn: ({ signal }) => api.discord.binding(orgId, session!.id, signal),
    retry: false,
    refetchInterval: (q) => (q.state.data?.status === "WAITING" ? 2000 : false),
  });
  const current = poll.data ?? session;
  useEffect(() => {
    if (!session) return;
    const duration = Math.min(
      10 * 60_000,
      Math.max(0, new Date(session.expires_at).getTime() - Date.now()),
    );
    const timer = setTimeout(() => {
      setCode("");
      setExpired(true);
    }, duration);
    return () => clearTimeout(timer);
  }, [session]);
  function close() {
    setCode("");
    onClose();
  }
  return (
    <Dialog
      open
      title={t("integrations.discord.bind")}
      onClose={close}
      width={560}
      footer={
        <>
          <Button variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          {!session ? (
            <Button
              disabled={write.busy || write.blocked}
              onClick={() =>
                void write.run(
                  (signal) => api.discord.issueBinding(orgId, botId, signal),
                  (s) => {
                    setSession({ ...s, code: undefined });
                    setCode(s.code ?? "");
                  },
                )
              }
            >
              {t("integrations.discord.bind")}
            </Button>
          ) : (
            <Button
              disabled={
                current?.status !== "CANDIDATE" ||
                expired ||
                !consent ||
                write.busy ||
                write.blocked
              }
              onClick={() =>
                void write.run(
                  (signal) =>
                    api.discord.confirmBinding(
                      orgId,
                      session.id,
                      current!.version,
                      signal,
                    ),
                  close,
                )
              }
            >
              {t("integrations.discord.confirmIdentity")}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <p>{t("integrations.discord.codeHelp")}</p>
        {code && (
          <code className="break-all rounded bg-bg-subtle p-3 select-all">
            {code}
          </code>
        )}
        {current?.status === "WAITING" && !expired && (
          <p>{t("integrations.discord.waiting")}</p>
        )}
        {current?.status === "CANDIDATE" && !expired && (
          <>
            <p>
              {t("integrations.discord.candidate")}: {current.candidate_name}
            </p>
            <p>
              {t("integrations.discord.identityID")}:{" "}
              {current.candidate_user_id}
            </p>
            <label className="flex gap-2">
              <Checkbox
                checked={consent}
                onChange={(e) => setConsent(e.target.checked)}
              />
              {t("integrations.discord.identityConsent")}
            </label>
          </>
        )}
        {(expired ||
          (current && ["EXPIRED", "CANCELLED"].includes(current.status))) && (
          <Callout tone="warning">{t("integrations.discord.expired")}</Callout>
        )}
        {(write.error || poll.isError) && (
          <Callout tone="danger">
            {discordError(t, write.error ?? poll.error)}
          </Callout>
        )}
      </div>
    </Dialog>
  );
}
