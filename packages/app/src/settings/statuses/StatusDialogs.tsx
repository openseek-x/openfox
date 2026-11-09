import { useState } from "react";

import { isApiError, type TaskStatus } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  ColorSwatches,
  Dialog,
  Field,
  LABEL_COLORS,
  STATUS_LIFECYCLE_COLORS,
  Select,
  Switch,
  TextInput,
  Textarea,
  useToast,
} from "@openfox/ui";

import { useApi } from "../../context";
import { StatusIcon } from "../../tasks/primitives/StatusIcon";
import { describeError, useIdempotencyKey } from "../common";
import { STATUS_PAGE_CATEGORIES, checkMove, type PageCategory } from "./order";

/** 6 个生命周期色 + 11 个通用色 */
const STATUS_COLORS: readonly string[] = [
  ...STATUS_PAGE_CATEGORIES.map(
    (category) => STATUS_LIFECYCLE_COLORS[category],
  ),
  ...LABEL_COLORS,
];

/** 颜色是否有效：浏览器能解析，且不超过契约上限 64 个字符。 */
export function isValidColor(value: string): boolean {
  const v = value.trim();
  if (!v || v.length > 64) return false;
  if (typeof CSS !== "undefined" && typeof CSS.supports === "function")
    return CSS.supports("color", v);
  return /^#(?:[0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/i.test(v);
}

/**
 * 状态创建 / 编辑弹窗。
 * 编辑时改了分组会在保存后调用 move 接口移到新分组末尾。
 */
export function StatusEditorDialog({
  orgId,
  status,
  initialCategory,
  onClose,
  onSaved,
}: {
  orgId: string;
  /** 不传为创建 */
  status?: TaskStatus;
  initialCategory: PageCategory;
  onClose: () => void;
  onSaved: (message: string) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const keyFor = useIdempotencyKey();
  const [name, setName] = useState(status?.name ?? "");
  const [description, setDescription] = useState(status?.description ?? "");
  const [colorText, setColorText] = useState(
    status?.color || STATUS_LIFECYCLE_COLORS[initialCategory],
  );
  const [category, setCategory] = useState<PageCategory>(initialCategory);
  const [collapsed, setCollapsed] = useState(
    status?.collapsed_by_default ?? false,
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const colorValid = isValidColor(colorText);
  const canSave = Boolean(name.trim()) && colorValid && !saving;

  const submit = async () => {
    if (!canSave) return;
    const color = colorText.trim();
    setError(null);
    if (status && category !== status.category) {
      if (checkMove(status, category) === "default-category") {
        toast(t("settings.statuses.defaultCategoryOnly"), { tone: "error" });
        return;
      }
    }
    setSaving(true);
    try {
      if (!status) {
        const input = {
          name: name.trim(),
          description: description.trim() || undefined,
          category,
          color,
        };
        await api.taskStatuses.create(orgId, input, keyFor(input));
        onSaved(t("settings.statuses.toast.created", { name: input.name }));
        return;
      }
      const updated = await api.taskStatuses.update(orgId, status.id, {
        name: name.trim(),
        description: description.trim(),
        color,
        collapsed_by_default: collapsed,
        version: status.version,
      });
      if (category !== status.category)
        await api.taskStatuses.move(orgId, status.id, {
          category,
          before_status_id: null,
          version: updated.version,
        });
      onSaved(t("settings.statuses.toast.updated"));
    } catch (err) {
      if (isApiError(err, "VERSION_CONFLICT")) {
        toast(t("settings.errors.versionConflict"), { tone: "error" });
        onSaved("");
        return;
      }
      setError(describeError(err, t));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      open
      onClose={saving ? undefined : onClose}
      title={t(
        status
          ? "settings.statuses.dialog.editTitle"
          : "settings.statuses.dialog.createTitle",
      )}
      width={440}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </Button>
          <Button
            onClick={() => void submit()}
            loading={saving}
            disabled={!canSave}
          >
            {t("common.save")}
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
        <Field label={t("settings.statuses.dialog.name")}>
          <TextInput
            data-autofocus
            value={name}
            maxLength={100}
            placeholder={t("settings.statuses.dialog.namePlaceholder")}
            onChange={(event) => setName(event.target.value)}
            leading={
              <StatusIcon
                category={category}
                color={colorValid ? colorText.trim() : null}
              />
            }
          />
        </Field>
        <Field label={t("settings.statuses.dialog.description")}>
          <Textarea
            value={description}
            maxLength={1000}
            rows={2}
            placeholder={t("settings.statuses.dialog.descriptionPlaceholder")}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
        <Field
          label={t("settings.statuses.dialog.color")}
          error={
            colorValid ? undefined : t("settings.statuses.dialog.colorInvalid")
          }
        >
          <TextInput
            value={colorText}
            maxLength={64}
            invalid={!colorValid}
            spellCheck={false}
            onChange={(event) => setColorText(event.target.value)}
            leading={
              <span
                aria-hidden
                className="size-3.5 rounded-full border border-line-default"
                style={
                  colorValid ? { backgroundColor: colorText.trim() } : undefined
                }
              />
            }
          />
        </Field>
        <ColorSwatches
          colors={STATUS_COLORS}
          value={colorValid ? colorText.trim() : null}
          label={t("settings.statuses.dialog.swatches")}
          onChange={setColorText}
          className="-mt-2"
        />
        <Field label={t("settings.statuses.dialog.category")}>
          <Select
            value={category}
            onChange={setCategory}
            options={STATUS_PAGE_CATEGORIES.map((value) => ({
              value,
              label: t(`settings.statuses.categories.${value}.name`),
              icon: <StatusIcon category={value} />,
            }))}
          />
        </Field>
        {status && (
          <label className="flex items-center justify-between gap-3 text-body">
            <span className="flex flex-col">
              <span className="font-medium">
                {t("settings.statuses.dialog.collapsed")}
              </span>
              <span className="text-caption text-fg-muted">
                {t("settings.statuses.dialog.collapsedHint")}
              </span>
            </span>
            <Switch
              checked={collapsed}
              onCheckedChange={setCollapsed}
              label={t("settings.statuses.dialog.collapsed")}
            />
          </label>
        )}
        {error && (
          <p role="alert" className="text-caption text-danger">
            {error}
          </p>
        )}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

/** 删除有任务的状态：选择接收任务的状态。 */
export function DeleteStatusDialog({
  status,
  statuses,
  busy,
  onCancel,
  onConfirm,
}: {
  status: TaskStatus;
  statuses: TaskStatus[];
  busy: boolean;
  onCancel: () => void;
  onConfirm: (replacementId: string) => void;
}) {
  const t = useT();
  const [replacement, setReplacement] = useState<string | null>(null);
  // 排除自身；允许跨分类
  const options = statuses
    .filter((item) => item.id !== status.id && !item.archived_at)
    .filter((item) => item.category !== "TRIAGE")
    .map((item) => ({
      value: item.id,
      label: item.name,
      icon: <StatusIcon category={item.category} color={item.color} />,
      description: t(
        `settings.statuses.categories.${item.category as PageCategory}.name`,
      ),
    }));
  return (
    <Dialog
      open
      onClose={busy ? undefined : onCancel}
      title={t("settings.statuses.delete.title", { name: status.name })}
      description={t("settings.statuses.delete.desc", {
        count: status.task_count,
      })}
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
          <Button
            variant="danger"
            loading={busy}
            disabled={!replacement}
            onClick={() => replacement && onConfirm(replacement)}
          >
            {t("settings.statuses.delete.confirm")}
          </Button>
        </>
      }
    >
      <Field label={t("settings.statuses.delete.replacement")}>
        <Select
          value={replacement}
          onChange={setReplacement}
          placeholder={t("settings.statuses.delete.replacementPlaceholder")}
          options={options}
        />
      </Field>
    </Dialog>
  );
}
