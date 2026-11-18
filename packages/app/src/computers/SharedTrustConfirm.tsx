import { useEffect, useId, useState } from "react";

import type { DeviceGrantScope } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Callout, Checkbox, ConfirmDialog } from "@openfox/ui";

// 阶段 12e · NF-07：共享电脑前的信任说明。
// 工作目录隔离只是把每次运行放进各自的目录，不是安全沙箱：共享对象触发的运行
// 以本机用户身份执行，可读取该用户可读的文件。扩大共享范围或把电脑加入另一个组织前必须确认。

export type SharedTrustVariant =
  /** 扩大共享范围（个人改为组织或空间等） */
  | "widen"
  /** 把电脑添加到另一个组织 */
  | "addOrganization";

export interface SharedTrustConfirmProps {
  open: boolean;
  /** 标题与说明的变体 */
  variant: SharedTrustVariant;
  /** 勾选「我已了解」后点击确认 */
  onConfirm: () => void;
  onCancel: () => void;
  /** 确认后的写操作进行中：禁用按钮，不允许关闭 */
  loading?: boolean;
}

/**
 * 共享范围变化是否需要信任确认：新范围让原来不能用这台电脑的人可以用时需要。
 * 改回个人、或从组织收窄到某个空间属于缩小范围，不需要。
 */
export function needsSharedTrustConfirm(
  from: DeviceGrantScope,
  to: DeviceGrantScope,
): boolean {
  if (to === "PERSONAL") return false;
  if (from === "ORGANIZATION" && to === "SPACE") return false;
  return true;
}

/** 信任说明确认弹窗：必须勾选「我已了解」才能确认；每次打开都要重新勾选。 */
export function SharedTrustConfirm({
  open,
  variant,
  onConfirm,
  onCancel,
  loading = false,
}: SharedTrustConfirmProps) {
  const t = useT();
  const checkboxId = useId();
  const [acknowledged, setAcknowledged] = useState(false);
  useEffect(() => {
    if (open) setAcknowledged(false);
  }, [open]);
  const addOrganization = variant === "addOrganization";
  return (
    <ConfirmDialog
      open={open}
      title={t(
        addOrganization
          ? "governance.trust.titleAddOrganization"
          : "governance.trust.titleWiden",
      )}
      description={t(
        addOrganization
          ? "governance.trust.leadAddOrganization"
          : "governance.trust.leadWiden",
      )}
      confirmLabel={t(
        addOrganization
          ? "governance.trust.confirmAddOrganization"
          : "governance.trust.confirm",
      )}
      confirmDisabled={!acknowledged}
      loading={loading}
      onCancel={onCancel}
      onConfirm={() => {
        if (acknowledged) onConfirm();
      }}
    >
      <div className="flex flex-col gap-3">
        <Callout tone="warning" title={t("governance.trust.notSandbox")}>
          {t("governance.trust.runsAsUser")}
        </Callout>
        <p className="text-body-sm text-fg-muted">
          {t("governance.trust.advice")}
        </p>
        <label
          htmlFor={checkboxId}
          className="flex cursor-pointer items-center gap-2 text-body"
        >
          <Checkbox
            id={checkboxId}
            checked={acknowledged}
            disabled={loading}
            onChange={(event) => setAcknowledged(event.target.checked)}
          />
          {t("governance.trust.acknowledge")}
        </label>
      </div>
    </ConfirmDialog>
  );
}
