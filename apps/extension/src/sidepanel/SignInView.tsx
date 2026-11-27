import { useState } from "react";
import { Callout, Icon } from "@openfox/ui";

export function SignInView({
  origin,
  error,
  retry,
}: {
  origin: string;
  error?: string;
  retry: () => void;
}) {
  const [openFailed, setOpenFailed] = useState(false);
  async function signIn() {
    try {
      await chrome.tabs.create({ url: origin + "/login" });
      setOpenFailed(false);
    } catch {
      setOpenFailed(true);
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
        <h1>在浏览器中使用 OpenFox</h1>
        <p className="connect-intro">
          在当前 Chrome 登录工作台后，侧栏会自动连接你的账号。
        </p>
        {error && (
          <div role="alert">
            <Callout tone="warning">{error}</Callout>
          </div>
        )}
        {openFailed && <div role="alert">无法打开登录页，请重试。</div>}
        <button
          className="accent-button connect-submit"
          type="button"
          onClick={() => void signIn()}
        >
          登录 OpenFox
          <Icon name="arrow-right" />
        </button>
        {error && (
          <button className="retry-session" type="button" onClick={retry}>
            重新检查登录状态
          </button>
        )}
        <aside className="privacy-note">
          <Icon name="shield" size={18} />
          <div>
            <strong>网页引用权限</strong>
            <p>
              勾选「引用当前页面」并发送后，Agent
              才会收到页面标题、地址和选中文字。
            </p>
          </div>
        </aside>
      </div>
    </main>
  );
}
