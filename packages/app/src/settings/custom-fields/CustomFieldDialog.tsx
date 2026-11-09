import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";

import {
  isApiError,
  newIdempotencyKey,
  type CustomField,
  type CustomFieldKind,
  type CustomFieldMemberScope,
  type CustomFieldOption,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Field,
  Icon,
  Select,
  TextInput,
  Textarea,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { describeError, useIdempotencyKey } from "../common";
import { DefaultValueInput } from "./DefaultValueInput";
import {
  FIELD_KINDS,
  KIND_META,
  MEMBER_SCOPES,
  buildDefault,
  draftFromValue,
  emptyDraft,
  hasOptions,
  type DefaultDraft,
  type OptionRow,
} from "./kinds";
import { OptionsEditor, nextOptionColor } from "./OptionsEditor";

function blankRows(count: number): OptionRow[] {
  const rows: OptionRow[] = [];
  for (let i = 0; i < count; i++)
    rows.push({
      key: `new-${crypto.randomUUID()}`,
      label: "",
      color: nextOptionColor(rows),
    });
  return rows;
}

const rowFromOption = (option: CustomFieldOption): OptionRow => ({
  key: option.id,
  id: option.id,
  label: option.label,
  color: option.color,
  version: option.version,
});

/**
 * 创建 / 编辑自定义字段，弹窗宽 448。
 * 编辑时类型不可改；选项改动实时调用 options 接口保存，其余字段点「保存」提交。
 */
export function CustomFieldDialog({
  orgId,
  field,
  onClose,
  onCreated,
}: {
  orgId: string;
  /** 不传为创建 */
  field?: CustomField;
  onClose: () => void;
  onCreated?: (field: CustomField) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const keyFor = useIdempotencyKey();
  const listKey = keys.customFields(orgId);
  const editing = Boolean(field);
  // 编辑时从缓存读最新版本号（选项操作可能让字段版本变化）
  const fields = useQuery({
    queryKey: listKey,
    queryFn: () => api.customFields.list(orgId),
    enabled: editing,
  });
  const latest = fields.data?.find((item) => item.id === field?.id) ?? field;

  const [name, setName] = useState(field?.name ?? "");
  const [kind, setKind] = useState<CustomFieldKind>(field?.kind ?? "TEXT");
  const [description, setDescription] = useState(field?.description ?? "");
  const [memberScope, setMemberScope] = useState<CustomFieldMemberScope>(
    field?.member_scope ?? "ALL",
  );
  const [rows, setRows] = useState<OptionRow[]>(() =>
    field ? field.options.map(rowFromOption) : [],
  );
  const [draft, setDraft] = useState<DefaultDraft>(() =>
    field ? draftFromValue(field.kind, field.default_value) : emptyDraft(),
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // 新增选项的幂等键：按行保存，重试时复用
  const optionKeys = useRef(new Map<string, string>());

  const changeKind = (next: CustomFieldKind) => {
    setKind(next);
    setDraft(emptyDraft());
    if (hasOptions(next) && rows.length === 0) setRows(blankRows(2));
  };

  const refreshList = () =>
    void queryClient.invalidateQueries({ queryKey: listKey });
  const reportOptionError = (err: unknown) => {
    toast(
      isApiError(err, "VERSION_CONFLICT")
        ? t("settings.errors.versionConflict")
        : describeError(err, t),
      { tone: "error" },
    );
    refreshList();
  };
  const patchRow = (key: string, patch: Partial<OptionRow>) =>
    setRows((list) =>
      list.map((row) => (row.key === key ? { ...row, ...patch } : row)),
    );

  // 编辑已有字段时，选项实时保存
  const live = field
    ? {
        onLabelCommit: (row: OptionRow) => {
          const label = row.label.trim();
          if (!label) return;
          if (!row.id) {
            let key = optionKeys.current.get(row.key);
            if (!key) {
              key = newIdempotencyKey();
              optionKeys.current.set(row.key, key);
            }
            api.customFields
              .createOption(orgId, field.id, { label, color: row.color }, key)
              .then((option) => {
                patchRow(row.key, { id: option.id, version: option.version });
                refreshList();
              })
              .catch(reportOptionError);
            return;
          }
          const saved = latest?.options.find((option) => option.id === row.id);
          if (saved && saved.label === label) return;
          api.customFields
            .updateOption(orgId, field.id, row.id, {
              label,
              version: row.version ?? 1,
            })
            .then((option) => {
              patchRow(row.key, { version: option.version });
              refreshList();
            })
            .catch(reportOptionError);
        },
        onColor: (row: OptionRow, color: string) => {
          if (!row.id) return;
          api.customFields
            .updateOption(orgId, field.id, row.id, {
              color,
              version: row.version ?? 1,
            })
            .then((option) => {
              patchRow(row.key, { version: option.version });
              refreshList();
            })
            .catch(reportOptionError);
        },
        onRemove: (row: OptionRow) => {
          setDraft((current) => ({
            ...current,
            keys: current.keys.filter((key) => key !== row.key),
          }));
          if (!row.id) return;
          api.customFields
            .removeOption(orgId, field.id, row.id)
            .then(refreshList)
            .catch(reportOptionError);
        },
        onMove: (row: OptionRow, position: number) => {
          if (!row.id) return;
          api.customFields
            .updateOption(orgId, field.id, row.id, {
              position,
              version: row.version ?? 1,
            })
            .then((option) => {
              patchRow(row.key, { version: option.version });
              refreshList();
            })
            .catch(reportOptionError);
        },
      }
    : undefined;

  const built = buildDefault(
    kind,
    draft,
    // 编辑时只能引用已保存的选项
    editing ? rows.filter((row) => row.id) : rows,
    editing ? "update" : "create",
  );
  const namedRows = rows.filter((row) => row.label.trim());
  const optionsMissing = hasOptions(kind) && namedRows.length === 0;
  const canSave =
    Boolean(name.trim()) && built.ok && !optionsMissing && !saving;
  const defaultError = built.ok
    ? undefined
    : built.reason === "url"
      ? t("settings.customFields.dialog.urlInvalid")
      : t("settings.customFields.dialog.numberInvalid");

  const submit = async () => {
    if (!canSave || !built.ok) return;
    setSaving(true);
    setError(null);
    try {
      if (!field) {
        const input = {
          name: name.trim(),
          kind,
          description: description.trim() || undefined,
          default_value: built.value ?? undefined,
          member_scope: kind === "MEMBER" ? memberScope : undefined,
          options: hasOptions(kind)
            ? namedRows.map((row) => ({
                label: row.label.trim(),
                color: row.color,
              }))
            : undefined,
        };
        const created = await api.customFields.create(
          orgId,
          input,
          keyFor(input),
        );
        queryClient.setQueryData<CustomField[]>(listKey, (list) =>
          list
            ? [...list.filter((item) => item.id !== created.id), created]
            : list,
        );
        refreshList();
        onCreated?.(created);
        return;
      }
      await api.customFields.update(orgId, field.id, {
        name: name.trim(),
        description: description.trim(),
        default_value: built.value,
        member_scope: kind === "MEMBER" ? memberScope : undefined,
        version: latest?.version ?? field.version,
      });
      refreshList();
      toast(t("settings.customFields.toast.updated", { name: name.trim() }));
      onClose();
    } catch (err) {
      if (isApiError(err, "VERSION_CONFLICT")) {
        toast(t("settings.errors.versionConflict"), { tone: "error" });
        refreshList();
        onClose();
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
        editing
          ? "settings.customFields.dialog.editTitle"
          : "settings.customFields.dialog.createTitle",
      )}
      width={448}
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
        <Field label={t("settings.customFields.dialog.name")}>
          <TextInput
            data-autofocus
            value={name}
            maxLength={64}
            placeholder={t("settings.customFields.dialog.namePlaceholder")}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field
          label={t("settings.customFields.dialog.kind")}
          helper={
            editing ? t("settings.customFields.dialog.kindLocked") : undefined
          }
        >
          <Select
            value={kind}
            disabled={editing}
            onChange={changeKind}
            options={FIELD_KINDS.map((value) => ({
              value,
              label: t(`settings.customFields.kinds.${value}`),
              icon: (
                <Icon
                  name={KIND_META[value].icon}
                  style={{ color: KIND_META[value].color }}
                />
              ),
            }))}
          />
        </Field>
        {hasOptions(kind) && (
          <Field
            label={t("settings.customFields.dialog.options")}
            error={
              optionsMissing && rows.length > 0
                ? t("settings.customFields.dialog.optionsRequired")
                : undefined
            }
          >
            <OptionsEditor rows={rows} onRowsChange={setRows} live={live} />
          </Field>
        )}
        {kind === "MEMBER" && (
          <Field label={t("settings.customFields.dialog.memberScope")}>
            <Select
              value={memberScope}
              onChange={setMemberScope}
              options={MEMBER_SCOPES.map((value) => ({
                value,
                label: t(`settings.customFields.memberScope.${value}`),
              }))}
            />
          </Field>
        )}
        <Field
          label={t("settings.customFields.dialog.default")}
          error={defaultError}
        >
          <DefaultValueInput
            orgId={orgId}
            kind={kind}
            draft={draft}
            onChange={setDraft}
            options={editing ? rows.filter((row) => row.id) : rows}
            memberScope={memberScope}
            invalid={!built.ok}
          />
        </Field>
        <Field label={t("settings.customFields.dialog.description")}>
          <Textarea
            value={description}
            maxLength={1000}
            rows={2}
            placeholder={t(
              "settings.customFields.dialog.descriptionPlaceholder",
            )}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
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
