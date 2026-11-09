import { useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";

import type { CustomField } from "@openfox/api";
import { useI18n } from "@openfox/i18n";
import {
  Avatar,
  Badge,
  Button,
  ConfirmDialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  cn,
  formatDateLabel,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { useAgentDirectory, useOrgPeople } from "../../tasks/queries";
import {
  ConfigPageHeader,
  EmptyState,
  ListSkeleton,
  useCanManage,
  useConfigMutation,
} from "../common";
import { AddToTypesDialog } from "./AddToTypesDialog";
import { CustomFieldDialog } from "./CustomFieldDialog";
import { OptionDot } from "./DefaultValueInput";
import { KIND_META } from "./kinds";

/** 全局配置 · 自定义字段。 */
export function CustomFieldsPage() {
  const { orgId = "" } = useParams();
  const { t } = useI18n();
  const api = useApi();
  const toast = useToast();
  const canManage = useCanManage(orgId);
  const listKey = keys.customFields(orgId);
  const fieldsQuery = useQuery({
    queryKey: listKey,
    queryFn: () => api.customFields.list(orgId),
  });
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [editor, setEditor] = useState<{ field?: CustomField } | null>(null);
  const [binding, setBinding] = useState<CustomField | null>(null);
  const [deleting, setDeleting] = useState<CustomField | null>(null);

  const remove = useConfigMutation<CustomField, unknown, CustomField[]>({
    queryKey: listKey,
    mutationFn: (field) => api.customFields.remove(orgId, field.id),
    optimistic: (list, field) => list.filter((item) => item.id !== field.id),
    onSuccess: (_, field) => {
      setDeleting(null);
      toast(t("settings.customFields.toast.deleted", { name: field.name }));
    },
    onError: () => {
      setDeleting(null);
    },
  });

  const requestDelete = (field: CustomField) => {
    if (field.used_by_type_count > 0) {
      toast(t("settings.customFields.inUse"), { tone: "error" });
      return;
    }
    setDeleting(field);
  };

  const toggle = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const fields = fieldsQuery.data ?? [];

  return (
    <div className="mx-auto flex max-w-[768px] flex-col gap-5 px-6 pt-10 pb-16">
      <ConfigPageHeader
        title={t("settings.customFields.title")}
        subtitle={t("settings.customFields.subtitle")}
        action={
          canManage && (
            <Button
              leading={<Icon name="plus" />}
              onClick={() => setEditor({})}
            >
              {t("settings.customFields.create")}
            </Button>
          )
        }
      />
      <div className="flex items-start gap-4 rounded-xl border border-line-default bg-surface-card px-4 py-3">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <p className="text-body font-medium">
            {t("settings.customFields.hintTitle")}
          </p>
          <p className="text-body-sm text-fg-muted">
            {t("settings.customFields.hintDesc")}
          </p>
        </div>
        <Link
          to={`/o/${orgId}/settings/spaces`}
          className="inline-flex h-7 shrink-0 items-center rounded-md border border-line-default px-2.5 text-body-sm font-medium hover:bg-surface-hover"
        >
          {t("settings.customFields.goToTypes")}
        </Link>
      </div>

      {fieldsQuery.isLoading ? (
        <ListSkeleton label={t("settings.customFields.loading")} />
      ) : fieldsQuery.isError ? (
        <EmptyState
          title={t("settings.customFields.loadFailed")}
          action={
            <Button
              variant="outline"
              onClick={() => void fieldsQuery.refetch()}
            >
              {t("common.retry")}
            </Button>
          }
        />
      ) : fields.length === 0 ? (
        <EmptyState
          title={t("settings.customFields.emptyTitle")}
          description={t(
            canManage
              ? "settings.customFields.emptyDesc"
              : "settings.customFields.emptyReadonly",
          )}
        />
      ) : (
        <ul
          aria-label={t("settings.customFields.title")}
          className="flex flex-col gap-2"
        >
          {fields.map((field) => (
            <FieldCard
              key={field.id}
              orgId={orgId}
              field={field}
              open={expanded.has(field.id)}
              canManage={canManage}
              onToggle={() => toggle(field.id)}
              onEdit={() => setEditor({ field })}
              onDelete={() => requestDelete(field)}
            />
          ))}
        </ul>
      )}

      {editor && (
        <CustomFieldDialog
          orgId={orgId}
          field={editor.field}
          onClose={() => setEditor(null)}
          onCreated={(field) => {
            setEditor(null);
            toast(
              t("settings.customFields.toast.created", { name: field.name }),
            );
            setBinding(field);
          }}
        />
      )}
      {binding && (
        <AddToTypesDialog
          orgId={orgId}
          field={binding}
          onClose={() => setBinding(null)}
        />
      )}
      <ConfirmDialog
        open={deleting !== null}
        danger
        loading={remove.isPending}
        title={t("settings.customFields.deleteTitle", {
          name: deleting?.name ?? "",
        })}
        description={t("settings.customFields.deleteDesc")}
        confirmLabel={t("settings.customFields.deleteAction")}
        onCancel={() => setDeleting(null)}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </div>
  );
}

function FieldCard({
  orgId,
  field,
  open,
  canManage,
  onToggle,
  onEdit,
  onDelete,
}: {
  orgId: string;
  field: CustomField;
  open: boolean;
  canManage: boolean;
  onToggle: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  const meta = KIND_META[field.kind];
  const detailsId = `field-details-${field.id}`;
  return (
    <li className="rounded-xl border border-line-default bg-surface-card">
      <div className="flex h-14 items-center gap-3 px-3">
        <span
          className="flex size-8 shrink-0 items-center justify-center rounded-lg"
          style={{ backgroundColor: `${meta.color}1a`, color: meta.color }}
        >
          <Icon name={meta.icon} />
        </span>
        <span className="flex min-w-0 flex-1 items-baseline gap-1.5">
          <span className="truncate text-body font-medium">{field.name}</span>
          {field.used_by_type_count > 0 && (
            <span className="shrink-0 text-caption text-fg-muted">
              {t("settings.customFields.usedBy", {
                count: field.used_by_type_count,
              })}
            </span>
          )}
        </span>
        <Badge variant="outline">
          {t(`settings.customFields.kinds.${field.kind}`)}
        </Badge>
        {canManage && (
          <Menu
            label={t("settings.customFields.menu.label", { name: field.name })}
            placement="bottom-end"
            trigger={({ ref, open: menuOpen, toggle }) => (
              <IconButton
                ref={ref}
                label={t("settings.customFields.menu.label", {
                  name: field.name,
                })}
                icon={<Icon name="ellipsis" />}
                size="28"
                active={menuOpen}
                aria-haspopup="menu"
                aria-expanded={menuOpen}
                onClick={toggle}
              />
            )}
          >
            <MenuItem icon={<Icon name="pencil" />} onSelect={onEdit}>
              {t("settings.customFields.menu.edit")}
            </MenuItem>
            <MenuSeparator />
            <MenuItem icon={<Icon name="trash-2" />} danger onSelect={onDelete}>
              {t("settings.customFields.menu.delete")}
            </MenuItem>
          </Menu>
        )}
        <IconButton
          label={t(
            open
              ? "settings.customFields.collapse"
              : "settings.customFields.expand",
            { name: field.name },
          )}
          icon={
            <Icon
              name="chevron-right"
              className={cn("transition-transform", open && "rotate-90")}
            />
          }
          size="28"
          aria-expanded={open}
          aria-controls={detailsId}
          onClick={onToggle}
        />
      </div>
      {open && (
        <dl
          id={detailsId}
          className="grid grid-cols-[96px_1fr] gap-x-4 gap-y-2.5 border-t border-line-soft px-4 py-3 text-body"
        >
          <Detail label={t("settings.customFields.details.description")}>
            {field.description || (
              <Muted>{t("settings.customFields.details.noDescription")}</Muted>
            )}
          </Detail>
          <Detail label={t("settings.customFields.details.default")}>
            <DefaultDisplay orgId={orgId} field={field} />
          </Detail>
          {field.options.length > 0 && (
            <Detail label={t("settings.customFields.details.options")}>
              <span className="flex flex-wrap gap-1.5">
                {field.options.map((option) => (
                  <OptionPill
                    key={option.id}
                    label={option.label}
                    color={option.color}
                  />
                ))}
              </span>
            </Detail>
          )}
          {field.kind === "MEMBER" && (
            <Detail label={t("settings.customFields.details.scope")}>
              {t(
                `settings.customFields.memberScope.${field.member_scope ?? "ALL"}`,
              )}
            </Detail>
          )}
          <Detail label={t("settings.customFields.details.creator")}>
            {field.created_by ? (
              <span className="flex items-center gap-1.5">
                <Avatar name={field.created_by.email} size={16} />
                {field.created_by.email}
              </span>
            ) : (
              <Muted>{t("settings.customFields.details.system")}</Muted>
            )}
          </Detail>
        </dl>
      )}
    </li>
  );
}

function Detail({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-fg-muted">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </>
  );
}

function Muted({ children }: { children: ReactNode }) {
  return <span className="text-fg-muted">{children}</span>;
}

function OptionPill({ label, color }: { label: string; color: string }) {
  return (
    <span className="inline-flex h-6 items-center gap-1.5 rounded-full border border-line-default px-2 text-body-sm">
      <OptionDot color={color} />
      {label}
    </span>
  );
}

function DefaultDisplay({
  orgId,
  field,
}: {
  orgId: string;
  field: CustomField;
}) {
  const { t, locale } = useI18n();
  const value = field.default_value;
  const none = <Muted>{t("settings.customFields.details.noDefault")}</Muted>;
  if (value === null || value === undefined) return none;
  switch (field.kind) {
    case "SELECT":
    case "MULTI_SELECT": {
      const ids = Array.isArray(value) ? value : [value];
      const options = field.options.filter((option) => ids.includes(option.id));
      if (options.length === 0) return none;
      return (
        <span className="flex flex-wrap gap-1.5">
          {options.map((option) => (
            <OptionPill
              key={option.id}
              label={option.label}
              color={option.color}
            />
          ))}
        </span>
      );
    }
    case "BOOLEAN":
      return (
        <>
          {t(
            value === true
              ? "settings.customFields.boolean.true"
              : "settings.customFields.boolean.false",
          )}
        </>
      );
    case "DATE":
      return (
        <>{typeof value === "string" ? formatDateLabel(value, locale) : ""}</>
      );
    case "MEMBER":
      return <MemberName orgId={orgId} value={value} />;
    case "NUMBER":
      return <>{String(value)}</>;
    default:
      return (
        <span className="line-clamp-3 whitespace-pre-wrap">
          {typeof value === "string" ? value : ""}
        </span>
      );
  }
}

function MemberName({ orgId, value }: { orgId: string; value: unknown }) {
  const member = value as { kind?: string; id?: string };
  const people = useOrgPeople(orgId);
  const directory = useAgentDirectory(orgId, member.kind === "AGENT");
  const name =
    member.kind === "AGENT"
      ? directory.data?.entries.find((entry) => entry.id === member.id)?.name
      : people.data?.find((person) => person.id === member.id)?.email;
  const display = name ?? member.id ?? "";
  return (
    <span className="flex items-center gap-1.5">
      <Avatar
        name={display}
        kind={member.kind === "AGENT" ? "agent" : "user"}
        size={16}
      />
      {display}
    </span>
  );
}
