import { useId, useMemo, type CSSProperties } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";

import { isApiError, type Task } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, IconButton } from "@openfox/ui";

import { AgentAvatar } from "../agents/AgentAvatar";
import { useAgentSettingsParam } from "../agents/params";
import {
  PageSkeleton,
  PageState,
  useAgentConfig,
  useCrewMembers,
} from "../conversations/ChatPage";
import { ConversationStatusMenu } from "../conversations/ConversationHeader";
import { ConversationView } from "../conversations/ConversationView";
import { conversationTitle } from "../conversations/model";
import { useConversation } from "../conversations/queries";
import { useTaskReadOnly } from "../spaces/readonly";
import { useRejectNavLocation } from "../shell/navMemory";
import { PanelResizeHandle } from "../shell/PanelResizeHandle";
import { usePanelWidth } from "../shell/panelWidth";
import { localKeys } from "../storage";
import { TaskConversationTools } from "./detail/TaskConversationTools";
import { TaskDetailView } from "./detail/TaskDetailDrawer";
import { taskPagePath, useTaskPageExit, useTaskPanel } from "./navigation";
import { useTaskParam } from "./parts";
import { useTaskByDisplayId } from "./queries";

/**
 * 任务全页：/o/:orgId/tasks/:displayId，与抽屉共用 TaskDetailView。
 * ?view=chat&conversationId=<id> 时右侧显示该会话的 AI 面板（窄屏覆盖任务）。
 */
export function TaskPage() {
  const t = useT();
  const { orgId = "", displayId = "" } = useParams();
  const id = decodeURIComponent(displayId).toUpperCase();
  const location = useLocation();
  const navigate = useNavigate();
  const taskParam = useTaskParam();
  const panel = useTaskPanel();
  const panelId = useId();
  const resize = usePanelWidth({
    storageKey: localKeys.taskAiPanelWidth,
    initial: 480,
    min: 320,
    max: 960,
    remaining: 400,
  });
  const detail = useTaskByDisplayId(orgId, id);
  const task = detail.data;
  // 没有进入前的页面（深链接、刷新）时，关闭回到任务所在空间的任务页
  const fallback = task
    ? `/o/${orgId}/spaces/${task.space_id}/tasks`
    : `/o/${orgId}/inbox`;
  const exit = useTaskPageExit(fallback);
  const redirectCanonical = useMemo(
    () => (canonical: string) =>
      navigate(taskPagePath(orgId, canonical) + location.search, {
        replace: true,
        state: location.state,
      }),
    [navigate, orgId, location.search, location.state],
  );
  return (
    <div
      ref={resize.containerRef}
      className="@container/task relative flex h-full min-h-0"
    >
      <div className="flex min-w-0 flex-1 flex-col">
        <TaskDetailView
          orgId={orgId}
          displayId={id}
          onOpenTask={taskParam.open}
          onDeleted={exit.close}
          onExpand={(shown) => exit.restore(shown)}
          expandLabel={t("tasks.detail.restoreDrawer")}
          expandIcon="minimize-2"
          rejectReplacement={`/o/${orgId}/inbox`}
          redirectCanonical={redirectCanonical}
          notFoundActions={
            <div className="flex gap-2">
              <Button
                variant="outline"
                onClick={() => navigate(`/o/${orgId}/inbox`)}
              >
                {t("tasks.detail.backToInbox")}
              </Button>
              <Button variant="outline" onClick={exit.close}>
                {t("tasks.detail.goToSpace")}
              </Button>
            </div>
          }
          frame={({ title, actions, children }) => (
            <section
              aria-label={t("tasks.detail.label")}
              className="flex min-h-0 flex-1 flex-col"
            >
              <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-3">
                <IconButton
                  label={t("common.close")}
                  icon={<Icon name="x" />}
                  size="28"
                  onClick={exit.close}
                />
                <h1 className="min-w-0 flex-1 truncate text-body font-medium">
                  {title}
                </h1>
                {actions}
              </div>
              <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
            </section>
          )}
        />
      </div>
      {panel.conversationId && (
        <aside
          id={panelId}
          aria-label={t("tasks.detail.aiPanel")}
          style={
            { "--task-ai-panel-width": `${resize.width}px` } as CSSProperties
          }
          className="absolute inset-0 z-10 flex min-h-0 min-w-0 flex-col bg-surface-card @[720px]/task:relative @[720px]/task:w-[var(--task-ai-panel-width)] @[720px]/task:shrink-0 @[720px]/task:border-l @[720px]/task:border-line-default"
        >
          <PanelResizeHandle
            {...resize}
            label={t("tasks.detail.aiPanelResize")}
            controls={panelId}
            side="right"
            onCollapse={panel.close}
            className="absolute inset-y-0 -left-1 hidden w-2 @[720px]/task:block"
          />
          <TaskAiPanel
            orgId={orgId}
            task={task}
            conversationId={panel.conversationId}
            onClose={panel.close}
            taskKnown={Boolean(task) || Boolean(detail.error)}
          />
        </aside>
      )}
    </div>
  );
}

/** 右侧 AI 面板：任务的某个会话。会话不存在 / 无权访问 / 不属于该任务时不展示内容。 */
function TaskAiPanel({
  orgId,
  task,
  conversationId,
  onClose,
  taskKnown,
}: {
  orgId: string;
  task: Task | undefined;
  conversationId: string;
  onClose: () => void;
  taskKnown: boolean;
}) {
  const t = useT();
  const location = useLocation();
  const query = useConversation(orgId, conversationId);
  const conversation = query.data;
  const readOnly = useTaskReadOnly(orgId, task?.space_id);
  const settings = useAgentSettingsParam();
  const crewState = useCrewMembers(
    orgId,
    conversation?.space_id,
    conversation?.crew_id ?? null,
  );
  const config = useAgentConfig(
    orgId,
    conversation?.agent.space_id ?? conversation?.space_id,
    conversation?.agent_id ?? "",
  );
  const missing =
    isApiError(query.error) &&
    (query.error.status === 404 || query.error.status === 403);
  const foreign = Boolean(
    conversation && task && conversation.task_id !== task.id,
  );
  // 会话失效或不属于任务：主导航记忆改记去掉面板参数的地址
  useRejectNavLocation(
    missing || foreign,
    missing || foreign ? stripPanel(location.pathname, location.search) : null,
  );

  let body: React.ReactNode;
  if (
    query.isLoading ||
    !taskKnown ||
    (conversation?.crew_id && crewState.query.isLoading)
  )
    body = <PageSkeleton />;
  else if (foreign)
    body = (
      <PageState
        icon={
          <Icon name="openfox-alert-circle" size={28} className="text-danger" />
        }
        title={t("tasks.detail.aiPanelNotInTask")}
      />
    );
  else if (!conversation)
    body = (
      <PageState
        icon={
          <Icon name="openfox-alert-circle" size={28} className="text-danger" />
        }
        title={missing ? t("chat.page.notFound") : t("chat.page.loadFailed")}
        action={
          !missing && (
            <Button
              variant="outline"
              size="28"
              onClick={() => void query.refetch()}
            >
              {t("common.retry")}
            </Button>
          )
        }
      />
    );
  else
    body = (
      <ConversationView
        orgId={orgId}
        agent={{ ...conversation.agent, ...config }}
        crew={crewState.crew}
        conversation={conversation}
        spaceId={conversation.space_id}
        onOpenSettings={() =>
          settings.open(
            conversation.agent_id,
            conversation.agent.space_id ?? conversation.space_id,
          )
        }
        empty={
          <div className="flex flex-col items-center gap-3 text-center">
            <Icon name="openfox-bot" size={32} className="text-fg-muted" />
            <p className="text-body text-fg-secondary">
              {t("chat.page.readyEmpty", {
                title: conversationTitle(conversation) ?? t("chat.untitled"),
              })}
            </p>
          </div>
        }
      />
    );

  return (
    <>
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-3">
        <IconButton
          label={t("tasks.detail.aiPanelClose")}
          icon={<Icon name="x" />}
          size="28"
          onClick={onClose}
        />
        {conversation && !foreign && (
          <>
            <AgentAvatar
              name={conversation.agent.name}
              avatar={conversation.agent.avatar}
              size={20}
            />
            <span className="min-w-0 truncate text-body font-medium">
              {crewState.crew?.name ?? conversation.agent.name}
              {conversation.title && (
                <span className="font-normal text-fg-muted">
                  {" "}
                  · {conversation.title}
                </span>
              )}
            </span>
            <ConversationStatusMenu
              conversation={conversation}
              readOnly={readOnly}
            />
          </>
        )}
        <span className="flex-1" />
        {conversation && task && !foreign && (
          <TaskConversationTools
            key={conversation.id}
            orgId={orgId}
            task={task}
            conversation={conversation}
            readOnly={readOnly}
          />
        )}
      </header>
      <div className="flex min-h-0 flex-1 flex-col">{body}</div>
    </>
  );
}

function stripPanel(pathname: string, search: string): string {
  const params = new URLSearchParams(search);
  params.delete("view");
  params.delete("conversationId");
  const rest = params.toString();
  return rest ? `${pathname}?${rest}` : pathname;
}
