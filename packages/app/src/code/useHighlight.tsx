import { useEffect, useMemo, useState, type ReactNode } from "react";

import type { CodeToken } from "./highlight";
import "./highlight.css";

// 懒加载高亮模块并把结果渲染成 React 元素。加载完成前（或不支持的语言、过长的代码）返回 null，
// 调用方此时显示纯文本，所以首屏不会等高亮库。

type HighlightModule = typeof import("./highlight");

let loading: Promise<HighlightModule> | null = null;
let loaded: HighlightModule | null = null;

function loadHighlighter(): Promise<HighlightModule> {
  loading ??= import("./highlight").then((module) => {
    loaded = module;
    return module;
  });
  return loading;
}

/** 把片段渲染成带 hljs-* 类名的 span；普通文本直接是文本节点。 */
export function renderTokens(tokens: readonly CodeToken[]): ReactNode[] {
  return tokens.map((token, index) =>
    token.classes.length === 0 ? (
      token.text
    ) : (
      <span key={index} className={token.classes.join(" ")}>
        {token.text}
      </span>
    ),
  );
}

/**
 * 返回高亮后的内容；还没加载好、语言不支持或不需要高亮时返回 null。
 * language 为 null 且 auto 为 true 时按内容自动识别（只用于较短的代码）。
 */
export function useHighlight(
  code: string,
  language: string | null,
  options: { auto?: boolean } = {},
): ReactNode[] | null {
  const auto = options.auto ?? false;
  const wanted = Boolean(code) && (language !== null || auto);
  const [module, setModule] = useState<HighlightModule | null>(loaded);
  useEffect(() => {
    if (!wanted || module) return;
    let alive = true;
    loadHighlighter().then(
      (next) => {
        if (alive) setModule(next);
      },
      () => {
        // 高亮库加载失败时保持纯文本
      },
    );
    return () => {
      alive = false;
    };
  }, [wanted, module]);
  return useMemo(() => {
    if (!wanted || !module) return null;
    const tokens = module.highlightTokens(code, language, { auto });
    return tokens ? renderTokens(tokens) : null;
  }, [wanted, module, code, language, auto]);
}

/** <code> 内的内容：有高亮就用高亮，否则原文。 */
export function HighlightedCode({
  code,
  language,
  auto,
}: {
  code: string;
  language: string | null;
  auto?: boolean;
}) {
  const highlighted = useHighlight(code, language, { auto });
  return <>{highlighted ?? code}</>;
}
