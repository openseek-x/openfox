import { useEffect, useState } from "react";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Field, Switch, TextInput } from "@openfox/ui";
import { desktopBridge, type DesktopProxy } from "../desktop";

export function ProxyDialog({ onClose }: { onClose: () => void }) {
  const t = useT();
  const bridge = desktopBridge();
  const [draft, setDraft] = useState<DesktopProxy>({
    mode: "system",
    protocol: "http",
    port: 7890,
  });
  const [busy, setBusy] = useState(true);
  const [notice, setNotice] = useState("");
  useEffect(() => {
    let live = true;
    bridge
      ?.getProxy?.()
      .then((value) => {
        if (live) setDraft((prev) => ({ ...prev, ...value }));
      })
      .catch(() => {
        if (live) setNotice(t("shell.proxyFailure"));
      })
      .finally(() => {
        if (live) setBusy(false);
      });
    return () => {
      live = false;
    };
  }, [bridge, t]);
  const change = (patch: Partial<DesktopProxy>) => {
    setDraft((prev) => ({ ...prev, ...patch }));
    setNotice("");
  };
  const perform = async (save: boolean) => {
    setBusy(true);
    try {
      if (save) {
        await bridge!.saveProxy!(draft);
        onClose();
      } else
        setNotice(
          t(
            (await bridge!.testProxy!(draft))
              ? "shell.proxySuccess"
              : "shell.proxyFailure",
          ),
        );
    } catch {
      setNotice(t(save ? "shell.proxySaveError" : "shell.proxyFailure"));
    } finally {
      setBusy(false);
    }
  };
  const invalid =
    draft.mode === "manual" &&
    (!draft.host?.trim() ||
      !draft.port ||
      draft.port < 1 ||
      draft.port > 65535);
  return (
    <Dialog
      open
      title={t("shell.proxyTitle")}
      description={t("shell.proxyDesc")}
      width={520}
      onClose={busy ? undefined : onClose}
      footer={
        <>
          <Button
            variant="ghost"
            disabled={busy || invalid}
            onClick={() => void perform(false)}
          >
            {t("shell.proxyTest")}
          </Button>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button disabled={busy || invalid} onClick={() => void perform(true)}>
            {t("shell.proxySave")}
          </Button>
        </>
      }
    >
      <fieldset disabled={busy} className="flex flex-col gap-3">
        {(["system", "direct", "manual"] as const).map((mode, i) => (
          <label key={mode} className="flex items-center gap-2 text-body">
            <input
              type="radio"
              name="proxy-mode"
              checked={draft.mode === mode}
              onChange={() => change({ mode })}
            />
            {t(
              (
                [
                  "shell.proxySystem",
                  "shell.proxyDirect",
                  "shell.proxyManual",
                ] as const
              )[i]!,
            )}
          </label>
        ))}
        {draft.mode === "manual" && (
          <>
            <Field label={t("shell.proxyProtocol")}>
              <select
                aria-label={t("shell.proxyProtocol")}
                value={draft.protocol}
                onChange={(e) =>
                  change({
                    protocol: e.target.value as DesktopProxy["protocol"],
                  })
                }
              >
                {["http", "https", "socks5"].map((p) => (
                  <option key={p} value={p}>
                    {p.toUpperCase()}
                  </option>
                ))}
              </select>
            </Field>
            <Field label={t("shell.proxyHost")}>
              <TextInput
                aria-label={t("shell.proxyHost")}
                value={draft.host ?? ""}
                placeholder="proxy.example.com"
                onChange={(e) => change({ host: e.target.value })}
              />
            </Field>
            <Field label={t("shell.proxyPort")}>
              <TextInput
                aria-label={t("shell.proxyPort")}
                type="number"
                min={1}
                max={65535}
                value={draft.port || ""}
                onChange={(e) => change({ port: Number(e.target.value) })}
              />
            </Field>
            <label className="flex items-center justify-between text-body">
              {t("shell.proxyAuth")}
              <Switch
                label={t("shell.proxyAuth")}
                checked={!!draft.authenticated}
                onCheckedChange={(authenticated) => change({ authenticated })}
              />
            </label>
            {draft.authenticated && (
              <>
                <TextInput
                  aria-label={t("shell.proxyUser")}
                  placeholder={t("shell.proxyUser")}
                  value={draft.username ?? ""}
                  onChange={(e) => change({ username: e.target.value })}
                />
                <TextInput
                  aria-label={t("shell.proxyPassword")}
                  placeholder={t("shell.proxyPassword")}
                  type="password"
                  value={draft.password ?? ""}
                  onChange={(e) => change({ password: e.target.value })}
                />
              </>
            )}
            <Field label={t("shell.proxyBypass")}>
              <textarea
                aria-label={t("shell.proxyBypass")}
                className="rounded-lg border border-line-default p-2"
                value={draft.bypass ?? ""}
                onChange={(e) => change({ bypass: e.target.value })}
              />
            </Field>
          </>
        )}
        {notice && (
          <p role="status" className="text-body-sm">
            {notice}
          </p>
        )}
      </fieldset>
    </Dialog>
  );
}
