import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { useI18n, useT } from "@openfox/i18n";
import { Button, Icon, useToast } from "@openfox/ui";

import {
  desktopBridge,
  type DesktopAbout,
  type DesktopUpdateStatus,
} from "../desktop";
import {
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "../spaces/settings/parts";

// 阶段 12h · 关于（仅桌面端）：当前版本、上次检查结果、手动检查更新。
// 安装包未签名，只做「检查并提示下载」：下载地址只存在于主进程，页面只能请求「打开下载」。

const aboutKey = ["desktop", "about"] as const;

export function AboutPage() {
  const t = useT();
  const { locale } = useI18n();
  const toast = useToast();
  const queryClient = useQueryClient();
  const bridge = desktopBridge();
  const [checkedStatus, setCheckedStatus] =
    useState<DesktopUpdateStatus | null>(null);
  const about = useQuery({
    queryKey: aboutKey,
    queryFn: () => bridge!.getAbout!(),
    enabled: Boolean(bridge?.getAbout),
  });
  const setAbout = (next: DesktopAbout) =>
    queryClient.setQueryData(aboutKey, next);
  const check = useMutation({
    mutationFn: () => bridge!.checkForUpdate!(),
    onSuccess: (result) => {
      setCheckedStatus(result.status);
      setAbout(result.about);
    },
    onError: () => toast(t("me.about.failed"), { tone: "error" }),
  });
  const skip = useMutation({
    mutationFn: () => bridge!.skipUpdate!(),
    onSuccess: (result) => {
      if (result.ok) setAbout(result.about);
      else toast(t("me.about.failed"), { tone: "error" });
    },
    onError: () => toast(t("me.about.failed"), { tone: "error" }),
  });
  const download = useMutation({
    mutationFn: () => bridge!.openUpdateDownload!(),
    onSuccess: (result) => {
      if (!result.ok) toast(t("me.about.failed"), { tone: "error" });
    },
    onError: () => toast(t("me.about.failed"), { tone: "error" }),
  });

  if (!bridge?.getAbout || !about.data) return null;
  const data = about.data;
  const last = data.last_check;
  const statusText = (status: DesktopUpdateStatus, version: string | null) =>
    t(`me.about.status.${status}`, { version: version ?? "" });
  const lastText = last
    ? `${new Date(last.at).toLocaleString(locale)} · ${statusText(last.status, last.version)}`
    : t("me.about.never");

  return (
    <SettingsColumn narrow>
      <SettingsSection title={t("me.about.title")}>
        <SettingsCard>
          <div className="flex items-center gap-3 px-4 py-3">
            <Icon name="brand-logo" size={40} />
            <div>
              <div className="text-body font-medium text-fg-default">
                OpenFox
              </div>
              <div className="flex gap-1.5 text-body-sm text-fg-secondary">
                <span>{t("me.about.version")}</span>
                <span>{data.version}</span>
              </div>
            </div>
          </div>
          <SettingRow
            title={t("me.about.lastCheck")}
            control={
              <span className="text-body-sm text-fg-secondary">{lastText}</span>
            }
          />
          <SettingRow
            title={t("me.about.check")}
            description={
              checkedStatus && checkedStatus !== "update_available"
                ? statusText(checkedStatus, null)
                : undefined
            }
            control={
              <Button
                variant="outline"
                size="28"
                disabled={check.isPending}
                onClick={() => check.mutate()}
              >
                {check.isPending ? t("me.about.checking") : t("me.about.check")}
              </Button>
            }
          />
        </SettingsCard>
        {data.update && (
          <SettingsCard>
            <SettingRow
              title={t("me.about.available", { version: data.update.version })}
              description={t("me.about.unsigned")}
              control={
                <div className="flex gap-2">
                  <Button
                    size="28"
                    disabled={download.isPending}
                    onClick={() => download.mutate()}
                  >
                    {t("me.about.download")}
                  </Button>
                  <Button
                    variant="outline"
                    size="28"
                    disabled={skip.isPending}
                    onClick={() => skip.mutate()}
                  >
                    {t("me.about.skip")}
                  </Button>
                </div>
              }
            />
            <SettingRow
              title={t("me.about.sha256")}
              control={
                <code className="max-w-[360px] break-all text-caption text-fg-secondary">
                  {data.update.sha256}
                </code>
              }
            />
          </SettingsCard>
        )}
        {!data.update && data.skipped_version && (
          <p className="text-body-sm text-fg-muted">
            {t("me.about.skipped", { version: data.skipped_version })}
          </p>
        )}
      </SettingsSection>
    </SettingsColumn>
  );
}
