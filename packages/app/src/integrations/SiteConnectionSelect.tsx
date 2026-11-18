import type { IntegrationConnection } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Callout, Select } from "@openfox/ui";
import { useIntegrationDetail } from "./queries";

export function SiteConnectionSelect({
  orgId,
  provider,
  value,
  onChange,
}: {
  orgId: string;
  provider: "jira" | "zentao";
  value: string;
  onChange: (id: string, connection?: IntegrationConnection) => void;
}) {
  const t = useT();
  const detail = useIntegrationDetail(orgId, provider);
  const connections =
    detail.data?.connections.filter((c) => c.status === "ACTIVE") ?? [];
  const selected = connections.find((c) => c.id === value);
  return (
    <div className="grid gap-2">
      <Select
        aria-label={t("integrations.site.choose")}
        value={value || "new"}
        onChange={(id) =>
          onChange(
            id === "new" ? "" : id,
            connections.find((c) => c.id === id),
          )
        }
        options={[
          { value: "new", label: t("integrations.site.other") },
          ...connections.map((c) => ({
            value: c.id,
            label: c.display_name + " · " + c.id.slice(0, 8),
          })),
        ]}
      />
      {selected && (
        <p className="break-all text-body-sm text-fg-muted">
          {selected.instance_url} ·{" "}
          {selected.site_identity || selected.display_name}
        </p>
      )}
      {detail.isError && (
        <Callout tone="warning">{t("integrations.loadFailed")}</Callout>
      )}
    </div>
  );
}
