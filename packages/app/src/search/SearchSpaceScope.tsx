import { useRef, useState } from "react";

import type { SpaceDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, Popover, cn, handleMenuKeyDown } from "@openfox/ui";

import { SpaceIcon } from "../spaces/SpaceIcon";
import { ALL_SPACES, type SpaceScope } from "./history";

export function SearchSpaceScope({
  spaces,
  scope,
  onChange,
}: {
  spaces: SpaceDetail[];
  scope: SpaceScope;
  onChange: (scope: SpaceScope) => void;
}) {
  const t = useT();
  const anchor = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [needle, setNeedle] = useState("");
  const selected =
    scope.mode === "all"
      ? spaces
      : spaces.filter((space) => scope.ids.includes(space.id));
  const matches = spaces.filter((space) =>
    (space.name + " " + space.prefix)
      .toLowerCase()
      .includes(needle.trim().toLowerCase()),
  );
  const toggle = (id: string) => {
    const ids = selected.some((space) => space.id === id)
      ? selected.map((space) => space.id).filter((value) => value !== id)
      : [...selected.map((space) => space.id), id];
    onChange(
      ids.length === spaces.length
        ? ALL_SPACES
        : { mode: ids.length ? "subset" : "none", ids },
    );
  };
  return (
    <>
      <button
        type="button"
        ref={anchor}
        aria-label={t("commandPalette.spaceFilter")}
        aria-haspopup="menu"
        aria-expanded={open}
        className={cn(
          "flex h-7 shrink-0 items-center gap-1.5 rounded-full border border-line-default px-3 text-caption font-medium hover:bg-surface-hover",
          scope.mode === "all" ? "text-fg-muted" : "bg-surface-active",
        )}
        onClick={() => {
          setOpen(!open);
          setNeedle("");
        }}
      >
        {scope.mode === "all" ? (
          t("commandPalette.allSpaces")
        ) : !selected.length ? (
          t("commandPalette.noSpaces")
        ) : (
          <>
            {selected.slice(0, 5).map((space) => (
              <SpaceIcon key={space.id} icon={space.icon} size={14} />
            ))}
            {selected.length === 1 && (
              <span className="max-w-30 truncate">{selected[0]?.name}</span>
            )}
            {selected.length > 5 && <span>+{selected.length - 5}</span>}
          </>
        )}
        <Icon name="chevron-down" size={12} className="text-fg-muted" />
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        role="menu"
        aria-label={t("commandPalette.spaceFilter")}
        className="min-w-52 p-0"
      >
        <div
          onKeyDown={(event) => {
            if (
              event.target instanceof HTMLInputElement &&
              (event.key === "Home" || event.key === "End")
            )
              return;
            handleMenuKeyDown(event);
          }}
        >
          <div className="flex h-9 items-center gap-2 border-b border-line-default px-3 text-fg-muted">
            <Icon name="search" size={14} />
            <input
              autoFocus
              aria-label={t("commandPalette.findSpace")}
              placeholder={t("commandPalette.findSpace")}
              value={needle}
              onChange={(event) => setNeedle(event.target.value)}
              className="min-w-0 flex-1 bg-transparent text-body-sm outline-none"
            />
          </div>
          <div className="max-h-64 overflow-y-auto p-1">
            <button
              type="button"
              role="menuitemcheckbox"
              aria-checked={
                scope.mode === "subset" ? "mixed" : scope.mode === "all"
              }
              onClick={() =>
                onChange(
                  scope.mode === "all" ? { mode: "none", ids: [] } : ALL_SPACES,
                )
              }
              className="flex h-8 w-full items-center gap-2 rounded-md px-2 text-body-sm hover:bg-surface-hover"
            >
              <ScopeCheck
                checked={scope.mode !== "none"}
                mixed={scope.mode === "subset"}
              />
              {t("commandPalette.allSpaces")}
            </button>
            {matches.map((space) => {
              const checked = selected.some((item) => item.id === space.id);
              return (
                <div
                  key={space.id}
                  className="flex h-8 items-center rounded-md px-2 hover:bg-surface-hover"
                >
                  <button
                    type="button"
                    role="menuitemcheckbox"
                    aria-label={space.name}
                    aria-checked={checked}
                    onClick={() => toggle(space.id)}
                    className="flex h-full items-center pr-2"
                  >
                    <ScopeCheck checked={checked} />
                  </button>
                  <button
                    type="button"
                    aria-label={t("commandPalette.onlySpace", {
                      name: space.name,
                    })}
                    onClick={() => {
                      onChange({ mode: "subset", ids: [space.id] });
                      setOpen(false);
                      anchor.current?.focus();
                    }}
                    className="flex h-full min-w-0 flex-1 items-center gap-2 text-left text-body-sm"
                  >
                    <SpaceIcon icon={space.icon} size={14} />
                    <span className="truncate">{space.name}</span>
                    {space.visibility === "private" && (
                      <Icon name="lock" size={12} className="text-fg-muted" />
                    )}
                    <span className="text-caption text-fg-muted">
                      {space.prefix}
                    </span>
                  </button>
                </div>
              );
            })}
            {matches.length === 0 && needle && (
              <p className="px-2 py-2 text-body-sm text-fg-muted">
                {t("commandPalette.empty")}
              </p>
            )}
          </div>
        </div>
      </Popover>
    </>
  );
}

function ScopeCheck({ checked, mixed }: { checked: boolean; mixed?: boolean }) {
  return (
    <span
      aria-hidden
      className={cn(
        "flex size-4 items-center justify-center rounded-[5px] border",
        checked ? "border-accent bg-accent text-white" : "border-line-default",
      )}
    >
      {checked && <Icon name={mixed ? "minus" : "check"} size={12} />}
    </span>
  );
}
