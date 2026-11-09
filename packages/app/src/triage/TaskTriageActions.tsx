import { useEffect, useRef, useState } from "react";
import type { Task } from "@openfox/api";
import { useT, useI18n } from "@openfox/i18n";
import { Button, Icon, Tooltip, useToast } from "@openfox/ui";
import { useMatch } from "react-router-dom";
import { useReadOnly } from "../spaces/readonly";
import { TriageSnoozeMenu } from "./TriageSnoozeMenu";
import { TriageDecisionDialog } from "./TriageDecisionDialog";
import { TaskSearchDialog } from "../tasks/detail/TaskSearchDialog";
import { useTriageDuplicate, useTriageSnooze } from "./queries";

/** 分拣操作条（4.5 / 4.7–4.11）：分拣队列与任务详情共用。任务不在分拣台里时不渲染。 */
export type WorkbenchActions = {
  onProcessed: () => void;
  onSnoozed: () => void;
};
export function TaskTriageActions({
  orgId,
  task,
  workbench,
}: {
  workbench?: WorkbenchActions;
  orgId: string;
  task: Task;
}) {
  const t = useT();
  const { locale } = useI18n();
  const toast = useToast();
  const [dialog, setDialog] = useState<
    "accept" | "decline" | "duplicate" | null
  >(null);
  const [snoozeOpen, setSnoozeOpen] = useState(false);
  const snoozeAnchor = useRef<HTMLButtonElement | null>(null);
  const snooze = useTriageSnooze(orgId, task.space_id);
  const duplicate = useTriageDuplicate(orgId, task.space_id);
  const inQueue = useMatch("/o/:orgId/spaces/:spaceId/triage") !== null;
  const readOnly = useReadOnly();
  useEffect(() => {
    if (!inQueue || !task.triage || readOnly) return;
    let lastM = 0;
    const handle = (e: KeyboardEvent) => {
      if (
        e.defaultPrevented ||
        e.altKey ||
        e.metaKey ||
        e.ctrlKey ||
        e.shiftKey ||
        document.querySelector(
          '[role="dialog"],[role="menu"],[role="listbox"]',
        ) ||
        (e.target instanceof HTMLElement &&
          e.target.closest("input,textarea,select,[contenteditable=true]"))
      )
        return;
      const key = e.key.toLowerCase();
      if (key === "m" && Date.now() - lastM > 2500) {
        lastM = Date.now();
        return;
      }
      if (key === "1") setDialog("accept");
      else if (key === "2" || (key === "m" && Date.now() - lastM <= 2500))
        setDialog("duplicate");
      else if (key === "3") setDialog("decline");
      else if (key === "h") setSnoozeOpen(true);
      else {
        lastM = 0;
        return;
      }
      lastM = 0;
      e.preventDefault();
    };
    document.addEventListener("keydown", handle);
    return () => document.removeEventListener("keydown", handle);
  }, [inQueue, task.triage, readOnly]);
  if (!task.triage || readOnly) return null;
  const until = task.triage.snoozed_until;
  const snoozedUntil =
    until && Date.parse(until) > Date.now() ? new Date(until) : null;
  const snoozeLabel = snoozedUntil
    ? t("spaces.triage.snoozedUntil", {
        time: snoozedUntil.toLocaleString(locale),
      })
    : t("tasks.snooze.title");
  return (
    <div className="flex shrink-0 items-center gap-0.5">
      {(["accept", "decline", "duplicate"] as const).map((action, i) => (
        <Tooltip
          key={action}
          content={
            <>
              {t(`spaces.triage.${action}`)}{" "}
              <kbd>{["1", "3", "2 / MM"][i]}</kbd>
            </>
          }
        >
          <Button
            size="28"
            variant="ghost-muted"
            className="gap-1.5 px-2 text-caption"
            onClick={() => setDialog(action)}
          >
            <Icon
              size={14}
              name={
                action === "accept"
                  ? "circle-check"
                  : action === "decline"
                    ? "circle-x"
                    : "circle-slash"
              }
              className={
                action === "accept"
                  ? "text-success"
                  : action === "decline"
                    ? "text-danger"
                    : ""
              }
            />
            {t(`spaces.triage.${action}`)}
          </Button>
        </Tooltip>
      ))}
      <Tooltip
        content={
          <>
            {snoozeLabel} <kbd>H</kbd>
          </>
        }
      >
        <Button
          ref={snoozeAnchor}
          size="28"
          variant="ghost-muted"
          className="gap-1.5 px-2 text-caption"
          aria-label={snoozeLabel}
          disabled={snooze.isPending}
          onClick={() => setSnoozeOpen((v) => !v)}
        >
          <Icon name="clock" size={14} />
          {snoozedUntil
            ? snoozedUntil.toLocaleDateString(locale, {
                month: "short",
                day: "numeric",
              })
            : t("tasks.snooze.title")}
        </Button>
      </Tooltip>
      <TriageSnoozeMenu
        open={snoozeOpen}
        onOpenChange={setSnoozeOpen}
        anchorRef={snoozeAnchor}
        onUnsnooze={
          snoozedUntil
            ? () =>
                snooze.mutate(
                  { taskId: task.id, input: { until: null } },
                  { onSuccess: () => toast(t("spaces.triage.unsnoozed")) },
                )
            : undefined
        }
        onSnooze={(until) => {
          snooze.mutate(
            {
              taskId: task.id,
              input: { until: until.toISOString() },
            },
            {
              onSuccess: () => {
                workbench?.onSnoozed();
                toast(t("spaces.triage.snoozed"));
              },
            },
          );
          setSnoozeOpen(false);
        }}
      />
      {(dialog === "accept" || dialog === "decline") && (
        <TriageDecisionDialog
          orgId={orgId}
          task={task}
          onProcessed={workbench?.onProcessed}
          action={dialog}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === "duplicate" && (
        <TaskSearchDialog
          orgId={orgId}
          spaceId={task.space_id}
          duplicatePicker
          busy={duplicate.isPending}
          title={t("spaces.triage.searchDuplicate")}
          excludeIds={[task.id]}
          onClose={() => setDialog(null)}
          onPick={(target) =>
            duplicate.mutate(
              {
                taskId: task.id,
                input: {
                  version: task.version,
                  duplicate_of_task_id: target.id,
                  subscribe: true,
                },
              },
              {
                onSuccess: () => {
                  workbench?.onProcessed();
                  toast(t("spaces.triage.duplicated"));
                  setDialog(null);
                },
              },
            )
          }
        />
      )}
    </div>
  );
}
