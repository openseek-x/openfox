import type { Device } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge } from "@openfox/ui";

/**
 * 阶段 12e · NF-01：这台电脑上有待核对的运行时显示「核对中」。
 * 数量来自电脑列表与详情接口的只读派生字段 reconciling_runs；旧版服务端不返回该字段，视为 0。
 */
export function ReconcilingBadge({
  device,
}: {
  device: Pick<Device, "reconciling_runs">;
}) {
  const t = useT();
  const count = device.reconciling_runs ?? 0;
  if (count <= 0) return null;
  return (
    <span title={t("governance.computer.reconcilingHint", { count })}>
      <Badge tone="warning">{t("governance.computer.reconciling")}</Badge>
    </span>
  );
}
