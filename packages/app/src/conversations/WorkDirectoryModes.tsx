import { useState } from "react";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Icon, TextInput, Tooltip } from "@openfox/ui";

export type DirectoryMode = "DIRECT" | "WORKTREE";

export const validWorkspaceRef = (input: string) => {
  const value = input.trim();
  return (
    !value ||
    (new TextEncoder().encode(value).length <= 255 &&
      !/^[+-]|[\s\x00-\x1f\x7f-\x9f~^:?*\[\\]|\.\.|@\{|\.$/.test(value) &&
      (!value.startsWith("refs/") || value.startsWith("refs/heads/")) &&
      value !== "@" &&
      value
        .split("/")
        .every(
          (part) => !!part && !part.startsWith(".") && !part.endsWith(".lock"),
        ))
  );
};

export function BaseBranch({
  value,
  onChange,
}: {
  value: string;
  onChange: (value: string) => void;
}) {
  const t = useT(),
    [manual, setManual] = useState(!!value);
  return (
    <div className="flex flex-wrap items-center gap-2 text-[13px]">
      <span>{t("chat.workspace.branch")}</span>
      <Tooltip content={t("chat.workspace.branchHelp")}>
        <button
          type="button"
          aria-label={t("chat.workspace.branchHelp")}
          className="text-fg-muted"
        >
          <Icon name="info" size={14} />
        </button>
      </Tooltip>
      <div className="flex min-w-0 items-center rounded-md border border-line-default">
        <select
          aria-label={t("chat.workspace.branchMode")}
          value={manual ? "manual" : "default"}
          onChange={(e) => {
            setManual(e.target.value === "manual");
            if (e.target.value === "default") onChange("");
          }}
          className="h-7 rounded-md bg-transparent px-2"
        >
          <option value="default">{t("chat.workspace.defaultRef")}</option>
          <option value="manual">{t("chat.workspace.manualRef")}</option>
        </select>
        {manual && (
          <TextInput
            autoFocus
            aria-label={t("chat.workspace.branch")}
            placeholder={t("chat.workspace.branchInput")}
            value={value}
            aria-invalid={!validWorkspaceRef(value)}
            onChange={(e) => onChange(e.target.value)}
            className="min-w-0 flex-1 border-0 border-l"
          />
        )}
      </div>
      {!validWorkspaceRef(value) && (
        <span role="alert" className="w-full text-caption text-danger">
          {t("chat.workspace.invalidRef")}
        </span>
      )}
    </div>
  );
}

export function DirectoryModeDialog({
  path,
  initialMode,
  initialBase,
  canConfirm,
  worktree,
  onConfirm,
  onClose,
}: {
  path: string;
  initialMode: DirectoryMode | null;
  initialBase: string;
  canConfirm: boolean;
  worktree: boolean;
  onConfirm: (mode: DirectoryMode, base: string) => void;
  onClose: () => void;
}) {
  const t = useT(),
    [mode, setMode] = useState(initialMode),
    [base, setBase] = useState(initialBase);
  return (
    <Dialog
      open
      width={520}
      title={t("chat.workspace.chooseMode")}
      description={t("chat.workspace.modeHint")}
      onClose={onClose}
      className="p-5"
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={
              !canConfirm ||
              !mode ||
              (mode === "WORKTREE" && (!worktree || !validWorkspaceRef(base)))
            }
            onClick={() => {
              if (mode) onConfirm(mode, base.trim());
            }}
          >
            {t("chat.workspace.confirm")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <p className="break-all rounded-md bg-surface-muted px-3 py-2 font-mono text-caption">
          {path}
        </p>
        {!worktree && (
          <p className="text-caption text-fg-muted">
            {t("chat.workspace.noWorktree")}
          </p>
        )}
        <div className="space-y-2">
          {(["DIRECT", "WORKTREE"] as const).map((option) => {
            const active = mode === option,
              isDirect = option === "DIRECT";
            return (
              <button
                type="button"
                key={option}
                aria-pressed={active}
                disabled={!isDirect && !worktree}
                onClick={() => setMode(option)}
                className={`flex w-full items-start gap-3 rounded-lg border px-3 py-2.5 text-left disabled:opacity-50 ${active ? "border-fg-default bg-surface-selected" : "border-line-default hover:bg-surface-hover"}`}
              >
                <span className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-surface-muted">
                  <Icon name={isDirect ? "workflow" : "git-branch"} size={20} />
                </span>
                <span className="flex-1">
                  <span className="block text-body font-medium">
                    {t(
                      isDirect
                        ? "chat.workspace.direct"
                        : "chat.workspace.worktree",
                    )}
                  </span>
                  <span className="block text-caption text-fg-muted">
                    {t(
                      isDirect
                        ? "chat.workspace.directHelp"
                        : "chat.workspace.worktreeHelp",
                    )}
                  </span>
                </span>
                <span
                  aria-hidden="true"
                  className={`mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border ${active ? "border-fg-default" : "border-line-default"}`}
                >
                  {active && (
                    <span className="size-2 rounded-full bg-fg-default" />
                  )}
                </span>
              </button>
            );
          })}
        </div>
        {mode === "WORKTREE" && <BaseBranch value={base} onChange={setBase} />}
      </div>
    </Dialog>
  );
}
