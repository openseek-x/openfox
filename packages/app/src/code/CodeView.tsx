import { useMemo } from "react";

import { useHighlight } from "./useHighlight";

// 只读代码查看器：左侧行号 + 右侧代码（语法高亮，懒加载；加载前或不支持的语言显示纯文本）。
// 市场文件预览与技能详情共用。

export function CodeView({
  code,
  language,
}: {
  code: string;
  /** highlight.js 语言名或别名（见 languageForPath）；null 表示纯文本 */
  language: string | null;
}) {
  const lineCount = useMemo(() => code.split("\n").length, [code]);
  const highlighted = useHighlight(code, language);
  return (
    <div className="flex font-mono text-body-sm leading-6">
      <pre
        aria-hidden
        className="sticky left-0 shrink-0 bg-surface-muted px-3 py-3 text-right text-fg-muted select-none"
      >
        {Array.from({ length: lineCount }, (_, index) => index + 1).join("\n")}
      </pre>
      <pre
        data-language={language ?? undefined}
        className="min-w-0 flex-1 overflow-x-auto px-3 py-3"
      >
        <code>{highlighted ?? code}</code>
      </pre>
    </div>
  );
}
