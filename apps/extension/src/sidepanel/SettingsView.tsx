import { useState } from "react";

import { Button, Callout, Field, TextInput } from "@openfox/ui";

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
    <div className="flex flex-1 flex-col gap-4 p-4">
      <div className="flex flex-col gap-1">
        <h1 className="flex items-center gap-2 text-lg font-semibold">
          <img src="./openfox.png" alt="" width="28" height="28" />
          连接 OpenFox
        </h1>
        <p className="text-fg-muted">
          使用 OpenFox 账号与空间里的 Agent 对话。
        </p>
      </div>
      <Field
        label="服务器地址"
        helper="填写工作台网址，例如 https://openfox.example.com"
      >
        <TextInput
          variant="form"
          value={server}
          onChange={(event) => setServer(event.target.value)}
          placeholder="https://"
          autoComplete="off"
          spellCheck={false}
          disabled={busy}
        />
      </Field>
      <Field
        label="个人访问令牌"
        helper="在 OpenFox 网页版的个人访问令牌设置里生成，只保存在这个浏览器里"
      >
        <TextInput
          variant="form"
          type="password"
          value={token}
          onChange={(event) => setToken(event.target.value)}
          placeholder={`${TOKEN_PREFIX}…`}
          autoComplete="off"
          spellCheck={false}
          disabled={busy}
        />
      </Field>
      {error && <Callout tone="danger">{error}</Callout>}
      <Button loading={busy} onClick={() => void connect()}>
        连接
      </Button>
      <Callout tone="info" title="权限说明">
        连接时会申请访问该服务器。勾选“引用当前页面”并发送后，Agent
        会收到页面标题、地址和选中的文字。
      </Callout>
    </div>
  );
}
