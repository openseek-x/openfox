import { useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { UserPreferences } from "@openfox/api";
import { useI18n, useT, type Locale } from "@openfox/i18n";
import {
  Button,
  Icon,
  Select,
  Switch,
  Tooltip,
  cn,
  useToast,
} from "@openfox/ui";

import { desktopBridge, type DesktopPrefs } from "../desktop";
import {
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "../spaces/settings/parts";
import {
  OUTLINE_COLORS,
  useResolvedTheme,
  type ResolvedTheme,
  type ThemeChoice,
} from "./theme";
import {
  usePreferencesSaving,
  useMyPreferences,
  useUpdatePreferences,
} from "./queries";

// 阶段 4b · 偏好设置（Figma 页 19 第 2 节）：主题、常规（语言）、账号密度、任务会话。

type ThemeTile = {
  value: ThemeChoice;
  label: "system" | "light" | "dark" | "magicBlue" | "classicDark";
};

const THEME_TILES: ThemeTile[] = [
  { value: "system", label: "system" },
  { value: "light", label: "light" },
  { value: "dark", label: "dark" },
  { value: "magic-blue", label: "magicBlue" },
  { value: "classic-dark", label: "classicDark" },
];

export function PreferencesPage() {
  const prefs = useMyPreferences();
  if (!prefs.data) return null;
  return <PreferencesBody prefs={prefs.data} />;
}

function PreferencesBody({ prefs }: { prefs: UserPreferences }) {
  const t = useT();
  const toast = useToast();
  const { locale, setLocale } = useI18n();
  const update = useUpdatePreferences();
  const saving = usePreferencesSaving();
  const resolved = useResolvedTheme(prefs.theme);
  const failed = () => toast(t("me.preferences.saveFailed"), { tone: "error" });
  // 语言切换后错误提示要用新语言显示，回调里读最新的 t
  const latestT = useRef(t);
  latestT.current = t;

  const setTheme = (value: ThemeChoice) => {
    if (value !== prefs.theme)
      update.mutate({ theme: value }, { onError: failed });
  };
  const setOutline = (value: number) => {
    if (value !== prefs.outline_color)
      update.mutate({ outline_color: value }, { onError: failed });
  };
  // 先在本机切换，再同步到账号；同步失败只提示，本机语言保持不变
  const setAccountLocale = (value: Locale) => {
    if (value === locale) return;
    setLocale(value);
    update.mutate(
      { locale: value },
      {
        onError: () =>
          toast(latestT.current("me.preferences.locale.syncFailed"), {
            tone: "error",
          }),
      },
    );
  };
  // 开关乐观更新：失败时 hook 回滚到原来的值
  const setSession = (patch: {
    open_session_on_assign?: boolean;
    open_session_on_quick_command?: boolean;
  }) =>
    update.mutate(patch, {
      onError: () =>
        toast(t("me.preferences.sessions.failed"), { tone: "error" }),
    });

  return (
    <SettingsColumn>
      <SettingsSection title={t("me.preferences.sections.theme")}>
        <div
          role="radiogroup"
          aria-label={t("me.preferences.sections.theme")}
          className="grid grid-cols-5 gap-3"
        >
          {THEME_TILES.map((tile) => (
            <ThemeTileButton
              key={tile.label}
              tile={tile}
              selected={tile.value === prefs.theme}
              disabled={saving}
              onSelect={() => setTheme(tile.value)}
            />
          ))}
        </div>
        <SettingsCard className="@container">
          <SettingRow
            title={t("me.preferences.outline.title")}
            control={
              <div
                role="radiogroup"
                aria-label={t("me.preferences.outline.title")}
                className="flex max-w-[65cqw] flex-wrap justify-end gap-1.5"
              >
                {Array.from({ length: OUTLINE_COLORS.length + 1 }, (_, n) => (
                  <OutlineSwatch
                    key={n}
                    n={n}
                    selected={prefs.outline_color === n}
                    disabled={saving}
                    theme={resolved}
                    onSelect={() => setOutline(n)}
                  />
                ))}
              </div>
            }
          />
        </SettingsCard>
        <div>
          <Button
            variant="outline"
            size="28"
            disabled={
              saving || (prefs.theme === "system" && prefs.outline_color === 0)
            }
            onClick={() =>
              update.mutate(
                { theme: "system", outline_color: 0 },
                { onError: failed },
              )
            }
          >
            {t("me.preferences.theme.reset")}
          </Button>
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("me.preferences.sections.general")}
        description={t("me.preferences.locale.description")}
      >
        <SettingsCard>
          <SettingRow
            title={t("me.preferences.locale.title")}
            control={
              <Select<Locale>
                aria-label={t("me.preferences.locale.title")}
                value={prefs.locale ?? locale}
                disabled={saving}
                className="w-[180px]"
                options={[
                  { value: "zh-CN", label: t("me.preferences.locale.zh") },
                  { value: "en-US", label: t("me.preferences.locale.en") },
                ]}
                onChange={setAccountLocale}
              />
            }
          />
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t("me.preferences.sections.density")}
        description={t("me.preferences.density.description")}
      >
        <SettingsCard>
          <SettingRow
            title={t("me.preferences.sections.density")}
            control={
              <Select<UserPreferences["density"]>
                aria-label={t("me.preferences.sections.density")}
                value={prefs.density ?? "comfortable"}
                disabled={saving}
                className="w-[180px]"
                options={[
                  {
                    value: "comfortable",
                    label: t("me.preferences.density.comfortable"),
                  },
                  {
                    value: "compact",
                    label: t("me.preferences.density.compact"),
                  },
                ]}
                onChange={(density) => {
                  if (density !== (prefs.density ?? "comfortable"))
                    update.mutate({ density }, { onError: failed });
                }}
              />
            }
          />
        </SettingsCard>
      </SettingsSection>

      <DesktopPreferences />

      <SettingsSection title={t("me.preferences.sections.taskSessions")}>
        <SettingsCard>
          <SettingRow
            title={t("me.preferences.sessions.assign")}
            description={t("me.preferences.sessions.assignHelp")}
            control={
              <Switch
                tone="success"
                label={t("me.preferences.sessions.assign")}
                checked={prefs.open_session_on_assign}
                disabled={saving}
                onCheckedChange={(next) =>
                  setSession({ open_session_on_assign: next })
                }
              />
            }
          />
          <SettingRow
            title={t("me.preferences.sessions.quick")}
            description={t("me.preferences.sessions.quickHelp")}
            control={
              <Switch
                tone="success"
                label={t("me.preferences.sessions.quick")}
                checked={prefs.open_session_on_quick_command}
                disabled={saving}
                onCheckedChange={(next) =>
                  setSession({ open_session_on_quick_command: next })
                }
              />
            }
          />
        </SettingsCard>
      </SettingsSection>
    </SettingsColumn>
  );
}

function ThemeTileButton({
  tile,
  selected,
  disabled,
  onSelect,
}: {
  tile: ThemeTile;
  selected: boolean;
  disabled: boolean;
  onSelect: () => void;
}) {
  const t = useT();
  const label = t(`me.preferences.theme.${tile.label}`);
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      aria-label={label}
      disabled={disabled}
      onClick={onSelect}
      className="flex flex-col gap-2 text-left disabled:cursor-not-allowed disabled:opacity-60"
    >
      <span
        className={cn(
          "block aspect-[127.8/88] w-full overflow-hidden rounded-lg border",
          selected ? "border-2 border-accent" : "border-line-default",
        )}
      >
        <ThemePreview tile={tile} />
      </span>
      <span className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-body-sm text-fg-default">
        <span className="whitespace-nowrap">{label}</span>
      </span>
    </button>
  );
}

const PREVIEW: Record<Exclude<ThemeChoice, "system">, Palette> = {
  light: { app: "#f0f0f2", card: "#ffffff", bar: "#d4d4d8" },
  dark: { app: "#1f1f23", card: "#27272a", bar: "#52525b" },
  "magic-blue": { app: "#1a1d36", card: "#222542", bar: "#404665" },
  "classic-dark": { app: "#101010", card: "#161616", bar: "#303030" },
};

interface Palette {
  app: string;
  card: string;
  bar: string;
}

/** 主题缩略图：侧栏 + 卡片 + 文字条；「跟随系统」左浅右深。 */
function ThemePreview({ tile }: { tile: ThemeTile }) {
  if (tile.value === "system")
    return (
      <span className="grid size-full grid-cols-2">
        <Mock palette={PREVIEW.light} />
        <Mock palette={PREVIEW.dark} />
      </span>
    );
  return <Mock palette={PREVIEW[tile.value]} />;
}

function Mock({ palette }: { palette: Palette }) {
  return (
    <span
      className="flex size-full gap-1 p-1.5"
      style={{ background: palette.app }}
    >
      <span className="w-1/4 rounded-sm" style={{ background: palette.card }} />
      <span className="flex flex-1 flex-col gap-1">
        <span className="h-3 rounded-sm" style={{ background: palette.card }} />
        <span
          className="h-2 w-2/3 rounded-sm"
          style={{ background: palette.bar }}
        />
      </span>
    </span>
  );
}

/** 外边框色圆点：0 是「不设置」，1–12 是内置色；选中为 accent 外环。 */
function OutlineSwatch({
  n,
  selected,
  disabled,
  theme,
  onSelect,
}: {
  n: number;
  selected: boolean;
  disabled: boolean;
  theme: ResolvedTheme;
  onSelect: () => void;
}) {
  const t = useT();
  const label =
    n === 0
      ? t("me.preferences.outline.none")
      : t("me.preferences.outline.color", { n });
  const color = OUTLINE_COLORS[n - 1];
  return (
    <Tooltip content={label}>
      <button
        type="button"
        role="radio"
        aria-checked={selected}
        aria-label={label}
        disabled={disabled}
        onClick={onSelect}
        style={color ? { background: color[theme] } : undefined}
        className={cn(
          "flex size-7 shrink-0 items-center justify-center rounded-full border border-line-default bg-surface-card hover:ring-2 hover:ring-fg-default/20 disabled:cursor-not-allowed disabled:opacity-60",
          selected && "ring-2 ring-accent",
        )}
      >
        {n === 0 && (
          <Icon name="circle-slash" size={14} className="text-fg-muted" />
        )}
      </button>
    </Tooltip>
  );
}

const desktopPrefsKey = ["desktop", "prefs"] as const;

/**
 * 阶段 12h · 桌面应用偏好（仅桌面端且主进程提供对应接口时显示）。
 * 这两项只属于这台电脑，存在本机，不随账号同步；开机自启以系统实际值为准，被拒绝时开关回弹。
 */
function DesktopPreferences() {
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const bridge = desktopBridge();
  const enabled = Boolean(bridge?.getDesktopPrefs);
  const prefs = useQuery({
    queryKey: desktopPrefsKey,
    queryFn: () => bridge!.getDesktopPrefs!(),
    enabled,
  });
  const setPrefs = (next: DesktopPrefs) =>
    queryClient.setQueryData(desktopPrefsKey, next);
  const tray = useMutation({
    mutationFn: (value: boolean) => bridge!.setKeepInTray!(value),
    onSuccess: setPrefs,
    onError: () =>
      toast(t("me.preferences.desktop.keepInTrayFailed"), { tone: "error" }),
  });
  const login = useMutation({
    mutationFn: (value: boolean) => bridge!.setLoginItem!(value),
    onSuccess: (result) => {
      queryClient.setQueryData<DesktopPrefs>(desktopPrefsKey, (old) =>
        old ? { ...old, login_item: result.state } : old,
      );
      if (!result.ok)
        toast(
          t(
            result.state.reason === "requires_approval"
              ? "me.preferences.desktop.loginItemApproval"
              : "me.preferences.desktop.loginItemFailed",
          ),
          { tone: "error" },
        );
    },
    onError: () =>
      toast(t("me.preferences.desktop.loginItemFailed"), { tone: "error" }),
  });
  if (!enabled || !prefs.data) return null;
  const showTray = prefs.data.tray_supported && Boolean(bridge?.setKeepInTray);
  const showLogin =
    prefs.data.login_item.supported && Boolean(bridge?.setLoginItem);
  if (!showTray && !showLogin) return null;
  return (
    <SettingsSection
      title={t("me.preferences.desktop.title")}
      description={t("me.preferences.desktop.description")}
    >
      <SettingsCard>
        {showTray && (
          <SettingRow
            title={t("me.preferences.desktop.keepInTray")}
            description={t("me.preferences.desktop.keepInTrayHelp")}
            control={
              <Switch
                tone="success"
                label={t("me.preferences.desktop.keepInTray")}
                checked={
                  tray.isPending ? tray.variables : prefs.data.keep_in_tray
                }
                disabled={tray.isPending}
                onCheckedChange={(next) => tray.mutate(next)}
              />
            }
          />
        )}
        {showLogin && (
          <SettingRow
            title={t("me.preferences.desktop.loginItem")}
            description={t("me.preferences.desktop.loginItemHelp")}
            control={
              <Switch
                tone="success"
                label={t("me.preferences.desktop.loginItem")}
                checked={
                  login.isPending
                    ? login.variables
                    : prefs.data.login_item.enabled
                }
                disabled={login.isPending}
                onCheckedChange={(next) => login.mutate(next)}
              />
            }
          />
        )}
      </SettingsCard>
    </SettingsSection>
  );
}
