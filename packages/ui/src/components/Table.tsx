import type { HTMLAttributes, TdHTMLAttributes, ThHTMLAttributes } from "react";

import { cn } from "../cn";

/** 基础表格：使用原生 table 语义，带表头 caption 和 40px 行高。 */
export function Table({
  className,
  ...rest
}: HTMLAttributes<HTMLTableElement>) {
  return (
    <table
      className={cn("w-full border-collapse text-left text-body", className)}
      {...rest}
    />
  );
}

export function TableHead(props: HTMLAttributes<HTMLTableSectionElement>) {
  return <thead {...props} />;
}

export function TableBody(props: HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody {...props} />;
}

export interface TableRowProps extends HTMLAttributes<HTMLTableRowElement> {
  selected?: boolean;
}

export function TableRow({
  selected,
  className,
  onClick,
  ...rest
}: TableRowProps) {
  return (
    <tr
      aria-selected={selected}
      onClick={onClick}
      className={cn(
        "border-b border-line-soft",
        onClick && "cursor-pointer hover:bg-surface-hover",
        selected && "bg-surface-selected",
        className,
      )}
      {...rest}
    />
  );
}

export interface TableHeaderCellProps extends Omit<
  ThHTMLAttributes<HTMLTableCellElement>,
  "aria-sort"
> {
  /** 排序方向，写入 aria-sort */
  sort?: "ascending" | "descending" | "none";
}

export function TableHeaderCell({
  sort,
  className,
  scope = "col",
  ...rest
}: TableHeaderCellProps) {
  return (
    <th
      scope={scope}
      aria-sort={sort}
      className={cn(
        "h-[var(--density-group-row,36px)] px-3 text-caption font-normal text-fg-muted",
        className,
      )}
      {...rest}
    />
  );
}

export function TableCell({
  className,
  ...rest
}: TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <td
      className={cn(
        "h-[var(--density-table-row,40px)] px-3 align-middle",
        className,
      )}
      {...rest}
    />
  );
}
