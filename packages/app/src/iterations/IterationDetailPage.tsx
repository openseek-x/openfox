import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import type { Iteration, TaskListQuery } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Button,
  Checkbox,
  Drawer,
  Field,
  Icon,
  Kbd,
  Select,
  Spinner,
  TextInput,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { useSpaceDetail } from "../spaces/queries";
import { ReadOnlyContext } from "../spaces/readonly";
import { displayIdOf } from "../tasks/parts";
import { SpaceTaskWorkspace } from "../tasks/SpaceTaskWorkspace";
import { TaskState } from "../tasks/TaskWorkspace";
import { formatIterationRange } from "./dates";
import { IterationMenu } from "./IterationMenu";
import { IterationStatusBadge, iterationCounts } from "./parts";
import {
  iterationDisplayName,
  useIteration,
  useIterationMutation,
  useIterationSettings,
  useIterations,
  useUpdateIterationTasks,
} from "./queries";

/** 迭代详情 /o/:org/spaces/:space/iterations/:id。 */
export function IterationDetailPage() {
  const t = useT();
  const { locale } = useI18n();
  const navigate = useNavigate();
  const { orgId = "", spaceId = "", iterationId = "" } = useParams();
  const space = useSpaceDetail(orgId, spaceId);
  const settings = useIterationSettings(orgId, spaceId);
  const detail = useIteration(orgId, spaceId, iterationId);
  const siblings = useIterations(orgId, spaceId, {});
  const [planning, setPlanning] = useState(false);
  const [panel, setPanel] = useState(false);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "i") {
        event.preventDefault();
        setPanel((value) => !value);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  const listPath = `/o/${orgId}/spaces/${spaceId}/iterations`;

  if (space.isLoading || settings.isLoading || detail.isLoading) {
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );
  }
  const iteration = detail.data;
  if (!space.data || !settings.data || !iteration) {
    return <TaskState title={t("spaces.iteration.notFound")} />;
  }
  const retired = space.data.status === "retired";
  const canManage = space.data.can_manage && !retired;
  const manual = settings.data.mode === "manual";
  const open = iteration.status === "planned" || iteration.status === "active";
  const counts = iterationCounts(iteration);
  const range = formatIterationRange(
    iteration.start_date,
    iteration.end_date,
    locale,
    settings.data.timezone,
  );

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <Link
          to={listPath}
          aria-label={t("spaces.iteration.title")}
          className="text-fg-muted hover:text-fg-default"
        >
          <Icon name="chevron-left" size={14} />
        </Link>
        <Select
          aria-label={t("spaces.iteration.title")}
          value={iteration.id}
          options={(siblings.data?.iterations ?? [iteration]).map((item) => ({
            value: item.id,
            label: iterationDisplayName(item),
          }))}
          onChange={(id) => navigate(`${listPath}/${id}`)}
        />
        <IterationStatusBadge status={iteration.status} />
        <div className="ml-auto flex items-center gap-2">
          {canManage && open && (
            <Button
              size="28"
              variant="outline"
              onClick={() => setPlanning((value) => !value)}
            >
              {t("spaces.iteration.plan")}
            </Button>
          )}
          {canManage && (
            <IterationMenu
              key={iteration.version}
              orgId={orgId}
              spaceId={spaceId}
              iteration={iteration}
              manual={manual}
              onDeleted={() => navigate(listPath)}
            />
          )}
        </div>
      </header>
      <div className="flex items-center gap-3 border-b border-line-default px-6 py-2 text-body-sm text-fg-muted">
        <span>{range}</span>
        <span>
          {t("spaces.iteration.completedCount")} {counts.done}/{counts.scope}
        </span>
        <Button
          size="28"
          variant="ghost"
          className="ml-auto"
          onClick={() => setPanel(true)}
        >
          {t("spaces.iteration.details")} <Kbd>⌘I</Kbd>
        </Button>
      </div>
      <Drawer
        open={panel}
        onClose={() => setPanel(false)}
        title={iterationDisplayName(iteration)}
        width="min(480px, 100vw - 32px)"
      >
        <div className="flex flex-col gap-4 p-5">
          <p className="text-body-sm text-fg-muted">{range}</p>
          {iteration.description && (
            <p className="whitespace-pre-wrap text-body text-fg-muted">
              {iteration.description}
            </p>
          )}
          <div className="flex flex-wrap gap-6 text-body-sm">
            <Metric label={t("spaces.iteration.scope")} value={counts.scope} />
            <Metric
              label={t("spaces.iteration.completedCount")}
              value={`${counts.done}/${counts.scope}`}
            />
            <Metric label={t("spaces.task.estimate")} value={counts.points} />
            {iteration.capacity_percent !== null && (
              <Metric
                label={t("spaces.iteration.capacity")}
                value={`${iteration.capacity_percent}%`}
              />
            )}
          </div>
          <LinksSection
            orgId={orgId}
            spaceId={spaceId}
            iteration={iteration}
            canManage={canManage}
          />
        </div>
      </Drawer>
      <ReadOnlyContext.Provider value={retired}>
        {planning ? (
          <PlanningPanel
            orgId={orgId}
            spaceId={spaceId}
            iterationId={iteration.id}
          />
        ) : (
          <IterationTasks
            orgId={orgId}
            spaceId={spaceId}
            iterationId={iteration.id}
          />
        )}
      </ReadOnlyContext.Provider>
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string | number }) {
  return (
    <span className="text-fg-muted">
      {label} <span className="font-medium text-fg-default">{value}</span>
    </span>
  );
}

/** 迭代内任务：复用任务工作区，只是 baseParams 固定到该迭代。 */
function IterationTasks({
  orgId,
  spaceId,
  iterationId,
}: {
  orgId: string;
  spaceId: string;
  iterationId: string;
}) {
  const t = useT();
  const baseParams = useMemo<TaskListQuery>(
    () => ({ iteration_id: [iterationId] }),
    [iterationId],
  );
  return (
    <SpaceTaskWorkspace
      orgId={orgId}
      spaceId={spaceId}
      pageKey={`iteration:${spaceId}`}
      baseParams={baseParams}
      empty={<TaskState title={t("spaces.iteration.noTasks")} />}
    />
  );
}

/** 规划（3.21）：左边待规划任务勾选加入，右边本迭代任务勾选移出。 */
function PlanningPanel({
  orgId,
  spaceId,
  iterationId,
}: {
  orgId: string;
  spaceId: string;
  iterationId: string;
}) {
  const t = useT();
  return (
    <div className="grid min-h-0 flex-1 grid-cols-2 divide-x divide-line-default">
      <TaskPicker
        orgId={orgId}
        spaceId={spaceId}
        iterationId={iterationId}
        title={t("spaces.iteration.planTitle")}
        query={{ iteration_id: ["none"] }}
        action="add"
      />
      <TaskPicker
        orgId={orgId}
        spaceId={spaceId}
        iterationId={iterationId}
        title={t("spaces.iteration.tasks")}
        query={{ iteration_id: [iterationId] }}
        action="remove"
      />
    </div>
  );
}

function TaskPicker({
  orgId,
  spaceId,
  iterationId,
  title,
  query,
  action,
}: {
  orgId: string;
  spaceId: string;
  iterationId: string;
  title: string;
  query: TaskListQuery;
  action: "add" | "remove";
}) {
  const t = useT();
  const api = useApi();
  const [selected, setSelected] = useState<string[]>([]);
  const update = useUpdateIterationTasks(orgId, spaceId);
  const list = useQuery({
    queryKey: [...keys.tasks(orgId), "iteration-picker", spaceId, query],
    queryFn: () => api.tasks.list(orgId, spaceId, { ...query, limit: 100 }),
  });
  const tasks = list.data?.tasks ?? [];
  const toggle = (id: string) =>
    setSelected((current) =>
      current.includes(id) ? current.filter((x) => x !== id) : [...current, id],
    );
  return (
    <section className="flex min-h-0 flex-col">
      <div className="flex h-11 shrink-0 items-center gap-2 px-4">
        <h2 className="font-medium">{title}</h2>
        <Button
          size="28"
          className="ml-auto"
          disabled={selected.length === 0}
          loading={update.isPending}
          onClick={() =>
            update.mutate(
              { iterationId, task_ids: selected, action },
              { onSuccess: () => setSelected([]) },
            )
          }
        >
          {t(
            action === "add"
              ? "spaces.iteration.addToIteration"
              : "spaces.iteration.removeFromIteration",
          )}
        </Button>
      </div>
      <ul className="min-h-0 flex-1 overflow-auto px-4 pb-4">
        {list.isLoading && <Spinner />}
        {!list.isLoading && tasks.length === 0 && (
          <li className="text-body-sm text-fg-muted">
            {t("spaces.iteration.planEmpty")}
          </li>
        )}
        {tasks.map((task) => (
          <li key={task.id}>
            <label className="flex cursor-pointer items-center gap-2 rounded-lg px-2 py-1.5 hover:bg-surface-hover">
              <Checkbox
                checked={selected.includes(task.id)}
                onChange={() => toggle(task.id)}
              />
              <span className="text-body-sm text-fg-muted">
                {displayIdOf(task)}
              </span>
              <span className="truncate text-body">{task.title}</span>
            </label>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** 文档与链接（3.22），最多 20 条。 */
function LinksSection({
  orgId,
  spaceId,
  iteration,
  canManage,
}: {
  orgId: string;
  spaceId: string;
  iteration: Iteration;
  canManage: boolean;
}) {
  const t = useT();
  const api = useApi();
  const [title, setTitle] = useState("");
  const [url, setUrl] = useState("");
  const add = useIterationMutation<{ title: string; url: string }>(
    orgId,
    spaceId,
    (input) => api.iterations.addLink(orgId, spaceId, iteration.id, input),
  );
  const remove = useIterationMutation<string>(orgId, spaceId, (linkId) =>
    api.iterations.removeLink(orgId, spaceId, iteration.id, linkId),
  );
  if (iteration.links.length === 0 && !canManage) return null;
  return (
    <div className="flex flex-col gap-2">
      <h2 className="text-body-sm font-medium">
        {t("spaces.iteration.links")}
      </h2>
      <ul className="flex flex-col gap-1">
        {iteration.links.map((link) => (
          <li key={link.id} className="flex items-center gap-2 text-body-sm">
            <a
              className="text-accent hover:underline"
              href={link.url}
              target="_blank"
              rel="noreferrer noopener"
            >
              {link.title || link.url}
            </a>
            {canManage && (
              <button
                type="button"
                aria-label={t("spaces.iteration.removeLink")}
                className="text-fg-muted hover:text-fg-default"
                onClick={() => remove.mutate(link.id)}
              >
                <Icon name="x" size={12} />
              </button>
            )}
          </li>
        ))}
      </ul>
      {canManage && iteration.links.length < 20 && (
        <form
          className="flex items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            add.mutate(
              { title, url },
              { onSuccess: () => (setTitle(""), setUrl("")) },
            );
          }}
        >
          <Field label={t("spaces.iteration.linkTitle")}>
            <TextInput
              value={title}
              maxLength={200}
              onChange={(event) => setTitle(event.target.value)}
            />
          </Field>
          <Field label={t("spaces.iteration.linkUrl")}>
            <TextInput
              type="url"
              required
              value={url}
              onChange={(event) => setUrl(event.target.value)}
            />
          </Field>
          <Button type="submit" variant="outline" loading={add.isPending}>
            {t("spaces.iteration.addLink")}
          </Button>
        </form>
      )}
    </div>
  );
}
