import { Children, Fragment, type ReactNode } from "react";
import { Link } from "react-router-dom";

import { useT } from "@openfox/i18n";
import { Icon, cn, type IconName } from "@openfox/ui";

// 空间设置页的版式部件：内容列 max-w 768、分区间距 24、卡片行 p16 + 内缩 16 的分隔线。

export function SettingsColumn({
  children,
  wide = false,
  narrow = false,
}: {
  children: ReactNode;
  /** 标签页不限 768，最大 1100 */
  wide?: boolean;
  /** 个人资料页内容宽 720 */
  narrow?: boolean;
}) {
  return (
    <div
      className={cn(
        "mx-auto flex w-full flex-col gap-6 px-6 pt-12 pb-12",
        wide ? "max-w-[1100px]" : narrow ? "max-w-[720px]" : "max-w-[768px]",
      )}
    >
      {children}
    </div>
  );
}

export function SettingsSection({
  id,
  title,
  description,
  action,
  children,
}: {
  id?: string;
  title: string;
  description?: string;
  action?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <section id={id} aria-label={title} className="flex flex-col gap-3">
      <header className="flex items-start gap-3 px-1">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <h2 className="text-title font-medium text-fg-default">{title}</h2>
          {description && (
            <p className="text-body-sm text-fg-muted">{description}</p>
          )}
        </div>
        {action}
      </header>
      {children}
    </section>
  );
}

/** 设置卡：行之间自动插入左右内缩 16 的分隔线。 */
export function SettingsCard({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  const rows = Children.toArray(children).filter(Boolean);
  return (
    <div
      className={cn(
        "flex flex-col rounded-xl border border-line-default bg-surface-card",
        className,
      )}
    >
      {rows.map((row, index) => (
        <Fragment key={index}>
          {index > 0 && <div className="mx-4 h-px bg-line-soft" />}
          {row}
        </Fragment>
      ))}
    </div>
  );
}

export function SettingRow({
  icon,
  title,
  description,
  control,
  danger = false,
}: {
  icon?: IconName;
  title: string;
  /** 校验提示会替换说明文字，仍为 12px 灰字 */
  description?: ReactNode;
  control?: ReactNode;
  danger?: boolean;
}) {
  return (
    <div className="flex min-h-[68px] items-center gap-3 p-4">
      {icon && <Icon name={icon} className="shrink-0 text-fg-secondary" />}
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span
          className={cn(
            "text-body font-medium",
            danger ? "text-danger" : "text-fg-default",
          )}
        >
          {title}
        </span>
        {description && (
          <span className="text-caption text-fg-muted">{description}</span>
        )}
      </div>
      {control && <div className="flex shrink-0 items-center">{control}</div>}
    </div>
  );
}

/** 概览里的导航行：整行可点，右侧摘要 + chevron。 */
export function SettingNavRow({
  to,
  icon,
  title,
  description,
  summary,
}: {
  to: string;
  icon: IconName;
  title: string;
  description: string;
  summary?: string;
}) {
  return (
    <Link
      to={to}
      className="flex min-h-[68px] items-center gap-3 rounded-xl p-4 hover:bg-surface-hover"
    >
      <Icon name={icon} className="shrink-0 text-fg-secondary" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="text-body font-medium text-fg-default">{title}</span>
        <span className="text-caption text-fg-muted">{description}</span>
      </div>
      {summary && (
        <span className="shrink-0 text-body-sm text-fg-muted">{summary}</span>
      )}
      <Icon name="chevron-right" className="shrink-0 text-fg-muted" />
    </Link>
  );
}

/** 二级页顶部的返回条「‹ 空间名」。 */
export function BackBar({ to, label }: { to: string; label: string }) {
  return (
    <div className="sticky top-0 z-10 bg-surface-card px-6 pt-4 pb-2">
      <Link
        to={to}
        className="inline-flex h-7 items-center gap-1 rounded-lg px-1.5 text-body-sm text-fg-secondary hover:bg-surface-hover"
      >
        <Icon name="chevron-left" size={14} />
        <span className="max-w-[240px] truncate">{label}</span>
      </Link>
    </div>
  );
}

/** 停用空间的只读横幅（琥珀色）。 */
export function RetiredBanner({ message }: { message?: string }) {
  const t = useT();
  return (
    <div
      role="status"
      className="flex items-center gap-2 rounded-xl border border-warning/30 bg-warning/10 px-4 py-2.5 text-body-sm text-fg-default"
    >
      <Icon name="archive" size={14} className="shrink-0 text-warning" />
      <span>{message ?? t("spaces.retiredBanner")}</span>
    </div>
  );
}

/** 二级页的大标题区（24/500 + 说明）。 */
export function PageHeading({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <header className="flex items-start gap-4">
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <h1 className="text-subheading font-medium text-fg-default">{title}</h1>
        {description && (
          <p className="text-body text-fg-muted">{description}</p>
        )}
      </div>
      {action}
    </header>
  );
}
