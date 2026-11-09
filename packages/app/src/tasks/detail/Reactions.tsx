import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Suspense, lazy, useRef, useState } from "react";

import type { ReactionSummary, Task } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, IconButton, Popover, Spinner, cn, useToast } from "@openfox/ui";

import { keys, useApi } from "../../context";
import { taskErrorMessage } from "../queries";
import {
  ReadOnlyTip,
  useReadOnly,
  useReadOnlyReason,
} from "../../spaces/readonly";

// 选择器与表情数据第一次打开时才加载（单独分包）。
// 尺寸与 EmojiPickerPanel 保持一致；不从那里静态引入，否则整个库会进主包。
const EmojiPickerPanel = lazy(() => import("./EmojiPickerPanel"));
const PICKER_WIDTH = 336;
const PICKER_HEIGHT = 400;

/** 表情条：已有表情可点切换，「添加表情」弹出表情选择器。不传 commentId 时作用于描述。 */
export function Reactions({
  orgId,
  task,
  commentId,
  reactions,
}: {
  orgId: string;
  task: Task;
  commentId?: string;
  reactions: ReactionSummary[];
}) {
  const t = useT();
  const readOnly = useReadOnly();
  const readOnlyReason = useReadOnlyReason();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  // 乐观状态：emoji → 是否已回应
  const [pending, setPending] = useState<Record<string, boolean>>({});

  const toggle = useMutation({
    mutationFn: ({ emoji, on }: { emoji: string; on: boolean }) =>
      on
        ? api.tasks.addReaction(orgId, task.space_id, task.id, emoji, commentId)
        : api.tasks.removeReaction(
            orgId,
            task.space_id,
            task.id,
            emoji,
            commentId,
          ),
    onMutate: ({ emoji, on }) => setPending((p) => ({ ...p, [emoji]: on })),
    onError: (error) => toast(taskErrorMessage(t, error), { tone: "error" }),
    onSettled: async (_r, _e, { emoji }) => {
      await queryClient.invalidateQueries({
        queryKey: commentId
          ? keys.taskSub(orgId, task.id, "timeline")
          : keys.taskDetails(orgId),
      });
      setPending((p) => {
        const next = { ...p };
        delete next[emoji];
        return next;
      });
    },
  });

  const shown = new Map(reactions.map((r) => [r.emoji, { ...r }]));
  for (const [emoji, on] of Object.entries(pending)) {
    const current = shown.get(emoji);
    if (current && current.reacted !== on) {
      current.count += on ? 1 : -1;
      current.reacted = on;
      if (current.count <= 0) shown.delete(emoji);
    } else if (!current && on)
      shown.set(emoji, { emoji, count: 1, reacted: true });
  }

  return (
    <div
      role="group"
      aria-label={t("tasks.detail.reactions")}
      className="flex flex-wrap items-center gap-1"
    >
      {[...shown.values()].map((r) => (
        <button
          key={r.emoji}
          type="button"
          aria-pressed={r.reacted}
          aria-label={`${t("tasks.detail.react", { emoji: r.emoji })} ${r.count}`}
          disabled={readOnly}
          title={readOnlyReason}
          onClick={() => toggle.mutate({ emoji: r.emoji, on: !r.reacted })}
          className={cn(
            "flex h-6 items-center gap-1 rounded-full border px-2 text-caption",
            r.reacted
              ? "border-info bg-info-soft text-info"
              : "border-line-default text-fg-secondary hover:bg-surface-hover",
          )}
        >
          <span aria-hidden>{r.emoji}</span>
          {r.count}
        </button>
      ))}
      <ReadOnlyTip>
        <IconButton
          ref={anchor}
          label={t("tasks.detail.addReaction")}
          icon={<Icon name="smile" size={14} />}
          size="28"
          aria-expanded={open}
          disabled={readOnly}
          onClick={() => setOpen((v) => !v)}
        />
      </ReadOnlyTip>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        aria-label={t("tasks.detail.addReaction")}
        className="overflow-hidden p-0"
      >
        {/* 表情选择器懒加载；加载期间占位保持同样尺寸，弹层定位不跳 */}
        <Suspense
          fallback={
            <div
              style={{ width: PICKER_WIDTH, height: PICKER_HEIGHT }}
              className="flex items-center justify-center"
            >
              <Spinner />
            </div>
          }
        >
          <EmojiPickerPanel
            onPick={(emoji) => {
              setOpen(false);
              const current = shown.get(emoji);
              if (!current?.reacted) toggle.mutate({ emoji, on: true });
            }}
          />
        </Suspense>
      </Popover>
    </div>
  );
}
