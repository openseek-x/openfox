import { useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { newIdempotencyKey, type Run } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, useToast } from "@openfox/ui";
import { keys, useApi } from "../../context";

export function ScheduledRun({ orgId, run }: { orgId: string; run: Run }) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient(),
    toast = useToast();
  const [open, setOpen] = useState(false);
  const key = useRef("");
  const mutation = useMutation({
    mutationFn: () => api.runs.triggerNow(orgId, run.id, key.current),
    onSuccess: () => {
      setOpen(false);
      void cache.invalidateQueries({ queryKey: keys.run(orgId, run.id) });
      void cache.invalidateQueries({ queryKey: keys.conversationLists(orgId) });
    },
    onError: () => toast(t("agents.workingHours.failed"), { tone: "error" }),
  });
  if (run.state !== "QUEUED" || !run.schedule_pending) return null;
  const format = (value: string) =>
    new Date(value).toLocaleString(undefined, { timeZoneName: "short" });
  return (
    <Callout>
      <div className="flex flex-col gap-2" role="status">
        <strong>{t("agents.workingHours.waiting")}</strong>
        <span>
          {run.not_before
            ? t("agents.workingHours.planned", { time: format(run.not_before) })
            : t("agents.workingHours.noWindow")}
        </span>
        {run.requested_start_at && (
          <span>
            {t("agents.workingHours.manual", {
              time: format(run.requested_start_at),
            })}
          </span>
        )}
        {run.can_trigger_now && (
          <Button
            variant="outline"
            size="28"
            onClick={() => {
              key.current = newIdempotencyKey();
              setOpen(true);
            }}
          >
            {t("agents.workingHours.triggerNow")}
          </Button>
        )}
        <Dialog
          open={open}
          onClose={() => {
            if (!mutation.isPending) setOpen(false);
          }}
          title={t("agents.workingHours.confirmTitle")}
          description={t("agents.workingHours.confirmHint")}
          footer={
            <>
              <Button
                variant="outline"
                disabled={mutation.isPending}
                onClick={() => setOpen(false)}
              >
                {t("common.cancel")}
              </Button>
              <Button
                disabled={mutation.isPending}
                onClick={() => mutation.mutate()}
              >
                {t("agents.workingHours.triggerNow")}
              </Button>
            </>
          }
        />
      </div>
    </Callout>
  );
}
