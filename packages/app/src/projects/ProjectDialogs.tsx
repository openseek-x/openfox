import { useRef, useState } from "react";

import type { Project, ProjectStatus, TaskPriority } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Icon,
  IconButton,
  Popover,
  useToast,
} from "@openfox/ui";

import { useApi } from "../context";
import { ComposeFields, ComposeShell } from "../tasks/create/ComposeShell";
import { PropertyChip } from "../tasks/pickers";
import { usePeople } from "../tasks/queries";
import { EmojiGrid } from "./EmojiGrid";
import { ProjectIcon, ProjectPropertyChips } from "./parts";
import { useProjectMutation } from "./queries";

/** 新建专项：撰写式弹窗，与新建任务共用外壳与属性胶囊。 */
export function CreateProjectDialog({
  orgId,
  space,
  onClose,
  onCreated,
}: {
  orgId: string;
  space: { id: string; name: string; visibility: string };
  onClose: () => void;
  onCreated: (project: Project) => void;
}) {
  const t = useT();
  const api = useApi();
  const people = usePeople(orgId, space);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [status, setStatus] = useState<ProjectStatus>("planned");
  const [priority, setPriority] = useState<TaskPriority>("NONE");
  const [lead, setLead] = useState("");
  const [icon, setIcon] = useState<string | null>(null);
  const [emojiOpen, setEmojiOpen] = useState(false);
  const emojiAnchor = useRef<HTMLButtonElement | null>(null);
  const create = useProjectMutation(orgId, () =>
    api.projects.create(orgId, {
      space_id: space.id,
      name: name.trim(),
      status,
      priority,
      ...(description ? { description } : {}),
      ...(lead ? { lead_user_id: lead } : {}),
      ...(icon ? { icon } : {}),
    }),
  );
  const submit = () => {
    if (name.trim() && !create.isPending)
      create.mutate(undefined, {
        onSuccess: (project) => {
          onClose();
          onCreated(project);
        },
      });
  };
  return (
    <ComposeShell
      label={t("projects.create.title")}
      onBackdrop={onClose}
      style={{ width: 672 }}
      breadcrumb={
        <>
          <span className="max-w-[160px] truncate rounded-full border border-line-default px-2 py-0.5">
            {space.name}
          </span>
          <Icon name="chevron-right" size={12} className="text-fg-subtle" />
          <span className="truncate text-fg-default">
            {t("projects.create.title")}
          </span>
        </>
      }
      actions={
        <IconButton
          label={t("common.close")}
          icon={<Icon name="x" />}
          size="28"
          onClick={onClose}
        />
      }
    >
      <ComposeFields
        title={name}
        onTitle={setName}
        titleMax={80}
        titlePlaceholder={t("projects.namePlaceholder")}
        description={description}
        onDescription={setDescription}
        descPlaceholder={t("projects.descPlaceholder")}
        onSubmit={submit}
      >
        <div>
          <button
            ref={emojiAnchor}
            type="button"
            aria-label={t("projects.emoji")}
            aria-expanded={emojiOpen}
            onClick={() => setEmojiOpen((value) => !value)}
            className="flex size-9 items-center justify-center rounded-lg border border-line-default hover:bg-surface-hover"
          >
            <ProjectIcon icon={icon} size={18} />
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
              value={icon}
              onChange={(emoji) => {
                setIcon(emoji);
                setEmojiOpen(false);
              }}
            />
          </Popover>
        </div>
      </ComposeFields>
      <div className="flex flex-wrap items-center gap-1.5 px-5 py-3">
        <ProjectPropertyChips
          status={status}
          priority={priority}
          lead={lead}
          people={people}
          onStatus={setStatus}
          onPriority={setPriority}
          onLead={setLead}
        />
        <PropertyChip
          label={t("projects.spaces")}
          icon={<Icon name="folder" size={14} />}
        >
          {space.name}
        </PropertyChip>
      </div>
      <div className="flex h-14 shrink-0 items-center justify-end border-t border-line-default px-5">
        <Button
          onClick={submit}
          disabled={!name.trim()}
          loading={create.isPending}
        >
          {t("projects.save")}
        </Button>
      </div>
    </ComposeShell>
  );
}

/** 删除确认（6.12）：显示将被清空专项的任务数。 */
export function DeleteProjectDialog({
  orgId,
  project,
  onClose,
  onDeleted,
}: {
  orgId: string;
  project: Project | null;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const [count, setCount] = useState<number | null>(null);
  const [loadedFor, setLoadedFor] = useState<string | null>(null);
  // 打开时取一次影响范围
  if (project && loadedFor !== project.id) {
    setLoadedFor(project.id);
    setCount(null);
    api.projects
      .deleteImpact(orgId, project.id)
      .then((impact) => setCount(impact.task_count))
      .catch(() => setCount(0));
  }
  const remove = useProjectMutation(orgId, (target: Project) =>
    api.projects.remove(orgId, target.id, target.version),
  );
  return (
    <ConfirmDialog
      open={Boolean(project)}
      danger
      title={t("projects.delete.title", { name: project?.name ?? "" })}
      description={
        count === null
          ? t("tasks.empty.loading")
          : t("projects.delete.description", { count })
      }
      confirmLabel={t("projects.delete.confirm")}
      confirmDisabled={count === null}
      loading={remove.isPending}
      onCancel={onClose}
      onConfirm={() =>
        project &&
        remove.mutate(project, {
          onSuccess: () => {
            toast(t("projects.delete.done"));
            onDeleted();
          },
        })
      }
    />
  );
}
