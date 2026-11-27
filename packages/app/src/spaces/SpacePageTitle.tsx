/** 固定高度的页头始终显示模块名，较长的空间名可悬停查看。 */
export function SpacePageTitle({
  space,
  title,
}: {
  space: string;
  title: string;
}) {
  return (
    <h1 className="flex min-w-0 items-baseline gap-1.5 text-body">
      <span className="truncate font-normal text-fg-muted" title={space}>
        {space}
      </span>
      <span className="shrink-0 text-fg-subtle">·</span>
      <span className="shrink-0 font-semibold text-fg-default">{title}</span>
    </h1>
  );
}
