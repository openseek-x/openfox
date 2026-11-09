import { useState, type FormEvent } from "react";

import type { IntegrationProvider } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Dialog,
  Icon,
  IconButton,
  TextInput,
} from "@openfox/ui";

import { useCopy } from "../tasks/parts";
import type { OAuthExtra } from "./GitHubSections";
import { integrationErrorText } from "./queries";

// GitLab 自部署版 / 极狐：管理员填写实例地址与 OAuth 应用凭据，然后发起组织授权。

/** 实例地址是否像一个 http(s) 地址；公网 / SSRF 校验由服务端负责（INVALID_INSTANCE_URL）。 */
function looksLikeUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "http:";
  } catch {
    return false;
  }
}

export function GitLabBindDialog({
  provider,
  onClose,
  start,
}: {
  provider: IntegrationProvider;
  onClose: () => void;
  start: (
    purpose: "organization",
    connectionId: undefined,
    extra: OAuthExtra,
    handlers: { onSuccess: () => void; onError: (error: unknown) => void },
  ) => void;
}) {
  const t = useT();
  const copy = useCopy();
  const isCn = provider.id === "gitlab_cn";
  const defaultUrl = provider.default_instance_url ?? "";
  const redirect = provider.oauth_redirect_uri ?? "";
  // 极狐预填默认实例地址；自部署版必须自己填
  const [instance, setInstance] = useState(defaultUrl);
  const [appId, setAppId] = useState("");
  const [secret, setSecret] = useState("");
  const [shown, setShown] = useState(false);
  const [touched, setTouched] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");

  const url = instance.trim();
  const instanceError = !url
    ? isCn
      ? ""
      : t("integrations.gitlab.instanceUrlRequired")
    : looksLikeUrl(url)
      ? ""
      : t("integrations.gitlab.errors.invalidInstanceUrl");
  const idError = appId.trim() ? "" : t("integrations.gitlab.required");
  const secretError = secret.trim() ? "" : t("integrations.gitlab.required");
  const valid = !instanceError && !idError && !secretError;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (!valid) return;
    setError("");
    setPending(true);
    start(
      "organization",
      undefined,
      {
        ...(url ? { instance_url: url } : {}),
        application_id: appId.trim(),
        application_secret: secret.trim(),
      },
      {
        onSuccess: () => {
          setPending(false);
          onClose();
        },
        onError: (e) => {
          setPending(false);
          setError(integrationErrorText(t, e));
        },
      },
    );
  };

  const fieldError = (message: string) =>
    touched && message ? (
      <p role="alert" className="text-body-sm text-danger">
        {message}
      </p>
    ) : null;

  return (
    <Dialog
      open
      width={520}
      onClose={pending ? undefined : onClose}
      title={t("integrations.gitlab.bindTitle", { name: provider.name })}
      description={t("integrations.gitlab.bindDesc")}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={pending}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" form="gitlab-bind-form" loading={pending}>
            {t("integrations.gitlab.submit")}
          </Button>
        </>
      }
    >
      <form
        id="gitlab-bind-form"
        noValidate
        onSubmit={submit}
        className="flex flex-col gap-4"
      >
        {error && <Callout tone="danger">{error}</Callout>}
        {redirect && (
          <div className="flex flex-col gap-1.5 rounded-lg bg-surface-muted p-3">
            <span className="text-body-sm font-medium">
              {t("integrations.gitlab.redirectUri")}
            </span>
            <div className="flex items-center gap-1.5">
              <TextInput
                readOnly
                aria-label={t("integrations.gitlab.redirectUri")}
                value={redirect}
              />
              <IconButton
                label={`${t("integrations.github.webhook.copy")} ${t("integrations.gitlab.redirectUri")}`}
                icon={<Icon name="copy" />}
                size="28"
                onClick={() =>
                  void copy(redirect, t("integrations.github.webhook.copied"))
                }
              />
            </div>
            <p className="text-body-sm text-fg-muted">
              {t("integrations.gitlab.redirectGuide")}
            </p>
          </div>
        )}
        <div className="flex flex-col gap-1.5">
          <label htmlFor="gitlab-instance" className="text-body-sm font-medium">
            {t("integrations.gitlab.instanceUrl")}
          </label>
          <TextInput
            id="gitlab-instance"
            inputMode="url"
            autoComplete="off"
            placeholder="https://gitlab.example.com"
            invalid={touched && Boolean(instanceError)}
            value={instance}
            onChange={(e) => setInstance(e.target.value)}
          />
          <span className="text-caption text-fg-muted">
            {isCn && defaultUrl
              ? t("integrations.gitlab.instanceUrlHintCn", { url: defaultUrl })
              : t("integrations.gitlab.instanceUrlHint")}
          </span>
          {fieldError(instanceError)}
        </div>
        <div className="flex flex-col gap-1.5">
          <label htmlFor="gitlab-app-id" className="text-body-sm font-medium">
            {t("integrations.gitlab.applicationId")}
          </label>
          <TextInput
            id="gitlab-app-id"
            autoComplete="off"
            invalid={touched && Boolean(idError)}
            value={appId}
            onChange={(e) => setAppId(e.target.value)}
          />
          {fieldError(idError)}
        </div>
        <div className="flex flex-col gap-1.5">
          <label
            htmlFor="gitlab-app-secret"
            className="text-body-sm font-medium"
          >
            {t("integrations.gitlab.applicationSecret")}
          </label>
          <div className="flex items-center gap-1.5">
            <TextInput
              id="gitlab-app-secret"
              type={shown ? "text" : "password"}
              autoComplete="off"
              invalid={touched && Boolean(secretError)}
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
            />
            <IconButton
              label={t(
                shown
                  ? "integrations.gitlab.hideSecret"
                  : "integrations.gitlab.showSecret",
              )}
              icon={<Icon name={shown ? "eye-off" : "eye"} />}
              size="28"
              onClick={() => setShown((v) => !v)}
            />
          </div>
          {fieldError(secretError)}
        </div>
      </form>
    </Dialog>
  );
}
