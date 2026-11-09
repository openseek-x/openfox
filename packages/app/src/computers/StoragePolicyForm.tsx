import { useEffect, useRef, useState } from "react";
import type { DeviceStoragePolicy } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Checkbox, TextInput, useToast } from "@openfox/ui";
import { useDeviceCommand } from "./commands";
import { commandRequestError, commandSettledError } from "./ComputerStorage";

const limits = {
  interval_hours: [1, 720],
  retain_days: [1, 3650],
  repo_days: [30, 3650],
  min_free_gib: [0, 1048576],
  max_gib: [0, 1048576],
} as const;
const fields = Object.keys(limits) as (keyof typeof limits)[];
export function StoragePolicyForm({
  orgId,
  deviceId,
}: {
  orgId: string;
  deviceId: string;
}) {
  const t = useT(),
    toast = useToast();
  const [saved, setSaved] = useState<DeviceStoragePolicy | null>(null),
    [form, setForm] = useState<DeviceStoragePolicy | null>(null),
    [version, setVersion] = useState(0),
    [error, setError] = useState("");
  const cmd = useDeviceCommand(orgId, deviceId, "device_settings", {
    onError: (e) => setError(commandRequestError(e, t)),
    onSettled: (c) => {
      const failure = commandSettledError(c, t);
      const r = c.result;
      if (
        failure ||
        !r ||
        typeof r.version !== "number" ||
        typeof r.policy !== "object" ||
        !r.policy
      ) {
        setError(failure || t("connect.remote.requestFailed"));
        return;
      }
      const p = r.policy as DeviceStoragePolicy;
      setVersion(r.version);
      setSaved({ ...p, repo_days: p.repo_days ?? 37 });
      setForm({ ...p, repo_days: p.repo_days ?? 37 });
      setError("");
      if (c.params.policy) toast(t("connect.devicePolicy.saved"));
    },
  });
  const started = useRef(false);
  useEffect(() => {
    if (!started.current) {
      started.current = true;
      cmd.start({ storage: true });
    }
  }, [cmd.start]);
  return (
    <section className="grid gap-3 border-t border-line-soft pt-4">
      <h3 className="font-semibold">{t("connect.storage.autoPolicy")}</h3>
      <p className="text-caption text-fg-muted">
        {t("connect.storage.autoHint")}
      </p>
      {error && <Callout tone="warning">{error}</Callout>}
      {form ? (
        <fieldset disabled={cmd.running} className="grid gap-3">
          <label className="flex items-center gap-2">
            <Checkbox
              checked={form.enabled}
              onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
            />
            {t("connect.storage.autoEnable")}
          </label>
          <div className="grid grid-cols-2 gap-3">
            {fields.map((key) => (
              <label key={key} className="grid gap-1 text-body-sm">
                {t(`connect.storage.${key}`)}
                <TextInput
                  type="number"
                  min={limits[key][0]}
                  max={limits[key][1]}
                  value={Number.isFinite(form[key]) ? form[key] : ""}
                  onChange={(e) =>
                    setForm({ ...form, [key]: e.target.valueAsNumber })
                  }
                />
              </label>
            ))}
          </div>
          <Button
            size="28"
            className="justify-self-end"
            disabled={
              JSON.stringify(form) === JSON.stringify(saved) ||
              fields.some(
                (k) =>
                  !Number.isInteger(form[k]) ||
                  form[k] < limits[k][0] ||
                  form[k] > limits[k][1],
              )
            }
            onClick={() => cmd.start({ storage: true, version, policy: form })}
          >
            {t("common.save")}
          </Button>
        </fieldset>
      ) : (
        cmd.running && <span>{t("common.loading")}</span>
      )}
    </section>
  );
}
