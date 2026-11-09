import { useEffect, useRef, useState, type ReactNode } from "react";

import type {
  ConversationOverrideOptions,
  ReasoningEffort,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuLabel,
  Tooltip,
  cn,
  type IconName,
} from "@openfox/ui";

import { runtimeLabel } from "../agents/model";
import {
  APPROVAL_MODES,
  REASONING_EFFORTS,
  agentDefaultModel,
  approvalDescKey,
  approvalNameKey,
  patchFor,
  reasoningNameKey,
  type OverridePatch,
  type OverrideSelection,
  type OverrideValues,
} from "./overrides";

export type OptionsStatus = "loading" | "ready" | "error";

export interface OverrideControlsProps {
  hideApproval?: boolean;
  afterRuntime?: ReactNode;
  /** Agent 默认（清除覆盖后恢复成的值） */
  defaults: OverrideValues;
  /** 当前覆盖值，null = 沿用 Agent 默认 */
  selection: OverrideSelection;
  /** 下一次运行会采用的值 */
  effective: OverrideValues;
  options: ConversationOverrideOptions | null;
  optionsStatus: OptionsStatus;
  optionsRefreshing?: boolean;
  /** 只读：只展示当前值（没有发消息的权限、对话已关闭） */
  readOnly: boolean;
  /** 修改请求进行中：不能连发 */
  pending: boolean;
  onChange: (patch: OverridePatch) => void;
  onRetryOptions?: () => void;
}

function MenuNotes({
  agentDefault,
  status,
  offline,
  onRetry,
}: {
  agentDefault: string;
  status: OptionsStatus;
  offline: boolean;
  onRetry?: () => void;
}) {
  const t = useT();
  return (
    <>
      <div className="px-2.5 pb-1 text-caption text-fg-muted">
        {t("chat.overrides.agentDefault", { value: agentDefault })}
      </div>
      {status === "loading" && (
        <div role="status" className="px-2.5 pb-1 text-caption text-fg-muted">
          {t("chat.overrides.loading")}
        </div>
      )}
      {status === "error" && (
        <div
          role="alert"
          className="flex items-center gap-2 px-2.5 pb-1 text-caption text-danger"
        >
          {t("chat.overrides.loadFailed")}
          {onRetry && (
            <button
              type="button"
              onClick={onRetry}
              className="text-fg-default underline"
            >
              {t("common.retry")}
            </button>
          )}
        </div>
      )}
      {status === "ready" && offline && (
        <div className="px-2.5 pb-1 text-caption text-warning">
          {t("chat.overrides.computerOffline")}
        </div>
      )}
    </>
  );
}

/** 输入框上的一个弹出式按钮：显示有效值，是覆盖值时带小圆点；只读或进行中时禁用 */
function OverrideMenu({
  label,
  value,
  icon,
  iconOnly = false,
  overridden,
  disabled,
  disabledTitle,
  menuTitle,
  children,
}: {
  label: string;
  value: string;
  icon?: IconName;
  iconOnly?: boolean;
  overridden: boolean;
  disabled: boolean;
  disabledTitle?: string;
  menuTitle: string;
  children: ReactNode;
}) {
  const t = useT();
  const name = t(
    overridden ? "chat.overrides.buttonOverridden" : "chat.overrides.button",
    { label, value },
  );
  return (
    <Menu
      label={menuTitle}
      placement="top-start"
      className="w-[300px]"
      trigger={({ ref, open, toggle }) => (
        <Tooltip content={disabled && disabledTitle ? disabledTitle : name}>
          <button
            ref={ref}
            type="button"
            aria-label={name}
            aria-haspopup="menu"
            aria-expanded={open}
            disabled={disabled}
            onClick={toggle}
            className={cn(
              "relative flex h-7 items-center rounded-md text-body-sm text-fg-secondary hover:bg-surface-muted disabled:cursor-default disabled:hover:bg-transparent",
              iconOnly
                ? "w-7 shrink-0 justify-center"
                : "min-w-7 max-w-40 gap-1 px-1.5",
              icon && !iconOnly && "shrink-0",
            )}
          >
            {icon && <Icon name={icon} size={14} className="shrink-0" />}
            {!iconOnly && <span className="truncate">{value}</span>}
            {overridden && (
              <span
                aria-hidden="true"
                data-testid="override-dot"
                className="absolute top-1 right-0 size-1.5 rounded-full bg-accent"
              />
            )}
          </button>
        </Tooltip>
      )}
    >
      <MenuLabel>{menuTitle}</MenuLabel>
      {children}
    </Menu>
  );
}

const modelText = (model: string, fallback: string) =>
  model === "default" ? fallback : model;

function ModelOptions({
  items,
  effective,
  overridden,
  ready,
  refreshing,
  onRefresh,
  onSelect,
  onSelectDepth,
  reasoningEffort,
}: {
  items: {
    id: string;
    name: string;
    efforts: ReasoningEffort[];
    defaultEffort?: ReasoningEffort;
  }[];
  effective: string;
  overridden: boolean;
  ready: boolean;
  refreshing: boolean;
  onRefresh?: () => void;
  onSelect: (model: string) => void;
  onSelectDepth: (model: string, effort: ReasoningEffort) => void;
  reasoningEffort: ReasoningEffort;
}) {
  const t = useT();
  const [query, setQuery] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const frame = requestAnimationFrame(() => input.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, []);
  const visible = items.filter((item) =>
    `${item.id} ${item.name}`
      .toLowerCase()
      .includes(query.trim().toLowerCase()),
  );
  return (
    <>
      <div className="flex items-center gap-2 border-b border-line-default px-2.5 py-2">
        <Icon name="search" className="shrink-0 text-fg-muted" />
        <input
          ref={input}
          aria-label={t("chat.overrides.searchModels")}
          placeholder={t("chat.overrides.searchModels")}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (
              event.nativeEvent.isComposing ||
              event.keyCode === 229 ||
              event.key === "Home" ||
              event.key === "End"
            ) {
              event.stopPropagation();
              return;
            }
            if (event.key === "Enter") {
              event.preventDefault();
              list.current
                ?.querySelector<HTMLButtonElement>(
                  '[role="menuitemradio"]:not([aria-disabled="true"])',
                )
                ?.click();
            }
          }}
          className="min-w-0 flex-1 bg-transparent text-body outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
        />
        {onRefresh && (
          <IconButton
            label={t("chat.overrides.refreshModels")}
            icon={<Icon name="refresh-cw" size={14} />}
            size="28"
            disabled={refreshing}
            aria-busy={refreshing || undefined}
            onClick={onRefresh}
          />
        )}
      </div>
      <div ref={list} className="max-h-60 overflow-y-auto">
        {visible.map((item) => (
          <div key={item.id} className="flex items-center">
            <MenuItem
              selected={item.id === effective}
              disabled={item.id !== effective && !ready}
              trailing={
                item.id === effective && overridden
                  ? t("chat.overrides.thisConversation")
                  : undefined
              }
              onSelect={() => onSelect(item.id)}
            >
              {item.name}
            </MenuItem>
            {(item.efforts.length > 1 ||
              (item.id === effective && reasoningEffort !== "DEFAULT")) && (
              <Menu
                label={t("chat.overrides.chooseReasoning", {
                  model: item.name,
                })}
                placement="right-start"
                className="w-[200px]"
                trigger={({ ref, open, toggle }) => (
                  <button
                    ref={ref}
                    type="button"
                    disabled={!ready}
                    aria-label={t("chat.overrides.chooseReasoning", {
                      model: item.name,
                    })}
                    aria-haspopup="menu"
                    aria-expanded={open}
                    onClick={(event) => {
                      event.stopPropagation();
                      toggle();
                    }}
                    className="flex h-8 shrink-0 items-center gap-1 rounded-md px-2 text-caption text-fg-muted hover:bg-surface-active"
                  >
                    {item.id === effective &&
                      reasoningEffort !== "DEFAULT" &&
                      t(reasoningNameKey(reasoningEffort))}
                    <Icon name="chevron-right" size={14} />
                  </button>
                )}
              >
                <MenuLabel>{t("chat.overrides.reasoning")}</MenuLabel>
                {REASONING_EFFORTS.filter(
                  (effort) =>
                    item.efforts.includes(effort) ||
                    (item.id === effective && effort === reasoningEffort),
                ).map((effort) => (
                  <MenuItem
                    key={effort}
                    selected={
                      effort ===
                      (item.id === effective ? reasoningEffort : "DEFAULT")
                    }
                    disabled={!ready || !item.efforts.includes(effort)}
                    trailing={
                      effort !== "DEFAULT" &&
                      effort === item.defaultEffort &&
                      item.efforts.includes(effort)
                        ? t("chat.overrides.modelDefaultReasoning")
                        : undefined
                    }
                    onSelect={() => onSelectDepth(item.id, effort)}
                  >
                    {t(reasoningNameKey(effort))}
                  </MenuItem>
                ))}
              </Menu>
            )}
          </div>
        ))}
        {!visible.length && (
          <p className="px-2.5 py-3 text-body-sm text-fg-muted">
            {t("ui.searchMenu.empty")}
          </p>
        )}
      </div>
    </>
  );
}

/**
 * 输入框里的三个就地弹出控件：Runtime、模型、权限模式。
 * 控件是纯展示：请求、缓存与新对话的暂存都在调用方。
 */
export function OverrideControls({
  hideApproval = false,
  afterRuntime,
  defaults,
  selection,
  effective,
  options,
  optionsStatus,
  optionsRefreshing = false,
  readOnly,
  pending,
  onChange,
  onRetryOptions,
}: OverrideControlsProps) {
  const t = useT();
  const disabled = readOnly || pending;
  const disabledTitle = readOnly ? t("chat.overrides.readOnly") : undefined;
  const offline = options ? !options.computer_online : false;
  const ready = optionsStatus === "ready" && options !== null;
  const select = (field: keyof OverrideSelection, value: string | null) =>
    onChange(patchFor(field, value, defaults, selection));

  // ---------- Runtime ----------
  const reported = options?.runtimes ?? [];
  const runtimeItems = reported.map((item) => item.provider);
  // 当前有效的 Runtime 不在心跳里（例如电脑还没上报）：仍列出，禁用并说明原因
  if (!runtimeItems.includes(effective.runtime))
    runtimeItems.unshift(effective.runtime);
  const currentRuntime = reported.find(
    (item) => item.provider === effective.runtime,
  );

  // ---------- 模型 ----------
  const defaultText = t("agents.modelDefault");
  const models = currentRuntime?.models ?? [];
  const modelEfforts = (model?: { reasoning_efforts?: ReasoningEffort[] }) =>
    REASONING_EFFORTS.filter(
      (effort) =>
        effort === "DEFAULT" ||
        (currentRuntime?.reasoning_efforts?.includes(effort) &&
          model?.reasoning_efforts?.includes(effort)),
    );
  const modelDefault = agentDefaultModel(defaults, selection);
  const modelIds = models.map((model) => model.id);
  const modelItems = [
    {
      id: "default",
      name: defaultText,
      efforts: modelEfforts(models.find((model) => model.is_default)),
      defaultEffort: models.find((model) => model.is_default)
        ?.default_reasoning_effort,
    },
    ...models
      .filter((model) => model.id !== "default")
      .map((model) => ({
        id: model.id,
        name: model.display_name || model.id,
        efforts: modelEfforts(model),
        defaultEffort: model.default_reasoning_effort,
      })),
  ];
  // 当前有效的模型不在上报列表里（手动填的模型 ID 等）：仍列出，方便看到并恢复
  if (!modelItems.some((item) => item.id === effective.model))
    modelItems.push({
      id: effective.model,
      name: effective.model,
      efforts: ["DEFAULT"],
      defaultEffort: undefined,
    });

  const runtimeName = runtimeLabel(effective.runtime);
  const reasoningEffort = effective.reasoning_effort ?? "DEFAULT";
  const reason = (key: MessageKey, values?: Record<string, string>) =>
    t(key, values);

  return (
    <>
      <OverrideMenu
        label={t("chat.overrides.runtime")}
        value={runtimeName}
        icon="monitor"
        overridden={selection.runtime !== null}
        disabled={disabled}
        disabledTitle={disabledTitle}
        menuTitle={t("chat.overrides.runtimeTitle")}
      >
        <MenuNotes
          agentDefault={runtimeLabel(defaults.runtime)}
          status={optionsStatus}
          offline={offline}
          onRetry={onRetryOptions}
        />
        {runtimeItems.map((provider) => {
          const item = reported.find((entry) => entry.provider === provider);
          const blocked = !ready
            ? undefined
            : !item
              ? reason("chat.overrides.runtimeNotReported")
              : !item.runnable
                ? reason("chat.overrides.runtimeNotRunnable")
                : undefined;
          return (
            <MenuItem
              key={provider}
              selected={provider === effective.runtime}
              disabled={
                provider !== effective.runtime && (!ready || Boolean(blocked))
              }
              disabledReason={blocked}
              description={blocked}
              trailing={
                provider === effective.runtime && selection.runtime !== null
                  ? t("chat.overrides.thisConversation")
                  : undefined
              }
              onSelect={() => {
                if (provider !== effective.runtime) select("runtime", provider);
              }}
            >
              {runtimeLabel(provider)}
            </MenuItem>
          );
        })}
      </OverrideMenu>
      {afterRuntime}
      {!hideApproval && (
        <OverrideMenu
          label={t("chat.overrides.approval")}
          value={t(approvalNameKey(effective.approval_mode))}
          icon={
            effective.approval_mode === "FULL_ACCESS" ? "shield-off" : "shield"
          }
          iconOnly
          overridden={selection.approval_mode !== null}
          disabled={disabled}
          disabledTitle={disabledTitle}
          menuTitle={t("chat.overrides.approvalTitle")}
        >
          <MenuNotes
            agentDefault={t(approvalNameKey(defaults.approval_mode))}
            status={optionsStatus}
            offline={offline}
            onRetry={onRetryOptions}
          />
          {APPROVAL_MODES.map((mode) => {
            // FOLLOW_RUNTIME 永远可用；其他档位要 Runtime 上报支持（旧版后台服务没有上报，一律不支持）
            const supported =
              mode === "FOLLOW_RUNTIME" ||
              (currentRuntime?.approval_modes.includes(mode) ?? false);
            const blocked =
              ready && !supported
                ? t("chat.overrides.approvalUnsupported", {
                    runtime: runtimeName,
                  })
                : undefined;
            const current = mode === effective.approval_mode;
            return (
              <MenuItem
                key={mode}
                selected={current}
                disabled={!current && (!ready || !supported)}
                disabledReason={blocked}
                description={blocked ?? t(approvalDescKey(mode))}
                trailing={
                  current && selection.approval_mode !== null
                    ? t("chat.overrides.thisConversation")
                    : undefined
                }
                onSelect={() => select("approval_mode", mode)}
              >
                {t(approvalNameKey(mode))}
              </MenuItem>
            );
          })}
        </OverrideMenu>
      )}
      <span className="flex-1" />
      <OverrideMenu
        label={t("chat.overrides.model")}
        value={
          modelItems.find((item) => item.id === effective.model)?.name ??
          modelText(effective.model, defaultText)
        }
        overridden={
          selection.model !== null || selection.reasoning_effort != null
        }
        disabled={disabled}
        disabledTitle={disabledTitle}
        menuTitle={t("chat.overrides.modelTitle")}
      >
        <MenuNotes
          agentDefault={modelText(modelDefault, defaultText)}
          status={optionsStatus}
          offline={offline}
          onRetry={onRetryOptions}
        />
        {ready && modelIds.length === 0 && (
          <div className="px-2.5 pb-1 text-caption text-fg-muted">
            {t("chat.overrides.noModels")}
          </div>
        )}
        <ModelOptions
          key={effective.runtime}
          items={modelItems}
          effective={effective.model}
          overridden={selection.model !== null}
          ready={ready}
          refreshing={optionsRefreshing || optionsStatus === "loading"}
          onRefresh={onRetryOptions}
          onSelect={(model) => select("model", model)}
          reasoningEffort={reasoningEffort}
          onSelectDepth={(model, effort) =>
            onChange({
              ...patchFor("model", model, defaults, selection),
              ...patchFor("reasoning_effort", effort, defaults, selection),
            })
          }
        />
      </OverrideMenu>
    </>
  );
}
