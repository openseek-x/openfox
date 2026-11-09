import { useEffect, useId, useState } from "react";

import type { AssignmentConversation } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Radio } from "@openfox/ui";

/**
 * 选择运行对话：该任务已有这个 Agent 打开状态的指派会话时，
 * 从召唤菜单指派前先问一次在原有对话里运行还是新开对话运行。默认选中原有对话；取消不做任何写入。
 */
export function RunChoiceDialog({
  open,
  agentName,
  conversationTitle,
  onConfirm,
  onClose,
}: {
  open: boolean;
  agentName: string;
  /** 原有指派会话的标题；空标题显示「无标题」 */
  conversationTitle: string | null;
  onConfirm: (mode: AssignmentConversation) => void;
  onClose: () => void;
}) {
  const t = useT();
  const id = useId();
  const [mode, setMode] = useState<AssignmentConversation>("reuse");
  // 每次打开都回到默认选项
  useEffect(() => {
    if (open) setMode("reuse");
  }, [open]);
  const title = conversationTitle?.trim() || t("chat.untitled");
  const option = (
    value: AssignmentConversation,
    label: string,
    hint?: string,
  ) => (
    // 整张卡片可点；单选的名称只取标题，说明作为描述
    <label className="flex cursor-pointer items-start gap-2 rounded-lg border border-line-default p-3 text-body hover:bg-surface-hover">
      <Radio
        className="mt-0.5"
        name={`run-choice-${id}`}
        value={value}
        checked={mode === value}
        onChange={() => setMode(value)}
        aria-labelledby={`${id}-${value}`}
        aria-describedby={hint ? `${id}-${value}-hint` : undefined}
      />
      <span className="flex min-w-0 flex-col gap-0.5">
        <span id={`${id}-${value}`} className="break-words">
          {label}
        </span>
        {hint && (
          <span
            id={`${id}-${value}-hint`}
            className="text-body-sm text-fg-muted"
          >
            {hint}
          </span>
        )}
      </span>
    </label>
  );
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("tasks.runChoice.title")}
      description={t("tasks.runChoice.description", { agent: agentName })}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("tasks.runChoice.cancel")}
          </Button>
          <Button onClick={() => onConfirm(mode)}>
            {t("tasks.runChoice.start")}
          </Button>
        </>
      }
    >
      <div
        role="radiogroup"
        aria-label={t("tasks.runChoice.title")}
        className="flex flex-col gap-2"
      >
        {option("reuse", t("tasks.runChoice.reuse", { title }))}
        {option("new", t("tasks.runChoice.new"), t("tasks.runChoice.newHint"))}
      </div>
    </Dialog>
  );
}
