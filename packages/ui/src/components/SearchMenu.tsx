import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from "react";

import { useT } from "@openfox/i18n";

import { cn } from "../cn";
import { Icon } from "./Icon";
import { Popover, type Placement } from "./Popover";

export interface SearchMenuOption {
  value: string;
  label: string;
  icon?: ReactNode;
  description?: ReactNode;
  trailing?: ReactNode;
  /** 额外参与搜索的关键字 */
  keywords?: string;
  disabled?: boolean;
  count?: number;
}

export interface SearchMenuGroup {
  id?: string;
  label?: string;
  options: SearchMenuOption[];
}

export interface SearchMenuProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  anchorRef: RefObject<HTMLElement | null>;
  placement?: Placement;
  groups: SearchMenuGroup[];
  /** 当前选中的值 */
  selected: string[];
  /** 点击某项；多选时调用方负责切换，菜单保持打开，单选时选中后自动关闭 */
  onSelect: (value: string) => void;
  multiple?: boolean;
  /** 可同时标记多类当前值，但点选一项后关闭（如人员 + Agent 指派）。 */
  closeOnSelect?: boolean;
  placeholder?: string;
  emptyText?: string;
  moreOptions?: { label: string; onClick: () => void };
  /** 无障碍名称 */
  label: string;
  /** 底部操作项，如「创建标签」；参数为当前搜索词 */
  footer?: {
    /** 函数形式可按搜索词生成文案，如「创建标签 "xxx"」 */
    label: string | ((query: string) => string);
    /** 为 true 时搜索词为空不显示 */
    requiresQuery?: boolean;
    icon?: ReactNode;
    onSelect: (query: string) => void;
  };
  className?: string;
}

/**
 * 带搜索框的选择菜单：分组、单选 / 多选、勾选标记、
 * 空态与底部操作。焦点留在搜索框，用 aria-activedescendant 表示当前项。
 */
export function SearchMenu({
  open,
  onOpenChange,
  anchorRef,
  placement,
  groups,
  selected,
  onSelect,
  multiple = false,
  closeOnSelect = !multiple,
  placeholder,
  emptyText,
  moreOptions,
  label,
  footer,
  className,
}: SearchMenuProps) {
  const t = useT();
  const baseId = useId();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    setQuery("");
    setActive(0);
    // Popover 首帧不可见，浏览器会忽略 autoFocus；定位显示后再聚焦。
    const frame = requestAnimationFrame(() => inputRef.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [open]);

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return groups
      .map((group) => ({
        ...group,
        options: group.options.filter(
          (option) =>
            !needle ||
            `${option.label} ${option.keywords ?? ""}`
              .toLowerCase()
              .includes(needle),
        ),
      }))
      .filter((group) => group.options.length > 0);
  }, [groups, query]);

  const flat = visible.flatMap((group) => group.options);
  const current = Math.min(active, Math.max(flat.length - 1, 0));

  const choose = (option: SearchMenuOption | undefined) => {
    if (!option || option.disabled) return;
    onSelect(option.value);
    if (closeOnSelect) onOpenChange(false);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (flat.length === 0 && event.key !== "Enter") return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setActive((current + 1) % flat.length);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setActive((current - 1 + flat.length) % flat.length);
    } else if (event.key === "Enter") {
      event.preventDefault();
      choose(flat[current]);
    }
  };

  const optionId = (value: string) => `${baseId}-${value}`;
  let index = -1;

  return (
    <Popover
      open={open}
      onOpenChange={onOpenChange}
      anchorRef={anchorRef}
      placement={placement}
      aria-label={label}
      className={cn("w-[260px] p-0", className)}
    >
      <div className="flex items-center gap-2 border-b border-line-default px-3 py-2">
        <Icon name="search" className="text-fg-muted" />
        <input
          ref={inputRef}
          autoFocus
          role="combobox"
          aria-expanded
          aria-controls={`${baseId}-list`}
          aria-activedescendant={
            flat[current] ? optionId(flat[current]!.value) : undefined
          }
          aria-label={placeholder ?? t("ui.searchMenu.placeholder")}
          placeholder={placeholder ?? t("ui.searchMenu.placeholder")}
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setActive(0);
          }}
          onKeyDown={onKeyDown}
          className="min-w-0 flex-1 bg-transparent text-body outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
        />
      </div>
      <div
        id={`${baseId}-list`}
        role="listbox"
        aria-label={label}
        aria-multiselectable={multiple || undefined}
        className="max-h-[320px] overflow-y-auto p-1"
      >
        {flat.length === 0 && (
          <div className="px-2.5 py-3 text-center text-body-sm text-fg-muted">
            {emptyText ?? t("ui.searchMenu.empty")}
          </div>
        )}
        {visible.map((group, g) => (
          <div
            key={group.id ?? group.label ?? g}
            role="group"
            aria-label={group.label}
          >
            {group.label && (
              <div className="px-2.5 pt-2 pb-1 text-caption text-fg-muted">
                {group.label}
              </div>
            )}
            {group.options.map((option) => {
              index += 1;
              const isActive = index === current;
              const isSelected = selected.includes(option.value);
              return (
                <div
                  key={option.value}
                  id={optionId(option.value)}
                  role="option"
                  aria-selected={isSelected}
                  aria-disabled={option.disabled || undefined}
                  // 鼠标按下不抢走搜索框的焦点
                  onMouseDown={(event) => event.preventDefault()}
                  onMouseMove={() => setActive(flat.indexOf(option))}
                  onClick={() => choose(option)}
                  className={cn(
                    "flex min-h-[34px] cursor-pointer items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-body",
                    isActive && "bg-surface-active",
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
                  {option.trailing !== undefined && (
                    <span className="shrink-0 text-[11px] text-fg-muted">
                      {option.trailing}
                    </span>
                  )}
                  {option.count !== undefined && (
                    <span className="text-caption tabular-nums text-fg-muted">
                      {option.count}
                    </span>
                  )}
                  {isSelected && (
                    <Icon name="check" className="text-fg-default" />
                  )}
                </div>
              );
            })}
          </div>
        ))}
      </div>
      {moreOptions && (
        <button
          type="button"
          className="w-full border-t border-line-soft px-3 py-2 text-left text-caption text-fg-muted hover:bg-surface-hover"
          onClick={moreOptions.onClick}
        >
          {moreOptions.label}
        </button>
      )}
      {footer && (!footer.requiresQuery || query.trim() !== "") && (
        <div className="border-t border-line-default p-1">
          <button
            type="button"
            onClick={() => {
              footer.onSelect(query.trim());
              onOpenChange(false);
            }}
            className="flex min-h-[34px] w-full items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-left text-body hover:bg-surface-active"
          >
            {footer.icon && (
              <span className="flex text-fg-muted">{footer.icon}</span>
            )}
            {typeof footer.label === "function"
              ? footer.label(query.trim())
              : footer.label}
          </button>
        </div>
      )}
    </Popover>
  );
}
