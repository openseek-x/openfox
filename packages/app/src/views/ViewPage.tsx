import { useMemo, useRef } from "react";
import { Link, useParams } from "react-router-dom";

import type { TaskListQuery } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Icon, Spinner } from "@openfox/ui";

import { useApi } from "../context";
import { useSpaceDetail } from "../spaces/queries";
import { ReadOnlyContext } from "../spaces/readonly";
import { useLayoutParam } from "../tasks/layoutParam";
import { defaultDisplay, normalizeDisplay } from "../tasks/model";
import { SpaceTaskWorkspace } from "../tasks/SpaceTaskWorkspace";
import { TaskState } from "../tasks/TaskWorkspace";
import { ViewIcon } from "./ViewIcon";
import { useView, useViewMutation } from "./queries";

/** 打开视图 /o/:org/spaces/:space/views/:id：任务工作区带 view_id，展示方式用本人的偏好。 */
export function ViewPage() {
  const t = useT();
  const api = useApi();
  const { orgId = "", spaceId = "", viewId = "" } = useParams();
  const space = useSpaceDetail(orgId, spaceId);
  const detail = useView(orgId, viewId);
  const view = detail.data;
  const setMode = useViewMutation(orgId, (mode: "list" | "board" | "table") =>
    api.views.setModeMine(orgId, viewId, mode),
  );
  // 地址里的 ?view= 优先。下面的 layout.current 也以它为准，
  // 这样仅打开带 ?view= 的链接不会改写本人展示方式，用户在工具栏切换布局时才写。
  // 工作区只在首次渲染时读取 initialDisplay，之后地址变化不会回灌。
  const urlLayout = useLayoutParam().layout;
  const initialDisplay = useMemo(() => {
    if (!view) return undefined;
    const display = normalizeDisplay(view.display, defaultDisplay("space"));
    return {
      ...display,
      layout: urlLayout ?? view.member_view_mode ?? display.layout,
    };
  }, [view, urlLayout]);
  // 当前生效的展示方式：用户在工具栏切换布局时才写到 view-mode-mine
  const layout = useRef<{ id: string; value: string } | null>(null);
  if (view && initialDisplay && layout.current?.id !== view.id)
    layout.current = { id: view.id, value: initialDisplay.layout };
  const baseParams = useMemo<TaskListQuery>(
    () => ({ view_id: viewId }),
    [viewId],
  );

  if (space.isLoading || detail.isLoading) {
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );
  }
  if (!space.data || !view || !initialDisplay)
    return <TaskState title={t("views.notFound")} />;

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <Link
          to={`/o/${orgId}/spaces/${spaceId}/views`}
          aria-label={t("views.title")}
          className="text-fg-muted hover:text-fg-default"
        >
          <Icon name="chevron-left" size={14} />
        </Link>
        <ViewIcon view={view} />
        <h1 className="truncate font-medium">{view.name}</h1>
        <Badge tone="muted">{t(`views.visibility.${view.visibility}`)}</Badge>
      </header>
      <ReadOnlyContext.Provider value={space.data.status === "retired"}>
        <SpaceTaskWorkspace
          orgId={orgId}
          spaceId={spaceId}
          pageKey={`view:${viewId}`}
          baseParams={baseParams}
          initialDisplay={initialDisplay}
          onChange={({ display }) => {
            // 甘特图不写入本人展示方式（接口只接受 list / board / table），只在当前会话生效
            if (display.layout === "gantt") return;
            if (!layout.current || display.layout === layout.current.value)
              return;
            layout.current.value = display.layout;
            setMode.mutate(display.layout);
          }}
          empty={<TaskState title={t("views.noTasks")} />}
        />
      </ReadOnlyContext.Provider>
    </div>
  );
}
