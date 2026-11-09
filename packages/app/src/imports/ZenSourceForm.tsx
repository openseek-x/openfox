import { useState, type ReactNode } from "react";
import type { ZenTaoCredentials } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Select, Switch, TextInput } from "@openfox/ui";
import { useApi } from "../context";

export function ZenSourceForm({
  orgId,
  credentials,
  onCredentials,
  project,
  onProject,
  children,
}: {
  orgId: string;
  credentials: ZenTaoCredentials;
  onCredentials: (c: ZenTaoCredentials) => void;
  project: string;
  onProject: (id: string) => void;
  children: ReactNode;
}) {
  const t = useT(),
    api = useApi();
  const [basic, setBasic] = useState(!!credentials.basic_user),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [sources, setSources] = useState<{ id: string; name: string }[] | null>(
      null,
    );
  function change(c: ZenTaoCredentials) {
    onCredentials(c);
    onProject("");
    setSources(null);
    setError("");
  }
  async function load() {
    setBusy(true);
    setError("");
    onProject("");
    try {
      setSources(await api.imports.zenSources(orgId, credentials));
    } catch {
      setSources(null);
      setError(t("imports.wizard.zenFailed"));
    } finally {
      setBusy(false);
    }
  }
  function field(
    key: keyof ZenTaoCredentials,
    label: string,
    type: "url" | "text" | "password",
    placeholder?: string,
  ) {
    return (
      <label className="grid gap-1 text-body-sm">
        {label}
        <TextInput
          type={type}
          placeholder={placeholder}
          autoComplete="off"
          value={credentials[key] ?? ""}
          onChange={(e) => change({ ...credentials, [key]: e.target.value })}
        />
      </label>
    );
  }
  return (
    <fieldset disabled={busy} className="grid gap-3">
      {field(
        "site",
        t("imports.wizard.zenSite"),
        "url",
        "https://zentao.example.com",
      )}
      <label className="flex items-center gap-2">
        <Switch
          checked={basic}
          onCheckedChange={() => {
            setBasic(!basic);
            change({
              ...credentials,
              basic_user: undefined,
              basic_password: undefined,
            });
          }}
        />
        Basic Auth
      </label>
      {basic && (
        <>
          {field("basic_user", t("imports.wizard.zenBasicUser"), "text")}
          {field(
            "basic_password",
            t("imports.wizard.zenBasicPassword"),
            "password",
          )}
        </>
      )}
      {field("account", t("imports.wizard.zenAccount"), "text")}
      {field("password", t("imports.wizard.zenPassword"), "password")}
      {children}
      <Button
        className="justify-self-start"
        variant="outline"
        disabled={
          !credentials.site.startsWith("https://") ||
          !credentials.account ||
          !credentials.password ||
          (basic && (!credentials.basic_user || !credentials.basic_password))
        }
        loading={busy}
        onClick={() => void load()}
      >
        {t("imports.wizard.zenLoad")}
      </Button>
      {sources &&
        (sources.length ? (
          <Select
            aria-label={t("imports.wizard.project")}
            value={project || null}
            onChange={onProject}
            options={sources.map((p) => ({
              value: p.id,
              label:
                t(
                  p.id.startsWith("products/")
                    ? "imports.wizard.zenProduct"
                    : "imports.wizard.zenProject",
                ) +
                " · " +
                p.name,
            }))}
            placeholder={t("imports.wizard.chooseProject")}
          />
        ) : (
          <p>{t("imports.wizard.noProject")}</p>
        ))}
      {error && <Callout tone="danger">{error}</Callout>}
    </fieldset>
  );
}
