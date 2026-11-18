import {
  createContext,
  useContext,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";

import type {
  StepPatchFile,
  StepSearchResult,
  StepWebResult,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  Icon,
  ChainOfThoughtTrigger,
  IconButton,
  Spinner,
  cn,
  useToast,
  type IconName,
} from "@openfox/ui";

import { copyText } from "../../tasks/parts";
import { languageForPath, languageLabel } from "../../code/languages";
import { CodeCard } from "../CodeCard";
import { runtimeLabel } from "../../agents/model";
import { formatDuration } from "./format";
import type { StepStatus, StepView, TurnFinalState, TurnItem } from "./model";

// Execution rows use a shared 32px rhythm and a 16px status-icon column.

/**
 * 阶段 9b ·「全部展开」信号：每点一次加 1，0 表示没有点过（缺省，对话里的时间线不受影响）。
 * 可展开的行看到新值时展开；信号非 0 时新挂载的行（如刚展开的分组里的子步骤）也默认展开。
 */
export const ExpandAllContext = createContext(0);

/** 跟随「全部展开」信号的展开状态；信号变化时置为展开，之后用户仍可自行收起。 */
export function useExpandAllSignal(
  initial: boolean | null,
): [boolean | null, (value: boolean | null) => void] {
  const signal = useContext(ExpandAllContext);
  const [value, setValue] = useState<boolean | null>(
    signal > 0 ? true : initial,
  );
  const [seen, setSeen] = useState(signal);
  if (seen !== signal) {
    // 渲染期间按新信号调整状态（React 允许组件在渲染中更新自身 state）
    setSeen(signal);
    setValue(true);
  }
  return [value, setValue];
}

/** 展开状态：用户点过后以用户为准，否则跟随默认值（如运行中展开、结束后收起）。 */
function useDisclosure(auto: boolean) {
  const [manual, setManual] = useExpandAllSignal(null);
  const open = manual ?? auto;
  return [open, () => setManual(!open)] as const;
}

/** 复制并 toast 提示。 */
function useCopy() {
  const toast = useToast();
  const t = useT();
  return async (text: string, message: string) => {
    if (await copyText(text)) toast(message);
    else toast(t("steps.error.copyFailed"), { tone: "error" });
  };
}

const FAILED_LIKE: ReadonlySet<StepStatus> = new Set(["failed", "denied"]);

function needsAttention(status: StepStatus) {
  return FAILED_LIKE.has(status) || status === "interrupted";
}

function StatusIcon({ status, icon }: { status: StepStatus; icon: IconName }) {
  if (status === "running") return <Spinner size={16} />;
  if (status === "interrupted")
    return (
      <Icon
        name="hifox-circle-stop"
        size={16}
        className="shrink-0 text-fg-secondary"
      />
    );
  if (FAILED_LIKE.has(status))
    return (
      <Icon name="triangle-alert" size={16} className="shrink-0 text-danger" />
    );
  return <Icon name={icon} size={16} className="shrink-0" />;
}

interface HeaderProps {
  status: StepStatus;
  icon: IconName;
  label: string;
  /** 标题后的对象（命令、文件名、查询词）；mono 时用等宽字体 */
  detail?: ReactNode;
  mono?: boolean;
  /** 尾部附加信息（「· 2.4 秒」「+14 −3」） */
  extra?: ReactNode;
  expanded?: boolean;
  /** 不传则不可展开 */
  onToggle?: () => void;
}

/** 步骤标题行：图标 + 动作 + 对象 + 附加信息 + chevron。 */
function StepHeader({
  status,
  icon,
  label,
  detail,
  mono,
  extra,
  expanded,
  onToggle,
}: HeaderProps) {
  const t = useT();
  const content = (
    <>
      <StatusIcon status={status} icon={icon} />
      <span
        className={cn("shrink-0", FAILED_LIKE.has(status) && "text-danger")}
      >
        {label}
      </span>
      {detail !== undefined && detail !== "" && (
        <span
          className={cn(
            "min-w-0 max-w-[42ch] truncate text-fg-secondary",
            mono && "font-mono text-code",
          )}
          title={typeof detail === "string" ? detail : undefined}
        >
          {detail}
        </span>
      )}
      {extra}
      {onToggle && (
        <Icon
          name={expanded ? "chevron-down" : "chevron-right"}
          size={16}
          className="ml-auto shrink-0 text-fg-muted"
        />
      )}
    </>
  );
  const className =
    "flex min-h-8 w-full min-w-0 items-center gap-2 rounded-md px-2 text-body-sm text-fg-secondary";
  if (!onToggle) return <div className={className}>{content}</div>;
  return (
    <button
      type="button"
      aria-expanded={expanded}
      title={expanded ? t("steps.collapse") : t("steps.expand")}
      onClick={onToggle}
      className={cn(
        className,
        "text-left transition-colors hover:bg-surface-hover focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
      )}
    >
      {content}
    </button>
  );
}

const Body = ({ children }: { children: ReactNode }) => (
  <div className="mt-1 mb-2 ml-8 flex min-w-0 flex-col gap-2">{children}</div>
);

const CodeBlock = ({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) => (
  <div
    className={cn(
      "rounded-md bg-surface-muted px-3 py-2 font-mono text-code text-fg-default",
      className,
    )}
  >
    {children}
  </div>
);

const Note = ({ children }: { children: ReactNode }) => (
  <p className="text-caption text-fg-muted">{children}</p>
);

/** running / done 之外的状态在标题后加后缀（只读类步骤用）。 */
function suffixOf(status: StepStatus, t: ReturnType<typeof useT>) {
  if (status === "failed" || status === "interrupted" || status === "denied")
    return (
      <span
        className={cn("shrink-0", FAILED_LIKE.has(status) && "text-danger")}
      >
        {t(`steps.stateSuffix.${status}`)}
      </span>
    );
  return null;
}

const durationExtra = (ms: number | undefined, t: ReturnType<typeof useT>) =>
  typeof ms === "number" ? (
    <span className="shrink-0 tabular-nums">· {formatDuration(ms, t)}</span>
  ) : null;

/** 只允许 http(s) 链接，避免运行时内容注入 javascript: 等地址。 */
export function safeHref(url: string | undefined): string | undefined {
  if (!url) return undefined;
  try {
    const parsed = new URL(url);
    return parsed.protocol === "http:" || parsed.protocol === "https:"
      ? parsed.href
      : undefined;
  } catch {
    return undefined;
  }
}

const baseName = (path: string) => path.split(/[\\/]/).pop() || path;

// ---------------------------------------------------------------- Row/PrepEnv

export function PrepEnvRow({ step }: { step: StepView }) {
  const t = useT();
  const contentId = useId();
  const status = step.status === "settled" ? "done" : step.status;
  const compact =
    status === "done" &&
    step.substeps?.every((substep) => substep.state === "done");
  const [open, toggle] = useDisclosure(
    status !== "done" ||
      Boolean(step.substeps?.some((substep) => substep.state !== "done")),
  );
  const progress =
    typeof step.progress === "number"
      ? Math.min(1, Math.max(0, step.progress))
      : null;
  return (
    <div>
      <ChainOfThoughtTrigger
        expanded={open}
        aria-controls={open && step.substeps?.length ? contentId : undefined}
        onClick={step.substeps?.length ? toggle : undefined}
        disabled={!step.substeps?.length}
        icon={
          <StatusIcon
            status={status}
            icon={status === "done" ? "check" : "cog"}
          />
        }
        className={cn(
          "text-fg-muted disabled:pointer-events-none disabled:[&>svg:last-child]:hidden",
          FAILED_LIKE.has(status) && "text-danger",
        )}
      >
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span>{t(`steps.prep.${status}`)}</span>
          {status === "running" && progress !== null ? (
            <span
              role="progressbar"
              aria-label={t("steps.prep.progress")}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(progress * 100)}
              className="h-1 w-16 shrink-0 overflow-hidden rounded-full bg-line-default"
            >
              <span
                className="block h-full rounded-full bg-accent transition-[width]"
                style={{ width: `${progress * 100}%` }}
              />
            </span>
          ) : (
            durationExtra(step.duration_ms, t)
          )}
        </span>
      </ChainOfThoughtTrigger>
      {open && step.substeps?.length ? (
        <ul
          id={contentId}
          aria-label={t("steps.prep.details")}
          className={cn(
            "mt-1 pl-6",
            compact
              ? "flex flex-wrap gap-x-4 gap-y-1.5"
              : "flex flex-col gap-1.5",
          )}
        >
          {step.substeps.map((substep, index) => (
            <li
              key={`${substep.kind}:${index}`}
              className={cn(
                "flex min-w-0 max-w-full items-start gap-1.5 text-caption text-fg-muted",
                substep.state !== "done" &&
                  substep.state !== "running" &&
                  "text-danger",
              )}
            >
              {substep.state === "running" ? (
                <Spinner size={16} />
              ) : substep.state === "done" ? (
                <Icon name="check" size={16} className="mt-0.5" />
              ) : (
                <Icon name="x" size={16} className="mt-0.5" />
              )}
              <span className="min-w-0 [overflow-wrap:anywhere]">
                {substep.label
                  ? substep.kind === "runtime"
                    ? runtimeLabel(substep.label)
                    : substep.label
                  : compact ||
                      (substep.state !== "running" && substep.state !== "done")
                    ? t(`steps.prep.substepNames.${substep.kind}`)
                    : t(
                        substep.state === "running"
                          ? `steps.prep.substepsRunning.${substep.kind}`
                          : `steps.prep.substeps.${substep.kind}`,
                      )}
                {substep.state !== "running" && substep.state !== "done" && (
                  <> · {t(`steps.stateSuffix.${substep.state}`)}</>
                )}
              </span>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

// --------------------------------------------------------------- Row/Thinking

export function ThinkingRow({ step }: { step: StepView }) {
  const t = useT();
  const [open, toggle] = useDisclosure(false);
  const running = step.status === "running";
  return (
    <div>
      <StepHeader
        status={running ? "running" : "done"}
        icon="message-circle"
        label={t(running ? "steps.thinking.running" : "steps.thinking.done")}
        extra={suffixOf(step.status, t)}
        expanded={open}
        onToggle={toggle}
      />
      {open && (
        <Body>
          <p className="whitespace-pre-wrap border-l-2 border-line-default pl-3 text-body-sm text-fg-muted">
            {step.text?.trim() ? step.text : t("steps.thinking.empty")}
          </p>
        </Body>
      )}
    </div>
  );
}

// -------------------------------------------------------------- Row/AgentText

/** 正文渲染（Markdown 等由调用方提供）；缺省按纯文本保留换行。 */
export type RenderText = (text: string) => ReactNode;

export const PlainText = ({ text }: { text: string }) => (
  <p className="whitespace-pre-wrap break-words text-body text-fg-default">
    {text}
  </p>
);

export const RenderTextContext = createContext<RenderText | null>(null);

/** 用调用方的渲染器或纯文本渲染一段正文。 */
export function StepText({ text }: { text: string }) {
  const render = useContext(RenderTextContext);
  return render ? <>{render(text)}</> : <PlainText text={text} />;
}

export function AgentTextRow({ step }: { step: StepView }) {
  if (!step.text?.trim()) return null;
  return <StepText text={step.text} />;
}

// ------------------------------------------------------------------- Row/Exec

const EXEC_ICON: Record<StepStatus, IconName> = {
  running: "terminal",
  done: "terminal",
  settled: "terminal",
  failed: "triangle-alert",
  interrupted: "terminal",
  denied: "triangle-alert",
};

/** 输出区：最高 400px 滚动；运行中且用户停在底部时自动跟随到底。 */
function ExecOutput({ output, follow }: { output: string; follow: boolean }) {
  const ref = useRef<HTMLPreElement>(null);
  const stick = useRef(true);
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && follow && stick.current) el.scrollTop = el.scrollHeight;
  }, [output, follow]);
  return (
    <pre
      ref={ref}
      data-testid="exec-output"
      onScroll={(event) => {
        const el = event.currentTarget;
        stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
      }}
      className="max-h-[400px] overflow-auto whitespace-pre-wrap break-all rounded-md bg-surface-muted px-3 py-2 font-mono text-code text-fg-default"
    >
      {output}
    </pre>
  );
}

export function ExecRow({
  step,
  nested = false,
}: {
  step: StepView;
  /** 在步骤分组内：运行中也默认收起 */
  nested?: boolean;
}) {
  const t = useT();
  const copy = useCopy();
  const [open, toggle] = useDisclosure(
    needsAttention(step.status) || (step.status === "running" && !nested),
  );
  const output = step.output ?? "";
  const footer: string[] = [];
  if (typeof step.exit_code === "number")
    footer.push(t("steps.exec.exitCode", { code: step.exit_code }));
  if (typeof step.duration_ms === "number")
    footer.push(formatDuration(step.duration_ms, t));
  return (
    <div>
      <StepHeader
        status={step.status}
        icon={EXEC_ICON[step.status]}
        label={t(`steps.exec.${step.status}`)}
        detail={step.title || step.command}
        mono={!step.title}
        expanded={open}
        onToggle={toggle}
      />
      {open && (
        <Body>
          {/* 适配器把工作目录内的路径转成相对路径，工作目录本身是「.」，不显示 */}
          {step.cwd && step.cwd !== "." && (
            <p className="font-mono text-caption text-fg-muted">{step.cwd}</p>
          )}
          <CodeBlock className="flex items-start gap-2">
            <span className="min-w-0 flex-1 whitespace-pre-wrap break-all">
              <span className="select-none text-fg-muted">$ </span>
              {step.command}
            </span>
            <IconButton
              label={t("steps.exec.copy")}
              icon={<Icon name="copy" size={16} />}
              size="28"
              variant="ghost"
              className="-my-1.5 -mr-2"
              onClick={() =>
                void copy(step.command ?? "", t("steps.exec.copied"))
              }
            />
          </CodeBlock>
          {output ? (
            <ExecOutput output={output} follow={step.status === "running"} />
          ) : step.status !== "running" ? (
            <CodeBlock className="font-sans text-body-sm text-fg-muted">
              {step.status === "denied"
                ? t("steps.exec.deniedBeforeOutput")
                : step.status === "interrupted" || step.terminated_before_output
                  ? t("steps.exec.interruptedBeforeOutput")
                  : t("steps.exec.noOutput")}
            </CodeBlock>
          ) : null}
          {step.truncated && <Note>{t("steps.exec.truncated")}</Note>}
          {footer.length > 0 && <Note>{footer.join(" · ")}</Note>}
        </Body>
      )}
    </div>
  );
}

// ------------------------------------------------------------------ Row/Patch

/** Tool input/output is display-only text, never HTML or executable arguments. */
export function ToolCallRow({ step }: { step: StepView }) {
  const t = useT();
  const copy = useCopy();
  const id = useId();
  const [open, toggle] = useDisclosure(needsAttention(step.status));
  const status = step.status === "settled" ? "done" : step.status;
  const name = step.tool_name || t("steps.tool.unnamed");
  return (
    <div
      className="min-w-0 rounded-lg border border-line-soft"
      data-tool-call={step.id}
    >
      <ChainOfThoughtTrigger
        expanded={open}
        aria-controls={open ? id : undefined}
        onClick={toggle}
        icon={<StatusIcon status={step.status} icon="blocks" />}
        className={cn(
          "w-full px-2 py-1 text-fg-secondary [&>span]:flex-1",
          needsAttention(step.status) && "text-danger",
        )}
      >
        <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <span className="min-w-0 break-all font-medium text-fg-default">
            {name}
          </span>
          <span
            className={cn(
              "ml-auto text-caption",
              FAILED_LIKE.has(status) ? "text-danger" : "text-fg-muted",
            )}
          >
            {t(`steps.tool.${status}`)}
          </span>
          <span className="text-caption">
            {durationExtra(step.duration_ms, t)}
          </span>
        </span>
      </ChainOfThoughtTrigger>
      {open && (
        <div
          id={id}
          className="flex min-w-0 flex-col gap-3 border-t border-line-default p-3"
        >
          {step.tool_input && (
            <div className="min-w-0">
              <div className="mb-1 flex items-center justify-between text-caption text-fg-muted">
                <span>{t("steps.tool.input")}</span>
                <IconButton
                  label={t("steps.tool.copyInput")}
                  icon={<Icon name="copy" size={16} />}
                  size="28"
                  variant="ghost"
                  onClick={() =>
                    void copy(step.tool_input!, t("steps.tool.copied"))
                  }
                />
              </div>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-md bg-surface-muted p-3 font-mono text-code text-fg-default">
                {step.tool_input}
              </pre>
            </div>
          )}
          {step.input_truncated && (
            <Note>{t("steps.tool.inputTruncated")}</Note>
          )}
          <div className="min-w-0">
            <p
              className={cn(
                "mb-1 text-caption",
                FAILED_LIKE.has(status) ? "text-danger" : "text-fg-muted",
              )}
            >
              {t(
                FAILED_LIKE.has(status)
                  ? "steps.tool.error"
                  : "steps.tool.output",
              )}
            </p>
            {step.output ? (
              <ExecOutput output={step.output} follow={status === "running"} />
            ) : (
              <p className="text-body-sm text-fg-muted">
                {t(
                  status === "running"
                    ? "steps.tool.pending"
                    : "steps.tool.noOutput",
                )}
              </p>
            )}
          </div>
          {step.truncated && <Note>{t("steps.tool.outputTruncated")}</Note>}
        </div>
      )}
    </div>
  );
}

const TOOL_KINDS = new Set([
  "tool_call",
  "exec",
  "read",
  "search",
  "web_search",
  "web_fetch",
  "write_file",
  "patch",
]);

function ToolActivityGroup({
  items,
  expanded,
}: {
  items: TurnItem[];
  expanded: boolean;
}) {
  const t = useT();
  const id = useId();
  const steps = items.flatMap((item) =>
    item.type === "step" ? [item.step] : [],
  );
  const running = steps.some((step) => step.status === "running");
  const attention = steps.some((step) => needsAttention(step.status));
  const [open, toggle] = useDisclosure(expanded || running || attention);
  const grouped = items.length > 1;
  return (
    <div className="min-w-0">
      {grouped && (
        <ChainOfThoughtTrigger
          expanded={open}
          aria-controls={open ? id : undefined}
          onClick={toggle}
          icon={
            <StatusIcon
              status={attention ? "failed" : running ? "running" : "done"}
              icon="layers"
            />
          }
          className={cn(
            "px-2 tabular-nums",
            attention ? "text-danger" : "text-fg-secondary",
          )}
        >
          {t(
            attention
              ? "steps.tool.groupAttention"
              : running
                ? "steps.tool.groupRunning"
                : "steps.tool.groupDone",
            { count: steps.length },
          )}
        </ChainOfThoughtTrigger>
      )}
      <div
        id={id}
        hidden={grouped && !open}
        className={cn(
          "min-w-0",
          grouped && "mt-1 ml-4 border-l border-line-soft pl-2",
        )}
      >
        <div className="flex min-w-0 flex-col gap-1">
          {items.map((item) => (
            <TurnItemRow key={item.key} item={item} nested={grouped} />
          ))}
        </div>
      </div>
    </div>
  );
}

/** Only adjacent calls are grouped. Commentary/warnings retain their event order. */
export function TurnActivityList({
  items,
  compact,
}: {
  items: TurnItem[];
  compact: boolean;
}) {
  const blocks: TurnItem[][] = [];
  let tools: TurnItem[] | null = null;
  for (const item of items) {
    if (
      compact &&
      item.type === "step" &&
      (item.step.kind === "prep_env" || item.step.kind === "thinking") &&
      !needsAttention(item.step.status)
    )
      continue;
    const tool = item.type === "step" && TOOL_KINDS.has(item.step.kind);
    if (tool) {
      if (!tools) {
        tools = [];
        blocks.push(tools);
      }
      tools.push(item);
    } else {
      tools = null;
      blocks.push([item]);
    }
  }
  return (
    <>
      {blocks.map((block) =>
        block[0]!.type === "step" && TOOL_KINDS.has(block[0]!.step.kind) ? (
          <ToolActivityGroup
            key={block[0]!.key}
            items={block}
            expanded={!compact}
          />
        ) : (
          <TurnItemRow key={block[0]!.key} item={block[0]!} />
        ),
      )}
    </>
  );
}

const DIFF_META =
  /^(diff --git |index |--- |\+\+\+ |new file mode|deleted file mode|similarity index|rename (from|to) )/;

/**
 * unified diff 按行着色；add / delete 且没有 @@ 块时整份内容视为新增 / 删除。
 * diff --git / --- / +++ 等文件头只出现在第一个 @@ 之前，之后以 --- 开头的是删除行，照常显示。
 * 不带行号的裸 @@（查找替换式编辑合成的 diff）是块分隔：第一个不显示，其余显示为省略号。
 */
export function DiffLines({
  diff,
  kind,
}: {
  diff: string;
  kind: StepPatchFile["kind"];
}) {
  const whole = !diff.includes("@@") && (kind === "add" || kind === "delete");
  const lines = diff.replace(/\n$/, "").split("\n");
  const firstHunk = lines.findIndex((line) => line.startsWith("@@"));
  return (
    <div className="max-h-[400px] overflow-auto rounded-md border border-line-soft font-mono text-code">
      {lines.map((line, index) => {
        const header = firstHunk < 0 || index < firstHunk;
        if (!whole && header && DIFF_META.test(line)) return null;
        if (!whole && line.trim() === "@@") {
          if (index === firstHunk) return null;
          return (
            <div
              key={index}
              data-diff="hunk"
              aria-hidden
              className="px-3 text-fg-muted select-none"
            >
              ⋯
            </div>
          );
        }
        const added = whole ? kind === "add" : line.startsWith("+");
        const removed = whole ? kind === "delete" : line.startsWith("-");
        const hunk = !whole && line.startsWith("@@");
        const sign = whole ? (added ? "+" : "-") : line.charAt(0);
        const text = whole ? line : line.slice(1);
        return (
          <div
            key={index}
            data-diff={added ? "add" : removed ? "del" : hunk ? "hunk" : "ctx"}
            className={cn(
              "flex whitespace-pre-wrap break-all px-3",
              added && "bg-success-tint",
              removed && "bg-danger-soft",
              hunk && "text-fg-muted",
            )}
          >
            {hunk ? (
              line
            ) : (
              <>
                <span
                  aria-hidden
                  className={cn(
                    "w-4 shrink-0 select-none",
                    added && "text-success",
                    removed && "text-danger",
                  )}
                >
                  {sign === "+" || sign === "-" ? sign : ""}
                </span>
                <span>{text}</span>
              </>
            )}
          </div>
        );
      })}
    </div>
  );
}

const editLabel = (
  status: StepStatus,
): `steps.patch.${"running" | "done" | "failed" | "interrupted" | "denied"}` =>
  `steps.patch.${status === "settled" ? "done" : status}`;

export function PatchRow({ step }: { step: StepView }) {
  const t = useT();
  const [open, toggle] = useDisclosure(false);
  const files = step.files ?? [];
  const additions = files.reduce((sum, file) => sum + file.additions, 0);
  const deletions = files.reduce((sum, file) => sum + file.deletions, 0);
  const first = files[0];
  return (
    <div>
      <StepHeader
        status={step.status === "settled" ? "done" : step.status}
        icon="pencil-line"
        label={t(editLabel(step.status))}
        detail={
          first
            ? `${baseName(first.path)}${files.length > 1 ? ` ${t("steps.patch.more", { count: files.length - 1 })}` : ""}`
            : undefined
        }
        extra={
          files.length > 0 ? (
            <span className="shrink-0 font-mono text-code">
              <span className="text-success">+{additions}</span>{" "}
              <span className="text-danger">−{deletions}</span>
            </span>
          ) : null
        }
        expanded={open}
        onToggle={files.length ? toggle : undefined}
      />
      {open && (
        <Body>
          {files.map((file, index) => (
            <div key={`${file.path}:${index}`} className="flex flex-col gap-1">
              <p className="flex items-center gap-2 font-mono text-caption text-fg-muted">
                <span className="truncate">{file.path}</span>
                {file.kind !== "update" && (
                  <span className="shrink-0 font-sans">
                    {t(
                      file.kind === "add"
                        ? "steps.patch.added"
                        : "steps.patch.deleted",
                    )}
                  </span>
                )}
                {file.move_path && (
                  <span className="shrink-0 truncate">
                    {t("steps.patch.moved", { path: file.move_path })}
                  </span>
                )}
              </p>
              {file.diff ? (
                <DiffLines diff={file.diff} kind={file.kind} />
              ) : (
                <Note>{t("steps.patch.noDiff")}</Note>
              )}
              {file.truncated && <Note>{t("steps.patch.truncated")}</Note>}
            </div>
          ))}
        </Body>
      )}
    </div>
  );
}

// ------------------------------------------------------------------ Row/Write

/** 写入内容的高亮语言：apply_patch 格式的补丁按 diff，其余按文件路径推断。 */
function writeLanguage(path: string | undefined, content: string) {
  if (content.startsWith("*** Begin Patch")) return "diff";
  return path ? languageForPath(path) : null;
}

export function WriteRow({ step }: { step: StepView }) {
  const t = useT();
  const copy = useCopy();
  const [open, toggle] = useDisclosure(false);
  const status = step.status === "settled" ? "done" : step.status;
  const language = step.content ? writeLanguage(step.path, step.content) : null;
  return (
    <div>
      <StepHeader
        status={status}
        icon="hifox-file-plus"
        label={t(`steps.write.${status}`)}
        detail={step.path ? baseName(step.path) : undefined}
        expanded={open}
        onToggle={toggle}
      />
      {open && (
        <Body>
          {step.path && (
            <p className="flex items-center gap-1 font-mono text-caption text-fg-muted">
              <span className="truncate">{step.path}</span>
              <IconButton
                label={t("steps.write.copyPath")}
                icon={<Icon name="copy" size={16} />}
                size="28"
                variant="ghost"
                className="-my-2"
                onClick={() => void copy(step.path!, t("steps.write.copied"))}
              />
            </p>
          )}
          {step.applied === false && <Note>{t("steps.write.notApplied")}</Note>}
          {step.diff_available === false && (
            <Note>{t("steps.write.noDiff")}</Note>
          )}
          {step.content ? (
            <CodeCard
              code={step.content}
              language={language}
              label={
                language && language !== "plaintext"
                  ? languageLabel(language)
                  : null
              }
            />
          ) : null}
          {step.truncated && <Note>{t("steps.write.truncated")}</Note>}
        </Body>
      )}
    </div>
  );
}

// ------------------------------------------- Row/Activity · Row/Search（读取、搜索、网页）

const isSearchResult = (
  value: StepSearchResult | StepWebResult,
): value is StepSearchResult => "file" in value;

function ReadRow({ step }: { step: StepView }) {
  const t = useT();
  let range: string | undefined;
  if (typeof step.line_start === "number")
    range =
      typeof step.line_end === "number"
        ? t("steps.read.lines", { start: step.line_start, end: step.line_end })
        : t("steps.read.from", { start: step.line_start });
  return (
    <StepHeader
      status={step.status}
      icon="file-text"
      label={t(
        `steps.read.${step.status === "settled" ? "done" : step.status}`,
      )}
      detail={step.path ? baseName(step.path) : undefined}
      extra={<>{range && <span className="shrink-0">{range}</span>}</>}
    />
  );
}

function SearchRow({ step }: { step: StepView }) {
  const t = useT();
  const [open, toggle] = useDisclosure(false);
  const results = (step.results ?? []).filter(isSearchResult);
  const listing = !step.query;
  const total = step.total_files ?? results.length;
  const running = step.status === "running";
  const label = listing
    ? running
      ? "steps.search.listRunning"
      : "steps.search.listDone"
    : running
      ? "steps.search.running"
      : "steps.search.done";
  const expandable = !running && (results.length > 0 || Boolean(step.scope));
  return (
    <div>
      <StepHeader
        status={step.status}
        icon="search"
        label={t(label)}
        detail={step.query || step.scope}
        extra={
          <>
            {!running && !open && step.total_files !== undefined && (
              <span className="shrink-0">
                · {t("steps.search.results", { count: total })}
              </span>
            )}
            {suffixOf(step.status, t)}
          </>
        }
        expanded={open}
        onToggle={expandable ? toggle : undefined}
      />
      {open && (
        <Body>
          {step.scope && (
            <Note>{t("steps.search.scope", { scope: step.scope })}</Note>
          )}
          {results.length === 0 ? (
            <Note>{t("steps.search.empty")}</Note>
          ) : (
            <ul className="flex flex-col gap-1">
              {results.map((result, index) => (
                <li
                  key={`${result.file}:${index}`}
                  className="flex items-center gap-2 text-body-sm"
                >
                  <span className="min-w-0 truncate font-mono text-code text-fg-default">
                    {result.file}
                  </span>
                  {typeof result.matches === "number" && result.matches > 0 && (
                    <span className="shrink-0 text-caption text-fg-muted">
                      {t("steps.search.matches", { count: result.matches })}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
          {total > results.length && (
            <Note>
              {t("steps.search.more", { count: total - results.length })}
            </Note>
          )}
        </Body>
      )}
    </div>
  );
}

function WebSearchRow({ step }: { step: StepView }) {
  const t = useT();
  const [open, toggle] = useDisclosure(false);
  const results = (step.results ?? []).filter(
    (value): value is StepWebResult => !isSearchResult(value),
  );
  const running = step.status === "running";
  return (
    <div>
      <StepHeader
        status={step.status}
        icon="globe"
        label={t(running ? "steps.webSearch.running" : "steps.webSearch.done")}
        detail={step.query}
        extra={suffixOf(step.status, t)}
        expanded={open}
        onToggle={running ? undefined : toggle}
      />
      {open && (
        <Body>
          {results.length === 0 ? (
            <Note>{t("steps.webSearch.empty")}</Note>
          ) : (
            <ul className="flex flex-col gap-1">
              {results.map((result, index) => {
                const href = safeHref(result.url);
                return (
                  <li key={`${result.url}:${index}`} className="text-body-sm">
                    {href ? (
                      <a
                        href={href}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="flex min-w-0 items-center gap-1.5 text-fg-secondary hover:underline"
                      >
                        <span className="truncate">
                          {result.title || result.url}
                        </span>
                        <Icon
                          name="external-link"
                          size={16}
                          className="shrink-0"
                        />
                      </a>
                    ) : (
                      <span className="truncate text-fg-secondary">
                        {result.title || result.url}
                      </span>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </Body>
      )}
    </div>
  );
}

function WebFetchRow({ step }: { step: StepView }) {
  const t = useT();
  const href = safeHref(step.url);
  const running = step.status === "running";
  return (
    <StepHeader
      status={step.status}
      icon="globe"
      label={t(running ? "steps.webFetch.running" : "steps.webFetch.done")}
      detail={
        href ? (
          <a
            href={href}
            target="_blank"
            rel="noopener noreferrer"
            className="hover:underline"
          >
            {step.url}
          </a>
        ) : (
          step.url
        )
      }
      extra={suffixOf(step.status, t)}
    />
  );
}

/** Row/Activity：读取、搜索、网页搜索、网页读取。搜索与网页搜索可展开结果（Row/Search）。 */
export function ActivityRow({ step }: { step: StepView }) {
  switch (step.kind) {
    case "read":
      return <ReadRow step={step} />;
    case "search":
      return <SearchRow step={step} />;
    case "web_search":
      return <WebSearchRow step={step} />;
    default:
      return <WebFetchRow step={step} />;
  }
}

// ------------------------------------------------------------------- Row/Todo

export function TodoRow({ step }: { step: StepView }) {
  const t = useT();
  const [open, toggle] = useDisclosure(true);
  const items = step.items ?? [];
  const done = items.filter((item) => item.status === "completed").length;
  const finished = step.status !== "running";
  return (
    <div>
      <StepHeader
        status={finished ? "done" : "running"}
        icon="list-checks"
        label={t(finished ? "steps.todo.done" : "steps.todo.running")}
        extra={
          items.length > 0 ? (
            <span className="shrink-0">
              {t("steps.todo.progress", { done, total: items.length })}
            </span>
          ) : null
        }
        expanded={open}
        onToggle={items.length ? toggle : undefined}
      />
      {open && items.length > 0 && (
        <Body>
          {step.explanation && <Note>{step.explanation}</Note>}
          <ul className="flex flex-col gap-1 pl-1.5">
            {items.map((item, index) => (
              <li
                key={index}
                className="flex items-center gap-2 text-body-sm text-fg-default"
              >
                {item.status === "completed" ? (
                  <Icon
                    name="circle-check"
                    size={16}
                    className="shrink-0 text-accent"
                    label={t("steps.todo.status.completed")}
                  />
                ) : item.status === "in_progress" ? (
                  <Spinner
                    size={16}
                    label={t("steps.todo.status.in_progress")}
                  />
                ) : (
                  <Icon
                    name="circle"
                    size={16}
                    className="shrink-0 text-fg-subtle"
                    label={t("steps.todo.status.pending")}
                  />
                )}
                <span
                  className={cn(
                    "min-w-0",
                    item.status === "completed" && "text-fg-muted",
                  )}
                >
                  {item.text}
                </span>
              </li>
            ))}
          </ul>
        </Body>
      )}
    </div>
  );
}

// ------------------------------------------------------------- Row/StepGroup

export function StepGroupRow({
  step,
  children,
}: {
  step: StepView;
  children: ReactNode;
}) {
  const t = useT();
  const [open, toggle] = useDisclosure(step.status === "running");
  return (
    <div>
      <StepHeader
        status={step.status === "settled" ? "done" : step.status}
        icon="layers"
        label={step.title || t("steps.group.untitled")}
        extra={suffixOf(step.status, t)}
        expanded={open}
        onToggle={toggle}
      />
      {open && (
        <div className="mt-1 flex flex-col gap-1 pl-2.5">{children}</div>
      )}
    </div>
  );
}

// ----------------------------------------------------------------- 条目分发

/** 渲染一个回合条目（步骤、分组或提示行）。 */
export function TurnItemRow({
  item,
  nested = false,
}: {
  item: TurnItem;
  nested?: boolean;
}) {
  const t = useT();
  if (item.type === "warning")
    return (
      <p className="flex items-start gap-1.5 text-body-sm text-fg-muted">
        <Icon
          name="triangle-alert"
          size={16}
          className="mt-0.5 shrink-0 text-warning"
          label={t("steps.timeline.warning")}
        />
        <span className="min-w-0 whitespace-pre-wrap break-words">
          {item.text}
        </span>
      </p>
    );
  if (item.type === "group")
    return (
      <StepGroupRow step={item.step}>
        {item.children.map((child) => (
          <TurnItemRow key={child.key} item={child} nested />
        ))}
      </StepGroupRow>
    );
  const { step } = item;
  switch (step.kind) {
    case "prep_env":
      return <PrepEnvRow step={step} />;
    case "thinking":
      return <ThinkingRow step={step} />;
    case "agent_text":
      return <AgentTextRow step={step} />;
    case "exec":
      return <ExecRow step={step} nested={nested} />;
    case "tool_call":
      return <ToolCallRow step={step} />;
    case "patch":
      return <PatchRow step={step} />;
    case "write_file":
      return <WriteRow step={step} />;
    case "read":
    case "search":
    case "web_search":
    case "web_fetch":
      return <ActivityRow step={step} />;
    case "todo":
      return <TodoRow step={step} />;
    default:
      return null;
  }
}

// ------------------------------------------------------------ Row/TurnSummary

const SUMMARY_KEYS: Record<
  TurnFinalState,
  { withTime: MessageKey; noTime: MessageKey }
> = {
  done: { withTime: "steps.summary.done", noTime: "steps.summary.doneNoTime" },
  failed: {
    withTime: "steps.summary.failed",
    noTime: "steps.summary.failedNoTime",
  },
  cancelled: {
    withTime: "steps.summary.cancelled",
    noTime: "steps.summary.cancelledNoTime",
  },
};

/** 回合收尾行：「已完成 1 分 18 秒」，点击展开 / 收起执行过程。 */
export function TurnSummaryRow({
  state,
  durationMs,
  expanded,
  onToggle,
  controlsId,
}: {
  state: TurnFinalState;
  durationMs: number | null;
  expanded: boolean;
  /** 不传则不可展开（没有步骤可看） */
  onToggle?: () => void;
  controlsId?: string;
}) {
  const t = useT();
  const keys = SUMMARY_KEYS[state];
  const label =
    durationMs === null
      ? t(keys.noTime)
      : t(keys.withTime, { duration: formatDuration(durationMs, t) });
  // -ml-2 抵消内边距，让图标与上方作者名、下方正文左对齐
  const className = cn(
    "-ml-2 flex h-8 items-center gap-2 px-2 text-body-sm tabular-nums",
    state === "failed" ? "text-danger" : "text-fg-muted",
  );
  if (!onToggle) return <div className={className}>{label}</div>;
  return (
    <ChainOfThoughtTrigger
      expanded={expanded}
      aria-controls={controlsId}
      icon={
        <Icon
          name={
            state === "failed"
              ? "triangle-alert"
              : state === "cancelled"
                ? "hifox-circle-stop"
                : "hifox-circle-check"
          }
          size={16}
          className={state === "done" ? "text-accent" : undefined}
        />
      }
      onClick={onToggle}
      className={cn(
        "-ml-2 px-2 tabular-nums",
        state === "failed" ? "text-danger" : "text-fg-secondary",
      )}
    >
      {label}
    </ChainOfThoughtTrigger>
  );
}

// -------------------------------------------------------------- Row/RunStatus

export type RunStatusKind =
  | "queued"
  | "processing"
  | "thinking"
  | "canceling"
  | "done"
  | "failed"
  | "cancelled";

/** 有专门文案的排队原因（wait_reason，与中心 runs_wait_reason_valid 一致，阶段 12d 补齐） */
const WAIT_REASONS = [
  "SCHEDULED",
  "ORGANIZATION_CAPACITY",
  "AGENT_CONCURRENCY",
  "AGENT_CHAT_CONCURRENCY",
  "COMPUTER_CONCURRENCY",
  "CONVERSATION_BUSY",
  "WORKDIR_BUSY",
  "AWAITING_COMPUTER",
  "COMPUTER_OFFLINE",
  "TASK_GATE",
  "OFFER_EXPIRED",
  "RUNTIME_UNAVAILABLE",
] as const;
type KnownWaitReason = (typeof WAIT_REASONS)[number];
export const isKnownWaitReason = (
  value: string | null | undefined,
): value is KnownWaitReason =>
  (WAIT_REASONS as readonly string[]).includes(value ?? "");

/**
 * 排队原因的文案键：已知原因用专门文案；非空但本客户端不认识的原因（新版中心新增）用通用文案；
 * 空原因返回 null（只显示「排队中」）。
 */
export const waitReasonKey = (
  value: string | null | undefined,
): `connect.waitReason.${KnownWaitReason | "UNKNOWN"}` | null => {
  if (!value) return null;
  return isKnownWaitReason(value)
    ? `connect.waitReason.${value}`
    : "connect.waitReason.UNKNOWN";
};

/** 运行状态行：「处理中 12 秒」「正在思考」「已完成 1 分 5 秒」「执行失败 8 秒」「用户已取消 3 秒」。 */
export function RunStatusRow({
  kind,
  durationMs,
  waitReason,
}: {
  kind: RunStatusKind;
  /** 计时；thinking / queued 不显示 */
  durationMs?: number | null;
  /** 排队原因；只在 queued 时显示 */
  waitReason?: string | null;
}) {
  const t = useT();
  const showTime =
    typeof durationMs === "number" && kind !== "thinking" && kind !== "queued";
  // 正在处理 / 思考：脉冲圆点 + 文字微光（styles.css，减少动态效果时静止）
  const active = kind === "processing" || kind === "thinking";
  return (
    <p
      role="status"
      aria-live="polite"
      className={cn(
        "flex h-8 items-center gap-1.5 text-body-sm tabular-nums",
        kind === "failed"
          ? "text-danger"
          : kind === "done" || kind === "cancelled"
            ? "text-fg-secondary"
            : "text-fg-subtle",
      )}
    >
      {active && <span aria-hidden="true" className="run-status-pulse mr-1" />}
      <span className={active ? "run-status-shimmer font-medium" : undefined}>
        {t(`steps.status.${kind}`)}
      </span>
      {kind === "queued" && waitReasonKey(waitReason) && (
        <span>· {t(waitReasonKey(waitReason)!)}</span>
      )}
      {showTime && <span>{formatDuration(durationMs, t)}</span>}
    </p>
  );
}

/** 阶段 12a：待核对原因码的文案；其他原因用通用文案。 */
const RECONCILIATION_REASONS = [
  "LEASE_EXPIRED",
  "UNKNOWN_PROCESS",
  "PID_REUSE",
] as const;

export interface ReconciliationRowProps {
  /** 核对案的原因码；案尚未加载时为空 */
  reason?: string | null;
  /** 当前用户是否可人工处理（组织 Owner/Admin）；false 时只显示联系管理员的提示 */
  canResolve: boolean;
  /** 人工标记失败；没有开放案时不传 */
  onMarkFailed?: () => void;
  pending?: boolean;
  error?: boolean;
}

/** 待核对：Run 因设备失联或进程无法确认而暂停收尾。证据确定时中心会自动收尾，只有无法确定时才需要人工。 */
export function ReconciliationRow({
  reason,
  canResolve,
  onMarkFailed,
  pending,
  error,
}: ReconciliationRowProps) {
  const t = useT();
  const [confirming, setConfirming] = useState(false);
  const known = (RECONCILIATION_REASONS as readonly string[]).includes(
    reason ?? "",
  );
  return (
    <div
      role="status"
      aria-live="polite"
      className="flex flex-col gap-1 text-body-sm text-fg-subtle"
    >
      <p className="flex items-center gap-1.5">
        <span className="text-fg-secondary">
          {t("steps.reconciliation.title")}
        </span>
        <span>
          ·{" "}
          {t(
            known
              ? (`steps.reconciliation.reason.${reason}` as MessageKey)
              : "steps.reconciliation.reason.generic",
          )}
        </span>
      </p>
      {!canResolve && <p>{t("steps.reconciliation.contactAdmin")}</p>}
      {canResolve && onMarkFailed && !confirming && (
        <div>
          <Button
            variant="ghost"
            size="28"
            disabled={pending}
            onClick={() => setConfirming(true)}
          >
            {t("steps.reconciliation.markFailed")}
          </Button>
        </div>
      )}
      {canResolve && onMarkFailed && confirming && (
        <div className="flex flex-col gap-1">
          <p>{t("steps.reconciliation.confirm")}</p>
          <div className="flex gap-2">
            <Button
              variant="ghost"
              size="28"
              disabled={pending}
              onClick={() => {
                onMarkFailed();
                setConfirming(false);
              }}
            >
              {t("steps.reconciliation.confirmAction")}
            </Button>
            <Button
              variant="ghost"
              size="28"
              onClick={() => setConfirming(false)}
            >
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      )}
      {error && (
        <p className="text-danger">{t("steps.reconciliation.failed")}</p>
      )}
    </div>
  );
}
