import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useParams } from "react-router-dom";

import { isApiError, type RelationType } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  ConfirmDialog,
  Dialog,
  Field,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
  TextInput,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import {
  ConfigPageHeader,
  EmptyState,
  describeError,
  useCanManage,
  useConfigMutation,
  useIdempotencyKey,
} from "../common";

/** 内置类型按 key 多语言渲染，自定义类型用用户文本。 */
function useRelationText() {
  const t = useT();
  return (type: RelationType) =>
    type.builtin && type.key
      ? {
          name: t(`settings.relationTypes.builtin.${type.key}.name`),
          outward: t(`settings.relationTypes.builtin.${type.key}.outward`),
          inward: t(`settings.relationTypes.builtin.${type.key}.inward`),
        }
      : {
          name: type.name,
          outward: type.outward_label,
          inward: type.inward_label,
        };
}

/** 全局配置 · 任务关联类型。 */
export function RelationTypesPage() {
  const { orgId = "" } = useParams();
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const canManage = useCanManage(orgId);
  const textOf = useRelationText();
  const listKey = keys.relationTypes(orgId);
  const typesQuery = useQuery({
    queryKey: listKey,
    queryFn: () => api.relationTypes.list(orgId),
  });
  const [editor, setEditor] = useState<{ type?: RelationType } | null>(null);
  const [deleting, setDeleting] = useState<RelationType | null>(null);

  const remove = useConfigMutation<RelationType, unknown, RelationType[]>({
    queryKey: listKey,
    mutationFn: (type) => api.relationTypes.remove(orgId, type.id),
    optimistic: (list, type) => list.filter((item) => item.id !== type.id),
    onSuccess: (_, type) => {
      setDeleting(null);
      toast(t("settings.relationTypes.toast.deleted", { name: type.name }));
    },
  });

  // 内置在前（服务端已排序），这里再保证一次
  const types = [...(typesQuery.data ?? [])].sort(
    (a, b) => Number(b.builtin) - Number(a.builtin),
  );

  return (
    <div className="mx-auto flex max-w-[1004px] flex-col gap-5 px-12 pt-10 pb-16">
      <ConfigPageHeader
        title={t("settings.relationTypes.title")}
        subtitle={t("settings.relationTypes.subtitle")}
        action={
          canManage && (
            <Button
              leading={<Icon name="plus" />}
              onClick={() => setEditor({})}
            >
              {t("settings.relationTypes.create")}
            </Button>
          )
        }
      />
      {typesQuery.isLoading ? (
        <p
          aria-busy="true"
          className="py-6 text-center text-body text-fg-muted"
        >
          {t("settings.relationTypes.loading")}
        </p>
      ) : typesQuery.isError ? (
        <EmptyState
          title={t("settings.relationTypes.loadFailed")}
          action={
            <Button variant="outline" onClick={() => void typesQuery.refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : (
        <div className="rounded-xl border border-line-default bg-surface-card px-2">
          <Table
            aria-label={t("settings.relationTypes.title")}
            className="table-fixed"
          >
            <TableHead>
              <TableRow>
                <TableHeaderCell className="w-[34%]">
                  {t("settings.relationTypes.columns.name")}
                </TableHeaderCell>
                <TableHeaderCell>
                  {t("settings.relationTypes.columns.outward")}
                </TableHeaderCell>
                <TableHeaderCell>
                  {t("settings.relationTypes.columns.inward")}
                </TableHeaderCell>
                <TableHeaderCell className="w-12">
                  <span className="sr-only">
                    {t("settings.relationTypes.columns.actions")}
                  </span>
                </TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {types.map((type) => {
                const text = textOf(type);
                return (
                  <TableRow key={type.id} className="last:border-b-0">
                    <TableCell>
                      <span className="flex min-w-0 items-center gap-2">
                        <span className="truncate font-medium">
                          {text.name}
                        </span>
                        {type.builtin && (
                          <Badge variant="outline" tone="muted">
                            <Icon name="lock" size={12} />
                            {t("settings.relationTypes.builtinBadge")}
                          </Badge>
                        )}
                      </span>
                    </TableCell>
                    <TableCell className="truncate text-fg-secondary">
                      {text.outward}
                    </TableCell>
                    <TableCell className="truncate text-fg-secondary">
                      {text.inward}
                    </TableCell>
                    <TableCell className="w-12 pl-0 text-right">
                      {type.builtin ? (
                        <span
                          role="img"
                          aria-label={t("settings.relationTypes.builtinLocked")}
                          title={t("settings.relationTypes.builtinLocked")}
                          className="inline-flex size-7 items-center justify-center text-fg-subtle"
                        >
                          <Icon name="lock" />
                        </span>
                      ) : (
                        canManage && (
                          <Menu
                            label={t("settings.relationTypes.menu.label", {
                              name: type.name,
                            })}
                            placement="bottom-end"
                            trigger={({ ref, open, toggle }) => (
                              <IconButton
                                ref={ref}
                                label={t("settings.relationTypes.menu.label", {
                                  name: type.name,
                                })}
                                icon={<Icon name="ellipsis" />}
                                size="28"
                                active={open}
                                aria-haspopup="menu"
                                aria-expanded={open}
                                onClick={toggle}
                              />
                            )}
                          >
                            <MenuItem
                              icon={<Icon name="pencil" />}
                              onSelect={() => setEditor({ type })}
                            >
                              {t("settings.relationTypes.menu.edit", {
                                name: type.name,
                              })}
                            </MenuItem>
                            <MenuItem
                              icon={<Icon name="trash-2" />}
                              danger
                              onSelect={() => setDeleting(type)}
                            >
                              {t("settings.relationTypes.menu.delete", {
                                name: type.name,
                              })}
                            </MenuItem>
                          </Menu>
                        )
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
      {editor && (
        <RelationTypeDialog
          orgId={orgId}
          type={editor.type}
          onClose={() => setEditor(null)}
        />
      )}
      <ConfirmDialog
        open={deleting !== null}
        danger
        loading={remove.isPending}
        title={t("settings.relationTypes.deleteTitle")}
        description={
          deleting
            ? t("settings.relationTypes.deleteDesc", { name: deleting.name })
            : undefined
        }
        confirmLabel={t("settings.relationTypes.deleteAction")}
        onCancel={() => setDeleting(null)}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </div>
  );
}

/** 创建 / 编辑关联类型：三项必填，失败时弹窗保留并显示错误。 */
function RelationTypeDialog({
  orgId,
  type,
  onClose,
}: {
  orgId: string;
  type?: RelationType;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const keyFor = useIdempotencyKey();
  const listKey = keys.relationTypes(orgId);
  const [name, setName] = useState(type?.name ?? "");
  const [outward, setOutward] = useState(type?.outward_label ?? "");
  const [inward, setInward] = useState(type?.inward_label ?? "");
  const [error, setError] = useState<string | null>(null);

  const save = useConfigMutation<
    { name: string; outward_label: string; inward_label: string },
    RelationType,
    RelationType[]
  >({
    queryKey: listKey,
    toastErrors: false,
    mutationFn: (input) =>
      type
        ? api.relationTypes.update(orgId, type.id, {
            ...input,
            version: type.version ?? 1,
          })
        : api.relationTypes.create(orgId, input, keyFor(input)),
    onError: (err) => {
      setError(describeError(err, t));
      return true;
    },
    onSuccess: (result) => {
      toast(
        t(
          type
            ? "settings.relationTypes.toast.updated"
            : "settings.relationTypes.toast.created",
          { name: result.name },
        ),
      );
      onClose();
    },
  });

  const input = {
    name: name.trim(),
    outward_label: outward.trim(),
    inward_label: inward.trim(),
  };
  const valid = Boolean(
    input.name && input.outward_label && input.inward_label,
  );
  const submit = () => {
    if (!valid || save.isPending) return;
    setError(null);
    save.mutate(input, {
      onError: (err) => {
        // 版本冲突时 useConfigMutation 已提示并刷新，关闭弹窗
        if (isApiError(err, "VERSION_CONFLICT")) onClose();
      },
    });
  };

  return (
    <Dialog
      open
      onClose={save.isPending ? undefined : onClose}
      title={t(
        type
          ? "settings.relationTypes.dialog.editTitle"
          : "settings.relationTypes.dialog.createTitle",
      )}
      description={t("settings.relationTypes.dialog.hint")}
      width={440}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={save.isPending}>
            {t("common.cancel")}
          </Button>
          <Button onClick={submit} loading={save.isPending} disabled={!valid}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <Field label={t("settings.relationTypes.dialog.name")} required>
          <TextInput
            data-autofocus
            value={name}
            maxLength={50}
            placeholder={t("settings.relationTypes.dialog.namePlaceholder")}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("settings.relationTypes.dialog.outward")} required>
          <TextInput
            value={outward}
            maxLength={50}
            placeholder={t("settings.relationTypes.dialog.outwardPlaceholder")}
            onChange={(event) => setOutward(event.target.value)}
          />
        </Field>
        <Field label={t("settings.relationTypes.dialog.inward")} required>
          <TextInput
            value={inward}
            maxLength={50}
            placeholder={t("settings.relationTypes.dialog.inwardPlaceholder")}
            onChange={(event) => setInward(event.target.value)}
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
