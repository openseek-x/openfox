import { Link } from "react-router-dom";

import { useT } from "@openfox/i18n";
import { Callout } from "@openfox/ui";

import { useCanManage } from "../settings/common";
import { useRuntimeUsage } from "../settings/governance/queries";

// 阶段 12e · NF-05：组织队列已满时，在对话输入框上方给一条明确提示。
// 队列满时服务端不报错——消息照常保存并保持等待，直到队列有空位才开始处理；
// 没有这条提示时用户只会看到消息发出后迟迟没有反应。不影响发送。

/**
 * 用量接口失败或还没返回时不显示（提示只是辅助信息）。
 * 管理员看到「调整资源配额」入口，其他成员看到「联系管理员」。
 */
export function QueueFullNotice({ orgId }: { orgId: string }) {
  const t = useT();
  const usage = useRuntimeUsage(orgId);
  const canManage = useCanManage(orgId);
  if (!usage.data?.queue_full) return null;
  return (
    <Callout
      tone="warning"
      title={t("governance.queueFull.title")}
      className="mb-3"
      action={
        canManage ? (
          <Link
            to={`/o/${orgId}/settings/quotas`}
            className="shrink-0 underline underline-offset-4"
          >
            {t("governance.queueFull.adjust")}
          </Link>
        ) : undefined
      }
    >
      <span>
        {t("governance.queueFull.desc")}
        {/* 具体上限只返回给所有者与管理员 */}
        {canManage && usage.data.queue_max !== undefined && (
          <>
            {" "}
            {t("governance.queueFull.usage", {
              queued: usage.data.queued,
              max: usage.data.queue_max,
            })}
          </>
        )}
        {!canManage && <> {t("governance.queueFull.contactAdmin")}</>}
      </span>
    </Callout>
  );
}
