import { useSortable } from "@dnd-kit/sortable";
import { CSS as DndCSS } from "@dnd-kit/utilities";

import type { QuickCommand } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, IconButton, cn } from "@openfox/ui";

import { QuickCommandIcon } from "./QuickCommandIcon";
import { unavailableText } from "./QuickCommandForm";

/** 管理列表的一行：可编辑的条目有拖动手柄、编辑、删除；不可编辑的只展示（不能拖动）。 */
export function QuickCommandRow({
  item,
  busy,
  onEdit,
  onDelete,
}: {
  item: QuickCommand;
  busy: boolean;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const t = useT();
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: item.id, disabled: !item.can_edit || busy });
  const target = item.target.available
    ? item.target.name
    : unavailableText(t, item);
  return (
    <li
      ref={setNodeRef}
      style={{ transform: DndCSS.Translate.toString(transform), transition }}
      className={cn(
        "flex min-h-12 items-center gap-2 border-b border-line-soft bg-surface-card px-2 py-1.5 first:rounded-t-xl last:rounded-b-xl last:border-b-0",
        isDragging && "relative z-10 shadow-popover",
      )}
    >
      {item.can_edit ? (
        <button
          ref={setActivatorNodeRef}
          type="button"
          aria-label={t("tasks.quickCommands.manage.dragHandle", {
            name: item.name,
          })}
          className="flex size-6 shrink-0 cursor-grab items-center justify-center rounded-md text-fg-subtle hover:bg-surface-muted active:cursor-grabbing"
          {...attributes}
          {...listeners}
        >
          <Icon name="grip-vertical" />
        </button>
      ) : (
        <span className="size-6 shrink-0" aria-hidden />
      )}
      <QuickCommandIcon command={item} />
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-body font-medium">{item.name}</span>
        <span
          className={cn(
            "truncate text-caption",
            item.target.available ? "text-fg-muted" : "text-danger",
          )}
        >
          {target}
        </span>
      </div>
      {item.can_edit && (
        <div className="flex shrink-0 items-center">
          <IconButton
            label={t("tasks.quickCommands.manage.edit", { name: item.name })}
            icon={<Icon name="pencil" size={14} />}
            size="28"
            variant="ghost"
            disabled={busy}
            onClick={onEdit}
          />
          <IconButton
            label={t("tasks.quickCommands.manage.remove", { name: item.name })}
            icon={<Icon name="trash-2" size={14} />}
            size="28"
            variant="ghost"
            disabled={busy}
            onClick={onDelete}
          />
        </div>
      )}
    </li>
  );
}
