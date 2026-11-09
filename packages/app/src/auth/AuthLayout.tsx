import type { ReactNode } from "react";

import { locales, useI18n } from "@openfox/i18n";
import { Icon, Select } from "@openfox/ui";

import { isMacDesktop } from "../desktop";

/** 登录与首登页外框：左上 Logo、右上语言切换、内容居中。 */
export function AuthLayout({
  children,
  aside,
}: {
  children: ReactNode;
  aside?: ReactNode;
}) {
  const { locale, setLocale, t } = useI18n();
  return (
    <div className="flex min-h-screen flex-col bg-surface-canvas">
      {/* macOS 桌面端：顶部留一条可拖动窗口的区域，放系统红黄绿按钮 */}
      {isMacDesktop() && (
        <div aria-hidden className="h-7 shrink-0 [-webkit-app-region:drag]" />
      )}
      <header className="flex h-16 items-center justify-between px-8">
        <Icon
          name="brand-logo"
          size={110}
          className="h-7 w-auto"
          label="OpenFox"
        />
        <div className="flex items-center gap-4">
          {aside}
          <div className="w-[132px]">
            <Select
              aria-label={t("common.language")}
              value={locale}
              onChange={setLocale}
              leading={<Icon name="globe" />}
              options={locales.map((item) => ({
                value: item.value,
                label: item.label,
              }))}
            />
          </div>
        </div>
      </header>
      <main className="flex flex-1 items-center justify-center px-6 pb-16">
        {children}
      </main>
    </div>
  );
}
