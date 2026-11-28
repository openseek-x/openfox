import { useRef, useState } from "react";
import { createPortal } from "react-dom";

import { useT } from "@openfox/i18n";
import { Icon, SearchMenu, Tooltip, type SearchMenuOption } from "@openfox/ui";

export function BoardColumnJump({
  toolbar,
  boardId,
  options,
}: {
  toolbar: HTMLElement | null;
  boardId: string;
  options: SearchMenuOption[];
}) {
  const t = useT();
  const anchor = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const label = t("tasks.board.jumpToColumn");

  function jump(key: string) {
    const heading = document.getElementById(`${boardId}-${key}`);
    const header = heading?.closest("header");
    if (!heading || !header) return;
    heading.focus({ preventScroll: true });
    header.scrollIntoView?.({ block: "nearest", inline: "center" });
    if (!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches)
      header.animate?.(
        [
          { backgroundColor: "var(--color-surface-selected)" },
          { backgroundColor: "transparent" },
        ],
        900,
      );
  }

  if (!toolbar || options.length === 0) return null;
  return createPortal(
    <>
      <Tooltip content={label}>
        <button
          ref={anchor}
          type="button"
          aria-label={label}
          aria-haspopup="dialog"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className="inline-flex size-8 shrink-0 items-center justify-center gap-1.5 rounded-full border border-line-default text-body-sm text-fg-secondary not-aria-expanded:hover:bg-surface-hover aria-expanded:bg-surface-active @2xl:w-auto @2xl:px-3"
        >
          <Icon name="target" />
          <span className="hidden @2xl:inline">{label}</span>
        </button>
      </Tooltip>
      <SearchMenu
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        placement="bottom-end"
        fitHeight
        label={label}
        groups={[{ options }]}
        selected={[]}
        onSelect={jump}
        placeholder={t("tasks.board.findColumn")}
      />
    </>,
    toolbar,
  );
}
