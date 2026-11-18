import type { ReactNode } from "react";
import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useT } from "@openfox/i18n";
import { Icon, IconButton } from "@openfox/ui";

export function SortableTypeRow({
  id,
  name,
  index,
  enabled,
  children,
}: {
  id: string;
  name: string;
  index: number;
  enabled: boolean;
  children: ReactNode;
}) {
  const t = useT();
  const sortable = useSortable({ id, disabled: !enabled });
  return (
    <li
      ref={sortable.setNodeRef}
      style={{
        transform: CSS.Transform.toString(sortable.transform),
        transition: sortable.transition,
        opacity: sortable.isDragging ? 0.65 : undefined,
      }}
      className={`flex items-center ${index > 0 ? "border-t border-line-soft/50" : ""}`}
    >
      {enabled && (
        <IconButton
          ref={sortable.setActivatorNodeRef}
          label={t("spaces.taskTypes.moveType", { name })}
          icon={<Icon name="grip-vertical" />}
          className="ml-3 touch-none"
          {...sortable.attributes}
          {...sortable.listeners}
        />
      )}
      {children}
    </li>
  );
}
