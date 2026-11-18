import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import {
  SortableContext,
  sortableKeyboardCoordinates,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { SortableTypeRow } from "./SortableTypeRow";
import { moveTaskType } from "./type-order";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";

import {
  isApiError,
  type CustomField,
  type SpacePurpose,
  type SpaceTaskType,
  type TaskStatus,
  type TaskStatusCategory,
  type TaskTypeTemplate,
  type UpdateSpaceTaskTypeInput,
} from "@openfox/api";
import { useT, type MessageKey, type Translate } from "@openfox/i18n";
import {
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  Dialog,
  Field,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  Popover,
  Select,
  Spinner,
  Switch,
  Tabs,
  TextInput,
  Textarea,
  Tooltip,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { CustomFieldDialog } from "../../settings/custom-fields/CustomFieldDialog";
import { KIND_META } from "../../settings/custom-fields/kinds";
import { STATUS_PAGE_CATEGORIES } from "../../settings/statuses/order";
import { STATUS_CATEGORIES } from "../../tasks/model";
import { StatusIcon } from "../../tasks/primitives/StatusIcon";
import { TaskTypeIcon } from "../../tasks/primitives/TaskTypeIcon";
import { useStatuses } from "../../tasks/queries";
import { spaceErrorMessage } from "../queries";
import { SPACE_ICON_CHOICES } from "../SpaceIcon";
import { spaceSettingsPath } from "../SpacesNav";
import {
  SPACE_PURPOSES,
  purposeLabel,
  type SpacePageProps,
} from "./GeneralPage";
import { PageHeading, RetiredBanner } from "./parts";

/** 空间的全部任务类型（含停用；不含归档），与任务模块共用缓存键。 */
function useAllSpaceTypes(
  orgId: string,
  spaceId: string,
  includeArchived = false,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.spaceTaskTypes(orgId, spaceId),
    queryFn: () => api.tasks.types(orgId, spaceId),
    // 进入设置时重读工作流计数，避免沿用任务视图刚缓存的类型列表。
    staleTime: 0,
    select: (types) =>
      includeArchived ? types : types.filter((type) => !type.archived_at),
  });
}

/** 模板名与描述按 key 翻译，缺译时用后端的英文默认值。 */
export function templateText(t: Translate, template: TaskTypeTemplate) {
  const lookup = (field: "name" | "description", fallback: string) => {
    const key = `spaces.templates.${template.key}.${field}` as MessageKey;
    const text = t(key);
    return text === key ? fallback : text;
  };
  return {
    name: lookup("name", template.name),
    description: lookup("description", template.description),
  };
}

/** 推荐区只列出与现有类型不重名的模板；「全部」时工程与产品类排前。 */
export function recommendedTemplates(
  t: Translate,
  templates: readonly TaskTypeTemplate[],
  existing: readonly { name: string }[],
  purpose: SpacePurpose | "all",
): TaskTypeTemplate[] {
  const names = new Set(existing.map((type) => type.name.trim().toLowerCase()));
  const engineering = (template: TaskTypeTemplate) =>
    template.purposes.includes("software_development") ? 0 : 1;
  return templates
    .filter(
      (template) => purpose === "all" || template.purposes.includes(purpose),
    )
    .filter(
      (template) =>
        !names.has(templateText(t, template).name.toLowerCase()) &&
        !names.has(template.name.toLowerCase()),
    )
    .sort((a, b) => (purpose === "all" ? engineering(a) - engineering(b) : 0));
}

function useTypeMutation(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  const invalidate = () => {
    void queryClient.invalidateQueries({
      queryKey: keys.spaceTaskTypes(orgId, spaceId),
    });
    void queryClient.invalidateQueries({ queryKey: keys.orgTaskTypes(orgId) });
    // 列表、看板、详情和收件箱的任务响应也带有类型图标快照。
    void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
    void queryClient.invalidateQueries({ queryKey: keys.inbox(orgId) });
    void queryClient.invalidateQueries({
      queryKey: keys.space(orgId, spaceId),
    });
  };
  return {
    invalidate,
    update: (
      type: SpaceTaskType,
      patch: Omit<UpdateSpaceTaskTypeInput, "version">,
    ) =>
      api.spaceTaskTypes.update(orgId, spaceId, type.id, {
        ...patch,
        version: type.version,
      }),
  };
}

/** 任务类型 & 状态：空间内排序，归档行保持原槽位。 */
export function TaskTypesPage({
  orgId,
  space,
  canManage,
  retired,
}: SpacePageProps) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const manage = canManage && !retired;
  const types = useAllSpaceTypes(orgId, space.id, true);
  const { update, invalidate } = useTypeMutation(orgId, space.id);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<SpaceTaskType | null>(null);
  const allTypes = types.data ?? [];
  const list = allTypes.filter((type) => !type.archived_at);
  const client = useQueryClient();
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );
  const reorder = useMutation({
    mutationFn: (next: SpaceTaskType[]) =>
      api.spaceTaskTypes.reorder(
        orgId,
        space.id,
        next.map(({ id, version }) => ({ id, version })),
      ),
    onSuccess: (result) =>
      client.setQueryData(keys.spaceTaskTypes(orgId, space.id), result),
    onError: (error) =>
      toast(spaceErrorMessage(t, error, "spaces.taskTypes.orderFailed"), {
        tone: "error",
      }),
    onSettled: invalidate,
  });
  const move = (id: string, target: string) => {
    if (!manage || reorder.isPending) return;
    const next = moveTaskType(allTypes, id, target);
    if (next !== allTypes) reorder.mutate(next);
  };

  const toggle = useMutation({
    mutationFn: (vars: { type: SpaceTaskType; enabled: boolean }) =>
      update(vars.type, { enabled: vars.enabled }),
    onError: (error) =>
      toast(spaceErrorMessage(t, error, "spaces.taskTypes.toggleFailed"), {
        tone: "error",
      }),
    onSettled: invalidate,
  });
  const setDefault = useMutation({
    mutationFn: (type: SpaceTaskType) => update(type, { is_default: true }),
    onSuccess: (_result, type) =>
      toast(t("spaces.taskTypes.defaultSet", { name: type.name })),
    onError: (error) =>
      toast(spaceErrorMessage(t, error, "spaces.taskTypes.defaultFailed"), {
        tone: "error",
      }),
    onSettled: invalidate,
  });
  const remove = useMutation({
    mutationFn: (type: SpaceTaskType) =>
      api.spaceTaskTypes.remove(orgId, space.id, type.id, type.version),
    onSuccess: () => {
      setDeleting(null);
      toast(t("spaces.taskTypes.deleted"));
    },
    onError: (error) => {
      setDeleting(null);
      toast(
        isApiError(error, "TYPE_IN_USE")
          ? t("spaces.errors.typeInUse")
          : spaceErrorMessage(t, error, "spaces.taskTypes.deleteFailed"),
        { tone: "error" },
      );
    },
    onSettled: invalidate,
  });
  const requestDelete = (type: SpaceTaskType) => {
    // 有任务的类型不弹确认，直接提示（Figma 4.6）
    if ((type.task_count ?? 0) > 0)
      toast(t("spaces.errors.typeInUse"), { tone: "error" });
    else setDeleting(type);
  };

  return (
    <div className="mx-auto flex w-full max-w-[768px] flex-col gap-5 px-6 pt-6 pb-12">
      {retired && <RetiredBanner />}
      <PageHeading
        title={t("spaces.sections.taskTypes")}
        description={t("spaces.taskTypes.description")}
      />
      <div className="flex items-center justify-between gap-3">
        <span className="text-body-sm text-fg-muted">
          {t("spaces.taskTypes.ownedNote")}
        </span>
        {manage && (
          <Button
            size="28"
            variant="outline"
            leading={<Icon name="plus" size={14} />}
            onClick={() => setCreating(true)}
          >
            {t("spaces.taskTypes.add")}
          </Button>
        )}
      </div>
      {types.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : types.isError ? (
        <p role="alert" className="text-body text-fg-muted">
          {t("spaces.taskTypes.loadFailed")}
        </p>
      ) : (
        <DndContext
          sensors={sensors}
          collisionDetection={closestCenter}
          accessibility={{
            screenReaderInstructions: {
              draggable: t("spaces.taskTypes.orderInstructions"),
            },
          }}
          onDragEnd={({ active, over }) => {
            if (over) move(String(active.id), String(over.id));
          }}
        >
          <SortableContext
            items={list.map((type) => type.id)}
            strategy={verticalListSortingStrategy}
          >
            <ul
              aria-label={t("spaces.sections.taskTypes")}
              className="flex flex-col overflow-hidden rounded-[10px] border border-line-default bg-surface-card"
            >
              {list.map((type, index) => (
                <SortableTypeRow
                  key={type.id}
                  id={type.id}
                  name={type.name}
                  index={index}
                  enabled={manage && !reorder.isPending}
                >
                  <div
                    role="button"
                    tabIndex={0}
                    aria-label={type.name}
                    onClick={() =>
                      navigate(
                        `${spaceSettingsPath(orgId, space.id, "task-types")}/${type.id}`,
                      )
                    }
                    onKeyDown={(event) => {
                      if (event.key === "Enter")
                        navigate(
                          `${spaceSettingsPath(orgId, space.id, "task-types")}/${type.id}`,
                        );
                    }}
                    className="flex min-w-0 flex-1 cursor-pointer items-center gap-3 p-4 hover:bg-surface-hover"
                  >
                    <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-surface-muted">
                      <TaskTypeIcon icon={type.icon} size={16} />
                    </span>
                    <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                      <span className="truncate text-body font-medium">
                        {type.name}
                      </span>
                      {type.description && (
                        <span className="truncate text-caption text-fg-muted">
                          {type.description}
                        </span>
                      )}
                      <span className="text-caption text-fg-muted">
                        {t("spaces.taskTypes.stats", {
                          statuses: type.statuses.length,
                          fields: type.field_count ?? 0,
                          tasks: type.task_count ?? 0,
                        })}
                      </span>
                    </span>
                    {type.is_default && (
                      <Badge tone="neutral">
                        {t("spaces.taskTypes.defaultBadge")}
                      </Badge>
                    )}
                    {!type.enabled && (
                      <Badge tone="neutral">
                        {t("spaces.taskTypes.disabledBadge")}
                      </Badge>
                    )}
                    <span
                      onClick={(event) => event.stopPropagation()}
                      className="flex items-center gap-1"
                    >
                      {type.is_default ? (
                        <Tooltip content={t("spaces.taskTypes.defaultLocked")}>
                          <span tabIndex={0} className="inline-flex">
                            <Switch
                              checked
                              disabled
                              label={t("spaces.taskTypes.enableFor", {
                                name: type.name,
                              })}
                            />
                          </span>
                        </Tooltip>
                      ) : (
                        <span
                          title={
                            type.enabled
                              ? t("spaces.taskTypes.activeHint")
                              : t("spaces.taskTypes.inactiveHint")
                          }
                          className="inline-flex"
                        >
                          <Switch
                            checked={type.enabled}
                            disabled={!manage}
                            label={t("spaces.taskTypes.enableFor", {
                              name: type.name,
                            })}
                            onCheckedChange={(enabled) =>
                              toggle.mutate({ type, enabled })
                            }
                          />
                        </span>
                      )}
                      {manage && (
                        <Menu
                          label={t("spaces.taskTypes.more", {
                            name: type.name,
                          })}
                          placement="bottom-end"
                          trigger={({ ref, toggle: open, open: isOpen }) => (
                            <IconButton
                              ref={ref}
                              label={t("spaces.taskTypes.more", {
                                name: type.name,
                              })}
                              icon={<Icon name="ellipsis" />}
                              size="32"
                              aria-expanded={isOpen}
                              onClick={open}
                            />
                          )}
                        >
                          <MenuItem
                            disabled={reorder.isPending || index === 0}
                            onSelect={() => move(type.id, list[index - 1]!.id)}
                          >
                            {t("spaces.taskTypes.moveTypeUp")}
                          </MenuItem>
                          <MenuItem
                            disabled={
                              reorder.isPending || index === list.length - 1
                            }
                            onSelect={() => move(type.id, list[index + 1]!.id)}
                          >
                            {t("spaces.taskTypes.moveTypeDown")}
                          </MenuItem>
                          <MenuSeparator />
                          {!type.is_default && type.enabled && (
                            <>
                              <MenuItem
                                icon={<Icon name="star" />}
                                onSelect={() => setDefault.mutate(type)}
                              >
                                {t("spaces.taskTypes.setDefault")}
                              </MenuItem>
                              <MenuSeparator />
                            </>
                          )}
                          <MenuItem
                            danger
                            icon={<Icon name="trash-2" />}
                            disabled={type.is_default}
                            disabledReason={t(
                              "spaces.taskTypes.defaultNotDeletable",
                            )}
                            onSelect={() => requestDelete(type)}
                          >
                            {t("spaces.taskTypes.delete")}
                          </MenuItem>
                        </Menu>
                      )}
                    </span>
                    <Icon name="chevron-right" className="text-fg-muted" />
                  </div>
                </SortableTypeRow>
              ))}
            </ul>
          </SortableContext>
        </DndContext>
      )}
      {manage && types.data && (
        <Recommended
          orgId={orgId}
          spaceId={space.id}
          purpose={space.purpose}
          existing={list}
          onAdded={invalidate}
        />
      )}
      {creating && (
        <TaskTypeDialog
          orgId={orgId}
          spaceId={space.id}
          onClose={() => setCreating(false)}
          onSaved={() => {
            setCreating(false);
            invalidate();
          }}
        />
      )}
      <ConfirmDialog
        open={Boolean(deleting)}
        danger
        title={t("spaces.taskTypes.deleteTitle", {
          name: deleting?.name ?? "",
        })}
        description={t("spaces.taskTypes.deleteDescription")}
        confirmLabel={t("spaces.taskTypes.delete")}
        loading={remove.isPending}
        onCancel={() => setDeleting(null)}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </div>
  );
}

/** 推荐任务类型（Figma 页 17 · 4.1 / 4.8 / 4.9）。 */
function Recommended({
  orgId,
  spaceId,
  purpose,
  existing,
  onAdded,
}: {
  orgId: string;
  spaceId: string;
  purpose: SpacePurpose;
  existing: SpaceTaskType[];
  onAdded: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const [filter, setFilter] = useState<SpacePurpose | "all">(purpose);
  const [adding, setAdding] = useState<string | null>(null);
  const templates = useQuery({
    queryKey: keys.taskTypeTemplates(orgId),
    queryFn: () => api.taskTypeTemplates.list(orgId),
    staleTime: Infinity,
  });
  const rows = recommendedTemplates(t, templates.data ?? [], existing, filter);
  const add = useMutation({
    mutationFn: (template: TaskTypeTemplate) => {
      const text = templateText(t, template);
      return api.spaceTaskTypes.create(orgId, spaceId, {
        name: text.name,
        description: text.description,
        icon: template.icon,
      });
    },
    onMutate: (template) => setAdding(template.key),
    onSuccess: (type) => {
      toast(t("spaces.taskTypes.templateAdded", { name: type.name }));
      onAdded();
    },
    onError: (error) =>
      toast(spaceErrorMessage(t, error, "spaces.taskTypes.templateAddFailed"), {
        tone: "error",
      }),
    onSettled: () => setAdding(null),
  });
  if (!templates.data || (rows.length === 0 && filter === "all")) return null;
  return (
    <section
      aria-label={t("spaces.taskTypes.recommended")}
      className="flex flex-col gap-3"
    >
      <div className="flex items-center justify-between">
        <h2 className="text-body font-semibold">
          {t("spaces.taskTypes.recommended")}
        </h2>
        <Select<SpacePurpose | "all">
          aria-label={t("spaces.taskTypes.category")}
          value={filter}
          onChange={setFilter}
          className="h-7 w-[200px] text-caption"
          options={[
            { value: "all", label: t("spaces.taskTypes.allCategories") },
            ...SPACE_PURPOSES.map((value) => ({
              value,
              label: t(purposeLabel(value)),
            })),
          ]}
        />
      </div>
      <ul className="flex flex-col overflow-hidden rounded-[10px] border border-line-default bg-surface-card">
        {rows.map((template, index) => {
          const text = templateText(t, template);
          const categories = template.purposes
            .slice(0, 2)
            .map((p) => t(purposeLabel(p)));
          return (
            <li
              key={template.key}
              className={`flex items-center gap-3 p-4 ${index > 0 ? "border-t border-line-soft/50" : ""}`}
            >
              <span
                className="flex size-8 shrink-0 items-center justify-center rounded-lg"
                style={{ backgroundColor: `${template.color}24` }}
              >
                <TaskTypeIcon
                  icon={template.icon}
                  color={template.color}
                  size={16}
                />
              </span>
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="truncate text-body font-medium">
                  {text.name}
                </span>
                <span className="truncate text-caption text-fg-muted">
                  {text.description}
                </span>
                <span className="text-caption text-fg-subtle">
                  {categories.join(" · ")}
                  {template.purposes.length > 2 && " ..."}
                </span>
              </span>
              <Button
                size="28"
                variant="outline"
                loading={adding === template.key}
                disabled={Boolean(adding)}
                aria-label={t("spaces.taskTypes.addTemplate", {
                  name: text.name,
                })}
                onClick={() => add.mutate(template)}
              >
                {adding === template.key
                  ? t("spaces.taskTypes.adding")
                  : t("spaces.taskTypes.addShort")}
              </Button>
            </li>
          );
        })}
        {rows.length === 0 && (
          <li className="p-4 text-body-sm text-fg-muted">
            {t("spaces.taskTypes.noRecommended")}
          </li>
        )}
      </ul>
    </section>
  );
}

/** 新建任务类型（Figma 页 17 · 4.7，宽 448）。 */
function TaskTypeDialog({
  orgId,
  spaceId,
  onClose,
  onSaved,
}: {
  orgId: string;
  spaceId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [commitType, setCommitType] = useState("");
  const commitValid = validCommitType(commitType);
  const create = useMutation({
    mutationFn: () =>
      api.spaceTaskTypes.create(orgId, spaceId, {
        name: name.trim(),
        description: description.trim() || undefined,
        commit_type: commitType.trim() || null,
        icon: "package",
      }),
    onSuccess: () => {
      toast(t("spaces.taskTypes.created"));
      onSaved();
    },
    onError: (error) =>
      toast(spaceErrorMessage(t, error, "spaces.taskTypes.createFailed"), {
        tone: "error",
      }),
  });
  return (
    <Dialog
      open
      width={448}
      onClose={create.isPending ? undefined : onClose}
      title={t("spaces.taskTypes.createTitle")}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={create.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button
            disabled={!name.trim() || !commitValid}
            loading={create.isPending}
            onClick={() => create.mutate()}
          >
            {t("spaces.common.save")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label={t("spaces.taskTypes.name")}>
          <TextInput
            autoFocus
            maxLength={64}
            value={name}
            onChange={(event) => setName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && name.trim() && commitValid)
                create.mutate();
            }}
          />
        </Field>
        <CommitTypeField
          value={commitType}
          onChange={setCommitType}
          disabled={create.isPending}
        />
        <Field label={t("spaces.taskTypes.descriptionLabel")}>
          <Textarea
            rows={3}
            maxLength={1000}
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
      </div>
    </Dialog>
  );
}

type DetailTab = "basic" | "fields" | "workflow";

/** 任务类型详情（Figma 页 17 · 5.x）：基本信息 / 自定义字段 / 状态。 */
export function TaskTypeDetailPage({
  orgId,
  space,
  canManage,
  retired,
  typeId,
}: SpacePageProps & { typeId: string }) {
  const t = useT();
  const [params, setParams] = useSearchParams();
  const types = useAllSpaceTypes(orgId, space.id);
  const manage = canManage && !retired;
  const tab: DetailTab =
    params.get("tab") === "workflow"
      ? "workflow"
      : params.get("tab") === "fields"
        ? "fields"
        : "basic";
  const api = useApi();
  const fields = useQuery({
    queryKey: [...keys.spaceTaskTypes(orgId, space.id), "fields", typeId],
    queryFn: () => api.typeFields.list(orgId, space.id, typeId),
  });
  if (types.isLoading) return null;
  const type = types.data?.find((item) => item.id === typeId);
  if (!type)
    return (
      <div className="mx-auto w-full max-w-[768px] px-6 pt-6">
        <p className="text-body text-fg-muted">
          {t("spaces.taskTypes.notFound")}
        </p>
      </div>
    );
  const count = (n: number) => (n > 0 ? ` (${n})` : "");
  return (
    <div className="mx-auto flex w-full max-w-[768px] flex-col gap-5 px-6 pt-6 pb-12">
      {retired && <RetiredBanner />}
      <header className="flex items-center gap-3">
        <span className="flex size-9 items-center justify-center rounded-lg bg-surface-muted">
          <TaskTypeIcon icon={type.icon} size={20} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col">
          <h1 className="truncate text-title font-semibold">{type.name}</h1>
          <span className="text-body-sm text-fg-muted">
            {t("spaces.taskTypes.ownedNote")}
          </span>
        </div>
        {type.is_default ? (
          <Badge tone="neutral">{t("spaces.taskTypes.defaultBadge")}</Badge>
        ) : (
          !manage && (
            <Badge tone="neutral">{t("spaces.taskTypes.readOnly")}</Badge>
          )
        )}
      </header>
      <Tabs
        label={type.name}
        value={tab}
        onChange={(id) => {
          const next = new URLSearchParams(params);
          if (id === "basic") next.delete("tab");
          else next.set("tab", id);
          setParams(next, { replace: true });
        }}
        tabs={[
          { id: "basic", label: t("spaces.taskTypes.tabBasic") },
          {
            id: "fields",
            label: `${t("spaces.taskTypes.tabFields")}${count(fields.data?.length ?? type.field_count ?? 0)}`,
          },
          {
            id: "workflow",
            label: `${t("spaces.taskTypes.tabStatuses")}${count(type.statuses.length)}`,
          },
        ]}
      />
      {tab === "basic" && (
        <BasicTab
          orgId={orgId}
          spaceId={space.id}
          type={type}
          manage={manage}
        />
      )}
      {tab === "fields" && (
        <FieldsTab
          orgId={orgId}
          spaceId={space.id}
          typeId={type.id}
          manage={manage}
          fields={fields.data}
          loading={fields.isLoading}
        />
      )}
      {tab === "workflow" && (
        <WorkflowTab
          orgId={orgId}
          spaceId={space.id}
          type={type}
          manage={manage}
        />
      )}
    </div>
  );
}

function BasicTab({
  orgId,
  spaceId,
  type,
  manage,
}: {
  orgId: string;
  spaceId: string;
  type: SpaceTaskType;
  manage: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const { update, invalidate } = useTypeMutation(orgId, spaceId);
  const [name, setName] = useState(type.name);
  const [description, setDescription] = useState(type.description);
  const [commitType, setCommitType] = useState(type.commit_type ?? "");
  const [icon, setIcon] = useState(type.icon ?? "");
  const [pickingIcon, setPickingIcon] = useState(false);
  useEffect(() => setName(type.name), [type.name]);
  useEffect(() => setDescription(type.description), [type.description]);
  useEffect(
    () => setCommitType(type.commit_type ?? ""),
    [type.commit_type, type.version],
  );
  const save = useMutation({
    mutationFn: (patch: {
      name?: string;
      description?: string;
      commit_type?: string | null;
      icon?: string;
    }) => update(type, patch),
    onSuccess: () => setPickingIcon(false),
    onError: (error) =>
      toast(spaceErrorMessage(t, error, "spaces.taskTypes.saveFailed"), {
        tone: "error",
      }),
    onSettled: invalidate,
  });
  return (
    <div className="flex flex-col gap-4">
      <Field label={t("spaces.taskTypes.icon")}>
        <Button
          variant="outline"
          disabled={!manage || save.isPending}
          leading={<TaskTypeIcon icon={type.icon} />}
          onClick={() => {
            setIcon(type.icon ?? "");
            setPickingIcon(true);
          }}
        >
          {t("spaces.taskTypes.chooseIcon")}
        </Button>
      </Field>
      <Dialog
        open={pickingIcon}
        width={360}
        title={t("spaces.taskTypes.chooseIcon")}
        onClose={save.isPending ? undefined : () => setPickingIcon(false)}
        footer={
          <>
            <Button
              variant="outline"
              disabled={save.isPending}
              onClick={() => setPickingIcon(false)}
            >
              {t("common.cancel")}
            </Button>
            <Button
              loading={save.isPending}
              disabled={!manage || save.isPending || icon === (type.icon ?? "")}
              onClick={() => save.mutate({ icon })}
            >
              {t("spaces.common.save")}
            </Button>
          </>
        }
      >
        <div className="grid grid-cols-8 gap-1">
          {["square-check", "package", ...SPACE_ICON_CHOICES].map((name) => (
            <button
              key={name}
              type="button"
              aria-label={name}
              aria-pressed={icon === name}
              disabled={!manage || save.isPending}
              onClick={() => setIcon(name)}
              className="flex size-8 items-center justify-center rounded-lg hover:bg-surface-hover aria-pressed:bg-surface-active disabled:opacity-50"
            >
              <TaskTypeIcon icon={name} size={18} />
            </button>
          ))}
        </div>
      </Dialog>
      <Field label={t("spaces.taskTypes.name")}>
        <TextInput
          className="w-[360px]"
          maxLength={64}
          value={name}
          disabled={!manage || save.isPending}
          onChange={(event) => setName(event.target.value)}
          onBlur={() => {
            const next = name.trim();
            if (!next) setName(type.name);
            else if (next !== type.name) save.mutate({ name: next });
          }}
        />
      </Field>
      <CommitTypeField
        value={commitType}
        onChange={setCommitType}
        disabled={!manage || save.isPending}
        onBlur={() => {
          if (
            validCommitType(commitType) &&
            (commitType.trim() || null) !== (type.commit_type ?? null)
          )
            save.mutate({ commit_type: commitType.trim() || null });
        }}
      />
      <Field label={t("spaces.taskTypes.descriptionLabel")}>
        <Textarea
          className="w-[360px]"
          rows={3}
          maxLength={1000}
          value={description}
          disabled={!manage || save.isPending}
          onChange={(event) => setDescription(event.target.value)}
          onBlur={() => {
            if (description.trim() !== type.description)
              save.mutate({ description: description.trim() });
          }}
        />
      </Field>
    </div>
  );
}

/** 自定义字段页签（Figma 页 17 · 5.2–5.8）：绑定 / 解绑组织级字段，复用全局配置的字段弹窗。 */
function FieldsTab({
  orgId,
  spaceId,
  typeId,
  manage,
  fields,
  loading,
}: {
  orgId: string;
  spaceId: string;
  typeId: string;
  manage: boolean;
  fields: CustomField[] | undefined;
  loading: boolean;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [picking, setPicking] = useState(false);
  const [creating, setCreating] = useState(false);
  const [removing, setRemoving] = useState<CustomField | null>(null);
  const bound = fields ?? [];
  const refresh = () => {
    void queryClient.invalidateQueries({
      queryKey: keys.spaceTaskTypes(orgId, spaceId),
    });
    void queryClient.invalidateQueries({ queryKey: keys.customFields(orgId) });
  };
  const bind = useMutation({
    mutationFn: async (ids: string[]) => {
      for (const id of ids)
        await api.typeFields.bind(orgId, spaceId, typeId, id);
      return ids;
    },
    onSuccess: (ids, _vars) => {
      setPicking(false);
      toast(t("spaces.taskTypes.fieldsAdded", { count: ids.length }));
    },
    onError: () =>
      toast(t("spaces.taskTypes.fieldsAddFailed"), { tone: "error" }),
    onSettled: refresh,
  });
  const unbind = useMutation({
    mutationFn: (field: CustomField) =>
      api.typeFields.unbind(orgId, spaceId, typeId, field.id),
    onSuccess: () => {
      setRemoving(null);
      toast(t("spaces.taskTypes.fieldRemoved"));
    },
    onError: () =>
      toast(t("spaces.taskTypes.fieldRemoveFailed"), { tone: "error" }),
    onSettled: refresh,
  });
  if (loading)
    return (
      <div className="flex justify-center py-6">
        <Spinner />
      </div>
    );
  const addButton = manage && (
    <Button
      ref={anchor}
      size="28"
      variant="outline"
      leading={<Icon name="plus" size={14} />}
      aria-expanded={picking}
      onClick={() => setPicking((v) => !v)}
    >
      {t("spaces.taskTypes.addField")}
    </Button>
  );
  return (
    <div className="flex flex-col gap-3">
      {bound.length === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-md border border-dashed border-line-default bg-surface-card px-4 py-6 text-center">
          <span className="flex size-10 items-center justify-center rounded-full bg-surface-muted">
            <Icon
              name="sliders-horizontal"
              size={20}
              className="text-fg-muted"
            />
          </span>
          <p className="text-body font-medium">
            {t("spaces.taskTypes.noFields")}
          </p>
          <p className="text-body-sm text-fg-muted">
            {t("spaces.taskTypes.noFieldsDescription")}
          </p>
          {addButton}
        </div>
      ) : (
        <>
          <ul className="flex flex-col gap-2">
            {bound.map((field) => (
              <li
                key={field.id}
                className="flex items-center gap-2 rounded-md border border-line-default px-3 py-2"
              >
                <Icon
                  name={KIND_META[field.kind].icon}
                  size={16}
                  style={{ color: KIND_META[field.kind].color }}
                />
                <span className="min-w-0 flex-1 truncate text-body">
                  {field.name}
                </span>
                {manage && (
                  <IconButton
                    label={t("spaces.taskTypes.removeField", {
                      name: field.name,
                    })}
                    icon={<Icon name="x" size={14} />}
                    size="28"
                    onClick={() => setRemoving(field)}
                  />
                )}
              </li>
            ))}
          </ul>
          <div>{addButton}</div>
        </>
      )}
      {picking && (
        <FieldPicker
          orgId={orgId}
          anchor={anchor}
          bound={new Set(bound.map((field) => field.id))}
          busy={bind.isPending}
          onClose={() => setPicking(false)}
          onAdd={(ids) => bind.mutate(ids)}
          onCreate={() => {
            setPicking(false);
            setCreating(true);
          }}
        />
      )}
      {creating && (
        <CustomFieldDialog
          orgId={orgId}
          onClose={() => setCreating(false)}
          onCreated={(field) => {
            setCreating(false);
            // 在任务类型页内创建的字段自动绑定到当前类型
            void api.typeFields.bind(orgId, spaceId, typeId, field.id).then(
              () => {
                toast(
                  t("spaces.taskTypes.fieldCreatedAndAdded", {
                    name: field.name,
                  }),
                );
                refresh();
              },
              () =>
                toast(t("spaces.taskTypes.fieldsAddFailed"), { tone: "error" }),
            );
          }}
        />
      )}
      <ConfirmDialog
        open={Boolean(removing)}
        danger
        title={t("spaces.taskTypes.removeFieldTitle", {
          name: removing?.name ?? "",
        })}
        description={t("spaces.taskTypes.removeFieldDescription")}
        confirmLabel={t("spaces.taskTypes.removeFieldConfirm")}
        loading={unbind.isPending}
        onCancel={() => setRemoving(null)}
        onConfirm={() => removing && unbind.mutate(removing)}
      />
    </div>
  );
}

function FieldPicker({
  orgId,
  anchor,
  bound,
  busy,
  onClose,
  onAdd,
  onCreate,
}: {
  orgId: string;
  anchor: React.RefObject<HTMLButtonElement | null>;
  bound: Set<string>;
  busy: boolean;
  onClose: () => void;
  onAdd: (ids: string[]) => void;
  onCreate: () => void;
}) {
  const t = useT();
  const api = useApi();
  const [query, setQuery] = useState("");
  const [picked, setPicked] = useState<Set<string>>(() => new Set());
  const all = useQuery({
    queryKey: keys.customFields(orgId),
    queryFn: () => api.customFields.list(orgId),
  });
  const available = (all.data ?? []).filter(
    (field) => !field.archived_at && !bound.has(field.id),
  );
  const q = query.trim().toLowerCase();
  const rows = available.filter(
    (field) => !q || field.name.toLowerCase().includes(q),
  );
  const toggle = (id: string) =>
    setPicked((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  return (
    <Popover
      open
      onOpenChange={(open) => !open && onClose()}
      anchorRef={anchor}
      placement="bottom-start"
      role="dialog"
      aria-label={t("spaces.taskTypes.addField")}
      className="flex w-[320px] flex-col"
    >
      {all.isLoading ? (
        <div className="flex justify-center p-4">
          <Spinner />
        </div>
      ) : available.length === 0 ? (
        <div className="flex flex-col items-center gap-2 p-4 text-center">
          <p className="text-body font-medium">
            {(all.data ?? []).length === 0
              ? t("spaces.taskTypes.noFields")
              : t("spaces.taskTypes.allFieldsAdded")}
          </p>
          <Button
            size="28"
            variant="outline"
            leading={<Icon name="plus" size={14} />}
            onClick={onCreate}
          >
            {t("spaces.taskTypes.createField")}
          </Button>
        </div>
      ) : (
        <>
          <div className="border-b border-line-default p-2">
            <TextInput
              autoFocus
              aria-label={t("spaces.taskTypes.searchFields")}
              placeholder={t("spaces.taskTypes.searchFields")}
              leading={<Icon name="search" size={14} />}
              className="h-8"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <ul className="max-h-[224px] overflow-y-auto p-1">
            {rows.length === 0 ? (
              <li className="p-3 text-center text-body-sm text-fg-muted">
                {t("spaces.taskTypes.noFieldMatch")}
              </li>
            ) : (
              rows.map((field) => (
                <li key={field.id}>
                  <label className="flex h-8 cursor-pointer items-center gap-2 rounded-md px-2 hover:bg-surface-hover">
                    <Checkbox
                      checked={picked.has(field.id)}
                      onChange={() => toggle(field.id)}
                    />
                    <Icon
                      name={KIND_META[field.kind].icon}
                      size={16}
                      style={{ color: KIND_META[field.kind].color }}
                    />
                    <span className="min-w-0 flex-1 truncate text-body">
                      {field.name}
                    </span>
                  </label>
                </li>
              ))
            )}
          </ul>
          <div className="flex items-center gap-2 border-t border-line-default p-2">
            <Button
              size="28"
              variant="ghost"
              leading={<Icon name="plus" size={14} />}
              onClick={onCreate}
            >
              {t("spaces.taskTypes.createField")}
            </Button>
            <span className="flex-1" />
            <Button
              size="28"
              disabled={picked.size === 0}
              loading={busy}
              onClick={() => onAdd([...picked])}
            >
              {picked.size > 1
                ? t("spaces.taskTypes.addCount", { count: picked.size })
                : t("spaces.taskTypes.addShort")}
            </Button>
          </div>
        </>
      )}
    </Popover>
  );
}

/** 状态页签（Figma 页 17 · 5.11–5.16）：按分类分组；从组织状态中添加或移出本类型。 */
function WorkflowTab({
  orgId,
  spaceId,
  type,
  manage,
}: {
  orgId: string;
  spaceId: string;
  type: SpaceTaskType;
  manage: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const statuses = useStatuses(orgId);
  const { update, invalidate } = useTypeMutation(orgId, spaceId);
  const [adding, setAdding] = useState(false);
  const colorOf = useMemo(
    () =>
      new Map((statuses.data ?? []).map((status) => [status.id, status.color])),
    [statuses.data],
  );
  const ids = type.statuses.map((status) => status.status_id);
  const patch = useMutation({
    mutationFn: (input: {
      status_ids?: string[];
      default_status_id?: string;
    }) => update(type, input),
    onError: (error) =>
      toast(
        isApiError(error, "COLLABORATION_CONFLICT")
          ? t("spaces.taskTypes.statusInUse")
          : spaceErrorMessage(t, error, "spaces.taskTypes.saveFailed"),
        { tone: "error" },
      ),
    onSettled: invalidate,
  });
  const removeStatus = (statusId: string, isDefault: boolean) => {
    if (ids.length <= 1)
      return toast(t("spaces.taskTypes.keepOneStatus"), { tone: "error" });
    if (isDefault)
      return toast(t("spaces.taskTypes.defaultStatusFirst"), { tone: "error" });
    patch.mutate({ status_ids: ids.filter((id) => id !== statusId) });
  };
  const moveStatus = (statusId: string, neighborId?: string) => {
    if (!neighborId) return;
    const ordered = [...ids];
    const from = ordered.indexOf(statusId);
    const to = ordered.indexOf(neighborId);
    if (from < 0 || to < 0) return;
    ordered[from] = neighborId;
    ordered[to] = statusId;
    patch.mutate({ status_ids: ordered });
  };
  const categoryName = (category: TaskStatusCategory) =>
    category === "TRIAGE"
      ? t("spaces.taskTypes.triageCategory")
      : t(
          `settings.statuses.categories.${category as (typeof STATUS_PAGE_CATEGORIES)[number]}.name`,
        );
  return (
    <div className="flex flex-col gap-5">
      <div className="flex items-start justify-between gap-3">
        <p className="text-body-sm text-fg-muted">
          {t("spaces.taskTypes.workflowHint")}
        </p>
        {manage && (
          <Button
            size="28"
            variant="outline"
            leading={<Icon name="plus" size={14} />}
            disabled={patch.isPending}
            onClick={() => setAdding(true)}
          >
            {t("spaces.taskTypes.addStatus")}
          </Button>
        )}
      </div>
      {STATUS_CATEGORIES.map((category) => {
        const rows = type.statuses
          .filter((status) => status.category === category)
          .sort((a, b) => a.position - b.position);
        if (rows.length === 0) return null;
        return (
          <section
            key={category}
            aria-label={categoryName(category)}
            className="flex flex-col gap-2"
          >
            <h3 className="text-body font-semibold">
              {categoryName(category)}
            </h3>
            <ul className="flex flex-col overflow-hidden rounded-xl border border-line-default bg-surface-card">
              {rows.map((status, index) => (
                <li
                  key={status.status_id}
                  className={`flex items-center gap-3 px-4 py-2.5 ${index > 0 ? "border-t border-line-soft/50" : ""}`}
                >
                  <span className="flex size-7 items-center justify-center rounded-lg bg-surface-muted">
                    <StatusIcon
                      category={status.category}
                      color={colorOf.get(status.status_id)}
                      size={14}
                    />
                  </span>
                  <span className="min-w-0 flex-1 truncate text-body font-medium">
                    {status.name}
                    {status.default && (
                      <span className="text-fg-muted">
                        {" "}
                        · {t("spaces.taskTypes.defaultBadge")}
                      </span>
                    )}
                  </span>
                  <span className="text-body-sm tabular-nums text-fg-muted">
                    {status.task_count === undefined
                      ? "—"
                      : t("spaces.taskTypes.workflowTaskCount", {
                          count: status.task_count,
                        })}
                  </span>
                  {manage && category !== "TRIAGE" && rows.length > 1 && (
                    <span className="flex items-center">
                      <IconButton
                        label={t("spaces.taskTypes.moveStatusUp", {
                          name: status.name,
                        })}
                        icon={<Icon name="chevron-up" size={14} />}
                        size="28"
                        disabled={patch.isPending || index === 0}
                        onClick={() =>
                          moveStatus(
                            status.status_id,
                            rows[index - 1]?.status_id,
                          )
                        }
                      />
                      <IconButton
                        label={t("spaces.taskTypes.moveStatusDown", {
                          name: status.name,
                        })}
                        icon={<Icon name="chevron-down" size={14} />}
                        size="28"
                        disabled={patch.isPending || index === rows.length - 1}
                        onClick={() =>
                          moveStatus(
                            status.status_id,
                            rows[index + 1]?.status_id,
                          )
                        }
                      />
                    </span>
                  )}
                  {manage && category !== "TRIAGE" && (
                    <Menu
                      label={t("spaces.taskTypes.statusMenu", {
                        name: status.name,
                      })}
                      placement="bottom-end"
                      className="w-[160px]"
                      trigger={({ ref, toggle, open }) => (
                        <IconButton
                          ref={ref}
                          label={t("spaces.taskTypes.statusMenu", {
                            name: status.name,
                          })}
                          icon={<Icon name="ellipsis" />}
                          size="28"
                          disabled={patch.isPending}
                          aria-expanded={open}
                          onClick={toggle}
                        />
                      )}
                    >
                      {!status.default &&
                        (category === "BACKLOG" || category === "TODO") && (
                          <MenuItem
                            icon={<Icon name="star" />}
                            onSelect={() =>
                              patch.mutate(
                                { default_status_id: status.status_id },
                                {
                                  onSuccess: () =>
                                    toast(
                                      t("spaces.taskTypes.defaultStatusSet", {
                                        name: status.name,
                                      }),
                                    ),
                                },
                              )
                            }
                          >
                            {t("spaces.taskTypes.setDefault")}
                          </MenuItem>
                        )}
                      <MenuItem
                        danger
                        icon={<Icon name="x" />}
                        onSelect={() =>
                          removeStatus(status.status_id, status.default)
                        }
                      >
                        {t("spaces.taskTypes.removeStatus")}
                      </MenuItem>
                    </Menu>
                  )}
                </li>
              ))}
            </ul>
          </section>
        );
      })}
      {adding && (
        <AddStatusDialog
          statuses={(statuses.data ?? []).filter(
            (status) =>
              status.category !== "TRIAGE" && !ids.includes(status.id),
          )}
          busy={patch.isPending}
          onClose={() => setAdding(false)}
          onAdd={(added) =>
            patch.mutate(
              { status_ids: [...ids, ...added] },
              { onSuccess: () => setAdding(false) },
            )
          }
        />
      )}
    </div>
  );
}

function AddStatusDialog({
  statuses,
  busy,
  onClose,
  onAdd,
}: {
  statuses: TaskStatus[];
  busy: boolean;
  onClose: () => void;
  onAdd: (ids: string[]) => void;
}) {
  const t = useT();
  const [picked, setPicked] = useState<Set<string>>(() => new Set());
  return (
    <Dialog
      open
      width={560}
      onClose={busy ? undefined : onClose}
      title={t("spaces.taskTypes.addStatus")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={picked.size === 0}
            loading={busy}
            onClick={() => onAdd([...picked])}
          >
            {t("spaces.taskTypes.addShort")}
          </Button>
        </>
      }
    >
      {statuses.length === 0 ? (
        <p className="text-body-sm text-fg-muted">
          {t("spaces.taskTypes.noMoreStatuses")}
        </p>
      ) : (
        <ul className="flex max-h-[320px] flex-col gap-0.5 overflow-y-auto">
          {statuses.map((status) => (
            <li key={status.id}>
              <label className="flex h-9 cursor-pointer items-center gap-2 rounded-md px-2 hover:bg-surface-hover">
                <Checkbox
                  checked={picked.has(status.id)}
                  onChange={() =>
                    setPicked((prev) => {
                      const next = new Set(prev);
                      if (next.has(status.id)) next.delete(status.id);
                      else next.add(status.id);
                      return next;
                    })
                  }
                />
                <StatusIcon
                  category={status.category}
                  color={status.color}
                  size={14}
                />
                <span className="text-body font-medium">{status.name}</span>
              </label>
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}

function validCommitType(value: string) {
  return (
    !/[\u0000-\u001f\u007f-\u009f]/.test(value) &&
    (value.trim() === "" || /^[A-Za-z][A-Za-z0-9-]{0,31}$/.test(value.trim()))
  );
}

function CommitTypeField({
  value,
  onChange,
  disabled,
  onBlur,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
  onBlur?: () => void;
}) {
  const t = useT();
  return (
    <Field label={t("spaces.taskTypes.commitType")}>
      <TextInput
        className="w-[360px] max-w-full"
        value={value}
        placeholder="feat"
        disabled={disabled}
        aria-invalid={!validCommitType(value)}
        onChange={(event) => onChange(event.target.value)}
        onBlur={onBlur}
      />
      <p className="text-xs text-fg-muted">
        {t("spaces.taskTypes.commitTypeHint")}
      </p>
      {!validCommitType(value) && (
        <p role="alert" className="text-xs text-danger">
          {t("spaces.taskTypes.commitTypeInvalid")}
        </p>
      )}
    </Field>
  );
}
