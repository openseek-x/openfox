import { useQueries } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import type {
  Label,
  Project,
  ProjectDistributionItem,
  ProjectPatchInput,
  TaskListQuery,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Icon,
  Menu,
  MenuItem,
  Radio,
  Segmented,
  Spinner,
  Tabs,
  Popover,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { canWriteSpace, useSpaceDetail, useSpaceList } from "../spaces/queries";
import { ReadOnlyContext } from "../spaces/readonly";
import { Picker, labelGroups, toggleLabel } from "../tasks/pickers";
import { usePeople, useSpaceAgents } from "../tasks/queries";
import { SpaceTaskWorkspace } from "../tasks/SpaceTaskWorkspace";
import { TaskState } from "../tasks/TaskWorkspace";
import { DeleteProjectDialog } from "./ProjectDialogs";
import { EmojiGrid } from "./EmojiGrid";
import { ProgressBar, ProjectIcon, ProjectPropertyChips } from "./parts";
import { useCopyProjectLink } from "./ProjectsPage";
import { useProject, useProjectMutation } from "./queries";

type Tab = "overview" | "tasks";
type Kind = "all" | "people" | "agents";

/** 专项详情 /o/:org/spaces/:space/projects/:id。 */
export function ProjectDetailPage() {
  const t = useT();
  const navigate = useNavigate();
  const { orgId = "", spaceId = "", projectId = "" } = useParams();
  const space = useSpaceDetail(orgId, spaceId);
  const detail = useProject(orgId, projectId);
  const [tab, setTab] = useState<Tab>("overview");
  const [deleting, setDeleting] = useState<Project | null>(null);
  const copyLink = useCopyProjectLink(orgId, spaceId);
  const listPath = `/o/${orgId}/spaces/${spaceId}/projects`;

  if (space.isLoading || detail.isLoading) {
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );
  }
  const project = detail.data;
  if (!space.data || !project)
    return <TaskState title={t("projects.notFound")} />;
  const canWrite = canWriteSpace(space.data);
  const primary = project.spaces.find((item) => item.is_primary);
  const canDelete =
    canWrite && space.data.can_manage && primary?.id === spaceId;

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <Link
          to={listPath}
          aria-label={t("projects.title")}
          className="text-fg-muted hover:text-fg-default"
        >
          <Icon name="chevron-left" size={14} />
        </Link>
        <span className="truncate text-body-sm text-fg-muted">
          {project.name}
        </span>
        <div className="ml-auto">
          <Menu
            label={t("projects.actions")}
            trigger={({ ref, toggle }) => (
              <Button
                ref={ref}
                variant="outline"
                size="28"
                onClick={toggle}
                leading={<Icon name="more" />}
              >
                <span className="sr-only">{t("projects.actions")}</span>
              </Button>
            )}
          >
            <MenuItem
              icon={<Icon name="link" />}
              onSelect={() => copyLink(project.id)}
            >
              {t("projects.copyLink")}
            </MenuItem>
            <MenuItem
              danger
              icon={<Icon name="trash-2" />}
              disabled={!canDelete}
              onSelect={() => setDeleting(project)}
            >
              {t("projects.delete.action")}
            </MenuItem>
          </Menu>
        </div>
      </header>
      <Tabs
        label={t("projects.title")}
        value={tab}
        onChange={(id) => setTab(id as Tab)}
        className="px-6"
        tabs={[
          { id: "overview", label: t("projects.overview") },
          { id: "tasks", label: t("projects.tasks") },
        ]}
      />
      {tab === "overview" ? (
        <Overview
          key={project.version}
          orgId={orgId}
          project={project}
          space={space.data}
          canWrite={canWrite}
        />
      ) : (
        <ProjectTasks orgId={orgId} spaceId={spaceId} projectId={project.id} />
      )}
      <DeleteProjectDialog
        orgId={orgId}
        project={deleting}
        onClose={() => setDeleting(null)}
        onDeleted={() => navigate(listPath)}
      />
    </div>
  );
}

function Overview({
  orgId,
  project,
  space,
  canWrite,
}: {
  orgId: string;
  project: Project;
  space: { id: string; visibility: string };
  canWrite: boolean;
}) {
  const t = useT();
  const api = useApi();
  const people = usePeople(orgId, space);
  const [name, setName] = useState(project.name);
  const [description, setDescription] = useState(project.description);
  const [adding, setAdding] = useState(false);
  const [emojiOpen, setEmojiOpen] = useState(false);
  const emojiAnchor = useRef<HTMLButtonElement | null>(null);
  const update = useProjectMutation(
    orgId,
    (patch: Omit<ProjectPatchInput, "version">) =>
      api.projects.update(orgId, project.id, {
        version: project.version,
        ...patch,
      }),
  );
  const setLabels = useProjectMutation(orgId, (labelIds: string[]) =>
    api.projects.setLabels(orgId, project.id, labelIds),
  );
  // 标签来自所有关联空间（含组织级标签），按 id 去重
  const labelQueries = useQueries({
    queries: project.spaces.map((item) => ({
      queryKey: keys.labels(orgId, `picker:${item.id}`),
      queryFn: () => api.labels.list(orgId, "picker", item.id),
    })),
  });
  const labels = useMemo(() => {
    const byId = new Map<string, Label>();
    for (const query of labelQueries)
      for (const label of query.data ?? []) byId.set(label.id, label);
    return [...byId.values()];
  }, [labelQueries]);
  const selected = project.labels.map((label) => label.id);

  return (
    <div className="mx-auto flex min-h-0 w-full max-w-[900px] flex-1 flex-col gap-5 overflow-auto px-6 py-6">
      <div className="flex items-center gap-3">
        <button
          ref={emojiAnchor}
          type="button"
          disabled={!canWrite}
          aria-label={t("projects.emoji")}
          aria-expanded={emojiOpen}
          onClick={() => setEmojiOpen((value) => !value)}
          className="flex size-12 shrink-0 items-center justify-center rounded-xl hover:bg-surface-hover disabled:hover:bg-transparent"
        >
          <ProjectIcon icon={project.icon} size={32} />
        </button>
        <Popover
          open={emojiOpen}
          onOpenChange={setEmojiOpen}
          anchorRef={emojiAnchor}
          placement="bottom-start"
          role="dialog"
          aria-label={t("projects.emoji")}
          className="w-[320px] p-3"
        >
          <EmojiGrid
            value={project.icon}
            onChange={(icon) => {
              setEmojiOpen(false);
              update.mutate({ icon });
            }}
          />
        </Popover>
        <input
          aria-label={t("projects.name")}
          value={name}
          maxLength={80}
          disabled={!canWrite}
          onChange={(event) => setName(event.target.value)}
          onBlur={() => {
            const next = name.trim();
            if (next && next !== project.name) update.mutate({ name: next });
          }}
          className="min-w-0 flex-1 bg-transparent text-heading font-medium outline-none focus-visible:shadow-none"
        />
      </div>

      <dl className="flex flex-col gap-4">
        <Row label={t("projects.attributes")}>
          <ProjectPropertyChips
            status={project.status}
            priority={project.priority}
            lead={project.lead?.id ?? ""}
            people={people}
            disabled={!canWrite}
            onStatus={(status) => update.mutate({ status })}
            onPriority={(priority) => update.mutate({ priority })}
            onLead={(id) => update.mutate({ lead_user_id: id || null })}
          />
        </Row>
        <Row label={t("projects.labels")}>
          {project.labels.map((label) => (
            <span
              key={label.id}
              className="rounded-full bg-surface-muted px-2 py-0.5 text-body-sm"
            >
              {label.name}
            </span>
          ))}
          <Picker
            label={t("projects.labels")}
            multiple
            groups={labelGroups(labels)}
            selected={selected}
            onSelect={(id) =>
              setLabels.mutate(toggleLabel(selected, id, labels))
            }
            trigger={({ ref, toggle }) => (
              <Button
                ref={ref}
                variant="ghost"
                size="28"
                disabled={!canWrite}
                onClick={toggle}
                leading={<Icon name="plus" />}
              >
                {t("projects.addLabel")}
              </Button>
            )}
          />
        </Row>
        <Row label={t("projects.spaces")}>
          {project.spaces.map((item) => (
            <span
              key={item.id}
              className="inline-flex h-7 items-center gap-1 rounded-full border border-line-default pl-2.5 text-body-sm"
            >
              {item.name}
              {item.is_primary ? (
                <span className="pr-2.5 text-caption text-fg-muted">
                  {t("projects.primary")}
                </span>
              ) : (
                <SpaceMenu
                  orgId={orgId}
                  projectId={project.id}
                  space={item}
                  disabled={!canWrite}
                />
              )}
            </span>
          ))}
          <Button
            variant="ghost"
            size="28"
            disabled={!canWrite}
            onClick={() => setAdding(true)}
          >
            {t("projects.addSpaceEllipsis")}
          </Button>
        </Row>
        <Row label={t("projects.tasks")}>
          <span className="text-body">
            {t("projects.taskCount", { count: project.progress.total })}
          </span>
        </Row>
        <Row label={t("projects.progress")}>
          <ProgressBar progress={project.progress} />
        </Row>
        {project.distribution && (
          <Row label={t("projects.distribution")} top>
            <Distribution
              assignees={project.distribution.assignees}
              iterations={project.distribution.iterations}
            />
          </Row>
        )}
        <Row label={t("projects.description")} top>
          <textarea
            aria-label={t("projects.description")}
            placeholder={t("projects.descPlaceholder")}
            rows={5}
            maxLength={10000}
            value={description}
            disabled={!canWrite}
            onChange={(event) => setDescription(event.target.value)}
            onBlur={() => {
              if (description !== project.description)
                update.mutate({ description });
            }}
            className="w-full resize-none bg-transparent text-body outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
          />
        </Row>
      </dl>
      <AddSpaceDialog
        open={adding}
        orgId={orgId}
        project={project}
        onClose={() => setAdding(false)}
      />
    </div>
  );
}

/** 左标签右内容的一行。 */
function Row({
  label,
  top,
  children,
}: {
  label: string;
  top?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className={`flex gap-4 ${top ? "items-start" : "items-center"}`}>
      <dt className="w-16 shrink-0 text-body-sm text-fg-muted">{label}</dt>
      <dd className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
        {children}
      </dd>
    </div>
  );
}

/** 非主空间的菜单（6.15）：设为主空间、从专项移除。 */
function SpaceMenu({
  orgId,
  projectId,
  space,
  disabled,
}: {
  orgId: string;
  projectId: string;
  space: Project["spaces"][number];
  disabled: boolean;
}) {
  const t = useT();
  const api = useApi();
  const primary = useProjectMutation(orgId, () =>
    api.projects.setPrimarySpace(orgId, projectId, space.id),
  );
  const remove = useProjectMutation(orgId, () =>
    api.projects.removeSpace(orgId, projectId, space.id),
  );
  return (
    <span>
      <Menu
        label={`${space.name} ${t("projects.actions")}`}
        trigger={({ ref, toggle }) => (
          <button
            ref={ref}
            type="button"
            disabled={disabled}
            aria-label={`${space.name} ${t("projects.actions")}`}
            onClick={toggle}
            className="flex size-7 items-center justify-center rounded-md text-fg-muted hover:bg-surface-hover disabled:opacity-50"
          >
            <Icon name="more" />
          </button>
        )}
      >
        <MenuItem onSelect={() => primary.mutate(undefined)}>
          {t("projects.setPrimary")}
        </MenuItem>
        <MenuItem danger onSelect={() => remove.mutate(undefined)}>
          {t("projects.removeSpace")}
        </MenuItem>
      </Menu>
    </span>
  );
}

/** 添加空间（6.16）：只列出自己能写任务、启用了专项且还没关联的空间。 */
function AddSpaceDialog({
  open,
  orgId,
  project,
  onClose,
}: {
  open: boolean;
  orgId: string;
  project: Project;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const spaces = useSpaceList(orgId);
  const [picked, setPicked] = useState("");
  const add = useProjectMutation(orgId, (spaceId: string) =>
    api.projects.addSpace(orgId, project.id, spaceId),
  );
  const linked = new Set(project.spaces.map((item) => item.id));
  const candidates = (spaces.data ?? []).filter(
    (item) =>
      !linked.has(item.id) && item.features.projects && canWriteSpace(item),
  );
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("projects.addSpace")}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={!picked}
            loading={add.isPending}
            onClick={() => add.mutate(picked, { onSuccess: onClose })}
          >
            {t("projects.addSpace")}
          </Button>
        </>
      }
    >
      {candidates.length === 0 ? (
        <p className="text-body text-fg-muted">{t("projects.noSpaces")}</p>
      ) : (
        <ul className="flex flex-col gap-1">
          {candidates.map((item) => (
            <li key={item.id}>
              <label className="flex cursor-pointer items-center gap-2 rounded-lg px-2 py-1.5 hover:bg-surface-hover">
                <Radio
                  name="project-space"
                  checked={picked === item.id}
                  onChange={() => setPicked(item.id)}
                />
                <span>{item.name}</span>
                <span className="text-body-sm text-fg-muted">
                  {item.prefix}
                </span>
              </label>
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}

/** 分布（6.8）：按处理人 / 迭代。 */
function Distribution({
  assignees,
  iterations,
}: {
  assignees: ProjectDistributionItem[];
  iterations: ProjectDistributionItem[];
}) {
  const t = useT();
  const [by, setBy] = useState<"assignees" | "iterations">("assignees");
  const rows = by === "assignees" ? assignees : iterations;
  return (
    <div className="flex w-full flex-col gap-2">
      <Segmented
        label={t("projects.distribution")}
        value={by}
        onChange={setBy}
        options={[
          { value: "assignees", label: t("projects.byAssignee") },
          { value: "iterations", label: t("projects.byIteration") },
        ]}
      />
      <ul className="flex flex-col divide-y divide-line-soft">
        {rows.map((row) => (
          <li key={row.key} className="flex items-center gap-3 py-2">
            <span className="min-w-0 flex-1 truncate text-body">
              {row.label ||
                t(
                  by === "assignees"
                    ? "projects.unassigned"
                    : "projects.noIteration",
                )}
            </span>
            <span className="text-body-sm text-fg-muted">
              {t("projects.distributionItem", {
                total: row.total,
                percent: row.total
                  ? Math.round((row.done / row.total) * 100)
                  : 0,
              })}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** 任务页签（6.9、6.10）：复用任务工作区，固定 project_id；全部 / 人员 / Agents 用处理人类型筛选。 */
function ProjectTasks({
  orgId,
  spaceId,
  projectId,
}: {
  orgId: string;
  spaceId: string;
  projectId: string;
}) {
  const t = useT();
  const space = useSpaceDetail(orgId, spaceId);
  const people = usePeople(orgId, space.data);
  const agents = useSpaceAgents(orgId, spaceId);
  const [kind, setKind] = useState<Kind>("all");
  const assignee = useMemo(() => {
    if (kind === "people") return people.map((p) => `user:${p.id}`);
    if (kind === "agents")
      return (agents.data ?? []).map((a) => `agent:${a.id}`);
    return undefined;
  }, [kind, people, agents.data]);
  const baseParams = useMemo<TaskListQuery>(
    () => ({ project_id: [projectId], ...(assignee ? { assignee } : {}) }),
    [projectId, assignee],
  );
  const tabs = (
    <Segmented
      label={t("projects.tasks")}
      value={kind}
      onChange={setKind}
      options={[
        { value: "all", label: t("projects.kindAll") },
        { value: "people", label: t("projects.kindPeople") },
        { value: "agents", label: t("projects.kindAgents") },
      ]}
    />
  );
  return (
    <ReadOnlyContext.Provider value={space.data?.status === "retired"}>
      {assignee?.length === 0 ? (
        <>
          <div className="px-4 py-2">{tabs}</div>
          <TaskState title={t("projects.noTasks")} />
        </>
      ) : (
        <SpaceTaskWorkspace
          orgId={orgId}
          spaceId={spaceId}
          pageKey={`project:${spaceId}`}
          baseParams={baseParams}
          leading={tabs}
          empty={<TaskState title={t("projects.noTasks")} />}
        />
      )}
    </ReadOnlyContext.Provider>
  );
}
