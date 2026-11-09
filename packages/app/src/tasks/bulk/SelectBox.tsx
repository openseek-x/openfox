import { useRef } from "react";

import { Checkbox } from "@openfox/ui";

/**
 * 任务行的选择框：onChange 时带上是否按住 Shift（连选）。
 * 复选框的 change 由 click 触发，先在 click 里记下 shiftKey 再交给 onToggle。
 */
export function SelectBox({
  label,
  checked,
  onToggle,
  className,
}: {
  label: string;
  checked: boolean;
  onToggle: (on: boolean, range: boolean) => void;
  className?: string;
}) {
  const shift = useRef(false);
  return (
    <Checkbox
      aria-label={label}
      checked={checked}
      className={className}
      onClick={(event) => {
        shift.current = event.shiftKey;
        // 不触发行上的打开任务
        event.stopPropagation();
      }}
      onChange={(event) => {
        onToggle(event.currentTarget.checked, shift.current);
        shift.current = false;
      }}
    />
  );
}
