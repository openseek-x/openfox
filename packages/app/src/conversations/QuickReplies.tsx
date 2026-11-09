import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { useEffect, useRef, useState, type RefObject } from "react";

import {
  QUICK_REPLY_BODY_MAX,
  QUICK_REPLY_LIMIT,
  QUICK_REPLY_TITLE_MAX,
  isApiError,
  type QuickReply,
  type QuickReplyList,
  type QuickReplyScope,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Icon,
  IconButton,
  Popover,
  Textarea,
  TextInput,
  cn,
  useToast,
} from "@openfox/ui";

import { useOrgRole } from "../agents/queries";
import { keys, useApi } from "../context";

export function useQuickReplies(orgId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: keys.quickReplies(orgId),
    queryFn: () => api.quickReplies.list(orgId),
    enabled,
  });
}

const refresh = (queryClient: QueryClient, orgId: string) =>
  queryClient.invalidateQueries({ queryKey: keys.quickReplies(orgId) });

/** 把接口错误转成提示文案 */
export function quickReplyError(t: Translate, error: unknown): string {
  if (isApiError(error, "QUICK_REPLY_LIMIT"))
    return t("quickReplies.errors.limit", { max: QUICK_REPLY_LIMIT });
  if (isApiError(error, "VERSION_CONFLICT"))
    return t("quickReplies.errors.conflict");
  if (isApiError(error, "ACTION_FORBIDDEN"))
    return t("quickReplies.errors.forbidden");
  return t("quickReplies.errors.generic");
}

export function QuickReplyMenu({
  orgId,
  open,
  onOpenChange,
  anchorRef,
  onPick,
  onSend,
  onManage,
  onCreate,
}: {
  orgId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  anchorRef: RefObject<HTMLElement | null>;
  onPick: (body: string) => void;
  /** Task comments support direct replies as well as inserting into a draft. */
  onSend?: (body: string) => void;
  onManage: () => void;
  onCreate?: () => void;
}) {
  const t = useT();
  const list = useQuickReplies(orgId, open);
  const directMenu = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() =>
      directMenu.current?.querySelector<HTMLButtonElement>("button")?.focus(),
    );
    return () => cancelAnimationFrame(frame);
  }, [open, list.isPending]);
  const all: QuickReply[] = [
    ...(list.data?.personal ?? []),
    ...(list.data?.organization ?? []),
  ];
  return (
    <Popover
      open={open}
      onOpenChange={onOpenChange}
      anchorRef={anchorRef}
      placement="top-end"
      aria-label={t("quickReplies.menuLabel")}
      className="w-72 max-w-[calc(100vw-16px)]"
    >
      <div
        ref={directMenu}
        onKeyDown={(event) => {
          if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key))
            return;
          const buttons = Array.from(
            event.currentTarget.querySelectorAll<HTMLButtonElement>(
              "button:not(:disabled)",
            ),
          );
          if (!buttons.length) return;
          event.preventDefault();
          const current = buttons.indexOf(
            document.activeElement as HTMLButtonElement,
          );
          const next =
            event.key === "Home"
              ? 0
              : event.key === "End"
                ? buttons.length - 1
                : (current +
                    (event.key === "ArrowDown" ? 1 : -1) +
                    buttons.length) %
                  buttons.length;
          buttons[next]?.focus();
        }}
      >
        <div className="max-h-60 overflow-y-auto">
          {all.map((item) => (
            <div
              key={item.id}
              className="group/quick flex items-center rounded-lg hover:bg-surface-hover focus-within:bg-surface-hover"
            >
              <button
                type="button"
                title={item.body}
                className="min-h-9 min-w-0 flex-1 rounded-lg px-2.5 py-2 text-left text-body"
                onClick={() => {
                  onOpenChange(false);
                  (onSend ?? onPick)(item.body);
                }}
              >
                <span className="block truncate">{item.title}</span>
              </button>
              <IconButton
                label={t("quickReplies.insert", { title: item.title })}
                icon={
                  <Icon
                    name="openfox-arrow-up"
                    size={16}
                    className="-rotate-90"
                  />
                }
                size="28"
                className="mr-1 opacity-0 group-hover/quick:opacity-100 group-focus-within/quick:opacity-100"
                onClick={() => {
                  onOpenChange(false);
                  onPick(item.body);
                }}
              />
            </div>
          ))}
          {all.length === 0 && !list.isPending && (
            <p className="px-3 py-2 text-body text-fg-muted">
              {list.isError
                ? t("quickReplies.loadFailed")
                : t("quickReplies.empty")}
            </p>
          )}
        </div>
        {onCreate && (
          <button
            type="button"
            disabled={
              list.isPending ||
              (list.data?.personal.length ?? 0) >= QUICK_REPLY_LIMIT
            }
            className="flex min-h-9 w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-body text-fg-muted hover:bg-surface-hover disabled:opacity-50"
            onClick={() => {
              onOpenChange(false);
              onCreate();
            }}
          >
            <Icon name="plus" />
            {t("quickReplies.add")}
          </button>
        )}
        <div className="my-1 h-px bg-line-default" />
        <button
          type="button"
          aria-label={t("quickReplies.manage")}
          className="flex min-h-9 w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-body text-fg-muted hover:bg-surface-hover"
          onClick={() => {
            onOpenChange(false);
            onManage();
          }}
        >
          <Icon name="settings" />
          {t("quickReplies.manageShort")}
        </button>
      </div>
    </Popover>
  );
}

type Editing = {
  id: string | null;
  title: string;
  body: string;
  version: number;
};

/** 管理对话框：「我的」与「组织」两个页签；组织级只有所有者 / 管理员能改 */
export function QuickReplyManager({
  orgId,
  open,
  onClose,
  initialCreate = false,
}: {
  orgId: string;
  open: boolean;
  onClose: () => void;
  initialCreate?: boolean;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const list = useQuickReplies(orgId, open);
  const role = useOrgRole(orgId)?.toLowerCase();
  const [scope, setScope] = useState<QuickReplyScope>("PERSONAL");
  const [editing, setEditing] = useState<Editing | null>(() =>
    initialCreate ? { id: null, title: "", body: "", version: 0 } : null,
  );
  // 同一份新建表单重试时复用 id，超时重试不会重复创建
  const newId = useRef<string>(crypto.randomUUID());

  const canEdit = scope === "PERSONAL" || role === "owner" || role === "admin";
  const items: QuickReply[] =
    (scope === "PERSONAL" ? list.data?.personal : list.data?.organization) ??
    [];

  const fail = (error: unknown) => {
    toast(quickReplyError(t, error), { tone: "error" });
    // 过期 / 排序不一致：刷新后让用户重新操作
    if (isApiError(error, "VERSION_CONFLICT")) void refresh(queryClient, orgId);
  };

  const save = useMutation({
    mutationFn: async (draft: Editing) => {
      const title = draft.title.trim();
      const body = draft.body.trim();
      if (draft.id)
        return api.quickReplies.update(orgId, draft.id, {
          version: draft.version,
          title,
          body,
        });
      return api.quickReplies.create(orgId, {
        id: newId.current,
        scope,
        title,
        body,
      });
    },
    onSuccess: (_, draft) => {
      toast(
        draft.id
          ? t("quickReplies.toast.updated")
          : t("quickReplies.toast.created"),
      );
      if (!draft.id) newId.current = crypto.randomUUID();
      setEditing(null);
      void refresh(queryClient, orgId);
    },
    onError: fail,
  });
  const remove = useMutation({
    mutationFn: (item: QuickReply) => api.quickReplies.remove(orgId, item.id),
    onSuccess: () => {
      toast(t("quickReplies.toast.removed"));
      void refresh(queryClient, orgId);
    },
    onError: fail,
  });
  const reorder = useMutation({
    mutationFn: (ids: string[]) => api.quickReplies.reorder(orgId, scope, ids),
    onSuccess: (updated) => {
      queryClient.setQueryData<QuickReplyList>(
        keys.quickReplies(orgId),
        (current) =>
          current &&
          (scope === "PERSONAL"
            ? { ...current, personal: updated }
            : { ...current, organization: updated }),
      );
    },
    onError: fail,
  });

  const move = (index: number, delta: -1 | 1) => {
    const ids = items.map((item) => item.id);
    const target = index + delta;
    if (target < 0 || target >= ids.length) return;
    [ids[index], ids[target]] = [ids[target]!, ids[index]!];
    reorder.mutate(ids);
  };

  const busy = save.isPending || remove.isPending || reorder.isPending;
  const valid =
    editing !== null &&
    editing.title.trim().length > 0 &&
    editing.body.trim().length > 0;

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("quickReplies.manager.title")}
      description={t("quickReplies.manager.description")}
      width={560}
    >
      <div className="flex flex-col gap-3">
        <div
          role="tablist"
          aria-label={t("quickReplies.manager.title")}
          className="inline-flex h-9 items-center gap-0.5 self-start rounded-full bg-surface-muted p-[3px]"
        >
          {(["PERSONAL", "ORG"] as const).map((value) => (
            <button
              key={value}
              type="button"
              role="tab"
              aria-selected={scope === value}
              onClick={() => {
                setScope(value);
                setEditing(null);
              }}
              className={cn(
                "h-[30px] rounded-full px-3 text-body-sm",
                scope === value
                  ? "bg-fg-default text-fg-inverse"
                  : "text-fg-secondary hover:text-fg-default",
              )}
            >
              {value === "PERSONAL"
                ? t("quickReplies.manager.personalTab")
                : t("quickReplies.manager.organizationTab")}
            </button>
          ))}
        </div>

        {!canEdit && (
          <p className="text-body-sm text-fg-muted">
            {t("quickReplies.manager.readOnly")}
          </p>
        )}

        <ul className="flex max-h-[320px] flex-col gap-1.5 overflow-y-auto">
          {items.length === 0 && !editing && (
            <li className="py-6 text-center text-body-sm text-fg-muted">
              {list.isError
                ? t("quickReplies.loadFailed")
                : t("quickReplies.manager.noneYet")}
            </li>
          )}
          {items.map((item, index) => (
            <li
              key={item.id}
              className="flex items-start gap-2 rounded-lg border border-line-default px-3 py-2"
            >
              <div className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-body font-medium">
                  {item.title}
                </span>
                <span className="line-clamp-2 text-body-sm text-fg-muted">
                  {item.body}
                </span>
              </div>
              {canEdit && item.can_edit && (
                <div className="flex shrink-0 items-center">
                  <IconButton
                    label={t("quickReplies.manager.moveUp", {
                      title: item.title,
                    })}
                    icon={<Icon name="chevron-up" size={14} />}
                    size="28"
                    variant="ghost"
                    disabled={busy || index === 0}
                    onClick={() => move(index, -1)}
                  />
                  <IconButton
                    label={t("quickReplies.manager.moveDown", {
                      title: item.title,
                    })}
                    icon={<Icon name="chevron-down" size={14} />}
                    size="28"
                    variant="ghost"
                    disabled={busy || index === items.length - 1}
                    onClick={() => move(index, 1)}
                  />
                  <IconButton
                    label={t("quickReplies.manager.edit", {
                      title: item.title,
                    })}
                    icon={<Icon name="pencil" size={14} />}
                    size="28"
                    variant="ghost"
                    disabled={busy}
                    onClick={() =>
                      setEditing({
                        id: item.id,
                        title: item.title,
                        body: item.body,
                        version: item.version,
                      })
                    }
                  />
                  <IconButton
                    label={t("quickReplies.manager.remove", {
                      title: item.title,
                    })}
                    icon={<Icon name="trash-2" size={14} />}
                    size="28"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => remove.mutate(item)}
                  />
                </div>
              )}
            </li>
          ))}
        </ul>

        {editing ? (
          <form
            className="flex flex-col gap-2 rounded-lg border border-line-default p-3"
            onSubmit={(event) => {
              event.preventDefault();
              if (valid && !busy) save.mutate(editing);
            }}
          >
            <label className="flex flex-col gap-1 text-body-sm text-fg-secondary">
              {t("quickReplies.manager.titleLabel")}
              <TextInput
                data-autofocus
                value={editing.title}
                maxLength={QUICK_REPLY_TITLE_MAX}
                placeholder={t("quickReplies.manager.titlePlaceholder")}
                onChange={(event) =>
                  setEditing({ ...editing, title: event.target.value })
                }
              />
            </label>
            <label className="flex flex-col gap-1 text-body-sm text-fg-secondary">
              {t("quickReplies.manager.bodyLabel")}
              <Textarea
                rows={4}
                value={editing.body}
                maxLength={QUICK_REPLY_BODY_MAX}
                placeholder={t("quickReplies.manager.bodyPlaceholder")}
                onChange={(event) =>
                  setEditing({ ...editing, body: event.target.value })
                }
              />
            </label>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                size="28"
                onClick={() => setEditing(null)}
              >
                {t("quickReplies.manager.cancel")}
              </Button>
              <Button type="submit" size="28" disabled={!valid || busy}>
                {t("quickReplies.manager.save")}
              </Button>
            </div>
          </form>
        ) : (
          canEdit && (
            <div className="flex items-center justify-between">
              <span className="text-caption text-fg-muted">
                {t("quickReplies.manager.count", {
                  count: items.length,
                  max: QUICK_REPLY_LIMIT,
                })}
              </span>
              <Button
                variant="outline"
                size="28"
                disabled={items.length >= QUICK_REPLY_LIMIT}
                onClick={() =>
                  setEditing({ id: null, title: "", body: "", version: 0 })
                }
              >
                <Icon name="plus" size={14} />
                {t("quickReplies.manager.add")}
              </Button>
            </div>
          )
        )}
      </div>
    </Dialog>
  );
}
