import { useState } from "react";
import { useT } from "@openfox/i18n";
import { Button, Icon } from "@openfox/ui";
import type { AgentToolConnection, AgentToolState } from "./AgentToolsEditor";
import type { ToolCatalogEntry } from "./tool-catalog";

const setup = {
  feishu: {
    provider: "feishu",
    docs: "https://github.com/larksuite/cli/blob/main/README.zh.md",
    install: "npx @larksuite/cli@latest install",
    login: "lark-cli config init\nlark-cli auth login --domain calendar",
    verify: "lark-cli calendar +agenda --as user",
  },
  apifox: {
    provider: "apifox",
    docs: "https://docs.apifox.com/install-and-run-cli",
    install: "npm install -g apifox-cli@latest",
    login: "apifox auth login",
    verify: "apifox project list",
  },
  "cnb-api": {
    provider: "cnb",
    docs: "https://docs.cnb.cool/develops/cnb-cli.html",
    install: "npm install -g @cnbcool/cnb-cli",
    login: "cnb login",
    verify: "cnb users get-user-info",
  },
} as const;

export function AgentToolsProviderCLI({
  entry,
  connection,
  state,
  onClose,
  busy,
  setBusy,
}: {
  entry: ToolCatalogEntry;
  connection?: AgentToolConnection;
  state: AgentToolState;
  onClose: () => void;
  busy: boolean;
  setBusy: (v: boolean) => void;
}) {
  const t = useT();
  const [error, setError] = useState(""),
    [consent, setConsent] = useState(false);
  const recipe = Object.hasOwn(setup, entry.id)
    ? setup[entry.id as keyof typeof setup]
    : undefined;
  const allowed =
    state.canManage &&
    state.canUseDevice &&
    state.supportedTransports.includes("stdio");

  async function connect() {
    if (!recipe || !allowed || !consent || busy || connection) return;
    setBusy(true);
    setError("");
    try {
      await state.submit({
        catalogId: entry.id,
        name: entry.id,
        scope: "DEVICE_OWNER",
        transport: "stdio",
        authMode: "none",
        command: "openfox:provider-cli",
        args: [recipe.provider],
      });
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : t("agents.tools.failed"));
    } finally {
      setBusy(false);
    }
  }
  if (!recipe)
    return (
      <p role="status" className="p-6 text-body-sm text-fg-muted">
        {t("agents.tools.cliUnavailable")}
      </p>
    );
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="min-h-0 flex-1 space-y-4 overflow-auto px-6 py-5">
        <p className="text-body text-fg-secondary">
          {t("agents.tools.cliLocal", { name: entry.name })}
        </p>
        <a
          href={recipe.docs}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1.5 text-body-sm text-link"
        >
          {t("agents.tools.officialDocs")}
          <Icon name="external-link" size={14} />
        </a>
        {(["install", "login", "verify"] as const).map((step) => (
          <div key={step} className="space-y-1.5">
            <h3 className="text-body-sm font-medium">
              {t(`agents.tools.cliStep_${step}`)}
            </h3>
            <pre className="select-text overflow-x-auto rounded-md bg-surface-muted px-3 py-2 font-mono text-body-sm">
              <code>{recipe[step]}</code>
            </pre>
          </div>
        ))}
        <p className="text-body-sm text-fg-secondary">
          {t(`agents.tools.cliReads_${recipe.provider}`)}
        </p>
        {!allowed && (
          <p role="status" className="text-body-sm text-fg-muted">
            {t("agents.tools.cliOwner")}
          </p>
        )}
        {allowed && !connection && (
          <label className="flex cursor-pointer items-start gap-2 text-body-sm">
            <input
              type="checkbox"
              className="mt-0.5"
              disabled={busy}
              checked={consent}
              onChange={(e) => setConsent(e.target.checked)}
            />
            {t("agents.tools.cliConsent")}
          </label>
        )}
        {connection && (
          <p role="status" className="text-caption text-fg-muted">
            {t("agents.tools.configured")}
          </p>
        )}
        {error && (
          <p role="alert" className="text-body-sm text-danger">
            {error}
          </p>
        )}
      </div>
      <div className="flex justify-end gap-2 border-t border-line-default px-6 py-3">
        <Button variant="outline" disabled={busy} onClick={onClose}>
          {t(connection ? "common.close" : "common.cancel")}
        </Button>
        {!connection && (
          <Button
            disabled={!allowed || !consent || busy}
            loading={busy}
            onClick={() => void connect()}
          >
            {t("agents.tools.connect")}
          </Button>
        )}
      </div>
    </div>
  );
}
