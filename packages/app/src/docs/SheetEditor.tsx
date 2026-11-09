import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import type { DocNodeDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, IconButton, cn } from "@openfox/ui";

import { useApi } from "../context";
import { parseCsv, serializeCsv } from "./csv";
import { SaveStatus } from "./DocEditor";
import { putDocNode, useDebouncedSave } from "./queries";

// 表格（.csv）：简单的网格编辑。可以改单元格、增删行和列，1.5 秒后自动保存（PATCH 带 version）。
// CSV 只保存数据：没有表头概念，第一行与其他行一样可编辑。

/** 至少一行一列，空表格也能开始编辑。 */
function toGrid(body: string | null): string[][] {
  const rows = parseCsv(body ?? "");
  return rows.length > 0 ? rows : [[""]];
}

/** 按列数补齐一行，缺的单元格为空串。 */
function padRow(items: readonly string[], cols: number): string[] {
  return Array.from({ length: cols }, (_, col) => items[col] ?? "");
}

export function SheetEditor({
  orgId,
  spaceId,
  node,
  onFlushReady,
}: {
  orgId: string;
  spaceId: string;
  node: DocNodeDetail;
  onFlushReady: (flush: () => Promise<boolean>) => void;
}) {
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();
  const [rows, setRows] = useState<string[][]>(() => toGrid(node.body));
  const versionRef = useRef(node.version);
  useEffect(() => {
    if (node.version > versionRef.current) versionRef.current = node.version;
  }, [node.version]);
  const save = useDebouncedSave<{ body: string }>(async (patch) => {
    const saved = await api.docNodes.update(orgId, spaceId, node.id, {
      version: versionRef.current,
      body: patch.body,
    });
    versionRef.current = saved.version;
    putDocNode(queryClient, orgId, spaceId, saved);
  });
  const editable = node.can_edit;
  useEffect(() => {
    onFlushReady(save.flush);
  }, [onFlushReady, save.flush]);
  const cols = Math.max(1, ...rows.map((items) => items.length));

  /** 每次修改后整体替换网格，并把新内容交给自动保存。 */
  const commit = (next: string[][]) => {
    setRows(next);
    save.schedule({ body: serializeCsv(next) });
  };
  const setCell = (row: number, col: number, value: string) =>
    commit(
      rows.map((items, r) => {
        if (r !== row) return items;
        const next = padRow(items, cols);
        next[col] = value;
        return next;
      }),
    );
  const addRow = () => commit([...rows, padRow([], cols)]);
  const addColumn = () =>
    commit(rows.map((items) => [...padRow(items, cols), ""]));
  const removeRow = (row: number) => {
    if (rows.length > 1) commit(rows.filter((_, r) => r !== row));
  };
  const removeColumn = (col: number) => {
    if (cols > 1)
      commit(rows.map((items) => items.filter((_, c) => c !== col)));
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 p-6">
      {editable && (
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="28"
            leading={<Icon name="plus" />}
            onClick={addRow}
          >
            {t("docs.editor.addRow")}
          </Button>
          <Button
            variant="outline"
            size="28"
            leading={<Icon name="plus" />}
            onClick={addColumn}
          >
            {t("docs.editor.addColumn")}
          </Button>
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-auto rounded-xl border border-line-default">
        <table className="border-collapse text-body">
          {editable && (
            <thead>
              <tr>
                {Array.from({ length: cols }, (_, col) => (
                  <th key={col} className="px-1 py-0.5 font-normal">
                    {cols > 1 && (
                      <IconButton
                        label={t("docs.editor.removeColumn")}
                        icon={<Icon name="x" size={12} />}
                        size="28"
                        variant="ghost"
                        onClick={() => removeColumn(col)}
                      />
                    )}
                  </th>
                ))}
                <th className="w-8" />
              </tr>
            </thead>
          )}
          <tbody>
            {rows.map((items, row) => (
              <tr key={row} className="group">
                {Array.from({ length: cols }, (_, col) => (
                  <td
                    key={col}
                    className={cn(
                      "border-r border-b border-line-soft p-0",
                      row === 0 && "bg-surface-muted",
                    )}
                  >
                    <input
                      aria-label={t("docs.editor.cell", {
                        row: row + 1,
                        column: col + 1,
                      })}
                      value={items[col] ?? ""}
                      readOnly={!editable}
                      onChange={(event) =>
                        setCell(row, col, event.target.value)
                      }
                      className="h-8 w-40 bg-transparent px-2 outline-none focus:bg-surface-hover"
                    />
                  </td>
                ))}
                {editable && (
                  <td className="w-8 px-1">
                    {rows.length > 1 && (
                      <IconButton
                        label={t("docs.editor.removeRow")}
                        icon={<Icon name="x" size={12} />}
                        size="28"
                        variant="ghost"
                        className="opacity-0 group-focus-within:opacity-100 group-hover:opacity-100"
                        onClick={() => removeRow(row)}
                      />
                    )}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <SaveStatus error={save.error} />
    </div>
  );
}
