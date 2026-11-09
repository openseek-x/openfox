import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type KeyboardEvent } from "react";

import type { OrganizationRole } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Field, Icon, Select, cn, useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import { organizationErrorMessage } from "../organization/queries";
import { useOrgRole } from "../settings/common";
import { InviteLinkPanel } from "./InviteLinkPanel";
import { InviteSpacesField, useDefaultInviteSpace } from "./InviteSpacesField";
import { isValidEmail, mergeEmails, splitEmails } from "./model";

const ROLES = [
  "MEMBER",
  "ADMIN",
  "GUEST",
] as const satisfies ReadonlyArray<OrganizationRole>;

/**
 * 邀请成员：「通过邮箱」与「通过链接」两个页签。
 * 输入逗号、空格或回车时把文字转成邮箱 chip，非法地址标红；每个邮箱各发一次邀请（带角色与加入空间）。
 * 管理员不能邀请管理员（只有所有者可以）。
 */
export function InviteDialog({
  orgId,
  onClose,
}: {
  orgId: string;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const [emails, setEmails] = useState<string[]>([]);
  const [text, setText] = useState("");
  const [role, setRole] = useState<(typeof ROLES)[number]>("MEMBER");
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState<"email" | "link">("email");
  const [spaceIds, setSpaceIds] = useState<string[] | null>(null);
  const defaultSpace = useDefaultInviteSpace(orgId);
  const queryClient = useQueryClient();
  const isOwner = useOrgRole(orgId) === "OWNER";
  // 加入空间默认勾选第一个公开空间（空间列表加载后才知道）
  useEffect(() => {
    if (spaceIds === null && defaultSpace) setSpaceIds([defaultSpace]);
  }, [defaultSpace, spaceIds]);

  const commit = (raw: string) => {
    const parts = splitEmails(raw);
    if (parts.length) setEmails((list) => mergeEmails(list, parts));
    setText("");
  };
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter" || event.key === "," || event.key === " ") {
      event.preventDefault();
      commit(text);
    } else if (event.key === "Backspace" && !text && emails.length) {
      setEmails((list) => list.slice(0, -1));
    }
  };

  const all = text.trim() ? mergeEmails(emails, splitEmails(text)) : emails;
  const invalid = all.filter((email) => !isValidEmail(email));

  const submit = async () => {
    commit(text);
    if (all.length === 0) return;
    if (invalid.length > 0) {
      toast(t("organization.invite.invalidToast"), { tone: "error" });
      return;
    }
    setBusy(true);
    const results = await Promise.allSettled(
      all.map((email) =>
        api.invitations.create(orgId, {
          email,
          role,
          space_ids: spaceIds ?? [],
        }),
      ),
    );
    setBusy(false);
    void queryClient.invalidateQueries({ queryKey: keys.invitations(orgId) });
    // 创建失败（如该邮箱已是成员）的邮箱留在输入框里；已创建但邮件没发出去的在「待接受邀请」里可重试
    const rejected = results.flatMap((result, index) =>
      result.status === "rejected"
        ? [{ email: all[index]!, reason: result.reason as unknown }]
        : [],
    );
    const undelivered = results.some(
      (result) =>
        result.status === "fulfilled" &&
        result.value.delivery_status === "FAILED",
    );
    if (rejected.length === 0 && !undelivered) {
      toast(t("organization.invite.sent"));
      onClose();
      return;
    }
    toast(
      rejected[0]
        ? organizationErrorMessage(
            t,
            rejected[0].reason,
            "organization.invite.failed",
          )
        : t("organization.invite.failed"),
      { tone: "error" },
    );
    if (rejected.length === 0) onClose();
    else setEmails(rejected.map((item) => item.email));
  };

  return (
    <Dialog
      open
      onClose={busy ? undefined : onClose}
      title={t("agents.invite.title")}
      width={672}
      footer={
        tab === "email" ? (
          <>
            <Button variant="outline" onClick={onClose} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button
              loading={busy}
              disabled={all.length === 0}
              leading={<Icon name="send" />}
              onClick={() => void submit()}
            >
              {busy
                ? t("organization.invite.sending")
                : t("agents.invite.submit")}
            </Button>
          </>
        ) : undefined
      }
    >
      <div className="flex flex-col gap-4 px-0.5 pb-0.5">
        <div
          role="tablist"
          aria-label={t("agents.invite.tabs.label")}
          className="grid grid-cols-2 rounded-lg bg-surface-muted p-[3px]"
        >
          {(["email", "link"] as const).map((id) => (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={tab === id}
              disabled={busy}
              onClick={() => setTab(id)}
              className={cn(
                "h-8 rounded-md text-body",
                tab === id
                  ? "bg-surface-card font-medium shadow-card"
                  : "text-fg-muted",
              )}
            >
              {t(`agents.invite.tabs.${id}`)}
            </button>
          ))}
        </div>
        {tab === "link" ? (
          <InviteLinkPanel orgId={orgId} />
        ) : (
          <>
            <Field
              label={t("agents.invite.emails")}
              helper={t("agents.invite.emailsHelp")}
              error={
                invalid.length
                  ? t("agents.invite.invalid", { count: invalid.length })
                  : undefined
              }
            >
              <div className="flex min-h-24 flex-wrap content-start gap-1.5 rounded-lg border border-line-default p-2.5 focus-within:border-line-focus">
                {emails.map((email) => {
                  const bad = !isValidEmail(email);
                  return (
                    <span
                      key={email}
                      data-invalid={bad || undefined}
                      className={cn(
                        "flex h-6 items-center gap-1 rounded-md px-2 text-body-sm",
                        bad ? "bg-danger-soft text-danger" : "bg-surface-muted",
                      )}
                    >
                      {email}
                      <button
                        type="button"
                        aria-label={t("agents.invite.removeEmail", { email })}
                        onClick={() =>
                          setEmails((list) => list.filter((e) => e !== email))
                        }
                        className="text-fg-muted hover:text-fg-default"
                      >
                        <Icon name="x" size={12} />
                      </button>
                    </span>
                  );
                })}
                <input
                  autoFocus
                  aria-label={t("agents.invite.emails")}
                  value={text}
                  placeholder={
                    emails.length ? "" : t("agents.invite.emailsPlaceholder")
                  }
                  onChange={(event) => {
                    const value = event.target.value;
                    if (/[,，\s]/.test(value)) commit(value);
                    else setText(value);
                  }}
                  onPaste={(event) => {
                    event.preventDefault();
                    commit(text + event.clipboardData.getData("text"));
                  }}
                  onBlur={() => commit(text)}
                  onKeyDown={onKeyDown}
                  className="min-w-[160px] flex-1 bg-transparent text-body outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
                />
              </div>
            </Field>
            <Field
              label={t("agents.invite.role")}
              helper={t(`agents.invite.roles.${role}_DESC`)}
            >
              <Select
                value={role}
                disabled={busy}
                onChange={setRole}
                options={ROLES.map((value) => ({
                  value,
                  label: t(`agents.invite.roles.${value}`),
                  description:
                    value === "ADMIN" && !isOwner
                      ? t("organization.invite.adminOnlyOwner")
                      : t(`agents.invite.roles.${value}_DESC`),
                  disabled: value === "ADMIN" && !isOwner,
                }))}
              />
            </Field>
            <Field
              label={t("agents.invite.spaces")}
              helper={t("agents.invite.spacesHelp")}
            >
              <InviteSpacesField
                orgId={orgId}
                value={spaceIds ?? []}
                disabled={busy}
                onChange={setSpaceIds}
              />
            </Field>
          </>
        )}
      </div>
    </Dialog>
  );
}
