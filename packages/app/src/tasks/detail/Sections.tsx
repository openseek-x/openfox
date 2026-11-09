import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";

import {
  newIdempotencyKey,
  type RelationType,
  type Task,
  type TaskLink,
  type TaskRelation,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Field,
  Icon,
  IconButton,
  TextInput,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { displayIdOf } from "../parts";
import { StatusIcon } from "../primitives/StatusIcon";
import { taskErrorMessage, useUpdateTask } from "../queries";
import { TaskSearchDialog } from "./TaskSearchDialog";
import { ReadOnlyTip, useReadOnly } from "../../spaces/readonly";

export function Section({
  title,
  count,
  action,
  children,
}: {
  title: string;
  count?: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section aria-label={title} className="flex flex-col gap-1">
      <header className="flex h-8 items-center gap-2">
        <h3 className="text-body font-medium">{title}</h3>
        {count && <span className="text-body-sm text-fg-muted">{count}</span>}
        <span className="ml-auto flex items-center">{action}</span>
      </header>
      {children}
    </section>
  );
}

// ---------- 子任务 ----------

export function SubtasksSection({
  orgId,
  task,
  onOpenTask,
}: {
  orgId: string;
  task: Task;
  onOpenTask: (displayId: string) => void;
}) {
  const t = useT();
  const api = useApi();
  const subtasks = useQuery({
    queryKey: keys.taskSub(orgId, task.id, "subtasks"),
    queryFn: () =>
      api.tasks.list(orgId, task.space_id, {
        parent_task_id: task.id,
        limit: 100,
      }),
  });
  const list = subtasks.data?.tasks ?? [];
  const done = list.filter((x) => x.status_category === "DONE").length;
  if (list.length === 0) return null;
  return (
    <Section
      title={t("tasks.detail.subtasks")}
      count={list.length ? `${done}/${list.length}` : undefined}
    >
      <ul>
        {list.map((sub) => (
          <li key={sub.id}>
            <button
              type="button"
              onClick={() => onOpenTask(displayIdOf(sub))}
              className="flex h-8 w-full items-center gap-2 rounded-md px-1.5 text-left text-body hover:bg-surface-hover"
            >
              <StatusIcon
                category={sub.status_category}
                color={sub.status_color}
                labelled
              />
              <span className="w-16 shrink-0 text-caption text-fg-muted">
                {displayIdOf(sub)}
              </span>
              <span className="truncate">{sub.title}</span>
            </button>
          </li>
        ))}
      </ul>
    </Section>
  );
}

// ---------- 关联 ----------

/** 关联的显示文案：内置类型按 key 走多语言，自定义类型用自带文本。 */
export function relationLabel(
  t: Translate,
  relation: Pick<TaskRelation, "key" | "type_id" | "direction">,
  types: RelationType[],
): string {
  const side = relation.direction === "OUTWARD" ? "outward" : "inward";
  if (relation.key) return t(`tasks.relationLabels.${relation.key}.${side}`);
  const type = types.find((x) => x.id === relation.type_id);
  if (!type) return "";
  return side === "outward" ? type.outward_label : type.inward_label;
}

export function RelationsSection({
  orgId,
  task,
  relationTypes,
  onOpenTask,
  linking,
  onLinkingChange,
}: {
  orgId: string;
  task: Task;
  relationTypes: RelationType[];
  onOpenTask: (displayId: string) => void;
  linking: boolean;
  onLinkingChange: (open: boolean) => void;
}) {
  const t = useT();
  const readOnly = useReadOnly();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const queryKey = keys.taskSub(orgId, task.id, "relations");
  const update = useUpdateTask(orgId);
  const relations = useQuery({
    queryKey,
    queryFn: () => api.tasks.relations(orgId, task.space_id, task.id),
  });
  const remove = useMutation({
    mutationFn: (id: string) =>
      api.tasks.removeRelation(orgId, task.space_id, task.id, id),
    onMutate: async (id) => {
      await queryClient.cancelQueries({ queryKey });
      const previous = queryClient.getQueryData<TaskRelation[]>(queryKey);
      queryClient.setQueryData<TaskRelation[]>(queryKey, (list) =>
        list?.filter((r) => r.id !== id),
      );
      return { previous };
    },
    onError: (error, _id, context) => {
      queryClient.setQueryData(queryKey, context?.previous);
      toast(taskErrorMessage(t, error), { tone: "error" });
    },
    onSettled: (_result, _error, id, context) => {
      void queryClient.invalidateQueries({ queryKey });
      void queryClient.invalidateQueries({
        queryKey: keys.taskSub(orgId, task.id, "timeline"),
      });
      const targetId = context?.previous?.find((relation) => relation.id === id)
        ?.task.id;
      if (targetId) {
        void queryClient.invalidateQueries({
          queryKey: keys.taskSub(orgId, targetId, "relations"),
        });
        void queryClient.invalidateQueries({
          queryKey: keys.taskSub(orgId, targetId, "timeline"),
        });
      }
    },
  });
  // 选项值：<type_id>|OUTWARD / INWARD；对称类型只给一个方向
  const choices = relationTypes.flatMap((type) => {
    const outward = {
      value: `${type.id}|OUTWARD`,
      label: type.key
        ? t(`tasks.relationLabels.${type.key}.outward`)
        : type.outward_label,
    };
    if (type.symmetric) return [outward];
    return [
      {
        value: `${type.id}|INWARD`,
        label: type.key
          ? t(`tasks.relationLabels.${type.key}.inward`)
          : type.inward_label,
      },
      outward,
    ];
  });
  choices.unshift(
    { value: "parent", label: t("tasks.detail.isParentOf") },
    { value: "child", label: t("tasks.detail.isChildOf") },
  );
  const [choice, setChoice] = useState("builtin:BLOCKS|INWARD");
  useEffect(() => {
    if (!linking) setChoice("builtin:BLOCKS|INWARD");
  }, [linking]);
  const create = useMutation({
    mutationFn: async ({
      target,
      choice,
    }: {
      target: Task;
      choice: string;
    }) => {
      if (choice === "parent" || choice === "child") {
        const child = choice === "parent" ? target : task;
        const parent = choice === "parent" ? task : target;
        await update.mutateAsync({
          task: child,
          patch: { parent_task_id: parent.id },
          optimistic: {
            parent_task_id: parent.id,
            parent: {
              id: parent.id,
              display_id: parent.display_id,
              title: parent.title,
            },
          },
        });
        for (const id of new Set([child.parent_task_id, parent.id]))
          if (id)
            void queryClient.invalidateQueries({
              queryKey: keys.taskSub(orgId, id, "subtasks"),
            });
        return;
      }
      const [typeId, direction] = choice.split("|") as [
        string,
        "OUTWARD" | "INWARD",
      ];
      await api.tasks.createRelation(
        orgId,
        task.space_id,
        task.id,
        { type_id: typeId, target_task_id: target.id, direction },
        newIdempotencyKey(),
      );
    },
    onSuccess: (_result, { target }) => {
      onLinkingChange(false);
      void queryClient.invalidateQueries({ queryKey });
      void queryClient.invalidateQueries({
        queryKey: keys.taskSub(orgId, task.id, "timeline"),
      });
      void queryClient.invalidateQueries({
        queryKey: keys.taskSub(orgId, target.id, "relations"),
      });
      void queryClient.invalidateQueries({
        queryKey: keys.taskSub(orgId, target.id, "timeline"),
      });
    },
    onError: (error, { choice }) => {
      // 父子关系复用 useUpdateTask 的版本控制、回滚和错误提示。
      if (choice !== "parent" && choice !== "child")
        toast(taskErrorMessage(t, error), { tone: "error" });
    },
  });

  const list = relations.data ?? [];
  const grouped = new Map<string, TaskRelation[]>();
  for (const relation of list) {
    const label = relationLabel(t, relation, relationTypes);
    grouped.set(label, [...(grouped.get(label) ?? []), relation]);
  }
  return (
    <>
      {list.length > 0 && (
        <Section
          title={t("tasks.detail.relations")}
          count={list.length ? String(list.length) : undefined}
          action={
            <ReadOnlyTip>
              <IconButton
                label={t("tasks.detail.linkExisting")}
                icon={<Icon name="plus" />}
                size="28"
                disabled={readOnly}
                onClick={() => onLinkingChange(true)}
              />
            </ReadOnlyTip>
          }
        >
          {[...grouped].map(([label, items]) => (
            <div key={label} className="flex flex-col">
              <div className="px-1.5 pt-1 text-caption text-fg-muted">
                {label}
              </div>
              {items.map((relation) => (
                <div
                  key={relation.id}
                  className="group flex h-8 items-center gap-2 rounded-md px-1.5 hover:bg-surface-hover"
                >
                  <button
                    type="button"
                    onClick={() => onOpenTask(relation.task.display_id)}
                    className="flex min-w-0 flex-1 items-center gap-2 text-left text-body"
                  >
                    <StatusIcon
                      category={relation.task.status_category}
                      labelled
                    />
                    <span
                      title={relation.task.display_id.toUpperCase()}
                      className="w-28 shrink-0 truncate text-caption text-fg-muted"
                    >
                      {relation.task.display_id.toUpperCase()}
                    </span>
                    <span className="truncate">{relation.task.title}</span>
                  </button>
                  {!readOnly && (
                    <IconButton
                      label={t("tasks.detail.removeRelation")}
                      icon={<Icon name="x" />}
                      size="28"
                      disabled={remove.isPending}
                      className="opacity-0 group-focus-within:opacity-100 group-hover:opacity-100"
                      onClick={() => remove.mutate(relation.id)}
                    />
                  )}
                </div>
              ))}
            </div>
          ))}
        </Section>
      )}
      {linking && !readOnly && (
        <TaskSearchDialog
          orgId={orgId}
          spaceId={task.space_id}
          title={t("tasks.detail.selectRelatedTask")}
          excludeIds={[
            task.id,
            ...list
              .filter((r) => `${r.type_id}|${r.direction}` === choice)
              .map((r) => r.task.id),
          ]}
          busy={create.isPending}
          onClose={() => onLinkingChange(false)}
          onPick={(target) => {
            if (!create.isPending && !readOnly)
              create.mutate({ target, choice });
          }}
          header={
            <label className="flex items-center gap-2 text-body-sm">
              <span className="text-fg-muted">
                {t("tasks.detail.relationType")}
              </span>
              <select
                disabled={create.isPending}
                value={choice}
                onChange={(event) => setChoice(event.target.value)}
                className="h-8 rounded-md border border-line-default bg-surface-card px-2"
              >
                {choices.map((c) => (
                  <option key={c.value} value={c.value}>
                    {c.label}
                  </option>
                ))}
              </select>
            </label>
          }
        />
      )}
    </>
  );
}

// ---------- 链接 ----------

const URL_RE = /^https?:\/\/\S+$/i;

export function LinksSection({
  orgId,
  task,
  editing,
  onEditingChange,
}: {
  orgId: string;
  task: Task;
  /** null = 关闭；"new" = 添加；其他 = 编辑的链接 */
  editing: TaskLink | "new" | null;
  onEditingChange: (value: TaskLink | "new" | null) => void;
}) {
  const t = useT();
  const readOnly = useReadOnly();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const queryKey = keys.taskSub(orgId, task.id, "links");
  const links = useQuery({
    queryKey,
    queryFn: () => api.tasks.links(orgId, task.space_id, task.id),
  });
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey });
    void queryClient.invalidateQueries({
      queryKey: keys.taskSub(orgId, task.id, "timeline"),
    });
  };
  const remove = useMutation({
    mutationFn: (id: string) =>
      api.tasks.removeLink(orgId, task.space_id, task.id, id),
    onSuccess: refresh,
    onError: (error) => toast(taskErrorMessage(t, error), { tone: "error" }),
  });
  const list = links.data ?? [];
  if (list.length === 0 && !editing) return null;
  return (
    <Section
      title={t("tasks.detail.links")}
      count={list.length ? String(list.length) : undefined}
      action={
        <ReadOnlyTip>
          <IconButton
            label={t("tasks.detail.addLink")}
            icon={<Icon name="plus" />}
            size="28"
            disabled={readOnly}
            onClick={() => onEditingChange("new")}
          />
        </ReadOnlyTip>
      }
    >
      <ul>
        {list.map((link) => (
          <li
            key={link.id}
            className="group flex h-8 items-center gap-2 rounded-md px-1.5 hover:bg-surface-hover"
          >
            <Icon name="link" className="text-fg-muted" />
            <a
              href={link.url}
              target="_blank"
              rel="noopener noreferrer"
              className="min-w-0 flex-1 truncate text-body hover:underline"
            >
              {link.title || link.url}
            </a>
            {!readOnly && (
              <>
                <IconButton
                  label={t("tasks.detail.editLink")}
                  icon={<Icon name="pencil" />}
                  size="28"
                  className="opacity-0 group-focus-within:opacity-100 group-hover:opacity-100"
                  onClick={() => onEditingChange(link)}
                />
                <IconButton
                  label={t("tasks.detail.removeLink")}
                  icon={<Icon name="trash-2" />}
                  size="28"
                  className="opacity-0 group-focus-within:opacity-100 group-hover:opacity-100"
                  onClick={() => remove.mutate(link.id)}
                />
              </>
            )}
          </li>
        ))}
      </ul>
      {editing && (
        <LinkDialog
          initial={editing === "new" ? null : editing}
          onClose={() => onEditingChange(null)}
          onSave={async (url, title) => {
            try {
              if (editing === "new")
                await api.tasks.createLink(
                  orgId,
                  task.space_id,
                  task.id,
                  title ? { url, title } : { url },
                  newIdempotencyKey(),
                );
              else
                await api.tasks.updateLink(
                  orgId,
                  task.space_id,
                  task.id,
                  editing.id,
                  {
                    url,
                    title,
                    version: editing.version,
                  },
                );
              refresh();
              onEditingChange(null);
            } catch (error) {
              toast(taskErrorMessage(t, error), { tone: "error" });
              if (editing !== "new") refresh();
            }
          }}
        />
      )}
    </Section>
  );
}

function LinkDialog({
  initial,
  onClose,
  onSave,
}: {
  initial: TaskLink | null;
  onClose: () => void;
  onSave: (url: string, title: string) => Promise<void>;
}) {
  const t = useT();
  const [url, setUrl] = useState(initial?.url ?? "");
  const [title, setTitle] = useState(initial?.title ?? "");
  const [touched, setTouched] = useState(false);
  const [saving, setSaving] = useState(false);
  const valid = URL_RE.test(url.trim());
  return (
    <Dialog
      open
      onClose={onClose}
      title={initial ? t("tasks.detail.editLink") : t("tasks.detail.addLink")}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            type="submit"
            form="task-link-form"
            disabled={!valid}
            loading={saving}
          >
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form
        id="task-link-form"
        className="flex flex-col gap-3"
        onSubmit={async (event) => {
          event.preventDefault();
          setTouched(true);
          if (!valid) return;
          setSaving(true);
          await onSave(url.trim(), title.trim());
          setSaving(false);
        }}
      >
        <Field
          label={t("tasks.detail.linkUrl")}
          error={touched && !valid ? t("tasks.detail.linkInvalid") : undefined}
        >
          <TextInput
            data-autofocus
            value={url}
            maxLength={2048}
            placeholder="https://"
            onChange={(event) => setUrl(event.target.value)}
            onBlur={() => setTouched(true)}
          />
        </Field>
        <Field label={t("tasks.detail.linkTitle")}>
          <TextInput
            value={title}
            maxLength={200}
            onChange={(event) => setTitle(event.target.value)}
          />
        </Field>
      </form>
    </Dialog>
  );
}
