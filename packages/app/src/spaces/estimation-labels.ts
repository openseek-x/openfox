import type { MessageKey, Translate } from "@openfox/i18n";

import {
  isFixedScale,
  scaleSequence,
  type EstimationOptions,
  type EstimationScale,
} from "./estimation";

/** 刻度名的文案键。 */
export const scaleName = (scale: EstimationScale): MessageKey =>
  `spaces.estimates.scales.${scale}`;

/** 下拉选项文字：「斐波那契（1, 2, 3, 5, 8 点）」「T 恤（XS, S, M, L, XL）」「时间（周 / 天 / 时 / 分）」。 */
export function scaleOptionLabel(
  t: Translate,
  scale: EstimationScale,
  options: EstimationOptions,
): string {
  const name = t(scaleName(scale));
  if (scale === "storyPoints")
    return t("spaces.estimates.optionHint", {
      name,
      hint: t("spaces.estimates.hintStoryPoints"),
    });
  if (scale === "time")
    return t("spaces.estimates.optionHint", {
      name,
      hint: t("spaces.estimates.hintTime"),
    });
  if (!isFixedScale(scale)) return name;
  const points = scaleSequence(scale, options);
  return scale === "tShirt"
    ? t("spaces.estimates.optionSizes", { name, points })
    : t("spaces.estimates.optionPoints", { name, points });
}
