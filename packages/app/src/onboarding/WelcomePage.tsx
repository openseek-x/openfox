import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router-dom";

import { isApiError, newIdempotencyKey } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Field, TextInput, useToast } from "@openfox/ui";

import { AuthLayout } from "../auth/AuthLayout";
import { keys, useApi } from "../context";
import { isDesktop } from "../desktop";
import { localKeys, writeLocal } from "../storage";
import { startOnboarding } from "./state";
import { AccountBar } from "./AccountBar";

const SLUG = /^[a-z0-9-]{3,40}$/;

/** 用邮箱前缀生成组织 URL 的默认值，只保留小写字母、数字和连字符。 */
export function suggestSlug(email: string): string {
  const base = (email.split("@")[0] ?? "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 30);
  return base.length >= 3 ? `${base}-team` : "my-team";
}

/** 首登：桌面端先显示欢迎页（05），网页端直接创建组织（06）。?new=1 用于已有组织的用户再建一个。 */
export function WelcomePage() {
  const t = useT();
  const api = useApi();
  const [params] = useSearchParams();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const organizations = useQuery({
    queryKey: keys.organizations,
    queryFn: api.me.organizations,
  });
  const [started, setStarted] = useState(
    !isDesktop() || params.get("new") === "1",
  );

  if (
    organizations.data &&
    organizations.data.length > 0 &&
    params.get("new") !== "1"
  ) {
    return <Navigate to={`/o/${organizations.data[0]!.id}`} replace />;
  }
  const email = me.data?.email ?? "";

  if (!started) {
    return (
      <AuthLayout aside={<AccountBar email={email} />}>
        <div className="flex w-[560px] flex-col gap-8">
          <div className="flex flex-col gap-2">
            <h1 className="text-display font-medium">{t("welcome.title")}</h1>
            <p className="text-body text-fg-secondary">
              {t("welcome.subtitle")}
            </p>
            <p className="text-body text-fg-muted">
              {t("welcome.desktopHint")}
            </p>
          </div>
          <div className="flex gap-2">
            <Button size="lg" onClick={() => setStarted(true)}>
              {t("welcome.start")}
            </Button>
          </div>
          <ol className="flex flex-col gap-3 rounded-xl border border-line-default bg-surface-card p-5">
            <li className="text-body-sm text-fg-muted">
              {t("welcome.duration")}
            </li>
            {(
              [
                ["wizard.steps.workspace", "welcome.step1Desc"],
                ["wizard.steps.runtime", "welcome.step2Desc"],
                ["wizard.steps.agent", "welcome.step3Desc"],
              ] as const
            ).map(([title, desc], index) => (
              <li key={title} className="flex items-start gap-3">
                <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-surface-muted text-caption">
                  {index + 1}
                </span>
                <span className="flex flex-col">
                  <span className="text-body font-medium">{t(title)}</span>
                  <span className="text-body-sm text-fg-muted">{t(desc)}</span>
                </span>
              </li>
            ))}
          </ol>
        </div>
      </AuthLayout>
    );
  }
  return (
    <AuthLayout aside={<AccountBar email={email} />}>
      <CreateOrganizationForm email={email} />
    </AuthLayout>
  );
}

function CreateOrganizationForm({ email }: { email: string }) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState(() => suggestSlug(email));
  const [slugTouched, setSlugTouched] = useState(false);
  const [errors, setErrors] = useState<{ name?: string; slug?: string }>({});
  const [busy, setBusy] = useState(false);
  // 同一份输入重试时复用幂等键；输入改变后换新键，避免 IDEMPOTENCY_KEY_REUSED
  const idem = useRef<{ sig: string; key: string } | null>(null);

  const effectiveSlug = slugTouched ? slug : suggestSlug(email);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const trimmed = name.trim();
    const next: typeof errors = {};
    if (!trimmed || trimmed.length > 100) next.name = t("org.nameInvalid");
    if (!SLUG.test(effectiveSlug)) next.slug = t("org.slugInvalid");
    setErrors(next);
    if (next.name || next.slug) return;
    const sig = `${trimmed}\u0000${effectiveSlug}`;
    if (idem.current?.sig !== sig)
      idem.current = { sig, key: newIdempotencyKey() };
    setBusy(true);
    try {
      const org = await api.organizations.create(
        { name: trimmed, slug: effectiveSlug },
        idem.current.key,
      );
      await queryClient.invalidateQueries({ queryKey: keys.organizations });
      startOnboarding(org.id);
      writeLocal(localKeys.lastOrg, org.id);
      navigate(`/o/${org.id}`, { replace: true });
    } catch (error) {
      if (isApiError(error, "SLUG_TAKEN"))
        setErrors({ slug: t("org.slugTaken") });
      else if (isApiError(error, "INVALID_SLUG"))
        setErrors({ slug: t("org.slugInvalid") });
      else if (isApiError(error, "INVALID_NAME"))
        setErrors({ name: t("org.nameInvalid") });
      else if (isApiError(error, "ORGANIZATION_LIMIT_REACHED"))
        toast(t("org.limitReached"), { tone: "error" });
      else toast(t("org.createFailed"), { tone: "error" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      onSubmit={(e) => void submit(e)}
      noValidate
      className="flex w-[400px] flex-col gap-6"
    >
      <div className="flex flex-col gap-2">
        <h1 className="text-heading font-medium">{t("org.createTitle")}</h1>
        <p className="text-body text-fg-muted">{t("org.createSubtitle")}</p>
      </div>
      <Field
        label={t("org.name")}
        helper={t("org.nameHelper")}
        error={errors.name}
      >
        <TextInput
          variant="labeled"
          value={name}
          placeholder={t("org.namePlaceholder")}
          autoFocus
          onChange={(e) => setName(e.target.value)}
        />
      </Field>
      <Field
        label={t("org.slug")}
        helper={t("org.slugHelper")}
        error={errors.slug}
      >
        <TextInput
          variant="labeled"
          value={effectiveSlug}
          leading={
            <span className="text-body text-fg-subtle">{location.host}/</span>
          }
          onChange={(e) => {
            setSlugTouched(true);
            setSlug(e.target.value.toLowerCase());
          }}
        />
      </Field>
      <Button type="submit" size="lg" loading={busy}>
        {t("common.create")}
      </Button>
    </form>
  );
}
