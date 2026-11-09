import { useId, useRef, useState, useEffect, type ReactNode } from "react";

import { cn } from "../cn";
import { useFieldContext } from "./Field";
import { Icon } from "./Icon";
import { handleMenuKeyDown } from "./Menu";
import { Popover } from "./Popover";
import { controlShellClass, type ControlStyle } from "./TextInput";
import { useT } from "@openfox/i18n";

export interface SelectOption<T extends string> {
  value: T;
  label: ReactNode;
  icon?: ReactNode;
  description?: ReactNode;
  disabled?: boolean;
}

export interface SelectProps<T extends string> {
  value: T | null;
  onChange: (value: T) => void;
  options: ReadonlyArray<SelectOption<T>>;
  placeholder?: string;
  variant?: ControlStyle;
  leading?: ReactNode;
  disabled?: boolean;
  invalid?: boolean;
  className?: string;
  role?: "combobox";
  "aria-label"?: string;
}

/** 下拉选择：支持标签、表单和独立字段样式；选项以 listbox 浮层展示。 */
export function Select<T extends string>({
  value,
  onChange,
  options,
  placeholder,
  variant = "field",
  leading,
  disabled,
  invalid,
  className,
  ...aria
}: SelectProps<T>) {
  const t = useT();
  const field = useFieldContext();
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLButtonElement | null>(null);
  const listId = useId();
  const current = options.find((option) => option.value === value);
  const isInvalid = invalid ?? field?.invalid ?? false;

  // The portal is initially invisible until Popover measures its anchor. An
  // option's mount-time autoFocus is ignored by Chromium while it is hidden.
  useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() => {
      const list = document.getElementById(listId);
      (
        list?.querySelector<HTMLElement>('[aria-selected="true"]') ??
        list?.querySelector<HTMLElement>('[role="option"]')
      )?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [open, listId]);
  return (
    <>
      <button
        ref={anchor}
        type="button"
        id={field?.inputId}
        disabled={disabled}
        role={aria.role}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-invalid={isInvalid || undefined}
        aria-describedby={field?.describedBy}
        aria-label={aria["aria-label"]}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" && !open) {
            event.preventDefault();
            setOpen(true);
          }
        }}
        className={cn(
          controlShellClass(variant, isInvalid),
          "pr-2 text-left",
          open && "border-line-focus",
          className,
        )}
      >
        {(current?.icon ?? leading) && (
          <span className="flex text-fg-muted">{current?.icon ?? leading}</span>
        )}
        <span
          className={cn(
            "min-w-0 flex-1 truncate",
            !current && "text-fg-subtle",
          )}
        >
          {current?.label ?? placeholder ?? t("common.select")}
        </span>
        <Icon name="chevron-down" className="text-fg-muted" />
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        matchWidth
        role="presentation"
        className="max-h-80 overflow-y-auto"
      >
        <div
          id={listId}
          role="listbox"
          aria-label={aria["aria-label"]}
          onKeyDown={handleMenuKeyDown}
        >
          {options.map((option) => (
            <button
              key={option.value}
              type="button"
              role="option"
              aria-selected={option.value === value}
              aria-disabled={option.disabled || undefined}
              onClick={() => {
                if (option.disabled) return;
                onChange(option.value);
                setOpen(false);
                anchor.current?.focus();
              }}
              className={cn(
                "flex min-h-8 w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-body outline-none",
                "hover:bg-surface-hover focus-visible:bg-surface-hover focus-visible:shadow-none",
                option.disabled && "cursor-not-allowed opacity-50",
              )}
            >
              {option.icon && (
                <span className="flex text-fg-muted">{option.icon}</span>
              )}
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate">{option.label}</span>
                {option.description && (
                  <span className="text-caption text-fg-muted">
                    {option.description}
                  </span>
                )}
              </span>
              {option.value === value && <Icon name="check" />}
            </button>
          ))}
        </div>
      </Popover>
    </>
  );
}
