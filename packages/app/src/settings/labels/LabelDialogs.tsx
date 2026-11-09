import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { isApiError, type Label, type LabelGroup } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Field,
  LABEL_COLORS,
  Select,
  TextInput,
  Textarea,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { ColorDotPicker, randomColor } from "../ColorPicker";
import { describeError, useIdempotencyKey } from "../common";

const NO_GROUP = "__none__";

/** 创建标签弹窗：随机初始色、名称自动聚焦、描述、分组。 */
export function CreateLabelDialog({
  orgId,
  spaceId,
  groups,
  initialGroupId,
  onClose,
  onCreated,
}: {
  orgId: string;
  /** 空间标签页传入：创建该空间的标签 */
  spaceId?: string;
  groups: LabelGroup[];
  initialGroupId?: string;
  onClose: () => void;
  onCreated: (label: Label) => void;
}) {
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();
  const keyFor = useIdempotencyKey();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [color, setColor] = useState(() => randomColor(LABEL_COLORS));
  const [groupId, setGroupId] = useState(initialGroupId ?? NO_GROUP);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<{ name?: string; form?: string }>({});

  const submit = async () => {
    const trimmed = name.trim();
    if (!trimmed || saving) return;
    const input = {
      name: trimmed,
      description: description.trim() || undefined,
      color,
      group_id: groupId === NO_GROUP ? undefined : groupId,
      space_id: spaceId,
    };
    setSaving(true);
    setError({});
    try {
      const label = await api.labels.create(orgId, input, keyFor(input));
      void queryClient.invalidateQueries({ queryKey: keys.labelGroups(orgId) });
      onCreated(label);
    } catch (err) {
      const message = describeError(err, t);
      setError(
        isApiError(err, "NAME_TAKEN") ? { name: message } : { form: message },
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      open
      onClose={saving ? undefined : onClose}
      title={t("settings.labels.dialog.createTitle")}
      width={440}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </Button>
          <Button
            onClick={() => void submit()}
            loading={saving}
            disabled={!name.trim()}
          >
            {t("common.create")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <Field label={t("settings.labels.dialog.name")} error={error.name}>
          <TextInput
            data-autofocus
            value={name}
            maxLength={50}
            placeholder={t("settings.labels.dialog.namePlaceholder")}
            onChange={(event) => setName(event.target.value)}
            leading={
              <ColorDotPicker
                color={color}
                colors={LABEL_COLORS}
                onChange={setColor}
                label={t("settings.labels.dialog.color")}
              />
            }
          />
        </Field>
        <Field label={t("settings.labels.dialog.description")}>
          <Textarea
            value={description}
            maxLength={1000}
            rows={2}
            placeholder={t("settings.labels.dialog.descriptionPlaceholder")}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
        {groups.length > 0 && (
          <Field label={t("settings.labels.dialog.group")}>
            <Select
              value={groupId}
              onChange={setGroupId}
              options={[
                { value: NO_GROUP, label: t("settings.labels.dialog.noGroup") },
                ...groups.map((group) => ({
                  value: group.id,
                  label: group.name,
                })),
              ]}
            />
          </Field>
        )}
        {error.form && (
          <p role="alert" className="text-caption text-danger">
            {error.form}
          </p>
        )}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

/** 创建分组弹窗：名称、描述、颜色。 */
export function CreateGroupDialog({
  orgId,
  spaceId,
  onClose,
  onCreated,
}: {
  orgId: string;
  /** 空间标签页传入：创建该空间的分组 */
  spaceId?: string;
  onClose: () => void;
  onCreated: (group: LabelGroup) => void;
}) {
  const t = useT();
  const api = useApi();
  const keyFor = useIdempotencyKey();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [color, setColor] = useState(() => randomColor(LABEL_COLORS));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<{ name?: string; form?: string }>({});

  const submit = async () => {
    const trimmed = name.trim();
    if (!trimmed || saving) return;
    const input = {
      name: trimmed,
      description: description.trim() || undefined,
      color,
      space_id: spaceId,
    };
    setSaving(true);
    setError({});
    try {
      onCreated(await api.labelGroups.create(orgId, input, keyFor(input)));
    } catch (err) {
      const message = describeError(err, t);
      setError(
        isApiError(err, "NAME_TAKEN") ? { name: message } : { form: message },
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      open
      onClose={saving ? undefined : onClose}
      title={t("settings.labels.dialog.createGroupTitle")}
      width={440}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </Button>
          <Button
            onClick={() => void submit()}
            loading={saving}
            disabled={!name.trim()}
          >
            {t("common.create")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <Field label={t("settings.labels.dialog.name")} error={error.name}>
          <TextInput
            data-autofocus
            value={name}
            maxLength={50}
            placeholder={t("settings.labels.dialog.groupNamePlaceholder")}
            onChange={(event) => setName(event.target.value)}
            leading={
              <ColorDotPicker
                glyph="group"
                color={color}
                colors={LABEL_COLORS}
                onChange={setColor}
                label={t("settings.labels.dialog.color")}
              />
            }
          />
        </Field>
        <Field label={t("settings.labels.dialog.description")}>
          <Textarea
            value={description}
            maxLength={1000}
            rows={2}
            placeholder={t(
              "settings.labels.dialog.groupDescriptionPlaceholder",
            )}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
        {error.form && (
          <p role="alert" className="text-caption text-danger">
            {error.form}
          </p>
        )}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

export type LabelConfirm =
  | { kind: "archive"; labels: Label[] }
  | { kind: "delete"; labels: Label[] }
  | { kind: "merge"; labels: Label[]; target: Label };

/**
 * 标签确认弹窗：归档 / 删除 / 合并。
 * 删除已使用的标签时提示受影响任务数，并提供「归档」作为替代。
 */
export function LabelConfirmDialog({
  confirm,
  busy,
  onCancel,
  onArchive,
  onDelete,
  onMerge,
}: {
  confirm: LabelConfirm;
  busy: boolean;
  onCancel: () => void;
  onArchive: (labels: Label[]) => void;
  onDelete: (labels: Label[]) => void;
  onMerge: (labels: Label[], target: Label) => void;
}) {
  const t = useT();
  const { labels } = confirm;
  const single = labels.length === 1 ? labels[0]! : null;
  const used = labels.reduce((sum, label) => sum + label.task_count, 0);
  const canArchiveInstead =
    confirm.kind === "delete" &&
    used > 0 &&
    labels.every((label) => !label.archived_at);

  let title: string;
  let description: string;
  let action: { label: string; danger: boolean; run: () => void };
  if (confirm.kind === "archive") {
    title = single
      ? t("settings.labels.confirm.archiveTitle", { name: single.name })
      : t("settings.labels.confirm.bulkArchiveTitle", { count: labels.length });
    description = t("settings.labels.confirm.archiveDesc");
    action = {
      label: t("settings.labels.confirm.archiveAction"),
      danger: false,
      run: () => onArchive(labels),
    };
  } else if (confirm.kind === "delete") {
    title = single
      ? t("settings.labels.confirm.deleteTitle", { name: single.name })
      : t("settings.labels.confirm.bulkDeleteTitle", { count: labels.length });
    description =
      used > 0
        ? `${t(
            single
              ? "settings.labels.confirm.deleteUsed"
              : "settings.labels.confirm.bulkDeleteUsed",
            { count: used },
          )}${t("settings.labels.confirm.irreversible")}`
        : t("settings.labels.confirm.irreversible");
    action = {
      label: t("settings.labels.confirm.deleteAction"),
      danger: true,
      run: () => onDelete(labels),
    };
  } else {
    const { target } = confirm;
    title = t("settings.labels.confirm.mergeTitle", { name: target.name });
    description = t("settings.labels.confirm.mergeDesc", {
      count: labels.length,
      name: target.name,
    });
    action = {
      label: t("settings.labels.confirm.mergeAction"),
      danger: false,
      run: () => onMerge(labels, target),
    };
  }

  return (
    <Dialog
      open
      onClose={busy ? undefined : onCancel}
      title={title}
      description={description}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onCancel}
            disabled={busy}
            data-autofocus
          >
            {t("common.cancel")}
          </Button>
          {canArchiveInstead && (
            <Button
              variant="secondary"
              onClick={() => onArchive(labels)}
              disabled={busy}
            >
              {t("settings.labels.confirm.archiveAction")}
            </Button>
          )}
          <Button
            variant={action.danger ? "danger" : "primary"}
            onClick={action.run}
            loading={busy}
          >
            {action.label}
          </Button>
        </>
      }
    />
  );
}
