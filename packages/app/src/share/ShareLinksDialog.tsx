import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import type { ShareSnapshotMeta } from "@openfox/api";
import { useI18n, useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Dialog,
  TextInput,
  useToast,
} from "@openfox/ui";

import { shareUrl } from "./ShareDialog";
import { copyText } from "../tasks/parts";
import { keys, useApi } from "../context";

/** 分享管理列表：普通成员只看到自己的，所有者与管理员看到全组织（由服务端裁剪）。 */
export function ShareLinksDialog({
  orgId,
  open,
  onClose,
}: {
  orgId: string;
  open: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const { formatDate } = useI18n();
  const api = useApi();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const toast = useToast();
  const cache = useQueryClient();
  const [recoveredLink, setRecoveredLink] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<ShareSnapshotMeta | null>(null);
  const list = useQuery({
    queryKey: keys.shareSnapshots(orgId),
    queryFn: () => api.shareSnapshots.list(orgId),
    enabled: open,
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.shareSnapshots.revoke(orgId, id),
    onSuccess: async () => {
      setRevoking(null);
      setRecoveredLink(null);
      toast(t("share.manage.revoked"));
      await cache.invalidateQueries({ queryKey: keys.shareSnapshots(orgId) });
    },
    onError: () => {
      setRevoking(null);
      toast(t("share.errors.revokeFailed"), { tone: "error" });
    },
  });
  const recover = useMutation({
    mutationFn: (id: string) => api.shareSnapshots.recover(orgId, id),
    onSuccess: async (item) => {
      setRecoveredLink(shareUrl(item.token));
      toast(
        t(
          (await copyText(shareUrl(item.token)))
            ? "share.dialog.copied"
            : "share.dialog.copyFailed",
        ),
      );
    },
    onError: () => toast(t("share.errors.recoverFailed"), { tone: "error" }),
  });
  const date = (value: string) =>
    formatDate(new Date(value), { dateStyle: "medium", timeStyle: "short" });
  return (
    <>
      <Dialog
        open={open}
        onClose={onClose}
        title={t("share.manage.title")}
        description={t("share.manage.description")}
        width={560}
        footer={<Button onClick={onClose}>{t("share.dialog.done")}</Button>}
      >
        {recoveredLink && (
          <TextInput
            aria-label={t("share.dialog.linkLabel")}
            value={recoveredLink}
            readOnly
          />
        )}
        {list.isError ? (
          <p role="alert" className="text-body-sm text-danger">
            {t("share.errors.listFailed")}
          </p>
        ) : (list.data ?? []).length === 0 ? (
          list.isLoading ? null : (
            <p className="text-body-sm text-fg-muted">
              {t("share.manage.empty")}
            </p>
          )
        ) : (
          <ul className="flex max-h-[360px] flex-col divide-y divide-line-soft overflow-y-auto">
            {(list.data ?? []).map((item) => (
              <li key={item.id} className="flex items-start gap-3 py-2.5">
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <span className="truncate text-body font-medium">
                    {item.title}
                  </span>
                  <span className="text-caption text-fg-muted">
                    {[
                      t(
                        item.kind === "CONVERSATION"
                          ? "share.manage.kindConversation"
                          : "share.manage.kindComment",
                      ),
                      t(
                        item.scope === "PUBLIC"
                          ? "share.manage.scopePublic"
                          : "share.manage.scopeOrg",
                      ),
                      t("share.manage.items", { count: item.item_count }),
                      t("share.manage.creator", { name: item.creator_name }),
                    ].join(" · ")}
                  </span>
                  <span className="text-caption text-fg-muted">
                    {t("share.manage.createdAt", {
                      date: date(item.created_at),
                    })}
                    {" · "}
                    {item.expires_at
                      ? t("share.manage.expiresAt", {
                          date: date(item.expires_at),
                        })
                      : t("share.manage.neverExpires")}
                  </span>
                </div>
                <span className="shrink-0 text-caption text-fg-secondary">
                  {t(`share.manage.status.${item.status}` as MessageKey)}
                </span>
                {item.status === "ACTIVE" &&
                  item.created_by === me.data?.id && (
                    <Button
                      size="28"
                      variant="secondary"
                      disabled={recover.isPending}
                      onClick={() => recover.mutate(item.id)}
                    >
                      {t("share.manage.copy")}
                    </Button>
                  )}
                {item.status === "ACTIVE" && (
                  <Button
                    variant="secondary"
                    size="28"
                    aria-label={t("share.manage.revoke", { title: item.title })}
                    onClick={() => setRevoking(item)}
                  >
                    {t("share.manage.revokeConfirm")}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </Dialog>
      <ConfirmDialog
        open={revoking !== null}
        title={t("share.manage.revokeTitle")}
        description={t("share.manage.revokeBody")}
        confirmLabel={t("share.manage.revokeConfirm")}
        onCancel={() => setRevoking(null)}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
      />
    </>
  );
}
