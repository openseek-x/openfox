import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type Announcements,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";

import {
  QUICK_COMMAND_LIMIT,
  type QuickCommand,
  type QuickCommandVisibility,
} from "@openfox/api";
import { useT } from "@openfox/i18n";

import { pointerFirstCollision } from "../../../dnd/collision";
import { QuickCommandRow } from "./QuickCommandRow";

/**
 * 拖动结束后的新顺序；没有变化返回 null。共享分组里不可编辑的条目保持在原位（服务端同样不动它们），
 * 可编辑条目按拖动结果依次填进可编辑条目占的位置。
 */
export function reorderCommands(
  items: QuickCommand[],
  activeId: string | number,
  overId: string | number | undefined,
): QuickCommand[] | null {
  const ids = items.map((item) => item.id);
  const from = ids.indexOf(String(activeId));
  const to = ids.indexOf(String(overId));
  if (from < 0 || to < 0 || from === to) return null;
  const moved = arrayMove(items, from, to).filter((item) => item.can_edit);
  let next = 0;
  return items.map((item) => (item.can_edit ? moved[next++]! : item));
}

/** 一段（私有 / 共享）：标题 + 条数 + 可拖动列表。不可编辑的条目禁用拖动。 */
export function QuickCommandGroup({
  visibility,
  items,
  busy,
  onReorder,
  onEdit,
  onDelete,
}: {
  visibility: QuickCommandVisibility;
  items: QuickCommand[];
  busy: boolean;
  onReorder: (ordered: QuickCommand[]) => void;
  onEdit: (item: QuickCommand) => void;
  onDelete: (item: QuickCommand) => void;
}) {
  const t = useT();
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );
  const nameOf = (id: string | number) =>
    items.find((item) => item.id === id)?.name ?? "";
  const announcements: Announcements = {
    onDragStart: ({ active }) =>
      t("tasks.quickCommands.manage.dndPicked", { name: nameOf(active.id) }),
    onDragOver: () => "",
    onDragEnd: ({ active }) =>
      t("tasks.quickCommands.manage.dndDropped", { name: nameOf(active.id) }),
    onDragCancel: ({ active }) =>
      t("tasks.quickCommands.manage.dndCancelled", { name: nameOf(active.id) }),
  };
  const onDragEnd = (event: DragEndEvent) => {
    const next = reorderCommands(items, event.active.id, event.over?.id);
    if (next) onReorder(next);
  };
  const title =
    visibility === "PRIVATE"
      ? t("tasks.quickCommands.manage.privateGroup")
      : t("tasks.quickCommands.manage.sharedGroup");
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <h3 className="flex items-center justify-between px-1 text-body-sm font-medium text-fg-muted">
        {title}
        <span className="text-caption font-normal">
          {t("tasks.quickCommands.manage.count", {
            count: items.length,
            max: QUICK_COMMAND_LIMIT,
          })}
        </span>
      </h3>
      <DndContext
        sensors={sensors}
        collisionDetection={pointerFirstCollision}
        accessibility={{
          announcements,
          screenReaderInstructions: {
            draggable: t("tasks.quickCommands.manage.dndInstructions"),
          },
        }}
        onDragEnd={onDragEnd}
      >
        <SortableContext
          items={items.map((item) => item.id)}
          strategy={verticalListSortingStrategy}
        >
          <ul className="flex flex-col rounded-xl border border-line-default">
            {items.map((item) => (
              <QuickCommandRow
                key={item.id}
                item={item}
                busy={busy}
                onEdit={() => onEdit(item)}
                onDelete={() => onDelete(item)}
              />
            ))}
          </ul>
        </SortableContext>
      </DndContext>
    </section>
  );
}
