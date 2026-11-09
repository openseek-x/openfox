import { useMemo, useState } from "react";

import { Link, useNavigate, useParams } from "react-router-dom";

import type { View, ViewVisibility } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, Select, Spinner } from "@openfox/ui";

import { useApi } from "../context";
import { ReadOnlyContext } from "../spaces/readonly";
import {
  defaultDisplay,
  normalizeDisplay,
  queryToConditions,
  type DisplaySettings,
  type FilterQuery,
} from "../tasks/model";
import { SpaceTaskWorkspace } from "../tasks/SpaceTaskWorkspace";
import { TaskState } from "../tasks/TaskWorkspace";
import { ViewIconPicker, type ViewIconValue } from "./ViewIcon";
import { useView, useViewMutation } from "./queries";

/**
 * 新建 / 编辑视图页：面包屑、头部卡片（图标、名称、描述、取消 / 保存），
 * 可见性放在任务工作区工具栏左侧，筛选与显示设置用工作区自带的按钮，下方实时预览。
 */
export function ViewEditorPage() {
  const { orgId = "", spaceId = "", viewId } = useParams();
  const detail = useView(orgId, viewId);
  const t = useT();
  if (viewId && detail.isLoading)
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );
  if (viewId && !detail.data) return <TaskState title={t("views.notFound")} />;
  return (
    <ViewEditor
      key={viewId ?? "new"}
      orgId={orgId}
      spaceId={spaceId}
      view={detail.data ?? null}
    />
  );
}

function ViewEditor({
  orgId,
  spaceId,
  view,
}: {
  orgId: string;
  spaceId: string;
  /** null = 新建 */
  view: View | null;
}) {
  const t = useT();
  const navigate = useNavigate();
  const listPath = `/o/${orgId}/spaces/${spaceId}/views`;
  const onClose = () => navigate(listPath);

  const api = useApi();
  const [name, setName] = useState(view?.name ?? "");
  const [description, setDescription] = useState(view?.description ?? "");
  const [visibility, setVisibility] = useState<ViewVisibility>(
    view?.visibility ?? "personal",
  );
  const [icon, setIcon] = useState<ViewIconValue>({
    kind: view?.icon_kind ?? null,
    icon: view?.icon ?? null,
    url: view?.icon_url ?? null,
  });
  const initialDisplay = useMemo(
    () => normalizeDisplay(view?.display ?? {}, defaultDisplay("space")),
    [view],
  );
  const initialConditions = useMemo(
    () => queryToConditions((view?.filters ?? {}) as FilterQuery),
    [view],
  );
  const [state, setState] = useState<{
    filters: FilterQuery;
    display: DisplaySettings;
  }>({ filters: {}, display: initialDisplay });

  const save = useViewMutation(orgId, () => {
    const body = {
      name: name.trim(),
      description,
      visibility,
      filters: state.filters,
      display: { ...state.display },
    };
    if (!view)
      return api.views.create(orgId, spaceId, {
        ...body,
        ...(icon.kind && icon.icon
          ? { icon_kind: icon.kind, icon: icon.icon }
          : {}),
      });
    const iconChanged = icon.kind !== view.icon_kind || icon.icon !== view.icon;
    return api.views.update(orgId, view.id, {
      ...body,
      version: view.version,
      // 可见性只有创建者能改：没变就不带
      ...(visibility === view.visibility ? { visibility: undefined } : {}),
      ...(iconChanged
        ? { icon_kind: icon.kind, ...(icon.icon ? { icon: icon.icon } : {}) }
        : {}),
    });
  });

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-1.5 border-b border-line-default px-4 text-body-sm">
        <Link to={listPath} className="text-fg-muted hover:text-fg-default">
          {t("views.title")}
        </Link>
        <Icon name="chevron-right" size={12} className="text-fg-subtle" />
        <span className="font-medium">
          {view ? t("views.editor.editTitle") : t("views.editor.createTitle")}
        </span>
      </header>
      <div className="mx-4 mt-3 flex items-start gap-3 rounded-xl border border-line-default p-3">
        <ViewIconPicker orgId={orgId} value={icon} onChange={setIcon} />
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <input
            data-autofocus
            aria-label={t("views.editor.name")}
            placeholder={t("views.editor.name")}
            value={name}
            maxLength={80}
            onChange={(event) => setName(event.target.value)}
            className="w-full bg-transparent text-title font-medium outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
          />
          <input
            aria-label={t("views.editor.description")}
            placeholder={t("views.editor.description")}
            value={description}
            maxLength={500}
            onChange={(event) => setDescription(event.target.value)}
            className="w-full bg-transparent text-body outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
          />
        </div>
        <Button variant="outline" onClick={onClose}>
          {t("common.cancel")}
        </Button>
        <Button
          loading={save.isPending}
          disabled={!name.trim()}
          onClick={() => save.mutate(undefined, { onSuccess: onClose })}
        >
          {view ? t("common.save") : t("views.editor.create")}
        </Button>
      </div>
      <ReadOnlyContext.Provider value>
        <SpaceTaskWorkspace
          orgId={orgId}
          spaceId={spaceId}
          pageKey="view-editor"
          baseParams={{}}
          initialConditions={initialConditions}
          initialDisplay={initialDisplay}
          onChange={setState}
          leading={
            <label className="flex shrink-0 items-center gap-2 whitespace-nowrap text-body-sm text-fg-muted">
              {t("views.editor.visibility")}
              <Select
                aria-label={t("views.editor.visibility")}
                value={visibility}
                onChange={setVisibility}
                options={[
                  { value: "personal", label: t("views.visibility.personal") },
                  { value: "space", label: t("views.visibility.space") },
                ]}
              />
            </label>
          }
          empty={<TaskState title={t("views.editor.previewEmpty")} />}
        />
      </ReadOnlyContext.Provider>
    </div>
  );
}
