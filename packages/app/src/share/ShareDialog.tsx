import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";

import {
  isApiError,
  shareFindings,
  type ShareExpiry,
  type ShareFinding,
  type ShareScope,
  type ShareSnapshotCreateRequest,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Button, Dialog, Segmented, TextInput, useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import { useOrganizationSettings } from "../organization/queries";
import { copyText } from "../tasks/parts";
import { shareErrorMessage } from "./errors";

/** 分享来源：对话、单条消息，或任务评论线程。 */
export type ShareSource =
  | { kind: "CONVERSATION"; conversationId: string; messageId?: string }
  | { kind: "COMMENT"; spaceId: string; taskId: string; commentId: string };

const RULES = new Set([
  "private_key_block",
  "aws_access_key_id",
  "github_token",
  "gitlab_token",
  "anthropic_api_key",
  "openai_api_key",
  "slack_token",
  "google_api_key",
  "stripe_live_key",
  "jwt",
  "authorization_header",
  "bearer_token",
  "url_with_credentials",
  "credential_assignment",
]);

/** 访问者打开的页面地址：令牌放在 URL 片段里，不会随请求发给服务器，也不进 Referer。 */
export function shareUrl(token: string): string {
  return `${window.location.origin}/share#${token}`;
}

export function ShareDialog({
  orgId,
  source,
  open,
  onClose,
  onManage,
}: {
  orgId: string;
  source: ShareSource;
  open: boolean;
  onClose: () => void;
  /** 打开分享管理列表 */
  onManage?: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const cache = useQueryClient();
  // 开关状态只读：关闭时禁用「公开」选项并说明原因
  const settings = useOrganizationSettings(orgId);
  const publicAllowed = settings.data?.allow_public_sharing ?? false;
  const [scope, setScope] = useState<ShareScope>("ORGANIZATION");
  const [expiry, setExpiry] = useState<ShareExpiry>("30d");
  const [title, setTitle] = useState("");
  const [findings, setFindings] = useState<ShareFinding[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [link, setLink] = useState<string | null>(null);
  // 同一次点击重试使用同一个幂等键
  const attempt = useRef<{ key: string; body: string } | null>(null);

  const create = useMutation({
    mutationFn: () => {
      const body: ShareSnapshotCreateRequest = {
        kind: source.kind,
        scope,
        expiry,
        ...(title.trim() ? { title: title.trim() } : {}),
        ...(source.kind === "CONVERSATION"
          ? {
              conversation_id: source.conversationId,
              ...(source.messageId ? { message_id: source.messageId } : {}),
            }
          : {
              space_id: source.spaceId,
              task_id: source.taskId,
              comment_id: source.commentId,
            }),
      };
      const fingerprint = JSON.stringify(body);
      if (attempt.current?.body !== fingerprint)
        attempt.current = { key: crypto.randomUUID(), body: fingerprint };
      return api.shareSnapshots.create(orgId, body, attempt.current.key);
    },
    onSuccess: (created) => {
      attempt.current = null;
      setLink(shareUrl(created.token));
      void cache.invalidateQueries({ queryKey: keys.shareSnapshots(orgId) });
    },
    onError: (err) => {
      if (isApiError(err, "SENSITIVE_CONTENT_DETECTED")) {
        setFindings(shareFindings(err.details));
        setError(null);
        return;
      }
      setFindings([]);
      setError(shareErrorMessage(t, err));
    },
  });

  const close = () => {
    setLink(null);
    setFindings([]);
    setError(null);
    onClose();
  };

  const copy = async () => {
    if (!link) return;
    if (await copyText(link)) toast(t("share.dialog.copied"));
    else toast(t("share.dialog.copyFailed"), { tone: "error" });
  };

  const describe = (finding: ShareFinding) =>
    t("share.dialog.findingPosition", {
      target:
        finding.item === 0
          ? t("share.dialog.findingTitle")
          : t("share.dialog.findingItem", { item: finding.item }),
      line: finding.line,
      column: finding.column,
      rule: t(
        (RULES.has(finding.rule)
          ? `share.rules.${finding.rule}`
          : "share.rules.unknown") as MessageKey,
      ),
    });

  return (
    <Dialog
      open={open}
      onClose={close}
      title={link ? t("share.dialog.linkReady") : t("share.dialog.title")}
      description={
        link
          ? t("share.dialog.linkOnce")
          : source.kind === "CONVERSATION" && source.messageId
            ? t("share.dialog.messageDescription")
            : t("share.dialog.description")
      }
      width={480}
      footer={
        link ? (
          <>
            <Button variant="secondary" onClick={() => void copy()}>
              {t("share.dialog.copy")}
            </Button>
            <Button onClick={close}>{t("share.dialog.done")}</Button>
          </>
        ) : (
          <>
            {onManage && (
              <Button variant="ghost" onClick={onManage} className="mr-auto">
                {t("share.dialog.manage")}
              </Button>
            )}
            <Button variant="secondary" onClick={close}>
              {t("share.dialog.cancel")}
            </Button>
            <Button
              disabled={
                create.isPending || (scope === "PUBLIC" && !publicAllowed)
              }
              onClick={() => create.mutate()}
            >
              {t("share.dialog.create")}
            </Button>
          </>
        )
      }
    >
      {link ? (
        <label className="flex flex-col gap-1.5 text-body-sm text-fg-secondary">
          {t("share.dialog.linkLabel")}
          <input
            readOnly
            value={link}
            aria-label={t("share.dialog.linkLabel")}
            onFocus={(event) => event.currentTarget.select()}
            className="h-9 rounded-md border border-line-default bg-surface-muted px-2.5 text-body text-fg-default"
          />
        </label>
      ) : (
        <div className="flex flex-col gap-4">
          <Segmented
            label={t("share.dialog.scopeLabel")}
            value={scope}
            onChange={(value) => {
              setScope(value);
              setError(null);
            }}
            options={[
              { value: "ORGANIZATION", label: t("share.dialog.scopeOrg") },
              { value: "PUBLIC", label: t("share.dialog.scopePublic") },
            ]}
          />
          <p className="text-body-sm text-fg-muted">
            {scope === "PUBLIC"
              ? publicAllowed
                ? t("share.dialog.scopePublicHelp")
                : t("share.dialog.publicDisabled")
              : t("share.dialog.scopeOrgHelp")}
          </p>
          <Segmented
            label={t("share.dialog.expiryLabel")}
            value={expiry}
            onChange={setExpiry}
            options={[
              { value: "7d", label: t("share.dialog.expiry7d") },
              { value: "30d", label: t("share.dialog.expiry30d") },
              { value: "never", label: t("share.dialog.expiryNever") },
            ]}
          />
          <label className="flex flex-col gap-1.5 text-body-sm text-fg-secondary">
            {t("share.dialog.titleLabel")}
            <TextInput
              value={title}
              maxLength={200}
              placeholder={t("share.dialog.titlePlaceholder")}
              onChange={(event) => setTitle(event.target.value)}
            />
          </label>
          {error && (
            <p role="alert" className="text-body-sm text-danger">
              {error}
            </p>
          )}
          {findings.length > 0 && (
            <div
              role="alert"
              className="flex flex-col gap-1 rounded-md bg-surface-muted p-3 text-body-sm"
            >
              <strong>{t("share.dialog.findingsTitle")}</strong>
              <span>{t("share.dialog.findingsHint")}</span>
              <ul className="list-disc pl-5">
                {findings.map((finding, index) => (
                  <li key={index}>{describe(finding)}</li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </Dialog>
  );
}
