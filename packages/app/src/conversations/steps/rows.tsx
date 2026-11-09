import {
  createContext,
  useContext,
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
  Icon,
  ChainOfThoughtTrigger,
  IconButton,
  Spinner,
  cn,
  useToast,
  type IconName,
} from "@openfox/ui";

import { copyText } from "../../tasks/parts";
import { formatDuration } from "./format";
import type { StepStatus, StepView, TurnFinalState, TurnItem } from "./model";

// 执行步骤的行组件。每行 20px 高的标题行，可展开的行带 chevron。

/**
 * 「全部展开」信号：每点一次加 1，0 表示没有点过（缺省，对话里的时间线不受影响）。
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

function StatusIcon({ status, icon }: { status: StepStatus; icon: IconName }) {
  if (status === "running") return <Spinner size={14} />;
  if (FAILED_LIKE.has(status))
    return (
      <Icon name="triangle-alert" size={14} className="shrink-0 text-warning" />
    );
  return <Icon name={icon} size={14} className="shrink-0" />;
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
      <span className="shrink-0">{label}</span>
      {detail !== undefined && detail !== "" && (
        <span
          className={cn(
            "min-w-0 truncate text-fg-secondary",
            mono && "font-mono text-code",
          )}
        >
          {detail}
        </span>
      )}
      {extra}
      {onToggle && (
        <Icon
          name={expanded ? "chevron-down" : "chevron-right"}
          size={12}
          className="shrink-0"
        />
      )}
    </>
  );
  const className =
    "flex h-5 max-w-full items-center gap-1.5 text-body-sm text-fg-muted";
  if (!onToggle) return <div className={className}>{content}</div>;
  return (
    <button
      type="button"
      aria-expanded={expanded}
      title={expanded ? t("steps.collapse") : t("steps.expand")}
      onClick={onToggle}
      className={cn(className, "rounded-sm text-left hover:text-fg-secondary")}
    >
      {content}
    </button>
  );
}

const Body = ({ children }: { children: ReactNode }) => (
  <div className="mt-1.5 flex flex-col gap-1.5 pb-1">{children}</div>
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
    <span className="shrink-0">· {formatDuration(ms, t)}</span>
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
  const [open, toggle] = useDisclosure(step.status === "running");
  const status = step.status === "settled" ? "done" : step.status;
  const progress =
    typeof step.progress === "number"
      ? Math.min(1, Math.max(0, step.progress))
      : null;
  return (
    <div>
      <StepHeader
        status={status}
        icon="cog"
        label={t(`steps.prep.${status}`)}
        extra={
          status === "running" && progress !== null ? (
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
          )
        }
        expanded={open}
        onToggle={step.substeps?.length ? toggle : undefined}
      />
      {open && step.substeps?.length ? (
        <ul className="mt-1.5 flex flex-col gap-1 pl-1.5">
          {step.substeps.map((substep, index) => (
            <li
              key={`${substep.kind}:${index}`}
              className="flex h-5 items-center gap-1.5 text-body-sm text-fg-muted"
            >
              {substep.state === "running" ? (
                <Spinner size={14} />
              ) : substep.state === "done" ? (
                <Icon name="check" size={14} className="shrink-0" />
              ) : (
                <Icon name="x" size={14} className="shrink-0 text-danger" />
              )}
              <span className="truncate">
                {substep.label ||
                  t(
                    substep.state === "running"
                      ? `steps.prep.substepsRunning.${substep.kind}`
                      : `steps.prep.substeps.${substep.kind}`,
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
  const [open, toggle] = useDisclosure(step.status === "running" && !nested);
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
        detail={step.command}
        mono
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
              icon={<Icon name="copy" size={12} />}
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

const DIFF_META =
  /^(diff --git |index |--- |\+\+\+ |new file mode|deleted file mode|similarity index|rename (from|to) )/;

/** unified diff 按行着色；add / delete 且没有 @@ 块时整份内容视为新增 / 删除。 */
export function DiffLines({
  diff,
  kind,
}: {
  diff: string;
  kind: StepPatchFile["kind"];
}) {
  const whole = !diff.includes("@@") && (kind === "add" || kind === "delete");
  const lines = diff.replace(/\n$/, "").split("\n");
  return (
    <div className="max-h-[400px] overflow-auto rounded-md border border-line-soft font-mono text-code">
      {lines.map((line, index) => {
        if (!whole && DIFF_META.test(line)) return null;
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

export function WriteRow({ step }: { step: StepView }) {
  const t = useT();
  const copy = useCopy();
  const [open, toggle] = useDisclosure(false);
  const status = step.status === "settled" ? "done" : step.status;
  return (
    <div>
      <StepHeader
        status={status}
        icon="openfox-file-plus"
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
                icon={<Icon name="copy" size={12} />}
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
            <pre className="max-h-[400px] overflow-auto whitespace-pre-wrap break-all rounded-md bg-surface-muted px-3 py-2 font-mono text-code text-fg-default">
              {step.content}
            </pre>
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
        step.status === "running" ? "steps.read.running" : "steps.read.done",
      )}
      detail={step.path ? baseName(step.path) : undefined}
      extra={
        <>
          {range && <span className="shrink-0">{range}</span>}
          {suffixOf(step.status, t)}
        </>
      }
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
                          size={12}
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
                    size={14}
                    className="shrink-0 text-accent"
                    label={t("steps.todo.status.completed")}
                  />
                ) : item.status === "in_progress" ? (
                  <Spinner
                    size={14}
                    label={t("steps.todo.status.in_progress")}
                  />
                ) : (
                  <Icon
                    name="circle"
                    size={14}
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
          size={14}
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
  const className = cn(
    "flex h-8 items-center gap-1 text-body-sm",
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
                ? "openfox-circle-stop"
                : "openfox-circle-check"
          }
          size={14}
        />
      }
      onClick={onToggle}
      className={state === "failed" ? "text-danger" : "text-fg-muted"}
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

/** 有文案的排队原因（wait_reason）；其他原因只显示「排队中」 */
const WAIT_REASONS = ["AGENT_CHAT_CONCURRENCY"] as const;
type KnownWaitReason = (typeof WAIT_REASONS)[number];
export const isKnownWaitReason = (
  value: string | null | undefined,
): value is KnownWaitReason =>
  (WAIT_REASONS as readonly string[]).includes(value ?? "");

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
  return (
    <p
      role="status"
      aria-live="polite"
      className={cn(
        "flex h-8 items-center gap-1.5 text-body-sm",
        kind === "failed"
          ? "text-danger"
          : kind === "done" || kind === "cancelled"
            ? "text-fg-secondary"
            : "text-fg-subtle",
      )}
    >
      <span>{t(`steps.status.${kind}`)}</span>
      {kind === "queued" && isKnownWaitReason(waitReason) && (
        <span>· {t(`connect.waitReason.${waitReason}`)}</span>
      )}
      {showTime && <span>{formatDuration(durationMs, t)}</span>}
    </p>
  );
}
