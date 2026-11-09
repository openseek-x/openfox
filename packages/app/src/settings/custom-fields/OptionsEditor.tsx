import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS as DndCSS } from "@dnd-kit/utilities";

import { useT } from "@openfox/i18n";
import { Button, Icon, IconButton, OPTION_COLORS, cn } from "@openfox/ui";

import { ColorDotPicker } from "../ColorPicker";
import type { OptionRow } from "./kinds";

/** 下一个选项颜色：按顺序轮换 OPTION_COLORS。 */
export function nextOptionColor(rows: OptionRow[]): string {
  return OPTION_COLORS[rows.length % OPTION_COLORS.length]!;
}

/**
 * 单选 / 多选的选项编辑：
 * 拖动排序、改色、删除、「+ 添加」。改动先更新本地行，live 回调用于编辑时实时保存。
 */
export function OptionsEditor({
  rows,
  onRowsChange,
  live,
}: {
  rows: OptionRow[];
  onRowsChange: (rows: OptionRow[]) => void;
  /** 编辑已有字段时的实时保存 */
  live?: {
    onLabelCommit: (row: OptionRow) => void;
    onColor: (row: OptionRow, color: string) => void;
    onRemove: (row: OptionRow) => void;
    onMove: (row: OptionRow, position: number) => void;
  };
}) {
  const t = useT();
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );
  const onDragEnd = ({ active, over }: DragEndEvent) => {
    if (!over || active.id === over.id) return;
    const from = rows.findIndex((row) => row.key === active.id);
    const to = rows.findIndex((row) => row.key === over.id);
    if (from === -1 || to === -1) return;
    const next = arrayMove(rows, from, to);
    onRowsChange(next);
    const moved = next[to]!;
    // 服务端 position 只算已保存的选项
    if (live && moved.id)
      live.onMove(
        moved,
        next.filter((row) => row.id).findIndex((row) => row.key === moved.key),
      );
  };
  const update = (key: string, patch: Partial<OptionRow>) =>
    onRowsChange(
      rows.map((row) => (row.key === key ? { ...row, ...patch } : row)),
    );

  return (
    <div className="flex flex-col gap-1.5">
      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        accessibility={{
          screenReaderInstructions: {
            draggable: t("settings.customFields.options.dndInstructions"),
          },
        }}
        onDragEnd={onDragEnd}
      >
        <SortableContext
          items={rows.map((row) => row.key)}
          strategy={verticalListSortingStrategy}
        >
          <ul
            aria-label={t("settings.customFields.dialog.options")}
            className="flex flex-col gap-1.5"
          >
            {rows.map((row, index) => (
              <OptionItem
                key={row.key}
                row={row}
                index={index}
                onLabel={(label) => update(row.key, { label })}
                onLabelBlur={() => live?.onLabelCommit(row)}
                onColor={(color) => {
                  update(row.key, { color });
                  live?.onColor(row, color);
                }}
                onRemove={() => {
                  onRowsChange(rows.filter((item) => item.key !== row.key));
                  live?.onRemove(row);
                }}
              />
            ))}
          </ul>
        </SortableContext>
      </DndContext>
      <Button
        variant="outline"
        size="28"
        className="self-start"
        leading={<Icon name="plus" />}
        disabled={rows.length >= 200}
        onClick={() =>
          onRowsChange([
            ...rows,
            {
              key: `new-${crypto.randomUUID()}`,
              label: "",
              color: nextOptionColor(rows),
            },
          ])
        }
      >
        {t("settings.customFields.options.add")}
      </Button>
    </div>
  );
}

function OptionItem({
  row,
  index,
  onLabel,
  onLabelBlur,
  onColor,
  onRemove,
}: {
  row: OptionRow;
  index: number;
  onLabel: (label: string) => void;
  onLabelBlur: () => void;
  onColor: (color: string) => void;
  onRemove: () => void;
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
  } = useSortable({ id: row.key });
  const name =
    row.label.trim() ||
    t("settings.customFields.options.unnamed", { n: index + 1 });
  return (
    <li
      ref={setNodeRef}
      style={{ transform: DndCSS.Translate.toString(transform), transition }}
      className={cn(
        "flex h-9 items-center gap-1.5 rounded-lg border border-line-default bg-surface-card pr-1 pl-1",
        isDragging && "relative z-10 shadow-popover",
      )}
    >
      <button
        ref={setActivatorNodeRef}
        type="button"
        aria-label={t("settings.customFields.options.drag", { name })}
        className="flex size-6 cursor-grab items-center justify-center rounded-md text-fg-subtle hover:bg-surface-muted"
        {...attributes}
        {...listeners}
      >
        <Icon name="grip-vertical" size={14} />
      </button>
      <ColorDotPicker
        size="md"
        color={row.color}
        colors={OPTION_COLORS}
        label={t("settings.customFields.options.color", { name })}
        onChange={onColor}
      />
      <input
        value={row.label}
        maxLength={100}
        aria-label={t("settings.customFields.options.label", { n: index + 1 })}
        placeholder={t("settings.customFields.options.placeholder")}
        onChange={(event) => onLabel(event.target.value)}
        onBlur={onLabelBlur}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            event.currentTarget.blur();
          }
        }}
        className="h-full min-w-0 flex-1 bg-transparent px-1 text-body outline-none placeholder:text-fg-subtle"
      />
      <IconButton
        label={t("settings.customFields.options.remove", { name })}
        icon={<Icon name="trash-2" size={14} className="text-danger" />}
        size="28"
        onClick={onRemove}
      />
    </li>
  );
}
