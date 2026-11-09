import { useEffect, useId, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import {
  isApiError,
  type PersonalAccessToken,
  type PersonalAccessTokenCreated,
} from "@openfox/api";
import { useI18n, useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Dialog,
  Icon,
  Menu,
  MenuItem,
  Select,
  Skeleton,
  cn,
  useToast,
} from "@openfox/ui";

import { keys } from "../context";
import { SettingsCard, SettingsColumn } from "../spaces/settings/parts";
import { useCreateToken, useMyTokens, useRevokeToken } from "./queries";

// 个人访问令牌：列表、创建（一次性明文）与吊销。

type ExpiryKey = "d30" | "d90" | "y1" | "never";

/** 过期时间选项；days 为 null 表示永不过期 */
const EXPIRY_OPTIONS: Array<{
  value: ExpiryKey;
  days: 30 | 90 | 365 | null;
  label: MessageKey;
}> = [
  { value: "d30", days: 30, label: "me.tokens.dialog.expiry.d30" },
  { value: "d90", days: 90, label: "me.tokens.dialog.expiry.d90" },
  { value: "y1", days: 365, label: "me.tokens.dialog.expiry.y1" },
  { value: "never", days: null, label: "me.tokens.dialog.expiry.never" },
];

export function TokensPage() {
  const t = useT();
  const toast = useToast();
  const tokens = useMyTokens();
  const revoke = useRevokeToken();
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<PersonalAccessToken | null>(null);

  useEffect(() => {
    if (tokens.isError) toast(t("me.tokens.loadFailed"), { tone: "error" });
  }, [tokens.isError, t, toast]);

  const confirmRevoke = () => {
    if (!revoking) return;
    revoke.mutate(revoking.id, {
      onSuccess: () => {
        toast(t("me.tokens.revoke.done"));
        setRevoking(null);
      },
      onError: () => toast(t("me.tokens.revoke.failed"), { tone: "error" }),
    });
  };

  const list = tokens.data;
  // 吊销中：该行菜单禁用
  const revokingId = revoke.isPending ? revoke.variables : undefined;

  return (
    <SettingsColumn wide>
      <header className="flex items-start gap-4 px-1">
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <h2 className="text-[20px] leading-7 font-bold text-fg-default">
            {t("me.tokens.title")}
          </h2>
          <p className="text-body-sm text-fg-muted">
            {t("me.tokens.description")}
          </p>
        </div>
        <Button variant="primary" size="32" onClick={() => setCreating(true)}>
          {t("me.tokens.create")}
        </Button>
      </header>

      {tokens.isLoading && (
        <SettingsCard>
          <div className="flex flex-col gap-3 p-4" aria-busy="true">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        </SettingsCard>
      )}

      {list && list.length === 0 && (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-line-default bg-surface-card py-12">
          <Icon name="key-round" size={40} className="opacity-50" />
          <p className="text-body text-fg-default">{t("me.tokens.empty")}</p>
          <Button variant="outline" size="28" onClick={() => setCreating(true)}>
            {t("me.tokens.create")}
          </Button>
        </div>
      )}

      {list && list.length > 0 && (
        <SettingsCard>
          <div className="flex items-center gap-3 px-4 py-2 text-caption text-fg-muted">
            <span className="min-w-0 flex-1">
              {t("me.tokens.columns.name")}
            </span>
            <span className="hidden w-28 shrink-0 md:block">
              {t("me.tokens.columns.lastUsed")}
            </span>
            <span className="hidden w-28 shrink-0 md:block">
              {t("me.tokens.columns.expires")}
            </span>
            {/* 菜单按钮的占位，让表头与行的右侧对齐 */}
            <span className="w-7 shrink-0" />
          </div>
          {list.map((token) => (
            <TokenRow
              key={token.id}
              token={token}
              busy={revokingId === token.id}
              onDelete={() => setRevoking(token)}
            />
          ))}
        </SettingsCard>
      )}

      <CreateTokenDialog open={creating} onClose={() => setCreating(false)} />
      <ConfirmDialog
        open={revoking !== null}
        onCancel={() => setRevoking(null)}
        onConfirm={confirmRevoke}
        title={t("me.tokens.revoke.title")}
        description={t("me.tokens.revoke.description")}
        confirmLabel={t("me.tokens.revoke.confirm")}
        danger
        loading={revoke.isPending}
      />
    </SettingsColumn>
  );
}

function TokenRow({
  token,
  busy,
  onDelete,
}: {
  token: PersonalAccessToken;
  busy: boolean;
  onDelete: () => void;
}) {
  const t = useT();
  const { formatRelative, formatDate } = useI18n();
  const expired =
    token.expires_at !== null && Date.parse(token.expires_at) <= Date.now();
  const dayOptions = {
    year: "numeric",
    month: "numeric",
    day: "numeric",
  } as const;
  const created = t("me.tokens.created", {
    prefix: token.prefix,
    date: formatDate(token.created_at, dayOptions),
  });
  const lastUsed = token.last_used_at
    ? t("me.tokens.lastUsed", { time: formatRelative(token.last_used_at) })
    : t("me.tokens.unused");
  return (
    <div className="flex min-h-[64px] items-center gap-3 p-4 hover:bg-surface-hover">
      <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-surface-muted text-fg-secondary">
        <Icon name="key-round" size={14} />
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="truncate text-body font-medium text-fg-default">
          {token.name}
        </span>
        <span className="truncate text-caption text-fg-muted">
          {created} · {lastUsed}
        </span>
      </div>
      <span className="hidden w-28 shrink-0 text-body-sm text-fg-muted md:block">
        {token.last_used_at
          ? formatRelative(token.last_used_at)
          : t("me.tokens.never")}
      </span>
      <span
        className={cn(
          "hidden w-28 shrink-0 text-body-sm md:block",
          expired ? "text-fg-subtle" : "text-fg-muted",
        )}
      >
        {token.expires_at
          ? formatDate(token.expires_at, dayOptions)
          : t("me.tokens.noExpiry")}
      </span>
      <Menu
        label={t("me.tokens.menu")}
        placement="bottom-end"
        trigger={({ ref, toggle, open }) => (
          <button
            ref={ref}
            type="button"
            aria-label={t("me.tokens.menu")}
            aria-expanded={open}
            disabled={busy}
            onClick={toggle}
            className="flex size-7 shrink-0 items-center justify-center rounded-md text-fg-secondary hover:bg-surface-muted disabled:opacity-50"
          >
            <Icon name="ellipsis" />
          </button>
        )}
      >
        <MenuItem danger icon={<Icon name="trash-2" />} onSelect={onDelete}>
          {t("me.tokens.delete")}
        </MenuItem>
      </Menu>
    </div>
  );
}

/**
 * 创建令牌。提交后原地切换为「令牌已创建」，明文只在这里显示一次；
 * 关闭（包括复制并关闭）时清掉明文并刷新列表。
 */
function CreateTokenDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const create = useCreateToken();
  const nameId = useId();
  const [name, setName] = useState("");
  const [expiry, setExpiry] = useState<ExpiryKey>("never");
  const [created, setCreated] = useState<PersonalAccessTokenCreated | null>(
    null,
  );
  const [copied, setCopied] = useState(false);
  const busy = create.isPending;

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const close = () => {
    setCreated(null);
    setName("");
    setExpiry("never");
    setCopied(false);
    onClose();
    void queryClient.invalidateQueries({ queryKey: keys.tokens });
  };

  const submit = () => {
    const days = EXPIRY_OPTIONS.find((item) => item.value === expiry)?.days;
    create.mutate(
      { name: name.trim(), expires_in_days: days ?? null },
      {
        onSuccess: (result) => setCreated(result),
        onError: (error) =>
          toast(
            t(
              isApiError(error, "TOKEN_LIMIT")
                ? "me.tokens.dialog.limit"
                : "me.tokens.dialog.createFailed",
            ),
            { tone: "error" },
          ),
      },
    );
  };

  /** 写入剪贴板；失败时保留明文，便于手动选中复制。 */
  const copySecret = async (): Promise<boolean> => {
    if (!created) return false;
    try {
      await navigator.clipboard.writeText(created.secret);
      setCopied(true);
      return true;
    } catch {
      return false;
    }
  };

  if (created)
    return (
      <Dialog
        open={open}
        onClose={close}
        width={480}
        title={t("me.tokens.dialog.createdTitle")}
        description={t("me.tokens.dialog.createdDescription")}
        footer={
          <>
            <Button variant="outline" onClick={close}>
              {t("me.tokens.dialog.close")}
            </Button>
            <Button
              variant="primary"
              onClick={async () => {
                if (await copySecret()) close();
              }}
            >
              {t("me.tokens.dialog.copyAndClose")}
            </Button>
          </>
        }
      >
        <div className="flex items-start gap-2 rounded-lg bg-surface-muted p-3">
          <code className="min-w-0 flex-1 select-all break-all font-mono text-body-sm text-fg-default">
            {created.secret}
          </code>
          <button
            type="button"
            aria-label={t(copied ? "common.copied" : "common.copy")}
            onClick={() => void copySecret()}
            className="flex size-7 shrink-0 items-center justify-center rounded-md text-fg-secondary hover:bg-surface-active"
          >
            <Icon name={copied ? "check" : "copy"} size={14} />
          </button>
        </div>
      </Dialog>
    );

  return (
    <Dialog
      open={open}
      onClose={busy ? undefined : close}
      width={480}
      title={t("me.tokens.dialog.createTitle")}
      description={t("me.tokens.dialog.createDescription")}
      footer={
        <Button
          variant="primary"
          className="!w-full"
          disabled={name.trim() === "" || busy}
          loading={busy}
          onClick={submit}
        >
          {busy ? t("me.tokens.dialog.saving") : t("me.tokens.dialog.save")}
        </Button>
      }
    >
      <div className="flex flex-col gap-2">
        <label
          htmlFor={nameId}
          className="text-body-sm font-medium text-fg-default"
        >
          {t("me.tokens.dialog.name")}
        </label>
        {/* 名称与过期时间同在一个 40 高的输入壳里 */}
        <div className="flex h-10 items-center rounded-lg border border-line-default bg-surface-card focus-within:border-line-focus has-[:disabled]:opacity-50">
          <input
            id={nameId}
            value={name}
            disabled={busy}
            placeholder={t("me.tokens.dialog.namePlaceholder")}
            onChange={(event) => setName(event.target.value)}
            className="h-full min-w-0 flex-1 rounded-l-lg bg-transparent px-3 text-body text-fg-default outline-none placeholder:text-fg-subtle"
          />
          <span className="h-5 w-px shrink-0 bg-line-default" />
          <Select
            aria-label={t("me.tokens.dialog.expires")}
            value={expiry}
            disabled={busy}
            onChange={setExpiry}
            options={EXPIRY_OPTIONS.map((item) => ({
              value: item.value,
              label: t(item.label),
            }))}
            className="!h-full !w-36 !rounded-l-none !rounded-r-lg !border-0 !bg-transparent !px-3 !shadow-none"
          />
        </div>
      </div>
    </Dialog>
  );
}
