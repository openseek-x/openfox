import { useT } from "@openfox/i18n";
import { Icon, IconButton } from "@openfox/ui";

import { desktopBridge } from "../desktop";
import { isMacPlatform } from "../me/shortcuts";
import { useWorkspacePanel } from "./artifacts/context";
import { BROWSER_PANEL, isBrowserTab } from "./artifacts/params";
import { useDraftBrowserContexts, useDraftFileContexts } from "./drafts";

/** 会话顶栏右侧的统一工作区入口：macOS 桌面端直达浏览器 + 右侧工作区一键开关（含待发送批注计数）。 */
export function WorkspaceHeaderButtons({
  conversationId,
  workspaceLabel,
}: {
  conversationId?: string | null;
  /** 面板收起时的自定义无障碍标签（如任务侧栏保留「打开产物侧栏」文案）。 */
  workspaceLabel?: string;
}) {
  const t = useT();
  const panel = useWorkspacePanel();
  const fileDrafts = useDraftFileContexts(conversationId ?? "");
  const browserDrafts = useDraftBrowserContexts(conversationId ?? "");
  if (!conversationId || !panel) return null;

  const browserAvailable =
    desktopBridge()?.platform === "darwin" &&
    Boolean(desktopBridge()?.browserMount);
  const browserSelected =
    panel.open &&
    (panel.selected === BROWSER_PANEL || isBrowserTab(panel.selected));
  const pendingCount =
    fileDrafts.reduce((sum, item) => sum + item.comments.length, 0) +
    browserDrafts.reduce((sum, item) => sum + item.comments.length, 0);
  const shortcut = isMacPlatform() ? "⌘⇧\\" : "Ctrl+Shift+\\";
  const openLabel = workspaceLabel ?? t("artifacts.workspace.openWorkspace");
  const showBrowser = browserAvailable && !browserSelected;
  const showOpenWorkspace = !panel.open;
  if (!showBrowser && !showOpenWorkspace) return null;

  return (
    <div className="flex shrink-0 items-center gap-0.5">
      {showBrowser && (
        <IconButton
          label={t("artifacts.browser.label")}
          title={t("artifacts.browser.label")}
          icon={<Icon name="globe" size={16} />}
          size="28"
          variant="ghost"
          onClick={() => panel.select(BROWSER_PANEL)}
        />
      )}
      {showOpenWorkspace && (
        <div className="relative">
          <IconButton
            label={openLabel}
            title={`${openLabel} (${shortcut})`}
            icon={<Icon name="panel-left" size={16} className="rotate-180" />}
            size="28"
            variant="ghost"
            onClick={() => panel.togglePanel()}
          />
          {pendingCount > 0 && (
            <span
              aria-hidden="true"
              className="pointer-events-none absolute -top-0.5 -right-0.5 flex h-3.5 min-w-3.5 items-center justify-center rounded-full bg-accent px-1 text-[10px] font-medium leading-none text-fg-inverse"
            >
              {pendingCount > 99 ? "99+" : pendingCount}
            </span>
          )}
        </div>
      )}
    </div>
  );
}
