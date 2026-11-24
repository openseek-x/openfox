/** 固定高度的页头始终显示模块名，较长的空间名可悬停查看。 */
export function SpacePageTitle({
  space,
  title,
}: {
  space: string;
  title: string;
}) {
  return (
    <h1 className="flex min-w-0 items-baseline gap-1 font-medium">
      <span className="truncate" title={space}>
        {space}
      </span>
      <span className="shrink-0">· {title}</span>
    </h1>
  );
}
