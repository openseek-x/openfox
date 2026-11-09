import { createContext, useContext, useEffect, useState } from "react";

import type { SpaceEstimation } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, Select, TextInput } from "@openfox/ui";

import {
  estimationEnabled,
  formatDuration,
  formatEstimate,
  isValidEstimate,
  parseDuration,
  scalePoints,
} from "./estimation";
import { useSpaceDetail, useSpaceList } from "./queries";

// 任务估时在任务模块里的显示与输入。空间未启用估时时这些组件都不渲染。

/** 当前任务视图所在空间的估时设置；我的任务（跨空间）为 null。 */
export const EstimationContext = createContext<SpaceEstimation | null>(null);

export const useEstimation = () => useContext(EstimationContext);

/** 空间的估时设置：优先取空间列表缓存，列表里没有（已停用空间）时取详情。未启用时返回 null。 */
export function useSpaceEstimation(
  orgId: string,
  spaceId: string | undefined,
): SpaceEstimation | null {
  const list = useSpaceList(orgId);
  const fromList = list.data?.find((space) => space.id === spaceId);
  const detail = useSpaceDetail(
    orgId,
    list.data && !fromList ? spaceId : undefined,
  );
  const estimation = (fromList ?? detail.data)?.estimation;
  return estimation && estimationEnabled(estimation.scale) ? estimation : null;
}

/** 卡片 / 列表行上的估时标记；没有估时值时不显示。 */
export function EstimateChip({ value }: { value: number | null | undefined }) {
  const t = useT();
  const estimation = useEstimation();
  if (!estimation || value === null || value === undefined) return null;
  const text = formatEstimate(value, estimation);
  return (
    <span
      title={t("spaces.task.estimate")}
      className="inline-flex h-5 items-center gap-1 rounded-md bg-surface-muted px-1.5 text-caption text-fg-secondary"
    >
      <Icon name="clock" size={12} />
      {text}
    </span>
  );
}

const NONE = "__none__";

/**
 * 估时输入：固定档位刻度为下拉；故事点为数字输入；时间刻度输入「2w 3d 4h 30m」。
 * onChange 收到 null 表示清空；非法输入不回调，并显示提示。
 */
export function EstimateInput({
  estimation,
  value,
  onChange,
  disabled = false,
  className,
}: {
  estimation: SpaceEstimation;
  value: number | null | undefined;
  onChange: (value: number | null) => void;
  disabled?: boolean;
  className?: string;
}) {
  const t = useT();
  const current = value ?? null;
  const points = scalePoints(estimation.scale, {
    extended: estimation.extended,
    allowZero: estimation.allow_zero,
  });
  const display =
    current === null
      ? ""
      : estimation.scale === "time"
        ? formatDuration(current)
        : String(current);
  const [draft, setDraft] = useState(display);
  const [invalid, setInvalid] = useState(false);
  useEffect(() => {
    setDraft(display);
    setInvalid(false);
  }, [display]);

  if (points.length > 0)
    return (
      <Select<string>
        aria-label={t("spaces.task.estimate")}
        value={current === null ? NONE : String(current)}
        disabled={disabled}
        className={className}
        options={[
          { value: NONE, label: t("spaces.task.estimateNone") },
          ...points.map((point) => ({
            value: String(point.value),
            label: point.label,
          })),
        ]}
        onChange={(next) => onChange(next === NONE ? null : Number(next))}
      />
    );

  const commit = () => {
    const text = draft.trim();
    if (!text) {
      setInvalid(false);
      if (current !== null) onChange(null);
      return;
    }
    const parsed =
      estimation.scale === "time" ? parseDuration(text) : Number(text);
    if (parsed === null || !isValidEstimate(parsed, estimation)) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    if (parsed !== current) onChange(parsed);
  };
  return (
    <div className="flex flex-col gap-0.5">
      <TextInput
        aria-label={t("spaces.task.estimate")}
        value={draft}
        disabled={disabled}
        invalid={invalid}
        inputMode={estimation.scale === "time" ? "text" : "decimal"}
        placeholder={
          estimation.scale === "time"
            ? t("spaces.task.estimatePlaceholder")
            : t("spaces.task.estimateNone")
        }
        className={className}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            commit();
          }
        }}
      />
      {invalid && (
        <span role="alert" className="text-caption text-danger">
          {t("spaces.task.estimateInvalid")}
        </span>
      )}
    </div>
  );
}
