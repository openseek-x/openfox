import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";

import type { CustomField } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Checkbox,
  Dialog,
  Icon,
  Skeleton,
  TextInput,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { TaskTypeIcon } from "../../tasks/primitives/TaskTypeIcon";
import { EmptyState } from "../common";

const typeKey = (spaceId: string, typeId: string) => `${spaceId}/${typeId}`;

/**
 * 字段创建成功后「添加到任务类型」：
 * 按空间分组，空间标题复选框支持半选 / 全选，搜索同时匹配空间名和类型名。
 */
export function AddToTypesDialog({
  orgId,
  field,
  onClose,
}: {
  orgId: string;
  field: CustomField;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const typesQuery = useQuery({
    queryKey: keys.orgTaskTypes(orgId),
    queryFn: () => api.orgTaskTypes.list(orgId),
  });
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [saving, setSaving] = useState(false);

  const spaces = useMemo(() => typesQuery.data ?? [], [typesQuery.data]);
  const needle = query.trim().toLowerCase();
  const visible = useMemo(
    () =>
      spaces
        .map((space) => {
          const spaceHit = space.space_name.toLowerCase().includes(needle);
          return {
            ...space,
            task_types: space.task_types.filter(
              (type) =>
                !needle || spaceHit || type.name.toLowerCase().includes(needle),
            ),
          };
        })
        .filter((space) => space.task_types.length > 0),
    [spaces, needle],
  );
  const totalTypes = spaces.reduce(
    (sum, space) => sum + space.task_types.length,
    0,
  );
  const typeName = new Map(
    spaces.flatMap((space) =>
      space.task_types.map(
        (type) =>
          [
            typeKey(space.space_id, type.id),
            `${space.space_name} / ${type.name}`,
          ] as const,
      ),
    ),
  );

  const toggle = (keysToToggle: string[], on: boolean) =>
    setSelected((prev) => {
      const next = new Set(prev);
      for (const key of keysToToggle) {
        if (on) next.add(key);
        else next.delete(key);
      }
      return next;
    });

  const submit = async () => {
    if (selected.size === 0 || saving) return;
    setSaving(true);
    const targets = [...selected];
    const results = await Promise.allSettled(
      targets.map((key) => {
        const [spaceId, typeId] = key.split("/") as [string, string];
        return api.typeFields.bind(orgId, spaceId, typeId, field.id);
      }),
    );
    setSaving(false);
    const failed = results.filter((result) => result.status === "rejected");
    void queryClient.invalidateQueries({ queryKey: keys.customFields(orgId) });
    void queryClient.invalidateQueries({ queryKey: keys.orgTaskTypes(orgId) });
    if (failed.length > 0) {
      toast(
        t("settings.customFields.addToTypes.failed", { count: failed.length }),
        { tone: "error" },
      );
      // 只保留失败的，便于重试
      setSelected(
        new Set(targets.filter((_, i) => results[i]!.status === "rejected")),
      );
      return;
    }
    toast(
      targets.length === 1
        ? t("settings.customFields.addToTypes.addedOne", {
            name: typeName.get(targets[0]!) ?? "",
          })
        : t("settings.customFields.addToTypes.addedMany", {
            count: targets.length,
          }),
    );
    onClose();
  };

  return (
    <Dialog
      open
      onClose={saving ? undefined : onClose}
      title={t("settings.customFields.addToTypes.title", { name: field.name })}
      description={t("settings.customFields.addToTypes.desc")}
      width={480}
      footer={
        totalTypes > 0 ? (
          <div className="flex w-full items-center gap-2">
            <span className="flex-1 text-body-sm text-fg-muted">
              {t("settings.customFields.addToTypes.selected", {
                count: selected.size,
              })}
            </span>
            <Button variant="outline" onClick={onClose} disabled={saving}>
              {t("settings.customFields.addToTypes.skip")}
            </Button>
            <Button
              onClick={() => void submit()}
              loading={saving}
              disabled={selected.size === 0}
            >
              {t("settings.customFields.addToTypes.add", {
                count: selected.size,
              })}
            </Button>
          </div>
        ) : (
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
        )
      }
    >
      {typesQuery.isLoading ? (
        <div aria-busy="true" className="flex flex-col gap-2">
          <Skeleton className="h-8 w-full" />
          <Skeleton className="h-8 w-full" />
        </div>
      ) : totalTypes === 0 ? (
        <EmptyState
          title={t("settings.customFields.addToTypes.emptyTitle")}
          description={t("settings.customFields.addToTypes.emptyDesc")}
        />
      ) : (
        <div className="flex flex-col gap-3">
          <TextInput
            type="search"
            data-autofocus
            aria-label={t("settings.customFields.addToTypes.search")}
            placeholder={t("settings.customFields.addToTypes.search")}
            leading={<Icon name="search" />}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          <div className="flex max-h-[320px] flex-col gap-2 overflow-y-auto">
            {visible.length === 0 && (
              <p className="py-4 text-center text-body-sm text-fg-muted">
                {t("settings.customFields.addToTypes.noMatch")}
              </p>
            )}
            {visible.map((space) => {
              // 已绑定的类型不再可选
              const selectable = space.task_types.filter(
                (type) => !type.field_ids.includes(field.id),
              );
              const keysInSpace = selectable.map((type) =>
                typeKey(space.space_id, type.id),
              );
              const checkedCount = keysInSpace.filter((key) =>
                selected.has(key),
              ).length;
              const all =
                keysInSpace.length > 0 && checkedCount === keysInSpace.length;
              return (
                <fieldset key={space.space_id} className="flex flex-col">
                  <legend className="sr-only">{space.space_name}</legend>
                  <label className="flex h-8 items-center gap-2.5 rounded-md px-2 text-body font-medium hover:bg-surface-hover">
                    <Checkbox
                      checked={all}
                      indeterminate={checkedCount > 0 && !all}
                      disabled={keysInSpace.length === 0}
                      onChange={() => toggle(keysInSpace, !all)}
                    />
                    <span className="truncate">{space.space_name}</span>
                    <span className="text-caption font-normal text-fg-muted">
                      {space.space_prefix}
                    </span>
                  </label>
                  {space.task_types.map((type) => {
                    const key = typeKey(space.space_id, type.id);
                    const bound = type.field_ids.includes(field.id);
                    return (
                      <label
                        key={type.id}
                        className="flex h-8 items-center gap-2.5 rounded-md pr-2 pl-8 text-body hover:bg-surface-hover"
                      >
                        <Checkbox
                          checked={bound || selected.has(key)}
                          disabled={bound}
                          onChange={(event) =>
                            toggle([key], event.target.checked)
                          }
                        />
                        <TaskTypeIcon icon={type.icon} />
                        <span className="truncate">{type.name}</span>
                        {bound && (
                          <span className="text-caption text-fg-muted">
                            {t("settings.customFields.addToTypes.bound")}
                          </span>
                        )}
                      </label>
                    );
                  })}
                </fieldset>
              );
            })}
          </div>
        </div>
      )}
    </Dialog>
  );
}
