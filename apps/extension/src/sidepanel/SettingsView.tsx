import { useState } from "react";

import { Callout, Icon } from "@openfox/ui";

import { createExtensionApi, describeApiError } from "../lib/client";
import {
  originPermissionPattern,
  parseServerUrl,
  saveSettings,
} from "../lib/config";

const TOKEN_PREFIX = "ofx_pat_";

/** 连接设置：服务器地址与个人访问令牌，用当前用户接口校验后才保存。 */
export function SettingsView() {
  const [server, setServer] = useState("");
  const [token, setToken] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function connect() {
    setError(null);
    const parsed = parseServerUrl(server);
    if (!parsed.ok) {
      setError(parsed.reason);
      return;
    }
    const secret = token.trim();
    if (!secret.startsWith(TOKEN_PREFIX)) {
      setError(`请粘贴以 ${TOKEN_PREFIX} 开头的个人访问令牌`);
      return;
    }
    setBusy(true);
    try {
      // 权限申请需要用户手势：它必须是点击事件里第一个发出的异步调用。
      const granted = await chrome.permissions.request({
        origins: [originPermissionPattern(parsed.origin)],
      });
      if (!granted) {
        setError("需要授予访问该服务器的权限才能连接");
        return;
      }
      const settings = { serverOrigin: parsed.origin, token: secret };
      await createExtensionApi(settings).me.get();
      await saveSettings(settings);
    } catch (caught) {
      setError(describeApiError(caught));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="connect-page">
      <header className="brandbar">
        <img src="./openfox.png" alt="" width="32" height="32" />
        <span>OpenFox</span>
      </header>
      <div className="connect-content">
        <div className="connect-art" aria-hidden="true">
          <div className="art-window">
            <div className="art-toolbar">
              <i />
              <i />
              <i />
            </div>
            <div className="art-document">
              <i />
              <i />
              <i />
              <i />
            </div>
            <div className="art-panel">
              <img src="./openfox.png" alt="" width="22" height="22" />
              <i />
              <i />
            </div>
          </div>
          <span className="art-quote">
            <Icon name="quote" size={18} />
          </span>
        </div>
        <h1>连接 OpenFox</h1>
        <p className="connect-intro">
          连接工作台，在浏览网页时与空间里的 Agent 对话。
        </p>
        <form
          className="connect-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!busy) void connect();
          }}
        >
          <div>
            <label htmlFor="server">服务器地址</label>
            <div className="connect-input">
              <Icon name="globe" />
              <input
                id="server"
                value={server}
                onChange={(event) => setServer(event.target.value)}
                placeholder="https://openfox.example.com"
                autoComplete="off"
                spellCheck={false}
                disabled={busy}
              />
            </div>
          </div>
          <div>
            <label htmlFor="token">个人访问令牌</label>
            <div className="connect-input">
              <Icon name="key-round" />
              <input
                id="token"
                type="password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                placeholder={TOKEN_PREFIX + "…"}
                autoComplete="off"
                spellCheck={false}
                disabled={busy}
                aria-describedby="token-help"
              />
            </div>
            <p id="token-help">
              在工作台的「个人访问令牌」设置中生成。令牌仅保存在此浏览器。
            </p>
          </div>
          {error && (
            <div role="alert">
              <Callout tone="danger">{error}</Callout>
            </div>
          )}
          <button
            type="submit"
            className="accent-button connect-submit"
            disabled={busy}
          >
            {busy ? "正在连接…" : "连接"}
            <Icon name="arrow-right" />
          </button>
        </form>
        <aside className="privacy-note">
          <Icon name="shield" size={18} />
          <div>
            <strong>网页引用权限</strong>
            <p>
              连接时会申请访问工作台。勾选「引用当前页面」并发送后，Agent
              才会收到页面标题、地址和选中文字。
            </p>
          </div>
        </aside>
      </div>
    </main>
  );
}
