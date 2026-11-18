import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  newIdempotencyKey,
  type QQBindingSession,
  type QQBotDetail,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Checkbox, Dialog, Field, Select } from "@openfox/ui";
import { useApi } from "../context";
import { useMe } from "../agents/queries";
import { useOrgPeople, personName } from "../tasks/queries";
import { useQQWrite, qqKey } from "./qq-hooks";
import { qqError, qqState } from "./qq-model";

export function QQConfirmAction({
  label,
  description,
  execute,
  refresh,
  remote = false,
  disabled = false,
}: {
  label: string;
  description: string;
  execute: (checked: boolean, signal: AbortSignal) => Promise<unknown>;
  refresh: () => void;
  remote?: boolean;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button
        variant="outline"
        disabled={disabled}
        onClick={() => setOpen(true)}
      >
        {label}
      </Button>
      {open && (
        <QQConfirmBody
          label={label}
          description={description}
          execute={execute}
          refresh={refresh}
          remote={remote}
          close={() => setOpen(false)}
        />
      )}
    </>
  );
}
function QQConfirmBody({
  label,
  description,
  execute,
  refresh,
  remote,
  close,
}: {
  label: string;
  description: string;
  execute: (checked: boolean, signal: AbortSignal) => Promise<unknown>;
  refresh: () => void;
  remote: boolean;
  close: () => void;
}) {
  const t = useT(),
    write = useQQWrite(refresh),
    [checked, setChecked] = useState(false);
  return (
    <Dialog
      open
      title={label}
      onClose={close}
      footer={
        <>
          <Button variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={write.busy || write.blocked || (remote && !checked)}
            onClick={() => void write.run((s) => execute(checked, s), close)}
          >
            {t("common.confirm")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <p>{description}</p>
        {remote && (
          <label className="flex items-start gap-2">
            <Checkbox
              checked={checked}
              onChange={(e) => setChecked(e.target.checked)}
            />
            <span>{t("integrations.qq.remoteChecked")}</span>
          </label>
        )}{" "}
        {!!write.error && (
          <Callout tone="danger">{qqError(t, write.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
export function QQBindingDialog({
  orgId,
  initial,
  onClose,
  refresh,
}: {
  orgId: string;
  initial: QQBindingSession;
  onClose: () => void;
  refresh: () => void;
}) {
  const t = useT(),
    api = useApi(),
    me = useMe(),
    write = useQQWrite(refresh),
    [consent, setConsent] = useState(false);
  const q = useQuery({
    queryKey: [...qqKey(orgId), "binding", initial.id],
    queryFn: ({ signal }) => api.qq.binding(orgId, initial.id, signal),
    retry: false,
    refetchInterval: (q) =>
      ["PENDING", "OBSERVED"].includes(q.state.data?.status ?? initial.status)
        ? 2000
        : false,
  });
  const v = q.data ?? initial,
    live = Date.parse(v.expires_at) > Date.now(),
    mine = me.data?.id === v.owner_user_id;
  const command =
    initial.code && v.status === "PENDING" && live
      ? (v.purpose === "GROUP_DISCOVERY"
          ? "/openfox-group "
          : "/openfox-bind ") + initial.code
      : "";
  const group = v.purpose === "GROUP_DISCOVERY",
    confirm = v.status === "OBSERVED" && live && mine && (!group || consent);
  return (
    <Dialog
      open
      title={t("integrations.qq.bindingTitle")}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          {["PENDING", "OBSERVED"].includes(v.status) && (
            <Button
              variant="outline"
              disabled={write.busy || write.blocked}
              onClick={() =>
                void write.run(
                  (s) => api.qq.cancelBinding(orgId, v.id, v.version, s),
                  onClose,
                )
              }
            >
              {t("integrations.qq.cancelBinding")}
            </Button>
          )}
          <Button
            disabled={!confirm || write.busy || write.blocked || q.isError}
            onClick={() =>
              void write.run(
                (s) =>
                  api.qq.confirmBinding(
                    orgId,
                    v.id,
                    {
                      version: v.version,
                      public_reply_consent: group && consent,
                    },
                    s,
                  ),
                onClose,
              )
            }
          >
            {t(
              group
                ? "integrations.qq.confirmGroup"
                : "integrations.qq.confirmIdentity",
            )}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <p>{qqState(t, live ? v.status : "EXPIRED")}</p>
        <p>
          {t(
            v.purpose === "GROUP_ASSIGN"
              ? "integrations.qq.acceptHint"
              : group
                ? "integrations.qq.groupCommand"
                : "integrations.qq.privateCommand",
          )}
        </p>
        {command && (
          <pre className="break-all whitespace-pre-wrap rounded-lg bg-bg-muted p-3 select-all">
            {command}
          </pre>
        )}
        {v.status === "PENDING" && <p>{t("integrations.qq.waitingProof")}</p>}
        {v.subject_id && (
          <p className="break-all">
            {t("integrations.qq.source", {
              scope: group || v.purpose === "GROUP_ASSIGN" ? "GROUP" : "C2C",
              chat: v.chat_id,
              subject: v.subject_id,
            })}
          </p>
        )}
        {group && (
          <label className="flex items-start gap-2">
            <Checkbox
              checked={consent}
              onChange={(e) => setConsent(e.target.checked)}
            />
            <span>{t("integrations.qq.groupConsent")}</span>
          </label>
        )}
        <p>
          {t("integrations.qq.expires", {
            time: new Date(v.expires_at).toLocaleString(),
          })}
        </p>
        {!!(q.isError || write.error) && (
          <Callout tone="danger">{qqError(t, write.error ?? q.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
export function QQAssignDialog({
  orgId,
  detail,
  onClose,
  refresh,
}: {
  orgId: string;
  detail: QQBotDetail;
  onClose: () => void;
  refresh: () => void;
}) {
  const api = useApi(),
    t = useT(),
    people = useOrgPeople(orgId),
    write = useQQWrite(refresh);
  const [candidate, setCandidate] = useState<string | null>(null),
    [owner, setOwner] = useState<string | null>(null),
    [key] = useState(newIdempotencyKey),
    [invited, setInvited] = useState(false);
  const candidates = detail.candidates.filter((c) =>
    detail.groups.some((g) => g.enabled && g.chat_id === c.chat_id),
  );
  const selected = candidates.find((c) => c.receipt_id === candidate),
    group = detail.groups.find(
      (g) => g.enabled && g.chat_id === selected?.chat_id,
    );
  const options = (people.data ?? []).map((p) => ({
    value: p.id,
    label: personName(p),
  }));
  return (
    <Dialog
      open
      title={t("integrations.qq.invite")}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button
            disabled={
              !selected ||
              !group ||
              !owner ||
              invited ||
              write.busy ||
              write.blocked
            }
            onClick={() => {
              if (!selected || !group || !owner) return;
              void write.run(
                (s) =>
                  api.qq.startBinding(
                    orgId,
                    detail.bot.id,
                    {
                      version: detail.bot.version,
                      purpose: "GROUP_ASSIGN",
                      receipt_id: selected.receipt_id,
                      group_epoch_id: group.id,
                      owner_user_id: owner,
                      idempotency_key: key,
                    },
                    s,
                  ),
                () => setInvited(true),
              );
            }}
          >
            {t("integrations.qq.invite")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <p>{t("integrations.qq.groupIdentityHelp")}</p>
        {candidates.length === 0 && <p>{t("integrations.qq.noCandidates")}</p>}
        <Field label={t("integrations.qq.candidate")}>
          <Select
            aria-label={t("integrations.qq.candidate")}
            value={candidate}
            onChange={setCandidate}
            options={candidates.map((c) => ({
              value: c.receipt_id,
              label: `${c.chat_id} · ${c.subject_id}`,
            }))}
          />
        </Field>
        <Field label={t("integrations.qq.member")}>
          <Select
            aria-label={t("integrations.qq.member")}
            value={owner}
            onChange={setOwner}
            options={options}
          />
        </Field>
        {invited && (
          <Callout tone="info">
            {t("integrations.qq.invitationPending")}
          </Callout>
        )}
        {!!write.error && (
          <Callout tone="danger">{qqError(t, write.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
