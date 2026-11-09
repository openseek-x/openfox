import { useState } from "react";

import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Dialog,
  Field,
  TextInput,
  useToast,
} from "@openfox/ui";

import { URL_SOURCES } from "./model";
import { skillErrorText, useUrlImport } from "./queries";

// 从 URL 安装：URL 输入回车提交；下方五个来源外链。

/** 安装中按钮文案里的来源名：取地址主机名，解析不了时用原文。 */
function sourceName(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url;
  }
}

export function UrlInstallDialog({
  orgId,
  spaceId,
  onClose,
  onCreated,
}: {
  orgId: string;
  spaceId: string;
  onClose: () => void;
  onCreated: (skillId: string) => void;
}) {
  const t = useT();
  const toast = useToast();
  const install = useUrlImport(orgId, spaceId);
  const [url, setUrl] = useState("");
  const trimmed = url.trim();

  const submit = () => {
    if (!trimmed || install.isPending) return;
    install.mutate(trimmed, {
      onSuccess: (skill) => {
        toast(t("skills.url.installed"), { tone: "success" });
        onCreated(skill.id);
      },
    });
  };

  return (
    <Dialog
      open
      onClose={install.isPending ? undefined : onClose}
      title={t("skills.url.title")}
      width={560}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={install.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button
            onClick={submit}
            disabled={!trimmed}
            loading={install.isPending}
          >
            {install.isPending
              ? t("skills.url.installing", { source: sourceName(trimmed) })
              : t("skills.url.install")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        {install.isError && (
          <Callout tone="danger">
            {skillErrorText(
              t,
              install.error,
              "skills.url.failed",
              "skills.url.conflict",
            )}
          </Callout>
        )}
        <Field label={t("skills.url.label")} helper={t("skills.url.hint")}>
          <TextInput
            variant="form"
            autoFocus
            data-autofocus
            className="font-mono"
            inputMode="url"
            placeholder={t("skills.url.placeholder")}
            maxLength={2048}
            value={url}
            disabled={install.isPending}
            onChange={(event) => setUrl(event.target.value)}
          />
        </Field>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-caption text-fg-muted">
          <span>{t("skills.url.sources")}</span>
          {URL_SOURCES.map((source) => (
            <a
              key={source.label}
              href={source.href}
              target="_blank"
              rel="noopener noreferrer"
              className="text-fg-secondary underline-offset-2 hover:underline"
            >
              {source.label}
            </a>
          ))}
        </div>
      </form>
    </Dialog>
  );
}
