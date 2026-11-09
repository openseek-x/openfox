import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";

import { isApiError } from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import {
  Button,
  Field,
  Icon,
  OtpInput,
  TextInput,
  useToast,
} from "@openfox/ui";

import { useApi } from "../context";
import { desktopBridge, type DesktopAccount, isDesktop } from "../desktop";
import { AuthLayout } from "./AuthLayout";

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const RESEND_SECONDS = 60;

/** 登录后的跳转地址只接受站内路径，防止开放重定向。 */
export function safeNext(value: string | null): string | null {
  if (
    !value ||
    !value.startsWith("/") ||
    value.startsWith("//") ||
    value.startsWith("/\\")
  )
    return null;
  return value;
}

/** 剩余等待时间：不到 1 分钟按秒；不到 1 小时按「分 秒」；更长按「小时 分」（分钟向上取整）。 */
export function formatWait(t: Translate, ms: number): string {
  const seconds = Math.max(1, Math.ceil(ms / 1000));
  if (seconds < 60) return t("auth.wait.seconds", { s: seconds });
  if (seconds < 3600) {
    const m = Math.floor(seconds / 60);
    const s = seconds % 60;
    return s === 0
      ? t("auth.wait.minutes", { m })
      : t("auth.wait.minutesSeconds", { m, s });
  }
  const totalMinutes = Math.ceil(seconds / 60);
  const h = Math.floor(totalMinutes / 60);
  const m = totalMinutes % 60;
  return m === 0
    ? t("auth.wait.hours", { h })
    : t("auth.wait.hoursMinutes", { h, m });
}

/** 被限流（429）：文案键、截止时间（来自 Retry-After，没有时为空）、显示在哪一步。 */
interface RateLimit {
  key: "dailyLimit" | "rateLimited" | "tooManyAttempts";
  until: number | null;
  step: "email" | "code";
}

/** 邮箱验证码登录：输入邮箱 → 发送 6 位验证码 → 校验并建立会话；新邮箱自动注册。 */
export function LoginPage() {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [params] = useSearchParams();
  const next = safeNext(params.get("next"));

  const [accounts, setAccounts] = useState<DesktopAccount[]>([]);
  useEffect(() => {
    void desktopBridge()
      ?.accounts?.()
      .then((a) => setAccounts(a.filter((s) => !s.current && s.name)))
      .catch(() => {});
  }, []);
  const [step, setStep] = useState<"email" | "code">("email");
  const [email, setEmail] = useState("");
  const [emailError, setEmailError] = useState<string | null>(null);
  // 发送验证码失败（每日上限、请求过频、服务不可用等）：常驻显示在表单里，toast 消失后用户仍能看到原因。
  const [sendError, setSendError] = useState<string | null>(null);
  // 429 限流：按 Retry-After 倒计时显示剩余时间，时间到了自动清除，可以重新提交。
  const [limit, setLimit] = useState<RateLimit | null>(null);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!limit?.until) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [limit]);

  useEffect(() => {
    if (limit?.until && now >= limit.until) setLimit(null);
  }, [limit, now]);

  const limitFrom = (
    key: RateLimit["key"],
    error: { retryAfter?: number },
    where: RateLimit["step"],
  ) => {
    setLimit({
      key,
      until: error.retryAfter ? Date.now() + error.retryAfter * 1000 : null,
      step: where,
    });
  };

  const limitMessage = (where: RateLimit["step"]): string | null => {
    if (!limit || limit.step !== where) return null;
    if (!limit.until) return t(`auth.${limit.key}`);
    return t(`auth.${limit.key}In`, { time: formatWait(t, limit.until - now) });
  };
  const [code, setCode] = useState("");
  const [codeError, setCodeError] = useState<string | null>(null);
  const [cooldown, setCooldown] = useState(0);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (cooldown <= 0) return;
    const timer = setTimeout(() => setCooldown((value) => value - 1), 1000);
    return () => clearTimeout(timer);
  }, [cooldown]);

  const send = async () => {
    const value = email.trim();
    if (!EMAIL.test(value)) {
      setEmailError(t("auth.emailInvalid"));
      return;
    }
    setEmailError(null);
    setSendError(null);
    setLimit(null);
    setBusy(true);
    try {
      await api.auth.requestEmailCode(value);
      setStep("code");
      setCode("");
      setCodeError(null);
      setCooldown(RESEND_SECONDS);
    } catch (error) {
      if (isApiError(error, "INVALID_EMAIL")) {
        setEmailError(t("auth.emailInvalid"));
        return;
      }
      if (isApiError(error, "EMAIL_CODE_RATE_LIMITED")) {
        // 冷却中：上一封验证码仍然有效，直接进入输入步骤
        setStep("code");
        setCooldown(error.retryAfter ?? RESEND_SECONDS);
        return;
      }
      // 输入验证码时点「重新发送」失败，提示放在验证码下方；邮箱步骤放在表单里。
      if (isApiError(error, "EMAIL_DAILY_LIMIT")) {
        if (step === "code") setCodeError(null);
        limitFrom("dailyLimit", error, step);
        return;
      }
      if (isApiError(error, "RATE_LIMITED")) {
        if (step === "code") setCodeError(null);
        limitFrom("rateLimited", error, step);
        return;
      }
      const message = isApiError(error, "EMAIL_LOGIN_UNAVAILABLE")
        ? t("auth.unavailable")
        : isApiError(error, "EMAIL_DELIVERY_FAILED")
          ? t("auth.deliveryFailed")
          : t("common.networkError");
      if (step === "code") setCodeError(message);
      else setSendError(message);
    } finally {
      setBusy(false);
    }
  };

  const verify = async (value: string) => {
    if (value.length !== 6 || busy) return;
    setBusy(true);
    setCodeError(null);
    setLimit(null);
    try {
      await api.auth.verifyEmailCode(
        email.trim(),
        value,
        isDesktop() ? "desktop" : "web",
      );
      queryClient.clear();
      const organizations = await api.me.organizations();
      const first = organizations[0];
      navigate(next ?? (first ? `/o/${first.id}` : "/welcome"), {
        replace: true,
      });
    } catch (error) {
      if (isApiError(error, "INVALID_CREDENTIALS")) {
        setCodeError(t("auth.codeInvalid"));
        setCooldown(0);
      } else if (isApiError(error, "EMAIL_DAILY_LIMIT"))
        limitFrom("tooManyAttempts", error, "code");
      else if (isApiError(error, "RATE_LIMITED"))
        limitFrom("rateLimited", error, "code");
      else toast(t("common.networkError"), { tone: "error" });
    } finally {
      setBusy(false);
    }
  };

  const onSubmitEmail = (event: FormEvent) => {
    event.preventDefault();
    void send();
  };

  const codeMessage = codeError ?? limitMessage("code");
  const sendMessage = sendError ?? limitMessage("email");

  if (step === "code") {
    return (
      <AuthLayout>
        <div className="flex w-[360px] flex-col gap-6">
          <button
            type="button"
            onClick={() => {
              setStep("email");
              if (limit?.step === "code") setLimit(null);
            }}
            className="flex w-fit items-center gap-1 text-body-sm text-fg-secondary hover:text-fg-default"
          >
            <Icon name="arrow-left" />
            {t("common.back")}
          </button>
          <div className="flex flex-col gap-2">
            <h1 className="text-heading font-medium">{t("auth.checkEmail")}</h1>
            <p className="text-body text-fg-muted">
              {t("auth.codeSentTo", { email: email.trim() })}
            </p>
          </div>
          <div className="flex flex-col gap-2">
            <OtpInput
              value={code}
              onChange={setCode}
              invalid={Boolean(codeMessage)}
              disabled={busy}
              onComplete={(v) => void verify(v)}
              autoFocus
            />
            {codeMessage && (
              <p role="alert" className="text-caption text-danger">
                {codeMessage}
              </p>
            )}
          </div>
          <Button
            size="lg"
            loading={busy}
            disabled={code.length !== 6}
            onClick={() => void verify(code)}
          >
            {t("auth.login")}
          </Button>
          <div className="flex flex-col gap-1 text-body-sm">
            <span className="text-fg-muted">{t("auth.noCode")}</span>
            {cooldown > 0 ? (
              <span className="text-fg-subtle">
                {t("auth.resendIn", { seconds: cooldown })}
              </span>
            ) : (
              <button
                type="button"
                className="w-fit font-medium text-fg-default hover:underline"
                onClick={() => void send()}
                disabled={busy}
              >
                {t("auth.resend")}
              </button>
            )}
            <span className="text-caption text-fg-muted">
              {t("auth.codeHint")}
            </span>
          </div>
        </div>
      </AuthLayout>
    );
  }

  return (
    <AuthLayout>
      <form
        onSubmit={onSubmitEmail}
        noValidate
        className="flex w-[360px] flex-col gap-6"
      >
        <div className="flex flex-col gap-2">
          <h1 className="text-heading font-medium">{t("auth.welcome")}</h1>
          <p className="text-body text-fg-muted">{t("auth.emailLogin")}</p>
        </div>
        <Field
          label={t("auth.email")}
          labelTone="muted"
          helper={t("auth.emailHelper")}
          error={emailError}
        >
          <TextInput
            variant="labeled"
            type="email"
            autoComplete="email"
            placeholder={t("auth.emailPlaceholder")}
            leading={<Icon name="mail" />}
            value={email}
            autoFocus
            onChange={(event) => {
              setEmail(event.target.value);
              if (emailError) setEmailError(null);
              if (sendError) setSendError(null);
              // 每日上限按邮箱计算，换邮箱后不再适用；按 IP 的限流保留。
              if (limit?.key === "dailyLimit") setLimit(null);
            }}
          />
        </Field>
        {sendMessage && (
          <p role="alert" className="text-body-sm text-danger">
            {sendMessage}
          </p>
        )}
        <Button type="submit" size="lg" loading={busy}>
          {t("common.continue")}
        </Button>
        {accounts.map((account) => (
          <Button
            type="button"
            variant="ghost"
            key={account.id}
            onClick={() => void desktopBridge()?.selectAccount?.(account.id)}
          >
            {t("shell.switchAccount")} · {account.name}
          </Button>
        ))}
        <p className="text-caption text-fg-muted">{t("auth.terms")}</p>
      </form>
    </AuthLayout>
  );
}
