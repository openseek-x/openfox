import { type RefObject } from "react";

import { useI18n } from "@openfox/i18n";

import { formatDateLabel, quickDates, todayYmd } from "../date";
import { Calendar } from "./Calendar";
import { Icon } from "./Icon";
import { MenuItem, MenuSeparator, handleMenuKeyDown } from "./Menu";
import { Popover, type Placement } from "./Popover";

export interface DateFieldPopoverProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  anchorRef: RefObject<HTMLElement | null>;
  placement?: Placement;
  /** 当前日期 'YYYY-MM-DD'，空表示未设置 */
  value: string | null;
  /** 选择日期，或点「清除日期」时传 null；选择后自动关闭 */
  onChange: (value: string | null) => void;
  /** 「今天」，默认取本机日期；测试时可固定 */
  today?: string;
}

/** 日期选择浮层：快捷日期、月历和清除操作。 */
export function DateFieldPopover({
  open,
  onOpenChange,
  anchorRef,
  placement,
  value,
  onChange,
  today,
}: DateFieldPopoverProps) {
  const { t, locale } = useI18n();
  const todayValue = today ?? todayYmd();
  const quick = quickDates(todayValue);
  const choose = (next: string | null) => {
    onChange(next);
    onOpenChange(false);
  };
  const items: Array<[string, string]> = [
    [t("ui.date.today"), quick.today],
    [t("ui.date.tomorrow"), quick.tomorrow],
    [t("ui.date.thisWeekend"), quick.thisWeekend],
    [t("ui.date.nextWeek"), quick.nextWeek],
    [t("ui.date.nextWeekend"), quick.nextWeekend],
  ];
  return (
    <Popover
      open={open}
      onOpenChange={onOpenChange}
      anchorRef={anchorRef}
      placement={placement}
      aria-label={t("ui.date.pick")}
    >
      <div className="w-[268px]">
        <div
          role="menu"
          aria-label={t("ui.date.quick")}
          onKeyDown={handleMenuKeyDown}
        >
          {items.map(([label, date]) => (
            <MenuItem
              key={label}
              trailing={formatDateLabel(date, locale)}
              onSelect={() => choose(date)}
            >
              {label}
            </MenuItem>
          ))}
        </div>
        <MenuSeparator />
        <Calendar value={value} today={todayValue} onChange={choose} />
        {value && (
          <>
            <MenuSeparator />
            <div role="menu" onKeyDown={handleMenuKeyDown}>
              <MenuItem icon={<Icon name="x" />} onSelect={() => choose(null)}>
                {t("ui.date.clear")}
              </MenuItem>
            </div>
          </>
        )}
      </div>
    </Popover>
  );
}
