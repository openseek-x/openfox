import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { useParams } from "react-router-dom";

import type { EmailChallenge, Me } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Avatar,
  Button,
  Dialog,
  Icon,
  IconButton,
  OtpInput,
  Spinner,
  Textarea,
  TextInput,
  Tooltip,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "../spaces/settings/parts";
import {
  emailErrorMessage,
  refreshProfile,
  useCountdown,
  useUpdateProfile,
  useUploadAvatar,
} from "./queries";

// 个人资料：头像、全名、邮箱（两步修改）、简介。

const MAX_AVATAR_BYTES = 5 * 1024 * 1024;
const NAME_MAX = 80;
const BIO_MAX = 280;
const CODE_LENGTH = 6;
const NEW_EMAIL_COOLDOWN_MS = 60_000;
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export function ProfilePage() {
  const { orgId = "" } = useParams();
  const api = useApi();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  if (!me.data) return null;
  return <ProfileBody orgId={orgId} me={me.data} />;
}

function ProfileBody({ orgId, me }: { orgId: string; me: Me }) {
  const t = useT();
  return (
    <SettingsColumn narrow>
      <SettingsSection
        title={t("settings.items.profile")}
        description={t("me.profile.description")}
      >
        <SettingsCard>
          <SettingRow
            title={t("me.profile.avatar.title")}
            description={t("me.profile.avatar.help")}
            control={<AvatarPicker orgId={orgId} me={me} />}
          />
          <NameRow orgId={orgId} me={me} />
          <EmailRow orgId={orgId} me={me} />
          <BioRow orgId={orgId} me={me} />
        </SettingsCard>
      </SettingsSection>
    </SettingsColumn>
  );
}

/** 64×64 圆形按钮：悬停显示 40% 遮罩和相机图标，上传中显示转圈并禁用。 */
function AvatarPicker({ orgId, me }: { orgId: string; me: Me }) {
  const t = useT();
  const toast = useToast();
  const upload = useUploadAvatar(orgId);
  const input = useRef<HTMLInputElement | null>(null);
  const pick = (file: File) => {
    // 超过 5 MB 直接提示，不发请求
    if (file.size > MAX_AVATAR_BYTES) {
      toast(t("me.profile.avatarFailed"), { tone: "error" });
      return;
    }
    upload.mutate(file, {
      onSuccess: () => toast(t("me.profile.avatar.uploaded")),
      onError: () => toast(t("me.profile.avatarFailed"), { tone: "error" }),
    });
  };
  const uploading = upload.isPending;
  return (
    <>
      <button
        type="button"
        aria-label={t("me.profile.avatar.choose")}
        disabled={uploading}
        onClick={() => input.current?.click()}
        className="group relative size-16 shrink-0 rounded-full disabled:cursor-wait"
      >
        <Avatar name={me.full_name || me.email} src={me.avatar_url} size={64} />
        <span
          className={cn(
            "absolute inset-0 items-center justify-center rounded-full bg-black/40 text-fg-inverse",
            uploading ? "flex" : "hidden group-hover:flex",
          )}
        >
          {uploading ? <Spinner size={20} /> : <Icon name="camera" size={20} />}
        </span>
      </button>
      <input
        ref={input}
        type="file"
        accept="image/png,image/jpeg,image/webp"
        className="sr-only"
        tabIndex={-1}
        onChange={(event) => {
          const file = event.target.files?.[0];
          event.target.value = "";
          if (file) pick(file);
        }}
      />
    </>
  );
}

/** 全名：失焦或回车保存；清空后失焦不发请求，直接还原并提示。 */
function NameRow({ orgId, me }: { orgId: string; me: Me }) {
  const t = useT();
  const toast = useToast();
  const update = useUpdateProfile(orgId);
  const saved = me.full_name;
  const [draft, setDraft] = useState(saved);
  useEffect(() => setDraft(saved), [saved]);
  const commit = () => {
    if (draft === saved) return;
    const next = draft.trim();
    if (!next) {
      setDraft(saved);
      toast(t("me.profile.name.empty"), { tone: "error" });
      return;
    }
    if (next === saved) {
      setDraft(saved);
      return;
    }
    update.mutate(
      { full_name: next },
      {
        onSuccess: () => toast(t("me.profile.saved")),
        onError: () => {
          setDraft(saved);
          toast(t("me.profile.saveFailed"), { tone: "error" });
        },
      },
    );
  };
  return (
    <SettingRow
      title={t("me.profile.name.title")}
      description={t("me.profile.name.help")}
      control={
        <div className="w-[276px]">
          <TextInput
            aria-label={t("me.profile.name.label")}
            value={draft}
            maxLength={NAME_MAX}
            disabled={update.isPending}
            onChange={(event) => setDraft(event.target.value)}
            onBlur={commit}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                event.currentTarget.blur();
              }
            }}
          />
        </div>
      }
    />
  );
}

/** 邮箱：显示当前邮箱，「解绑」始终禁用（至少保留一个登录方式）；「修改」走两步验证。 */
function EmailRow({ orgId, me }: { orgId: string; me: Me }) {
  const t = useT();
  const [step, setStep] = useState<"verify" | "change" | null>(null);
  const [verifiedId, setVerifiedId] = useState<string | null>(null);
  return (
    <>
      <SettingRow
        title={t("me.profile.email.title")}
        description={t("me.profile.email.help")}
        control={
          <div className="flex items-center gap-2">
            <span
              title={me.email}
              className="max-w-[240px] truncate text-body-sm text-fg-default"
            >
              {me.email}
            </span>
            <Button
              variant="outline"
              size="28"
              onClick={() => setStep("verify")}
            >
              {t("me.profile.email.change")}
            </Button>
            <Tooltip content={t("me.profile.email.unlinkDisabled")}>
              <span className="inline-flex">
                <Button variant="outline" size="28" disabled>
                  {t("me.profile.email.unlink")}
                </Button>
              </span>
            </Tooltip>
          </div>
        }
      />
      {step === "verify" && (
        <VerifyEmailDialog
          email={me.email}
          onClose={() => setStep(null)}
          onVerified={(challengeId) => {
            setVerifiedId(challengeId);
            setStep("change");
          }}
        />
      )}
      {step === "change" && verifiedId && (
        <ChangeEmailDialog
          orgId={orgId}
          challengeId={verifiedId}
          onClose={() => setStep(null)}
        />
      )}
    </>
  );
}

/** 第 1 步：打开即向当前邮箱发码；验证通过后进入修改邮箱。 */
function VerifyEmailDialog({
  email,
  onClose,
  onVerified,
}: {
  email: string;
  onClose: () => void;
  onVerified: (challengeId: string) => void;
}) {
  const t = useT();
  const toast = useToast();
  const api = useApi();
  const [challenge, setChallenge] = useState<EmailChallenge | null>(null);
  const [code, setCode] = useState("");
  const started = useRef(false);
  const send = useMutation({
    mutationFn: () => api.me.emailChange.start(),
    onSuccess: (next) => {
      setChallenge(next);
      setCode("");
    },
    onError: (error) => toast(emailErrorMessage(t, error), { tone: "error" }),
  });
  const verify = useMutation({
    mutationFn: (input: { challengeId: string; code: string }) =>
      api.me.emailChange.verifyCurrent(input.challengeId, input.code),
    onSuccess: (_result, input) => onVerified(input.challengeId),
    onError: (error) => toast(emailErrorMessage(t, error), { tone: "error" }),
  });
  // 严格模式会重复执行 effect，用 ref 保证只发一次
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    send.mutate();
  }, []);
  const resendAt = challenge ? Date.parse(challenge.resend_after) : null;
  const cooldown = useCountdown(resendAt);
  return (
    <Dialog
      open
      width={448}
      onClose={verify.isPending ? undefined : onClose}
      title={t("me.profile.verify.title")}
      footer={
        <>
          <Button variant="ghost" disabled={verify.isPending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={verify.isPending}
            disabled={!challenge || code.length < CODE_LENGTH}
            onClick={() =>
              challenge &&
              verify.mutate({ challengeId: challenge.challenge_id, code })
            }
          >
            {t("me.profile.verify.next")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <p className="text-body-sm text-fg-muted">
          {t("me.profile.verify.sentTo", { email })}
        </p>
        <OtpInput
          value={code}
          onChange={setCode}
          disabled={verify.isPending}
          autoFocus
        />
        <Button
          variant="ghost"
          size="28"
          className="self-start"
          disabled={cooldown > 0 || send.isPending}
          onClick={() => send.mutate()}
        >
          {cooldown > 0
            ? t("me.profile.verify.resendIn", { seconds: cooldown })
            : t("me.profile.verify.resend")}
        </Button>
      </div>
    </Dialog>
  );
}

/** 第 2 步：输入新邮箱，发送验证码到新邮箱后保存。 */
function ChangeEmailDialog({
  orgId,
  challengeId,
  onClose,
}: {
  orgId: string;
  challengeId: string;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const api = useApi();
  const queryClient = useQueryClient();
  const emailId = useId();
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [challenge, setChallenge] = useState<EmailChallenge | null>(null);
  const [cooldownUntil, setCooldownUntil] = useState<number | null>(null);
  const cooldown = useCountdown(cooldownUntil);
  const request = useMutation({
    mutationFn: (next: string) =>
      api.me.emailChange.requestNew(challengeId, next),
    onSuccess: (next) => {
      setChallenge(next);
      setCode("");
      setCooldownUntil(Date.now() + NEW_EMAIL_COOLDOWN_MS);
    },
    onError: (error) => toast(emailErrorMessage(t, error), { tone: "error" }),
  });
  const confirm = useMutation({
    mutationFn: (input: { challengeId: string; code: string }) =>
      api.me.emailChange.confirm(input.challengeId, input.code),
    onSuccess: (me) => {
      refreshProfile(queryClient, orgId, me);
      toast(t("me.profile.emailChanged"));
      onClose();
    },
    onError: (error) => toast(emailErrorMessage(t, error), { tone: "error" }),
  });
  const trimmed = email.trim();
  // 验证码只对发送时的邮箱有效，改了邮箱要重新发送
  const canSave =
    Boolean(challenge) &&
    EMAIL_PATTERN.test(trimmed) &&
    code.length === CODE_LENGTH;
  const sendCode = () => {
    if (!EMAIL_PATTERN.test(trimmed)) {
      toast(t("me.profile.errors.emailInvalid"), { tone: "error" });
      return;
    }
    request.mutate(trimmed);
  };
  return (
    <Dialog
      open
      width={448}
      onClose={confirm.isPending ? undefined : onClose}
      title={t("me.profile.changeEmail.title")}
      footer={
        <>
          <Button
            variant="ghost"
            disabled={confirm.isPending}
            onClick={onClose}
          >
            {t("common.cancel")}
          </Button>
          <Button
            loading={confirm.isPending}
            disabled={!canSave}
            onClick={() =>
              challenge &&
              confirm.mutate({ challengeId: challenge.challenge_id, code })
            }
          >
            {t("me.profile.changeEmail.save")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="flex flex-col gap-1.5">
          <label htmlFor={emailId} className="text-body-sm font-medium">
            {t("me.profile.changeEmail.newEmail")}
          </label>
          <div className="flex items-center gap-2">
            <div className="min-w-0 flex-1">
              <TextInput
                id={emailId}
                type="email"
                autoComplete="off"
                value={email}
                disabled={request.isPending || confirm.isPending}
                onChange={(event) => {
                  setEmail(event.target.value);
                  setChallenge(null);
                }}
              />
            </div>
            <Button
              variant="outline"
              size="32"
              disabled={
                cooldown > 0 ||
                request.isPending ||
                confirm.isPending ||
                !trimmed
              }
              onClick={sendCode}
            >
              {cooldown > 0
                ? t("me.profile.changeEmail.sendCountdown", {
                    seconds: cooldown,
                  })
                : t("me.profile.changeEmail.send")}
            </Button>
          </div>
        </div>
        <div className="flex flex-col gap-1.5">
          <span className="text-body-sm font-medium">
            {t("me.profile.changeEmail.code")}
          </span>
          <OtpInput
            value={code}
            onChange={setCode}
            disabled={!challenge || confirm.isPending}
          />
        </div>
      </div>
    </Dialog>
  );
}

/** 简介：行内显示文本，点铅笔打开弹窗编辑；内容没变化直接关闭。 */
function BioRow({ orgId, me }: { orgId: string; me: Me }) {
  const t = useT();
  const toast = useToast();
  const update = useUpdateProfile(orgId);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(me.bio);
  const save = () => {
    if (draft === me.bio) {
      setEditing(false);
      return;
    }
    update.mutate(
      { bio: draft },
      {
        onSuccess: () => {
          toast(t("me.profile.saved"));
          setEditing(false);
        },
        onError: () => toast(t("me.profile.saveFailed"), { tone: "error" }),
      },
    );
  };
  const saving = update.isPending;
  return (
    <>
      <SettingRow
        title={t("me.profile.bio.title")}
        control={
          <div className="flex max-w-[360px] items-center gap-1">
            <span
              className={cn(
                "truncate text-body-sm",
                me.bio ? "text-fg-default" : "text-fg-subtle",
              )}
            >
              {me.bio || t("me.profile.bio.placeholder")}
            </span>
            <IconButton
              label={t("me.profile.bio.edit")}
              icon={<Icon name="pencil" />}
              size="28"
              variant="ghost"
              disabled={saving}
              onClick={() => {
                setDraft(me.bio);
                setEditing(true);
              }}
            />
          </div>
        }
      />
      <Dialog
        open={editing}
        width={448}
        onClose={saving ? undefined : () => setEditing(false)}
        title={t("me.profile.bio.dialogTitle")}
        footer={
          <>
            <Button
              variant="ghost"
              disabled={saving}
              onClick={() => setEditing(false)}
            >
              {t("common.cancel")}
            </Button>
            <Button loading={saving} onClick={save}>
              {t("me.profile.saveChanges")}
            </Button>
          </>
        }
      >
        <Textarea
          autoFocus
          rows={5}
          maxLength={BIO_MAX}
          aria-label={t("me.profile.bio.dialogTitle")}
          placeholder={t("me.profile.bio.placeholder")}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
        />
      </Dialog>
    </>
  );
}
