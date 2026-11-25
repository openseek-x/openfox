import { useQuery } from "@tanstack/react-query";
import { useT } from "@openfox/i18n";
import { Callout } from "@openfox/ui";

import { useApi } from "../context";

export function QueueFullNotice({ orgId }: { orgId: string }) {
  const t = useT();
  const api = useApi();
  const { data } = useQuery({
    queryKey: ["org", orgId, "runtime-usage"],
    queryFn: ({ signal }) => api.conversations.queueStatus(orgId, signal),
    enabled: Boolean(orgId),
    refetchInterval: 30_000,
    retry: false,
  });
  if (!data?.queue_full) return null;
  return (
    <Callout
      tone="warning"
      title={t("governance.queueFull.title")}
      className="mb-3"
    >
      {t("governance.queueFull.desc")}
      {data.queue_max !== undefined && (
        <>
          {" "}
          {t("governance.queueFull.usage", {
            queued: data.queued,
            max: data.queue_max,
          })}
        </>
      )}
    </Callout>
  );
}
