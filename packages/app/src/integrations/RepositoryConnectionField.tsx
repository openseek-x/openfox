import { useQueries } from "@tanstack/react-query";
import { useT } from "@openfox/i18n";
import { Field, Select } from "@openfox/ui";
import { keys, useApi } from "../context";
import { isOrgAdmin, useOrgRole } from "../spaces/queries";

export function RepositoryConnectionField({
  org,
  value,
  onChange,
}: {
  org: string;
  value: string;
  onChange: (value: string) => void;
}) {
  const api = useApi(),
    t = useT();
  const canBind = isOrgAdmin(useOrgRole(org));
  const results = useQueries({
    queries: ["github", "gitlab_cn", "gitlab"].map((provider) => ({
      queryKey: keys.integrationDetail(org, provider),
      queryFn: () => api.integrations.get(org, provider),
      enabled: canBind,
    })),
  });
  const connections = results
    .flatMap((result) => result.data?.connections ?? [])
    .filter(
      (connection) =>
        connection.scope === "ORGANIZATION" && connection.status === "ACTIVE",
    );
  const options = [
    { value: "", label: t("spaces.repos.anonymous") },
    ...connections.map((connection) => ({
      value: connection.id,
      label: `${connection.display_name || connection.external_id} · ${connection.provider === "github" ? "GitHub" : "GitLab"}`,
    })),
  ];
  if (value && !options.some((option) => option.value === value)) {
    options.push({ value, label: t("spaces.repos.boundConnection") });
  }
  return (
    <Field label={t("spaces.repos.connection")}>
      <Select
        aria-label={t("spaces.repos.connection")}
        value={value}
        options={options}
        onChange={onChange}
        disabled={!canBind}
      />
      <p className="text-caption text-fg-muted">
        {t("spaces.repos.connectionHelp")}
      </p>
      {canBind && results.some((result) => result.isError) && (
        <p role="alert" className="text-caption text-danger">
          {t("spaces.repos.connectionLoadError")}
        </p>
      )}
    </Field>
  );
}
