import { useEffect, useMemo, useState } from "react";

import { Callout, Spinner } from "@openfox/ui";

import { createExtensionApi } from "../lib/client";
import {
  clearSettings,
  watchSettings,
  type ConnectionSettings,
} from "../lib/config";
import { ChatView } from "./ChatView";
import { SettingsView } from "./SettingsView";

/** 侧边栏根组件：已有连接配置时显示对话页，否则显示连接设置页。 */
export function App() {
  // undefined 表示还在读取本地存储
  const [settings, setSettings] = useState<
    ConnectionSettings | null | undefined
  >(undefined);

  const [revision, setRevision] = useState(0);
  const [error, setError] = useState("");
  useEffect(
    () =>
      watchSettings((value) => {
        setSettings(value);
        setRevision((current) => current + 1);
        setError("");
      }),
    [],
  );

  const api = useMemo(
    () => (settings ? createExtensionApi(settings) : null),
    [settings],
  );

  async function disconnect() {
    try {
      await clearSettings();
    } catch {
      setError("清除本机连接失败，请再试一次");
    }
  }

  return (
    <div className="flex min-h-screen flex-col bg-surface-canvas font-sans text-body text-fg-default">
      {error && <Callout tone="danger">{error}</Callout>}
      {settings === undefined ? (
        <div className="flex flex-1 items-center justify-center">
          <Spinner />
        </div>
      ) : settings && api ? (
        <ChatView key={revision} api={api} onDisconnect={disconnect} />
      ) : (
        <SettingsView />
      )}
    </div>
  );
}
