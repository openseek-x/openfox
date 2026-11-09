import { useRef, useState } from "react";

import { ColorSwatches, Icon, Popover, cn } from "@openfox/ui";

/** 颜色圆点按钮 + 色板浮层，选中即回调并关闭。 */
export function ColorDotPicker({
  color,
  colors,
  onChange,
  label,
  disabled = false,
  size = "sm",
  glyph = "dot",
}: {
  color: string | null;
  colors: readonly string[];
  onChange: (color: string) => void;
  /** 按钮与色板的无障碍名称 */
  label: string;
  disabled?: boolean;
  size?: "sm" | "md";
  /** dot：圆点（标签）；group：分组图标 */
  glyph?: "dot" | "group";
}) {
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLButtonElement | null>(null);
  const dot =
    glyph === "group" ? (
      <Icon
        name="layout-grid"
        size={14}
        style={{ color: color ?? undefined }}
        className={color ? undefined : "text-fg-muted"}
      />
    ) : (
      <span
        aria-hidden
        className={cn(
          "block rounded-full",
          size === "sm" ? "size-2" : "size-3",
          !color && "border border-line-strong",
        )}
        style={color ? { backgroundColor: color } : undefined}
      />
    );
  if (disabled)
    return (
      <span className="flex size-5 shrink-0 items-center justify-center">
        {dot}
      </span>
    );
  return (
    <>
      <button
        ref={anchor}
        type="button"
        aria-label={label}
        title={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex size-5 shrink-0 items-center justify-center rounded-md hover:bg-surface-muted"
      >
        {dot}
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        aria-label={label}
      >
        <ColorSwatches
          colors={colors}
          value={color}
          label={label}
          className="w-[200px] p-2"
          onChange={(next) => {
            setOpen(false);
            anchor.current?.focus();
            if (next.toLowerCase() !== color?.toLowerCase()) onChange(next);
          }}
        />
      </Popover>
    </>
  );
}

/** 从色板里随机取一个颜色（新建标签 / 分组的初始色）。 */
export function randomColor(colors: readonly string[]): string {
  return colors[Math.floor(Math.random() * colors.length)] ?? colors[0]!;
}
