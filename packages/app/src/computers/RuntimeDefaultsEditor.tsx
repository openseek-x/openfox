import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  isApiError,
  type DeviceRuntimeDefaults,
  type DeviceRuntimeDefaultsPatch,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, TextInput } from "@openfox/ui";
import { keys, useApi } from "../context";
import { isReservedEnvName } from "../agents/model";
import { describeError } from "../settings/common";

export function RuntimeDefaultsEditor({
  orgId,
  deviceId,
  runtime,
}: {
  orgId: string;
  deviceId: string;
  runtime: string;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button size="28" variant="outline" onClick={() => setOpen(true)}>
        {t("computers.runtimeDefaults.title")}
      </Button>
      {open && (
        <Editor
          orgId={orgId}
          deviceId={deviceId}
          runtime={runtime}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  );
}
function Editor({
  orgId,
  deviceId,
  runtime,
  onClose,
}: {
  orgId: string;
  deviceId: string;
  runtime: string;
  onClose: () => void;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient();
  const key = [...keys.device(orgId, deviceId), "runtime-defaults", runtime];
  const query = useQuery({
    queryKey: key,
    queryFn: () => api.devices.getRuntimeDefaults(orgId, deviceId, runtime),
    staleTime: 0,
  });
  const [base, setBase] = useState<DeviceRuntimeDefaults>();
  const [model, setModel] = useState("");
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [setEnv, setSetEnv] = useState<Record<string, string>>({});
  const [removeEnv, setRemoveEnv] = useState<string[]>([]);
  const [invalid, setInvalid] = useState(false);
  // Copy the server revision once; a refetch/409 must never replace secret drafts.
  if (query.data && !base && !query.isFetching && !query.error) {
    setBase(query.data);
    setModel(query.data.model ?? "");
  }
  const save = useMutation({
    mutationFn: () =>
      api.devices.patchRuntimeDefaults(orgId, deviceId, runtime, {
        expected_version: base!.version,
        model: model.trim() || null,
        set_env: setEnv,
        remove_env: removeEnv,
      } satisfies DeviceRuntimeDefaultsPatch),
    onSuccess: (result) => {
      cache.setQueryData(key, result);
      void cache.invalidateQueries({ queryKey: keys.conversations(orgId) });
      onClose();
    },
  });
  const rows = [
    ...new Set([
      ...(base?.env_vars.map((x) => x.name) ?? []),
      ...Object.keys(setEnv),
    ]),
  ]
    .filter((n) => !removeEnv.includes(n))
    .sort();
  const add = () => {
    if (
      !/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(name) ||
      isReservedEnvName(name) ||
      new TextEncoder().encode(value).length > 8192 ||
      (!rows.includes(name) && rows.length >= 100)
    ) {
      setInvalid(true);
      return;
    }
    setSetEnv({ ...setEnv, [name]: value });
    setRemoveEnv(removeEnv.filter((n) => n !== name));
    setName("");
    setValue("");
    setInvalid(false);
  };
  const dirty =
    !!base &&
    (model.trim() !== (base.model ?? "") ||
      Object.keys(setEnv).length > 0 ||
      removeEnv.length > 0);
  return (
    <Dialog
      open
      title={`${t("computers.runtimeDefaults.title")} · ${runtime}`}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={save.isPending}
            disabled={!base || !dirty || !!name || !!value}
            onClick={() => save.mutate()}
          >
            {t("common.save")}
          </Button>
        </>
      }
    >
      <div className="grid gap-4 text-body-sm">
        <p>{t("computers.runtimeDefaults.scope")}</p>
        {runtime === "gemini" && (
          <Callout tone="info">
            <p>{t("agents.gemini.modelConfiguration")}</p>
            <p>{t("agents.gemini.devicePaths")}</p>
          </Callout>
        )}
        {runtime === "hermes" && (
          <Callout tone="info">
            <p>{t("agents.hermes.modelConfiguration")}</p>
            <p>{t("agents.hermes.contextDeclaration")}</p>
            <p>{t("agents.hermes.network")}</p>
            <p>{t("agents.hermes.devicePaths")}</p>
          </Callout>
        )}
        {!base && !query.error && <p>{t("common.loading")}</p>}
        {query.error && (
          <Callout tone="danger">{describeError(query.error, t)}</Callout>
        )}
        {base && (
          <>
            <label>
              {t("computers.runtimeDefaults.model")}
              <TextInput
                aria-label={t("computers.runtimeDefaults.model")}
                value={model}
                maxLength={128}
                placeholder={t("computers.runtimeDefaults.runtimeDefault")}
                onChange={(e) => setModel(e.target.value)}
              />
            </label>
            <p>{t("computers.runtimeDefaults.modelHint")}</p>
            <p>{t("computers.runtimeDefaults.envHint")}</p>
            {!base.secrets_available && (
              <Callout tone="warning">
                {t("computers.runtimeDefaults.noKey")}
              </Callout>
            )}
            <ul className="grid gap-2">
              {rows.map((n) => (
                <li key={n} className="flex items-center gap-2">
                  <code className="flex-1">{n}</code>
                  <span>••••••••</span>
                  <Button
                    size="28"
                    variant="ghost"
                    disabled={!base.secrets_available}
                    onClick={() => {
                      setName(n);
                      setValue("");
                    }}
                  >
                    {t("computers.runtimeDefaults.replace")}
                  </Button>
                  <Button
                    size="28"
                    variant="ghost"
                    onClick={() => {
                      const next = { ...setEnv };
                      delete next[n];
                      setSetEnv(next);
                      if (base.env_vars.some((x) => x.name === n))
                        setRemoveEnv([...removeEnv, n]);
                    }}
                  >
                    {t("agents.sections.runtime.envDelete")}
                  </Button>
                </li>
              ))}
            </ul>
            <div className="flex flex-wrap gap-2">
              <TextInput
                aria-label={t("computers.runtimeDefaults.name")}
                value={name}
                maxLength={128}
                disabled={!base.secrets_available}
                onChange={(e) => setName(e.target.value)}
              />
              <TextInput
                aria-label={t("computers.runtimeDefaults.value")}
                type="password"
                autoComplete="new-password"
                value={value}
                disabled={!base.secrets_available}
                onChange={(e) => setValue(e.target.value)}
              />
              <Button
                variant="outline"
                disabled={!base.secrets_available || !name}
                onClick={add}
              >
                {t("computers.runtimeDefaults.applyVariable")}
              </Button>
            </div>
            {invalid && (
              <Callout tone="danger">
                {t("computers.runtimeDefaults.invalid")}
              </Callout>
            )}
          </>
        )}
        {save.error && (
          <Callout tone="danger">
            {isApiError(save.error, "VERSION_CONFLICT")
              ? t("computers.runtimeDefaults.conflict")
              : describeError(save.error, t)}
          </Callout>
        )}
      </div>
    </Dialog>
  );
}
