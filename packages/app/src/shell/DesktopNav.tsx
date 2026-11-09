import { useEffect, useState } from "react";

import { useT } from "@openfox/i18n";
import { Icon, cn, type IconName } from "@openfox/ui";

// 桌面端标题栏的后退 / 前进 / 刷新：按钮 26×26、圆角 6、图标 16。
// 浏览器自带这三个按钮，所以只在桌面端显示。可否后退 / 前进读 Navigation API，不支持时一直可点。

interface NavigationLike extends EventTarget {
  canGoBack: boolean;
  canGoForward: boolean;
}

function navigationApi(): NavigationLike | undefined {
  return (globalThis as { navigation?: NavigationLike }).navigation;
}

function useHistoryState() {
  const read = () => {
    const nav = navigationApi();
    return {
      back: nav ? nav.canGoBack : true,
      forward: nav ? nav.canGoForward : true,
    };
  };
  const [state, setState] = useState(read);
  useEffect(() => {
    const nav = navigationApi();
    if (!nav) return;
    const update = () => setState(read());
    nav.addEventListener("currententrychange", update);
    return () => nav.removeEventListener("currententrychange", update);
  }, []);
  return state;
}

function NavButton({
  icon,
  label,
  disabled,
  onClick,
}: {
  icon: IconName;
  label: string;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "flex size-[26px] items-center justify-center rounded-md text-fg-secondary hover:bg-surface-muted",
        "disabled:cursor-default disabled:text-fg-subtle disabled:hover:bg-transparent",
      )}
    >
      <Icon name={icon} size={16} />
    </button>
  );
}

export function DesktopNav() {
  const t = useT();
  const history = useHistoryState();
  return (
    <div className="flex items-center gap-1">
      <NavButton
        icon="arrow-left"
        label={t("shell.desktop.back")}
        disabled={!history.back}
        onClick={() => window.history.back()}
      />
      <NavButton
        icon="arrow-right"
        label={t("shell.desktop.forward")}
        disabled={!history.forward}
        onClick={() => window.history.forward()}
      />
      <NavButton
        icon="refresh"
        label={t("shell.desktop.reload")}
        onClick={() => window.location.reload()}
      />
    </div>
  );
}
