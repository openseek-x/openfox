import { isApiError, type Api, type Me } from "@openfox/api";
import { createSessionApi, describeApiError, readCsrf } from "./client";

export type BrowserSession =
  | { kind: "checking" }
  | { kind: "signed-out"; error?: string }
  | { kind: "ready"; api: Api; me: Me; revision: number };

/** 侧栏可见时检查当前 Chrome 配置的工作台会话。多个面板分别跟随同一网页登录。 */
export function watchBrowserSession(
  origin: string,
  publish: (session: BrowserSession) => void,
) {
  let stopped = false;
  let checking = false;
  let timer: ReturnType<typeof setTimeout>;
  let active: { csrf: string; api: Api } | null = null;
  let lastVerified = 0;
  let revision = 0;

  async function check(force = false) {
    if (stopped || document.hidden || checking) return;
    checking = true;
    clearTimeout(timer);
    try {
      const csrf = await readCsrf(origin);
      if (stopped) return;
      if (active && active.csrf !== csrf) {
        active = null;
        publish({ kind: "checking" });
      }
      if (!csrf) {
        publish({ kind: "signed-out" });
        return;
      }
      if (active && !force && Date.now() - lastVerified < 30000) return;
      const previous = active;
      const api: Api =
        active?.api ??
        createSessionApi(origin, csrf, () => {
          if (!stopped && active?.api === api) {
            active = null;
            publish({ kind: "signed-out" });
          }
        });
      const me = await api.me.get();
      if (stopped || previous !== active) return;
      lastVerified = Date.now();
      if (!active) {
        active = { csrf, api };
        publish({ kind: "ready", api, me, revision: ++revision });
      }
    } catch (error) {
      if (stopped) return;
      if (isApiError(error) && error.status === 401) {
        active = null;
        publish({ kind: "signed-out" });
      } else if (!active) {
        publish({ kind: "signed-out", error: describeApiError(error) });
      }
    } finally {
      checking = false;
      if (!stopped && !document.hidden)
        timer = setTimeout(() => void check(), 2000);
    }
  }
  const refresh = () => void check(true);
  document.addEventListener("visibilitychange", refresh);
  window.addEventListener("focus", refresh);
  refresh();
  return {
    refresh,
    stop: () => {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", refresh);
      window.removeEventListener("focus", refresh);
    },
  };
}
