import { useId, type ReactNode } from "react";

import { useT } from "@openfox/i18n";
import { Button, Icon, cn, type IconName } from "@openfox/ui";

/**
 * 设置抽屉里的折叠卡片：图标 + 标题 + 说明，右侧摘要与箭头，
 * 展开后显示内容。id 用作锚点。
 */
export function SettingsCard({
  id,
  icon,
  title,
  desc,
  summary,
  open,
  onToggle,
  children,
}: {
  id: string;
  icon: IconName;
  title: string;
  desc: string;
  summary?: ReactNode;
  open: boolean;
  onToggle: () => void;
  children?: ReactNode;
}) {
  const bodyId = useId();
  return (
    <section
      id={id}
      aria-label={title}
      className="scroll-mt-4 overflow-hidden rounded-xl border border-line-default bg-surface-card"
    >
      <button
        type="button"
        aria-expanded={open}
        aria-controls={bodyId}
        onClick={onToggle}
        className="flex w-full items-center gap-3 px-4 py-3.5 text-left hover:bg-surface-hover"
      >
        <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary">
          <Icon name={icon} />
        </span>
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="text-body font-medium text-fg-default">{title}</span>
          <span className="truncate text-caption text-fg-muted">{desc}</span>
        </span>
        {summary !== undefined && (
          <span className="text-body-sm text-fg-muted">{summary}</span>
        )}
        <Icon
          name={open ? "chevron-down" : "chevron-right"}
          className="text-fg-muted"
        />
      </button>
      {open && (
        <div id={bodyId} className="border-t border-line-default px-4 py-4">
          {children}
        </div>
      )}
    </section>
  );
}

/** 设置行：标题与控件分列，说明另起一行；窄面板允许控件换行。 */
export function SettingRow({
  label,
  desc,
  children,
  className,
}: {
  label: ReactNode;
  desc?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-line-soft py-3 first:pt-0 last:border-b-0 last:pb-0",
        className,
      )}
    >
      <span className="min-w-[min(100%,12rem)] flex-1 text-body font-medium text-fg-default">
        {label}
      </span>
      <div className="flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2 [&>*]:max-w-full">
        {children}
      </div>
      {desc && (
        <span className="w-full text-caption text-fg-muted">{desc}</span>
      )}
    </div>
  );
}

/** 「即将开放」占位 */
export function ComingSoon({ footnote }: { footnote?: string }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-2">
      <div className="rounded-lg border border-dashed border-line-default px-4 py-5 text-center text-body-sm text-fg-muted">
        {t("agents.comingSoon")}
      </div>
      {footnote && <p className="text-caption text-fg-muted">{footnote}</p>}
    </div>
  );
}

/** 底部归档卡片。 */
export function DangerCard({
  desc,
  archived,
  onArchive,
  onRestore,
}: {
  desc: string;
  archived: boolean;
  onArchive: () => void;
  onRestore: () => void;
}) {
  const t = useT();
  return (
    <section className="flex items-center gap-3 rounded-xl border border-line-default px-4 py-3.5">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary">
        <Icon name="archive" />
      </span>
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="text-body font-medium">
          {t("agents.sections.archive.title")}
        </span>
        <span className="text-caption text-fg-muted">{desc}</span>
      </div>
      {archived ? (
        <Button variant="outline" size="28" onClick={onRestore}>
          {t("agents.sections.archive.restore")}
        </Button>
      ) : (
        <Button variant="danger-outline" size="28" onClick={onArchive}>
          {t("agents.sections.archive.action")}
        </Button>
      )}
    </section>
  );
}

/** 指标（任务 / 次运行 / Token / 最近运行） */
export function Stats({
  items,
}: {
  items: Array<{ label: string; value: ReactNode }>;
}) {
  return (
    <dl className="flex items-center">
      {items.map((item, index) => (
        <div
          key={item.label}
          className={cn(
            "flex flex-col gap-0.5 px-4",
            index > 0 && "border-l border-line-default",
          )}
        >
          <dd className="text-body font-medium text-fg-default">
            {item.value}
          </dd>
          <dt className="text-caption text-fg-muted">{item.label}</dt>
        </div>
      ))}
    </dl>
  );
}

/** Token 数显示：697.7K / 1.2M */
export function formatTokens(value: number): string {
  if (value < 1000) return String(value);
  if (value < 1_000_000) return `${(value / 1000).toFixed(1)}K`;
  return `${(value / 1_000_000).toFixed(1)}M`;
}
