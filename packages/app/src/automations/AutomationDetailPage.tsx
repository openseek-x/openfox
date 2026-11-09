import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import {
  isApiError,
  type Automation,
  type AutomationTrigger,
} from "@openfox/api";
import { useI18n, useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  Callout,
  ConfirmDialog,
  Icon,
  Menu,
  MenuItem,
  Spinner,
  Switch,
  Tooltip,
  cn,
  useToast,
} from "@openfox/ui";

import { canWriteSpace, useSpaceDetail } from "../spaces/queries";
import { TaskState } from "../tasks/TaskWorkspace";
import { useOrgPeople, personName, useStatuses } from "../tasks/queries";
import { AutomationEditorDialog } from "./AutomationEditorDialog";
import { DeliveryDetailDialog } from "./DeliveryDetailDialog";
import { RunDetailDrawer } from "./RunDetailDrawer";
import {
  DeliveryStatusIcon,
  RunStatusIcon,
  deliveryStatusLabel,
  formatDateTime,
  runStatusLabel,
  triggerIcon,
  triggerReason,
  triggerSummary,
  type SummaryStatus,
} from "./parts";
import {
  automationErrorMessage,
  useAutomation,
  useAutomationRuns,
  useDeleteAutomation,
  useRunAutomation,
  useToggleAutomation,
  useToggleTrigger,
  useWebhookDeliveries,
} from "./queries";
import { draftFromTrigger, triggerInput } from "./model";
import { browserTimezone } from "./ScheduleEditor";

// 自动化详情：头部、提示词折叠、触发器、运行记录、Webhook 投递。

/** 提示词超过 400px 折叠，底部渐变 +「展开更多」。 */
const PROMPT_COLLAPSED_PX = 400;

export function AutomationDetailPage() {
  const t = useT();
  const { locale } = useI18n();
  const toast = useToast();
  const navigate = useNavigate();
  const { orgId = "", spaceId = "", automationId = "" } = useParams();
  const space = useSpaceDetail(orgId, spaceId);
  const detail = useAutomation(orgId, spaceId, automationId);
  const runs = useAutomationRuns(orgId, spaceId, automationId);
  const toggle = useToggleAutomation(orgId, spaceId);
  const toggleTrigger = useToggleTrigger(orgId, spaceId);
  const remove = useDeleteAutomation(orgId, spaceId);
  const run = useRunAutomation(orgId, spaceId);
  // 触发器摘要里的状态名（与编辑弹窗同一个查询）
  const statuses = useStatuses(orgId);
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [runId, setRunId] = useState<string | null>(null);
  const [deliveryId, setDeliveryId] = useState<string | null>(null);
  const back = `/o/${orgId}/spaces/${spaceId}/automations`;

  if (space.isError || detail.isError) {
    const notFound = isApiError(detail.error) && detail.error.status === 404;
    return (
      <TaskState
        title={
          notFound ? t("automations.notFound") : t("automations.loadFailed")
        }
        actions={
          <Link to={back} className="text-body text-accent hover:underline">
            {t("automations.detail.back")}
          </Link>
        }
      />
    );
  }
  if (!space.data || !detail.data) {
    return (
      <TaskState busy icon={<Spinner />} title={t("automations.loading")} />
    );
  }

  const automation = detail.data;
  const canWrite = canWriteSpace(space.data);
  const running = automation.last_run?.status === "running" || run.isPending;
  // 旧服务端在没有记录时会返回 null，按空数组处理
  const runItems = runs.data?.pages.flatMap((page) => page.runs ?? []) ?? [];
  const hasWebhook = automation.triggers.some(
    (item) => item.kind === "webhook",
  );

  const runNow = () =>
    run.mutate(automation.id, {
      onSuccess: (result) =>
        toast(
          result.status === "skipped"
            ? t("automations.runSkipped")
            : t("automations.runStarted"),
        ),
      onError: (error) =>
        toast(automationErrorMessage(t, error, "automations.runFailed"), {
          tone: "error",
        }),
    });
  // 停用（暂停）不再禁用立即运行，只运行这一次，不恢复规则
  const runButton = (
    <Button
      size="32"
      disabled={!canWrite || running}
      leading={<Icon name="play" />}
      onClick={runNow}
    >
      {running ? t("automations.running") : t("automations.runNow")}
    </Button>
  );

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-12 shrink-0 items-center gap-3 border-b border-line-default px-4">
        <button
          type="button"
          onClick={() => navigate(back)}
          className="flex items-center gap-1 text-body text-fg-muted hover:text-fg-default"
        >
          <Icon name="arrow-left" size={14} />
          {t("automations.detail.back")}
        </button>
        <span className="min-w-0 flex-1 truncate text-fg-muted">
          / {automation.title}
        </span>
        <Switch
          checked={automation.enabled}
          disabled={!canWrite || toggle.isPending}
          aria-label={automation.title}
          onCheckedChange={(enabled) => toggle.mutate({ automation, enabled })}
        />
        {canWrite && (
          <Menu
            label={`${automation.title} ${t("automations.more")}`}
            trigger={({ ref, toggle: openMenu }) => (
              <button
                ref={ref}
                type="button"
                aria-label={`${automation.title} ${t("automations.more")}`}
                onClick={openMenu}
                className="flex size-7 items-center justify-center rounded-md text-fg-muted hover:bg-surface-muted"
              >
                <Icon name="ellipsis" />
              </button>
            )}
          >
            <MenuItem
              danger
              icon={<Icon name="trash-2" />}
              onSelect={() => setDeleting(true)}
            >
              {t("automations.delete")}
            </MenuItem>
          </Menu>
        )}
      </header>

      <div className="min-h-0 flex-1 overflow-auto p-6">
        {/* 主体最大宽 1200；≥900 时右侧为基础信息（240–280） */}
        <div className="mx-auto grid max-w-[1200px] gap-6 min-[900px]:grid-cols-[minmax(0,1fr)_260px]">
          <div className="flex min-w-0 flex-col gap-6">
            <div className="flex flex-col gap-3">
              <div className="flex flex-wrap items-center gap-3">
                <h1 className="min-w-[min(100%,240px)] flex-1 break-words text-[24px] font-bold">
                  {automation.title}
                </h1>
                {canWrite && (
                  <Button
                    variant="secondary"
                    size="32"
                    leading={<Icon name="pencil" />}
                    onClick={() => setEditing(true)}
                  >
                    {t("automations.edit")}
                  </Button>
                )}
                {/* 停用的规则也能手动运行一次，悬停说明只运行这一次 */}
                {automation.enabled ? (
                  runButton
                ) : (
                  <Tooltip content={t("automations.runOnceWhilePaused")}>
                    {runButton}
                  </Tooltip>
                )}
              </div>
              <PromptBlock prompt={automation.prompt} />
            </div>

            {automation.last_run?.status === "failed" && (
              <Callout
                tone="danger"
                title={t("automations.detail.lastRunFailed")}
              >
                {automation.last_run.reason}
                {automation.last_run.finished_at &&
                  ` · ${formatDateTime(locale, automation.last_run.finished_at)}`}
              </Callout>
            )}

            <section className="flex flex-col gap-2">
              <h2 className="text-body font-medium">
                {t("automations.detail.triggers")}
              </h2>
              {automation.triggers.map((trigger) => (
                <TriggerRow
                  key={trigger.id}
                  trigger={trigger}
                  statuses={statuses.data}
                  canWrite={canWrite}
                  disabled={toggleTrigger.isPending}
                  onToggle={(enabled) =>
                    toggleTrigger.mutate({
                      automation,
                      triggers: automation.triggers.map((item) => {
                        const input = triggerInput(
                          draftFromTrigger(item, item.id, browserTimezone()),
                        );
                        return item.id === trigger.id
                          ? { ...input, enabled }
                          : input;
                      }),
                    })
                  }
                />
              ))}
            </section>

            <section className="flex flex-col gap-2">
              <h2 className="text-body font-medium">
                {t("automations.detail.runs")}
              </h2>
              {runs.isLoading ? (
                <Spinner />
              ) : runItems.length === 0 ? (
                <p className="text-body-sm text-fg-muted">
                  {t("automations.detail.noRuns")}
                </p>
              ) : (
                <ul className="flex flex-col divide-y divide-line-default rounded-xl border border-line-default">
                  {runItems.map((item) => (
                    <li key={item.id}>
                      <button
                        type="button"
                        onClick={() => setRunId(item.id)}
                        className="flex w-full items-center gap-3 px-4 py-2.5 text-left text-body hover:bg-surface-hover"
                      >
                        <RunStatusIcon status={item.status} />
                        <span className="shrink-0 text-body-sm font-medium">
                          {runStatusLabel(t, item.status)}
                        </span>
                        <span className="min-w-0 flex-1 truncate text-body-sm text-fg-secondary">
                          {item.reason ||
                            item.trigger_title ||
                            triggerReason(t, item.trigger_kind)}
                        </span>
                        <span className="shrink-0 font-mono text-caption text-fg-muted">
                          {formatDateTime(locale, item.created_at)}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
              {runs.hasNextPage && (
                <Button
                  variant="ghost"
                  size="32"
                  className="self-start"
                  loading={runs.isFetchingNextPage}
                  onClick={() => void runs.fetchNextPage()}
                >
                  {t("automations.detail.loadMoreRuns")}
                </Button>
              )}
            </section>

            {hasWebhook && (
              <section className="flex flex-col gap-2">
                <h2 className="text-body font-medium">
                  {t("automations.detail.deliveries")}
                </h2>
                <DeliveryList
                  orgId={orgId}
                  spaceId={spaceId}
                  automationId={automation.id}
                  onOpen={setDeliveryId}
                />
              </section>
            )}
          </div>

          <aside className="flex h-fit flex-col gap-3 rounded-xl border border-line-default p-4 text-body-sm">
            <h2 className="font-medium">{t("automations.detail.basicInfo")}</h2>
            <Info
              label={t("automations.detail.agent")}
              value={automation.assignee.name}
            />
            <Info
              label={t("automations.detail.creator")}
              value={
                <CreatorName orgId={orgId} userId={automation.created_by} />
              }
            />
            <Info
              label={t("automations.detail.createdAt")}
              value={formatDateTime(locale, automation.created_at)}
            />
            <Info
              label={t("automations.detail.nextRun")}
              value={formatDateTime(locale, nextRunAt(automation))}
            />
            <Info
              label={t("automations.detail.lastRun")}
              value={formatDateTime(locale, automation.last_run?.created_at)}
            />
          </aside>
        </div>
      </div>

      <RunDetailDrawer
        orgId={orgId}
        spaceId={spaceId}
        automationId={automation.id}
        runId={runId}
        onClose={() => setRunId(null)}
        onEdit={() => {
          setRunId(null);
          setEditing(true);
        }}
      />
      <DeliveryDetailDialog
        orgId={orgId}
        spaceId={spaceId}
        automationId={automation.id}
        deliveryId={deliveryId}
        onClose={() => setDeliveryId(null)}
      />
      <AutomationEditorDialog
        orgId={orgId}
        spaceId={spaceId}
        automation={automation}
        open={editing}
        onClose={() => setEditing(false)}
      />
      <ConfirmDialog
        open={deleting}
        danger
        onCancel={() => setDeleting(false)}
        onConfirm={() =>
          remove.mutate(automation, {
            onSuccess: () => {
              setDeleting(false);
              toast(t("automations.deleted"));
              navigate(back);
            },
            onError: (error) => {
              setDeleting(false);
              toast(
                automationErrorMessage(t, error, "automations.deleteFailed"),
                { tone: "error" },
              );
            },
          })
        }
        title={t("automations.deleteTitle")}
        description={t("automations.deleteDesc")}
        confirmLabel={
          remove.isPending ? t("automations.deleting") : t("automations.delete")
        }
        loading={remove.isPending}
      />
    </div>
  );
}

/** 提示词：超过 400px 折叠，底部渐变，点击展开。 */
function PromptBlock({ prompt }: { prompt: string }) {
  const t = useT();
  const ref = useRef<HTMLDivElement | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflow, setOverflow] = useState(false);
  useLayoutEffect(() => {
    setOverflow((ref.current?.scrollHeight ?? 0) > PROMPT_COLLAPSED_PX);
  }, [prompt]);
  const collapsed = overflow && !expanded;
  return (
    <div className="flex flex-col gap-1">
      <div className="relative">
        <div
          ref={ref}
          className={cn(
            "text-body whitespace-pre-wrap text-fg-default",
            collapsed && "max-h-[400px] overflow-hidden",
          )}
        >
          {prompt}
        </div>
        {collapsed && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 h-20 bg-gradient-to-t from-surface-app to-transparent" />
        )}
      </div>
      {overflow && (
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="self-start text-body-sm text-accent hover:underline"
        >
          {expanded
            ? t("automations.detail.promptCollapse")
            : t("automations.detail.promptExpand")}
        </button>
      )}
    </div>
  );
}

/** 触发器行：32 图标块 + 类型 + 描述 + Switch。 */
function TriggerRow({
  trigger,
  statuses,
  canWrite,
  disabled,
  onToggle,
}: {
  trigger: AutomationTrigger;
  /** 组织任务状态列表；加载中为 undefined */
  statuses: readonly SummaryStatus[] | undefined;
  canWrite: boolean;
  disabled: boolean;
  onToggle: (enabled: boolean) => void;
}) {
  const t = useT();
  const kindLabel = t(`automations.trigger.kind.${trigger.kind}` as MessageKey);
  return (
    <div className="flex items-center gap-3 rounded-xl border border-line-default p-3">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-surface-muted">
        <Icon name={triggerIcon(trigger.kind)} size={16} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-body font-medium">{kindLabel}</div>
        <div className="truncate text-body-sm text-fg-muted">
          {triggerSummary(t, trigger, statuses)}
        </div>
      </div>
      <Switch
        checked={trigger.enabled}
        disabled={!canWrite || disabled}
        aria-label={kindLabel}
        onCheckedChange={onToggle}
      />
    </div>
  );
}

/** WEBHOOK 投递列表，空态与分页。 */
function DeliveryList({
  orgId,
  spaceId,
  automationId,
  onOpen,
}: {
  orgId: string;
  spaceId: string;
  automationId: string;
  onOpen: (deliveryId: string) => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const deliveries = useWebhookDeliveries(orgId, spaceId, automationId);
  if (deliveries.isLoading) return <Spinner />;
  // 旧服务端没有投递时返回 deliveries: null，展开前兜底成空数组
  const items =
    deliveries.data?.pages.flatMap((page) => page.deliveries ?? []) ?? [];
  if (items.length === 0) {
    return (
      <p className="text-body-sm text-fg-muted">
        {t("automations.detail.noDeliveries")}
      </p>
    );
  }
  return (
    <>
      <ul className="flex flex-col divide-y divide-line-default rounded-xl border border-line-default">
        {items.map((item) => (
          <li key={item.id}>
            <button
              type="button"
              onClick={() => onOpen(item.id)}
              className="flex w-full items-center gap-3 px-4 py-2.5 text-left text-body hover:bg-surface-hover"
            >
              <DeliveryStatusIcon status={item.status} />
              <span className="shrink-0 text-body-sm font-medium">
                {deliveryStatusLabel(t, item.status)}
              </span>
              <span className="shrink-0 text-caption text-fg-muted">
                {item.provider}
              </span>
              <span className="min-w-0 flex-1 truncate font-mono text-caption text-fg-secondary">
                {item.event}
              </span>
              {item.replay_of && (
                <span className="shrink-0 rounded-md bg-surface-muted px-1.5 py-0.5 text-caption">
                  {t("automations.detail.replay")}
                </span>
              )}
              <span className="shrink-0 rounded-md bg-surface-muted px-1.5 py-0.5 text-caption">
                {t("automations.detail.attempts", { count: item.attempts })}
              </span>
              <span className="shrink-0 font-mono text-caption text-fg-muted">
                {formatDateTime(locale, item.received_at)}
              </span>
            </button>
          </li>
        ))}
      </ul>
      {deliveries.hasNextPage && (
        <Button
          variant="ghost"
          size="32"
          className="self-start"
          loading={deliveries.isFetchingNextPage}
          onClick={() => void deliveries.fetchNextPage()}
        >
          {t("automations.detail.loadMoreDeliveries")}
        </Button>
      )}
    </>
  );
}

function CreatorName({ orgId, userId }: { orgId: string; userId: string }) {
  const people = useOrgPeople(orgId);
  const person = people.data?.find((item) => item.id === userId);
  return <>{person ? personName(person) : ""}</>;
}

function Info({ label, value }: { label: string; value: ReactNode }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-caption text-fg-muted">{label}</span>
      <span className="text-body">{value || t("automations.detail.none")}</span>
    </div>
  );
}

/** 基础信息里的「下次运行」：所有定时触发器中最早的一次。 */
function nextRunAt(automation: Automation): string | null {
  const times = automation.triggers
    .map((trigger) => trigger.next_run_at)
    .filter((value): value is string => Boolean(value))
    .sort();
  return times[0] ?? null;
}
