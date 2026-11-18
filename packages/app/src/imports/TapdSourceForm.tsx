import { useEffect, useState, type ReactNode } from "react";
import type { TAPDImportCredentials } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Field, Select, TextInput } from "@openfox/ui";
import { useApi } from "../context";
import { integrationErrorText } from "../integrations/queries";

export function TapdSourceForm({
  orgId,
  credentials,
  onCredentials,
  project,
  onProject,
  children,
}: {
  orgId: string;
  credentials: TAPDImportCredentials;
  onCredentials: (value: TAPDImportCredentials) => void;
  project: string;
  onProject: (id: string) => void;
  children: ReactNode;
}) {
  const api = useApi(),
    t = useT();
  const [projects, setProjects] = useState<
      { id: string; name: string }[] | null
    >(null),
    [error, setError] = useState<unknown>(null),
    [loading, setLoading] = useState(false);
  const basic = credentials.method === "basic";
  const ready =
    !!credentials.secret &&
    (!basic ||
      (!!credentials.account &&
        /^[0-9]{1,32}$/.test(credentials.company_id ?? "")));
  useEffect(() => {
    if (!ready) return;
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      setLoading(true);
      try {
        const result = await api.imports.tapdSources(
          orgId,
          credentials,
          controller.signal,
        );
        if (!controller.signal.aborted) setProjects(result);
      } catch (e) {
        if (!controller.signal.aborted) setError(e);
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    }, 550);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [api, orgId, credentials, ready]);
  function change(value: TAPDImportCredentials) {
    onCredentials(value);
    onProject("");
    setProjects(null);
    setError(null);
    setLoading(false);
  }
  return (
    <div className="grid gap-4">
      <div className="flex gap-2">
        {(["basic", "token"] as const).map((method) => (
          <Button
            key={method}
            aria-pressed={credentials.method === method}
            variant={credentials.method === method ? "secondary" : "ghost"}
            onClick={() => {
              if (method !== credentials.method) change({ method, secret: "" });
            }}
          >
            {t(method === "basic" ? "shell.apiAccount" : "shell.accessToken")}
          </Button>
        ))}
      </div>
      <p className="text-body-sm text-fg-muted">
        {t("imports.wizard.tapdAuthHint")}
      </p>
      {basic && (
        <Field label={t("shell.apiAccount")}>
          <TextInput
            aria-label={t("shell.apiAccount")}
            autoComplete="off"
            value={credentials.account ?? ""}
            onChange={(e) =>
              change({ ...credentials, account: e.target.value })
            }
          />
        </Field>
      )}
      <Field label={t(basic ? "shell.apiSecret" : "shell.accessToken")}>
        <TextInput
          aria-label={t(basic ? "shell.apiSecret" : "shell.accessToken")}
          type="password"
          autoComplete="off"
          value={credentials.secret}
          onChange={(e) => change({ ...credentials, secret: e.target.value })}
        />
      </Field>
      <p className="text-body-sm text-fg-muted">
        {t("imports.wizard.tapdSaveHint")}
      </p>
      {basic && (
        <Field label={t("shell.companyId")}>
          <TextInput
            aria-label={t("shell.companyId")}
            inputMode="numeric"
            value={credentials.company_id ?? ""}
            onChange={(e) =>
              change({ ...credentials, company_id: e.target.value })
            }
          />
        </Field>
      )}
      <Field label={t("imports.wizard.project")}>
        <Select
          aria-label={t("imports.wizard.project")}
          value={project || null}
          onChange={onProject}
          disabled={!projects?.length}
          options={(projects ?? []).map((p) => ({
            value: p.id,
            label: p.name,
          }))}
          placeholder={t("imports.wizard.chooseProject")}
        />
      </Field>
      <p className="text-body-sm text-fg-muted">
        {t(
          loading
            ? "common.loading"
            : projects?.length === 0
              ? "imports.wizard.noProject"
              : "imports.wizard.tapdProjectsHint",
        )}
      </p>
      {!!error && (
        <Callout tone="danger">{integrationErrorText(t, error)}</Callout>
      )}
      {children}
    </div>
  );
}
