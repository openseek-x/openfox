import { useEffect, useRef, useState } from "react";
import { Spinner } from "@openfox/ui";
import { watchBrowserSession, type BrowserSession } from "../lib/session";
import { ChatView } from "./ChatView";
import { SignInView } from "./SignInView";

export function App() {
  const origin = new URL(chrome.runtime.getManifest().homepage_url).origin;
  const [session, setSession] = useState<BrowserSession>({ kind: "checking" });
  const watcher = useRef<ReturnType<typeof watchBrowserSession> | null>(null);
  useEffect(() => {
    watcher.current = watchBrowserSession(origin, setSession);
    return () => {
      watcher.current?.stop();
    };
  }, [origin]);
  return (
    <div className="flex min-h-screen flex-col bg-surface-canvas font-sans text-body text-fg-default">
      {session.kind === "checking" ? (
        <div
          role="status"
          aria-label="正在检查登录状态"
          className="flex flex-1 items-center justify-center"
        >
          <Spinner />
        </div>
      ) : session.kind === "ready" ? (
        <ChatView key={session.revision} api={session.api} me={session.me} />
      ) : (
        <SignInView
          origin={origin}
          error={session.error}
          retry={() => watcher.current?.refresh()}
        />
      )}
    </div>
  );
}
