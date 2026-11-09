import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { useT, type MessageKey } from "@openfox/i18n";
import { Icon, cn } from "@openfox/ui";

import { stepPath } from "../me/GetStartedPage";
import { useGetStarted } from "../me/queries";
import { useOrgRole } from "../settings/common";
import { localKeys, readLocal, writeLocal } from "../storage";

// 侧栏底部「进阶指引」就地展开：六个动作 + 进度，数据复用「开始使用」页的同一个查询。

type Requires = "admin" | "member";

interface GuideItem {
  /** 对应「开始使用」接口里的步骤 key，完成状态直接取它的 completed */
  step: string;
  label: MessageKey;
  requires: Requires;
  path: (base: string, spaceId: string | undefined, orgId: string) => string;
}

/** 侧栏的六个进阶动作。跳转尽量用现有入口，没有直达入口的链到对应列表页。 */
const ITEMS: GuideItem[] = [
  {
    step: "connect_jira_or_tapd",
    label: "shell.guide.import",
    requires: "admin",
    path: (_, spaceId, orgId) =>
      stepPath("connect_jira_or_tapd", orgId, spaceId),
  },
  {
    step: "connect_feishu",
    label: "shell.guide.im",
    requires: "admin",
    path: (base) => `${base}/settings/integrations`,
  },
  {
    step: "connect_git",
    label: "shell.guide.git",
    requires: "member",
    path: (base) => `${base}/settings/integrations`,
  },
  {
    step: "create_crew",
    label: "shell.guide.crew",
    requires: "member",
    path: (_, spaceId, orgId) => stepPath("create_crew", orgId, spaceId),
  },
  {
    step: "create_automation",
    label: "shell.guide.automation",
    requires: "member",
    path: (_, spaceId, orgId) => stepPath("create_automation", orgId, spaceId),
  },
  {
    step: "invite_member",
    label: "shell.guide.invite",
    requires: "admin",
    path: (_, spaceId, orgId) => stepPath("invite_member", orgId, spaceId),
  },
];

/** 当前角色是否满足该项的权限要求；角色未加载时不提前禁用。 */
function allowed(role: string | undefined, requires: Requires): boolean {
  if (role === undefined) return true;
  if (requires === "admin") return role === "OWNER" || role === "ADMIN";
  return role !== "GUEST";
}

/** 进阶指引已被「开始使用」页隐藏时不渲染；展开状态只存在本机。 */
export function AdvancedGuide({
  orgId,
  firstSpaceId,
}: {
  orgId: string;
  firstSpaceId: string | undefined;
}) {
  const t = useT();
  const base = `/o/${orgId}`;
  const progress = useGetStarted(orgId);
  const role = useOrgRole(orgId);
  const [open, setOpen] = useState(
    () => readLocal(localKeys.advancedGuideOpen) === "1",
  );
  const list = useRef<HTMLDivElement | null>(null);
  // 侧栏内容可滚动、指引在最底部：展开后把整块列表滚进可视区，否则只露出第一项
  const justOpened = useRef(false);
  useEffect(() => {
    if (!open || !justOpened.current) return;
    justOpened.current = false;
    list.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [open]);
  if (readLocal(localKeys.advancedGuideHidden) === "1") return null;

  const steps = new Map(
    (progress.data?.categories ?? []).flatMap((category) =>
      category.steps.map((step) => [step.key, step] as const),
    ),
  );
  const done = ITEMS.filter((item) => steps.get(item.step)?.completed).length;
  const toggle = () => {
    writeLocal(localKeys.advancedGuideOpen, open ? null : "1");
    justOpened.current = !open;
    setOpen(!open);
  };

  return (
    <div className="flex flex-col">
      <button
        type="button"
        aria-expanded={open}
        aria-controls="advanced-guide-list"
        aria-label={t(open ? "shell.guide.collapse" : "shell.guide.expand")}
        onClick={toggle}
        className="flex h-10 items-center gap-2 rounded-lg px-2.5 text-body font-bold hover:bg-surface-hover"
      >
        <Icon name="graduation-cap" size={18} />
        <span className="flex-1 text-left">{t("shell.advancedGuide")}</span>
        {progress.data && (
          <span
            className="text-caption font-normal text-fg-muted"
            aria-label={t("shell.guide.progress", {
              done,
              total: ITEMS.length,
            })}
          >
            {done}/{ITEMS.length}
          </span>
        )}
        <Icon
          name="chevron-down"
          size={12}
          className={cn(
            "text-fg-muted transition-transform",
            !open && "-rotate-90",
          )}
        />
      </button>
      {open && (
        <div
          ref={list}
          id="advanced-guide-list"
          className="flex flex-col gap-0.5"
        >
          <ul className="flex flex-col gap-0.5">
            {ITEMS.map((item) => {
              const completed = steps.get(item.step)?.completed === true;
              const enabled = allowed(role, item.requires);
              const content = (
                <>
                  <span
                    aria-hidden
                    className={cn(
                      "flex size-4 shrink-0 items-center justify-center rounded-full",
                      completed
                        ? "bg-success text-fg-inverse"
                        : "border border-dashed border-line-strong",
                    )}
                  >
                    {completed && <Icon name="check" size={10} />}
                  </span>
                  <span className="flex min-w-0 flex-1 flex-col text-left">
                    <span
                      className={cn(
                        "text-body-sm",
                        completed && "text-fg-muted line-through",
                      )}
                    >
                      {t(item.label)}
                    </span>
                    {!enabled && (
                      <span className="text-caption text-fg-muted">
                        {t(
                          item.requires === "admin"
                            ? "shell.guide.needAdmin"
                            : "shell.guide.needMember",
                        )}
                      </span>
                    )}
                  </span>
                  {completed && (
                    <span className="sr-only">{t("shell.guide.done")}</span>
                  )}
                </>
              );
              return (
                <li key={item.step}>
                  {enabled ? (
                    <Link
                      to={item.path(base, firstSpaceId, orgId)}
                      className="flex min-h-8 items-center gap-2 rounded-lg px-2.5 py-1 hover:bg-surface-hover"
                    >
                      {content}
                    </Link>
                  ) : (
                    <span
                      role="link"
                      aria-disabled="true"
                      className="flex min-h-8 cursor-not-allowed items-center gap-2 rounded-lg px-2.5 py-1 opacity-60"
                    >
                      {content}
                    </span>
                  )}
                </li>
              );
            })}
          </ul>
          <Link
            to={`${base}/settings/get-started?guide=advanced`}
            className="flex h-8 items-center rounded-lg px-2.5 text-body-sm text-fg-muted hover:bg-surface-hover hover:text-fg-default"
          >
            {t("shell.guide.viewAll")}
          </Link>
        </div>
      )}
    </div>
  );
}
