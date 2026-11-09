import type { Translate } from "@openfox/i18n";

import { formatDuration } from "./format";
import type { StepView, Turn, TurnItem } from "./model";

// 把一次 Run 折叠后的回合转成纯文本（运行记录弹窗的「复制全部」）。
// 只输出行组件展开后本来就会显示的字段（与 rows.tsx 一一对应），不额外输出别的字段；
// 文案与界面上的标题行一致。纯函数，不依赖 React。

const INDENT = "  ";

const baseName = (path: string) => path.split(/[\\/]/).pop() || path;

/** 每行加缩进（分组子步骤、正文块）。 */
function indent(text: string, depth: number): string {
  const prefix = INDENT.repeat(depth);
  return text
    .split("\n")
    .map((line) => (line ? prefix + line : line))
    .join("\n");
}

/** running / done 之外的状态后缀（与 rows.tsx suffixOf 一致）。 */
function suffix(step: StepView, t: Translate): string {
  const status = step.status;
  return status === "failed" || status === "interrupted" || status === "denied"
    ? ` ${t(`steps.stateSuffix.${status}`)}`
    : "";
}

const plain = (status: StepView["status"]) =>
  status === "settled" ? "done" : status;

function stepLines(step: StepView, t: Translate): string[] {
  const lines: string[] = [];
  switch (step.kind) {
    case "prep_env": {
      const status = plain(step.status);
      lines.push(t(`steps.prep.${status}`));
      for (const substep of step.substeps ?? [])
        lines.push(
          INDENT +
            (substep.label ||
              t(
                substep.state === "running"
                  ? `steps.prep.substepsRunning.${substep.kind}`
                  : `steps.prep.substeps.${substep.kind}`,
              )),
        );
      break;
    }
    case "thinking": {
      const running = step.status === "running";
      lines.push(
        t(running ? "steps.thinking.running" : "steps.thinking.done") +
          suffix(step, t),
      );
      lines.push(
        indent(step.text?.trim() ? step.text : t("steps.thinking.empty"), 1),
      );
      break;
    }
    case "agent_text":
      if (step.text?.trim()) lines.push(step.text);
      break;
    case "exec": {
      lines.push(
        `${t(`steps.exec.${step.status}`)}${step.command ? ` ${step.command}` : ""}`,
      );
      if (step.cwd && step.cwd !== ".") lines.push(indent(step.cwd, 1));
      lines.push(indent(`$ ${step.command ?? ""}`, 1));
      if (step.output) lines.push(indent(step.output, 1));
      const footer: string[] = [];
      if (typeof step.exit_code === "number")
        footer.push(t("steps.exec.exitCode", { code: step.exit_code }));
      if (typeof step.duration_ms === "number")
        footer.push(formatDuration(step.duration_ms, t));
      if (step.truncated) lines.push(indent(t("steps.exec.truncated"), 1));
      if (footer.length) lines.push(indent(footer.join(" · "), 1));
      break;
    }
    case "patch": {
      const status = plain(step.status);
      const files = step.files ?? [];
      const additions = files.reduce((sum, file) => sum + file.additions, 0);
      const deletions = files.reduce((sum, file) => sum + file.deletions, 0);
      lines.push(
        `${t(`steps.patch.${status}`)}${files.length ? ` +${additions} −${deletions}` : ""}`,
      );
      for (const file of files) {
        lines.push(indent(file.path, 1));
        if (file.move_path)
          lines.push(
            indent(t("steps.patch.moved", { path: file.move_path }), 1),
          );
        if (file.diff) lines.push(indent(file.diff, 2));
        if (file.truncated) lines.push(indent(t("steps.patch.truncated"), 2));
      }
      break;
    }
    case "write_file": {
      const status = plain(step.status);
      lines.push(
        `${t(`steps.write.${status}`)}${step.path ? ` ${baseName(step.path)}` : ""}`,
      );
      if (step.path) lines.push(indent(step.path, 1));
      if (step.content) lines.push(indent(step.content, 1));
      if (step.truncated) lines.push(indent(t("steps.write.truncated"), 1));
      break;
    }
    case "read": {
      let range = "";
      if (typeof step.line_start === "number")
        range =
          typeof step.line_end === "number"
            ? ` ${t("steps.read.lines", { start: step.line_start, end: step.line_end })}`
            : ` ${t("steps.read.from", { start: step.line_start })}`;
      lines.push(
        `${t(step.status === "running" ? "steps.read.running" : "steps.read.done")}${step.path ? ` ${baseName(step.path)}` : ""}${range}${suffix(step, t)}`,
      );
      break;
    }
    case "search": {
      const running = step.status === "running";
      const label = !step.query
        ? running
          ? "steps.search.listRunning"
          : "steps.search.listDone"
        : running
          ? "steps.search.running"
          : "steps.search.done";
      const detail = step.query || step.scope;
      lines.push(`${t(label)}${detail ? ` ${detail}` : ""}${suffix(step, t)}`);
      if (step.scope)
        lines.push(indent(t("steps.search.scope", { scope: step.scope }), 1));
      for (const result of step.results ?? [])
        if ("file" in result) lines.push(indent(result.file, 1));
      break;
    }
    case "web_search": {
      const running = step.status === "running";
      lines.push(
        `${t(running ? "steps.webSearch.running" : "steps.webSearch.done")}${step.query ? ` ${step.query}` : ""}${suffix(step, t)}`,
      );
      for (const result of step.results ?? [])
        if (!("file" in result))
          lines.push(
            indent(
              result.title && result.title !== result.url
                ? `${result.title} ${result.url}`
                : result.url,
              1,
            ),
          );
      break;
    }
    case "web_fetch": {
      const running = step.status === "running";
      lines.push(
        `${t(running ? "steps.webFetch.running" : "steps.webFetch.done")}${step.url ? ` ${step.url}` : ""}${suffix(step, t)}`,
      );
      break;
    }
    case "todo": {
      const items = step.items ?? [];
      const done = items.filter((item) => item.status === "completed").length;
      const finished = step.status !== "running";
      lines.push(
        `${t(finished ? "steps.todo.done" : "steps.todo.running")}${items.length ? ` ${t("steps.todo.progress", { done, total: items.length })}` : ""}`,
      );
      if (step.explanation) lines.push(indent(step.explanation, 1));
      for (const item of items)
        lines.push(
          indent(
            `${item.status === "completed" ? "[x]" : item.status === "in_progress" ? "[~]" : "[ ]"} ${item.text}`,
            1,
          ),
        );
      break;
    }
    default:
      break;
  }
  return lines;
}

function itemLines(item: TurnItem, t: Translate): string[] {
  if (item.type === "warning") return [item.text];
  if (item.type === "group")
    return [
      `${item.step.title || t("steps.group.untitled")}${suffix(item.step, t)}`,
      ...item.children.flatMap((child) =>
        itemLines(child, t).map((block) => indent(block, 1)),
      ),
    ];
  return stepLines(item.step, t);
}

/**
 * 回合的纯文本：标题行（可选）+ 每个条目一段（段间空行）+ 错误详情 + 最终回复。
 * truncated 时在步骤后注明只显示了前一部分。
 */
export function turnTranscript(
  turn: Turn,
  t: Translate,
  options: {
    header?: string[];
    errorDetail?: string | null;
    finalText?: string | null;
    truncated?: boolean;
  } = {},
): string {
  const blocks: string[] = [];
  if (options.header?.length) blocks.push(options.header.join("\n"));
  for (const item of turn.items) {
    const lines = itemLines(item, t);
    if (lines.length) blocks.push(lines.join("\n"));
  }
  if (options.truncated)
    blocks.push(t("steps.timeline.truncated", { count: turn.eventCount }));
  if (options.errorDetail) blocks.push(options.errorDetail);
  if (options.finalText) blocks.push(options.finalText);
  return blocks.join("\n\n");
}
