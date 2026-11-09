import { useEffect, useState } from "react";

import type { UserPreferences } from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Button, Spinner, cn, useToast } from "@openfox/ui";

import {
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "../spaces/settings/parts";
import {
  useMyPreferences,
  usePreferencesSaving,
  useUpdatePreferences,
} from "./queries";
import {
  COMMAND_IDS,
  effectiveShortcut,
  isMacPlatform,
  recordShortcut,
  shortcutKeycaps,
  withShortcut,
  type CommandId,
  type SendShortcut,
} from "./shortcuts";

// 快捷键：发送方式（Enter / Shift+Enter）与应用命令的按键录制。
// 应用命令（命令菜单、移动到空间、当前视图查找）目前只做设置，界面上还没有对应的快捷键处理。

const SEND_OPTIONS: Array<{
  value: SendShortcut;
  title: MessageKey;
  help: MessageKey;
}> = [
  {
    value: "enter",
    title: "me.shortcuts.send.enter",
    help: "me.shortcuts.send.enterHelp",
  },
  {
    value: "shift_enter",
    title: "me.shortcuts.send.shiftEnter",
    help: "me.shortcuts.send.shiftEnterHelp",
  },
];

const COMMAND_TEXT: Record<CommandId, { title: MessageKey; help: MessageKey }> =
  {
    command_menu: {
      title: "me.shortcuts.commands.commandMenu",
      help: "me.shortcuts.commands.commandMenuHelp",
    },
    move_to_space: {
      title: "me.shortcuts.commands.moveToSpace",
      help: "me.shortcuts.commands.moveToSpaceHelp",
    },
    find_in_view: {
      title: "me.shortcuts.commands.findInView",
      help: "me.shortcuts.commands.findInViewHelp",
    },
  };

export function ShortcutsPage() {
  const prefs = useMyPreferences();
  if (!prefs.data) return null;
  return <ShortcutsBody prefs={prefs.data} />;
}

function ShortcutsBody({ prefs }: { prefs: UserPreferences }) {
  const t = useT();
  const toast = useToast();
  const update = useUpdatePreferences();
  const saving = usePreferencesSaving();
  const mac = isMacPlatform();
  const [recording, setRecording] = useState<CommandId | null>(null);
  const custom = prefs.command_shortcuts;

  const saveOptions = {
    onSuccess: () => toast(t("me.shortcuts.saved")),
    onError: () => toast(t("me.shortcuts.saveFailed"), { tone: "error" }),
  };

  // 录制期间拦截所有按键：单独的修饰键继续等待，其余按键按规则校验后保存或提示
  useEffect(() => {
    if (!recording) return;
    const onKeyDown = (event: KeyboardEvent) => {
      event.preventDefault();
      event.stopPropagation();
      const result = recordShortcut(event);
      if (result === null) return;
      if (!result.ok) {
        toast(
          t(
            result.reason === "needModifier"
              ? "me.shortcuts.commands.needModifier"
              : "me.shortcuts.commands.unsupported",
          ),
          { tone: "error" },
        );
        return;
      }
      const clash = COMMAND_IDS.find(
        (other) =>
          other !== recording &&
          effectiveShortcut(custom, other) === result.value,
      );
      if (clash) {
        toast(
          t("me.shortcuts.commands.conflict", {
            name: t(COMMAND_TEXT[clash].title),
          }),
          { tone: "error" },
        );
        return;
      }
      setRecording(null);
      update.mutate(
        { command_shortcuts: withShortcut(custom, recording, result.value) },
        {
          onSuccess: () => toast(t("me.shortcuts.saved")),
          onError: () => toast(t("me.shortcuts.saveFailed"), { tone: "error" }),
        },
      );
    };
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, [recording, custom, t, toast, update.mutate]);

  return (
    <SettingsColumn>
      <p className="px-1 text-body-sm text-fg-muted">
        {t("me.shortcuts.description")}
      </p>
      <SettingsSection
        title={t("me.shortcuts.send.title")}
        description={t("me.shortcuts.send.description")}
      >
        <div
          role="radiogroup"
          aria-label={t("me.shortcuts.send.title")}
          aria-busy={saving}
          className={cn(
            "flex flex-col gap-2",
            saving && "pointer-events-none opacity-70",
          )}
        >
          {SEND_OPTIONS.map((option) => {
            const selected = prefs.send_shortcut === option.value;
            return (
              <button
                key={option.value}
                type="button"
                role="radio"
                aria-checked={selected}
                disabled={saving}
                onClick={() => {
                  if (!selected)
                    update.mutate({ send_shortcut: option.value }, saveOptions);
                }}
                className={cn(
                  "flex min-h-[68px] w-full items-center gap-3 rounded-xl border p-4 text-left",
                  selected
                    ? "border-primary bg-surface-muted"
                    : "border-line-default bg-surface-card hover:bg-surface-hover",
                )}
              >
                <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <span className="text-body font-medium text-fg-default">
                    {t(option.title)}
                  </span>
                  <span className="text-caption text-fg-muted">
                    {t(option.help)}
                  </span>
                </span>
                {selected && saving && <Spinner size={14} />}
              </button>
            );
          })}
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("me.shortcuts.commands.title")}
        description={t("me.shortcuts.commands.description")}
      >
        <SettingsCard>
          {COMMAND_IDS.map((id) => {
            const isRecording = recording === id;
            const customized = custom[id] !== undefined;
            const lockedByOther = recording !== null && !isRecording;
            return (
              <SettingRow
                key={id}
                title={t(COMMAND_TEXT[id].title)}
                description={t(COMMAND_TEXT[id].help)}
                control={
                  <div className="flex items-center gap-2">
                    <span className="flex items-center gap-1">
                      {shortcutKeycaps(effectiveShortcut(custom, id), mac).map(
                        (cap, index) => (
                          <kbd
                            key={index}
                            className={cn(
                              "inline-flex h-5 min-w-5 items-center justify-center rounded-sm border bg-surface-muted px-1 font-sans text-caption text-fg-muted",
                              isRecording
                                ? "border-line-focus"
                                : "border-line-default",
                            )}
                          >
                            {cap}
                          </kbd>
                        ),
                      )}
                    </span>
                    <Button
                      variant={isRecording ? "primary" : "outline"}
                      size="28"
                      disabled={saving || lockedByOther}
                      onClick={() => setRecording(isRecording ? null : id)}
                    >
                      {isRecording
                        ? t("me.shortcuts.commands.recording")
                        : t("me.shortcuts.commands.record")}
                    </Button>
                    <Button
                      variant="ghost"
                      size="28"
                      disabled={saving || recording !== null || !customized}
                      onClick={() =>
                        update.mutate(
                          { command_shortcuts: withShortcut(custom, id, null) },
                          saveOptions,
                        )
                      }
                    >
                      {t("me.shortcuts.commands.reset")}
                    </Button>
                  </div>
                }
              />
            );
          })}
        </SettingsCard>
      </SettingsSection>
    </SettingsColumn>
  );
}
