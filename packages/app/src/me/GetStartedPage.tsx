import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";

import type {
  GetStartedCategory,
  GetStartedProgress,
  GetStartedStep,
  Organization,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Badge,
  Button,
  Icon,
  Menu,
  MenuItem,
  cn,
  type IconName,
} from "@openfox/ui";

import { ConnectComputerDialog } from "../computers/ConnectComputer";
import { keys, useApi } from "../context";
import { OrgIcon } from "../organization/OrgIcon";
import { joinedSpaces } from "../spaces/queries";
import { SettingsColumn } from "../spaces/settings/parts";
import { readLocal, writeLocal, localKeys } from "../storage";
import { useGetStarted } from "./queries";

// 开始使用：左栏进度环与组织信息，右栏六个分类的设置向导。

type CategoryKey = GetStartedCategory["key"];

const CATEGORY_ICON: Record<CategoryKey, IconName> = {
  get_started: "rocket",
  collaboration: "users",
  agents: "bot",
  tasks: "square-check",
  space_management: "folder-kanban",
  automations: "zap",
};

/**
 * 默认展开第一个分类；?guide=first 只展开「开始使用」；
 * ?guide=advanced 展开所有还有未完成可用步骤的分类。
 */
export function initialExpanded(
  categories: readonly GetStartedCategory[],
  guide: string | null,
): Set<CategoryKey> {
  if (guide === "first") return new Set<CategoryKey>(["get_started"]);
  if (guide === "advanced")
    return new Set(
      categories
        .filter((category) =>
          category.steps.some((step) => step.available && !step.completed),
        )
        .map((category) => category.key),
    );
  const first = categories[0];
  return new Set<CategoryKey>(first ? [first.key] : []);
}

/** 步骤的 CTA 去向：找不到对应页面的跳设置首页；空间相关的步骤没有空间时跳空间设置列表。 */
export function stepPath(
  key: string,
  orgId: string,
  spaceId: string | undefined,
): string {
  const base = `/o/${orgId}`;
  const spaceTasks = spaceId
    ? `${base}/spaces/${spaceId}/tasks`
    : `${base}/my-tasks`;
  const spaceRoute = (path: string) =>
    spaceId ? `${base}/spaces/${spaceId}/${path}` : `${base}/settings/spaces`;
  const spaceSettings = (section: string) =>
    spaceId
      ? `${base}/settings/spaces/${spaceId}/${section}`
      : `${base}/settings/spaces`;
  switch (key) {
    case "create_agent":
      return `${base}/agents`;
    case "create_task":
    case "follow_task":
    case "create_subtask":
    case "link_related_task":
      return spaceTasks;
    case "delegate_task_to_agent":
    case "assign_task_person":
    case "comment_or_reply":
      return `${base}/my-tasks`;
    case "connect_jira_or_tapd":
      return `${base}/settings/import-export`;
    case "connect_feishu":
      return `${base}/settings/integrations/feishu`;
    case "create_automation":
    case "run_automation":
    case "create_scheduled_automation":
    case "create_task_status_automation":
    case "create_comment_automation":
    case "create_webhook_automation":
      return spaceRoute("automations");
    case "invite_member":
      return `${base}/settings/members`;
    case "process_inbox":
    case "clear_inbox":
      return `${base}/inbox`;
    case "configure_agent_instructions":
    case "attach_skill_to_agent":
      return `${base}/settings/agents`;
    case "chat_with_agent":
      return `${base}/chat`;
    case "share_agent_to_space":
      return `${base}/settings/spaces`;
    case "create_crew":
      return `${base}/settings/crews`;
    case "add_label":
      return `${base}/settings/labels`;
    case "create_view":
      return spaceRoute("views/new");
    case "save_org_view":
      return spaceRoute("views");
    case "create_project":
    case "link_task_to_project":
      return spaceRoute("projects");
    case "configure_task_type_field":
      return spaceSettings("task-types");
    case "manage_task_status":
      return `${base}/settings/statuses`;
    case "create_sprint":
      return spaceSettings("iterations");
    case "enable_space_estimation":
      return spaceSettings("estimates");
    case "connect_git":
      return spaceSettings("repos");
    default:
      // install_skill 目前没有独立页面
      return `${base}/settings`;
  }
}

/** 设置导航「开始使用」右侧的「已完成 / 可用数」。 */
export function GetStartedNavCount({ orgId }: { orgId: string }) {
  const progress = useGetStarted(orgId);
  const category = progress.data?.categories.find(
    (item) => item.key === "get_started",
  );
  if (!category) return null;
  const available = category.steps.filter((step) => step.available);
  const done = available.filter((step) => step.completed).length;
  return (
    <span className="shrink-0 text-caption text-fg-muted">
      {done}/{available.length}
    </span>
  );
}

export function GetStartedPage() {
  const t = useT();
  const api = useApi();
  const { orgId = "" } = useParams();
  const [params] = useSearchParams();
  const progress = useGetStarted(orgId);
  const organizations = useQuery({
    queryKey: keys.organizations,
    queryFn: api.me.organizations,
  });
  if (progress.isError)
    return (
      <SettingsColumn wide>
        <p className="text-body text-fg-muted">
          {t("me.getStarted.loadFailed")}
        </p>
      </SettingsColumn>
    );
  if (!progress.data) return null;
  const org = organizations.data?.find((item) => item.id === orgId);
  return (
    <GetStartedBody
      orgId={orgId}
      org={org}
      progress={progress.data}
      guide={params.get("guide")}
    />
  );
}

function GetStartedBody({
  orgId,
  org,
  progress,
  guide,
}: {
  orgId: string;
  org: Organization | undefined;
  progress: GetStartedProgress;
  guide: string | null;
}) {
  const t = useT();
  const api = useApi();
  const navigate = useNavigate();
  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
  const firstSpace = joinedSpaces(spaces.data)[0];
  const [expanded, setExpanded] = useState(() =>
    initialExpanded(progress.categories, guide),
  );
  const [openStep, setOpenStep] = useState<string | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [hidden, setHidden] = useState(
    () => readLocal(localKeys.advancedGuideHidden) === "1",
  );
  const allDone = progress.total > 0 && progress.score === progress.total;

  const toggleGuide = () => {
    writeLocal(localKeys.advancedGuideHidden, hidden ? null : "1");
    setHidden(!hidden);
  };

  const act = (step: GetStartedStep) => {
    if (step.key === "connect_machine") setConnecting(true);
    else navigate(stepPath(step.key, orgId, firstSpace?.id));
  };

  return (
    <SettingsColumn wide>
      <div className="flex items-start gap-8">
        <aside className="sticky top-12 flex w-[340px] shrink-0 flex-col items-center gap-4 self-start">
          <ProgressRing score={progress.score} total={progress.total} />
          {/* OrgIcon 没有 48 的尺寸，取最接近的 40 */}
          {org && <OrgIcon org={org} size={40} />}
          <div className="flex flex-col items-center gap-2 text-center">
            <h1 className="text-[24px] leading-8 font-bold text-fg-default">
              {allDone ? t("me.getStarted.allDone") : t("me.getStarted.title")}
            </h1>
            <p className="text-body text-fg-muted">
              {allDone
                ? t("me.getStarted.allDoneSubtitle")
                : t("me.getStarted.subtitle")}
            </p>
          </div>
        </aside>

        <div className="flex min-w-0 flex-1 flex-col gap-6">
          <header className="flex items-start gap-4">
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <h2 className="text-[20px] leading-7 font-medium text-fg-default">
                {t("me.getStarted.guideTitle")}
              </h2>
              <p className="text-body text-fg-muted">
                {t("me.getStarted.guideDescription")}
              </p>
            </div>
            <Menu
              label={t("me.getStarted.guideTitle")}
              placement="bottom-end"
              trigger={({ ref, toggle, open }) => (
                <button
                  ref={ref}
                  type="button"
                  aria-label={t("me.getStarted.guideTitle")}
                  aria-expanded={open}
                  onClick={toggle}
                  className="flex size-7 shrink-0 items-center justify-center rounded-md text-fg-secondary hover:bg-surface-muted"
                >
                  <Icon name="ellipsis" />
                </button>
              )}
            >
              <MenuItem onSelect={toggleGuide}>
                {t(
                  hidden
                    ? "me.getStarted.showGuide"
                    : "me.getStarted.hideGuide",
                )}
              </MenuItem>
            </Menu>
          </header>

          {progress.categories.map((category) => (
            <CategoryCard
              key={category.key}
              category={category}
              open={expanded.has(category.key)}
              onToggle={() =>
                setExpanded((current) => {
                  const next = new Set(current);
                  if (next.has(category.key)) next.delete(category.key);
                  else next.add(category.key);
                  return next;
                })
              }
              openStep={openStep}
              onStepToggle={(key) =>
                setOpenStep((current) => (current === key ? null : key))
              }
              onAct={act}
            />
          ))}
        </div>
      </div>
      <ConnectComputerDialog
        orgId={orgId}
        open={connecting}
        onClose={() => setConnecting(false)}
      />
    </SettingsColumn>
  );
}

/** 左栏 200 的圆环进度（描边 12）。 */
function ProgressRing({ score, total }: { score: number; total: number }) {
  const t = useT();
  const size = 200;
  const stroke = 12;
  const radius = (size - stroke) / 2;
  const length = 2 * Math.PI * radius;
  const ratio = total > 0 ? Math.min(1, score / total) : 0;
  return (
    <div className="relative flex size-[200px] items-center justify-center">
      <svg
        width={size}
        height={size}
        viewBox={`0 0 ${size} ${size}`}
        className="-rotate-90"
        aria-hidden
      >
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          strokeWidth={stroke}
          className="stroke-surface-muted"
        />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          strokeWidth={stroke}
          strokeLinecap="round"
          strokeDasharray={length}
          strokeDashoffset={length * (1 - ratio)}
          className="stroke-success transition-[stroke-dashoffset]"
        />
      </svg>
      <div
        className="absolute flex flex-col items-center"
        role="img"
        aria-label={`${t("me.getStarted.scored")} ${score} / ${total}`}
      >
        <span className="text-caption text-fg-muted">
          {t("me.getStarted.scored")}
        </span>
        <span className="text-[32px] leading-10 font-bold text-fg-default">
          {score}
          <span className="text-body text-fg-muted"> / {total}</span>
        </span>
      </div>
    </div>
  );
}

/** 18 的小环形进度，用于分类头。 */
function MiniRing({ done, total }: { done: number; total: number }) {
  const size = 18;
  const stroke = 2.5;
  const radius = (size - stroke) / 2;
  const length = 2 * Math.PI * radius;
  const ratio = total > 0 ? Math.min(1, done / total) : 0;
  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${size} ${size}`}
      className="-rotate-90 shrink-0"
      aria-hidden
    >
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        strokeWidth={stroke}
        className="stroke-surface-muted"
      />
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        strokeWidth={stroke}
        strokeLinecap="round"
        strokeDasharray={length}
        strokeDashoffset={length * (1 - ratio)}
        className="stroke-success"
      />
    </svg>
  );
}

function CategoryCard({
  category,
  open,
  onToggle,
  openStep,
  onStepToggle,
  onAct,
}: {
  category: GetStartedCategory;
  open: boolean;
  onToggle: () => void;
  openStep: string | null;
  onStepToggle: (key: string) => void;
  onAct: (step: GetStartedStep) => void;
}) {
  const t = useT();
  const available = category.steps.filter((step) => step.available);
  const done = available.filter((step) => step.completed).length;
  return (
    <section className="flex flex-col rounded-xl border border-line-default bg-surface-card">
      <button
        type="button"
        aria-expanded={open}
        onClick={onToggle}
        className="flex w-full items-center gap-3 rounded-xl p-4 text-left hover:bg-surface-hover"
      >
        <Icon
          name={CATEGORY_ICON[category.key]}
          className="shrink-0 text-fg-secondary"
        />
        <span className="min-w-0 flex-1 text-body font-medium text-fg-default">
          {t(`me.getStarted.categories.${category.key}` as MessageKey)}
        </span>
        {/* 整个分类都依赖未上线功能时不显示进度 */}
        {available.length === 0 ? (
          <Badge tone="muted" variant="outline">
            {t("me.getStarted.soon")}
          </Badge>
        ) : (
          <>
            <span className="text-caption text-fg-muted">
              {done} / {available.length}
            </span>
            <MiniRing done={done} total={available.length} />
          </>
        )}
        <Icon
          name="chevron-down"
          className={cn(
            "shrink-0 text-fg-muted transition-transform",
            !open && "-rotate-90",
          )}
        />
      </button>
      {open && (
        <ul className="flex flex-col gap-0.5 border-t border-line-soft px-2 py-2">
          {category.steps.map((step) => (
            <StepRow
              key={step.key}
              step={step}
              expanded={openStep === step.key}
              onToggle={() => onStepToggle(step.key)}
              onAct={() => onAct(step)}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

function StepRow({
  step,
  expanded,
  onToggle,
  onAct,
}: {
  step: GetStartedStep;
  expanded: boolean;
  onToggle: () => void;
  onAct: () => void;
}) {
  const t = useT();
  const title = t(`me.getStarted.steps.${step.key}.title` as MessageKey);
  const description = t(
    `me.getStarted.steps.${step.key}.description` as MessageKey,
  );
  const cta = t(`me.getStarted.steps.${step.key}.cta` as MessageKey);
  return (
    <li className="flex flex-col">
      <button
        type="button"
        aria-expanded={expanded}
        onClick={onToggle}
        className="flex w-full items-center gap-3 rounded-lg px-2 py-2 text-left hover:bg-surface-hover"
      >
        <span
          aria-hidden
          className={cn(
            "flex size-5 shrink-0 items-center justify-center rounded-full",
            step.completed
              ? "bg-success text-fg-inverse"
              : "border border-dashed border-line-strong",
          )}
        >
          {step.completed && <Icon name="check" size={12} />}
        </span>
        <span
          className={cn(
            "min-w-0 flex-1 text-body",
            step.completed && "text-fg-muted line-through",
            !step.available && "text-fg-subtle",
            step.available && !step.completed && "text-fg-default",
          )}
        >
          {title}
        </span>
        {!step.available && (
          <Badge tone="muted" variant="outline">
            {t("me.getStarted.soon")}
          </Badge>
        )}
        {step.available && (
          <span className="flex shrink-0 items-center gap-1 text-caption text-fg-muted">
            {t("me.getStarted.points", { points: step.points })}
            <Icon name="diamond" size={12} />
          </span>
        )}
      </button>
      {expanded && (
        <div className="ml-8 flex flex-col gap-3 pt-1 pr-2 pb-3">
          <Description text={description} />
          {step.available && !step.completed && (
            <div>
              <Button variant="primary" size="28" onClick={onAct}>
                {cta}
              </Button>
            </div>
          )}
        </div>
      )}
    </li>
  );
}

/** 步骤说明支持 \n 换行：多行时渲染成列表。 */
function Description({ text }: { text: string }) {
  const lines = text.split("\n");
  if (lines.length === 1)
    return <p className="text-body-sm text-fg-muted">{text}</p>;
  return (
    <ul className="flex list-disc flex-col gap-1 pl-4 text-body-sm text-fg-muted">
      {lines.map((line, index) => (
        <li key={index}>{line}</li>
      ))}
    </ul>
  );
}
