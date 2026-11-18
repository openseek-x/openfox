import { useState } from "react";
import type { DevicePreferences } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Checkbox,
  Dialog,
  Icon,
  IconButton,
  TextInput,
  useToast,
} from "@openfox/ui";
import { commandRequestError, commandSettledError } from "./ComputerStorage";
import { useDeviceCommand } from "./commands";

type Section = "cli" | "args" | "protection";
export function DeviceSettingsEditor({
  orgId,
  deviceId,
  disabled,
  section,
}: {
  orgId: string;
  deviceId: string;
  disabled: boolean;
  section: Section;
}) {
  const t = useT(),
    toast = useToast();
  const [open, setOpen] = useState(false),
    [original, setOriginal] = useState<DevicePreferences | null>(null),
    [draft, setDraft] = useState<DevicePreferences | null>(null),
    [version, setVersion] = useState(0),
    [error, setError] = useState("");
  const command = useDeviceCommand(orgId, deviceId, "device_settings", {
    onError: (e) => setError(commandRequestError(e, t)),
    onSettled: (c) => {
      const failed = commandSettledError(c, t);
      if (failed) {
        setError(failed);
        return;
      }
      const r = c.result;
      if (
        !r ||
        typeof r.version !== "number" ||
        !r.settings ||
        typeof r.settings !== "object"
      ) {
        setError(t("connect.remote.requestFailed"));
        return;
      }
      if (c.params.settings) {
        setOpen(false);
        toast(t("connect.devicePolicy.saved"));
        return;
      }
      const settings = r.settings as DevicePreferences;
      setVersion(r.version);
      setOriginal(settings);
      setDraft(settings);
    },
  });
  const label = t(`connect.devicePolicy.${section}`);
  function change(patch: Partial<DevicePreferences>) {
    if (draft) setDraft({ ...draft, ...patch });
    setError("");
  }
  const rows = draft?.codex_args?.length ? draft.codex_args : [""];
  function editRow(index: number, text: string) {
    change({
      codex_args: [
        ...rows.slice(0, index),
        ...text.split(/\r?\n/),
        ...rows.slice(index + 1),
      ],
    });
  }
  const payload = draft && {
    ...draft,
    codex_args: (draft.codex_args ?? []).map((v) => v.trim()).filter(Boolean),
  };
  return (
    <>
      <Button
        size="28"
        variant="outline"
        disabled={disabled || command.running}
        onClick={() => {
          setOpen(true);
          setError("");
          setOriginal(null);
          setDraft(null);
          command.start({});
        }}
      >
        {label}
      </Button>
      {open && (
        <Dialog
          open
          title={label}
          onClose={() => setOpen(false)}
          footer={
            <>
              <Button variant="outline" onClick={() => setOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button
                loading={command.running}
                disabled={
                  !draft ||
                  JSON.stringify(payload) === JSON.stringify(original) ||
                  (payload?.codex_args.length ?? 0) > 16 ||
                  !draft.min_memory_mib ||
                  !validSessionDuration(draft.session_idle_minutes, 1440) ||
                  !validSessionDuration(draft.session_max_minutes, 10080)
                }
                onClick={() => {
                  if (payload) command.start({ version, settings: payload });
                }}
              >
                {t(
                  section === "cli"
                    ? "connect.devicePolicy.verify"
                    : "common.save",
                )}
              </Button>
            </>
          }
        >
          <div className="grid gap-4 text-body">
            {!draft && !error && <span>{t("common.loading")}</span>}
            {draft && section === "cli" && (
              <>
                <p>{t("connect.devicePolicy.findCli")}</p>
                <div className="flex items-center justify-between rounded-md bg-surface-muted px-3 py-2">
                  <code>command -v codex</code>
                  <Button
                    size="28"
                    variant="ghost"
                    onClick={() =>
                      void navigator.clipboard
                        .writeText("command -v codex")
                        .catch(() =>
                          setError(t("connect.remote.requestFailed")),
                        )
                    }
                  >
                    {t("common.copy")}
                  </Button>
                </div>
                <label className="grid gap-1">
                  {t("connect.devicePolicy.cli")}
                  <TextInput
                    value={draft.codex_path}
                    placeholder="~/.local/bin/codex"
                    onChange={(e) => change({ codex_path: e.target.value })}
                  />
                </label>
                <Button
                  variant="ghost"
                  size="28"
                  disabled={!draft.codex_path}
                  onClick={() => change({ codex_path: "" })}
                >
                  {t("connect.devicePolicy.restore")}
                </Button>
              </>
            )}
            {draft && section === "args" && (
              <>
                <p>{t("connect.devicePolicy.argsHint")}</p>
                {rows.map((v, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <textarea
                      className="min-w-0 flex-1 rounded-md border border-line-default bg-surface-card p-2 font-mono"
                      rows={1}
                      aria-label={i === 0 ? label : label + " " + (i + 1)}
                      placeholder="-c model_reasoning_effort=high"
                      value={v}
                      onChange={(e) => editRow(i, e.target.value)}
                    />
                    <IconButton
                      size="28"
                      label={t("agents.tools.removeArg") + " " + (i + 1)}
                      icon={<Icon name="trash-2" />}
                      onClick={() =>
                        change({
                          codex_args: rows.filter((_, idx) => idx !== i),
                        })
                      }
                    />
                  </div>
                ))}
                <Button
                  size="28"
                  variant="outline"
                  disabled={rows.length >= 16}
                  onClick={() => change({ codex_args: [...rows, ""] })}
                >
                  {t("agents.tools.addArg")}
                </Button>
              </>
            )}
            {draft && section === "protection" && (
              <>
                {(["session_idle_minutes", "session_max_minutes"] as const).map(
                  (key) => (
                    <label key={key} className="grid gap-1">
                      {t(`connect.devicePolicy.${key}`)}
                      <TextInput
                        type="number"
                        min={1}
                        max={key === "session_idle_minutes" ? 1440 : 10080}
                        value={
                          draft[key] ??
                          (key === "session_idle_minutes" ? 10 : 120)
                        }
                        onChange={(e) =>
                          change({ [key]: e.target.valueAsNumber })
                        }
                      />
                    </label>
                  ),
                )}
                <label className="flex items-center gap-2">
                  <Checkbox
                    checked={draft.memory_guard}
                    onChange={(e) => change({ memory_guard: e.target.checked })}
                  />
                  {t("connect.devicePolicy.memory")}
                </label>
                <label className="grid gap-1">
                  {t("connect.devicePolicy.minimum")}
                  <TextInput
                    type="number"
                    min={1}
                    max={1048576}
                    value={draft.min_memory_mib || ""}
                    disabled={!draft.memory_guard}
                    onChange={(e) =>
                      change({ min_memory_mib: Number(e.target.value) })
                    }
                  />
                </label>
                <label className="flex items-center gap-2">
                  <Checkbox
                    checked={draft.load_guard}
                    onChange={(e) => change({ load_guard: e.target.checked })}
                  />
                  {t("connect.devicePolicy.load")}
                </label>
                <label className="grid gap-1">
                  {t("connect.devicePolicy.policy")}
                  <select
                    className="rounded-md border border-line-default bg-surface-card p-2"
                    disabled={!draft.load_guard}
                    value={draft.load_policy}
                    onChange={(e) =>
                      change({
                        load_policy: e.target
                          .value as DevicePreferences["load_policy"],
                      })
                    }
                  >
                    {(["conservative", "balanced", "performance"] as const).map(
                      (k) => (
                        <option key={k} value={k}>
                          {t(`connect.devicePolicy.${k}`)}
                        </option>
                      ),
                    )}
                  </select>
                </label>
                <p className="text-caption text-fg-muted">
                  {t("connect.devicePolicy.guardHint")}
                </p>
              </>
            )}
            {error && <Callout tone="warning">{error}</Callout>}
          </div>
        </Dialog>
      )}
    </>
  );
}

function validSessionDuration(n: number | undefined, max: number) {
  return n === undefined || (Number.isInteger(n) && n >= 1 && n <= max);
}
