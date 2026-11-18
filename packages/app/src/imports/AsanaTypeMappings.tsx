import type { ImportAnalysis, ImportConfig } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Field, Select } from "@openfox/ui";
import { useSpaceTypes } from "../tasks/queries";

export function AsanaTypeMappings({
  org,
  space,
  analysis,
  value,
  onChange,
}: {
  org: string;
  space?: string;
  analysis: Pick<ImportAnalysis, "types">;
  value: ImportConfig["types"];
  onChange: (value: ImportConfig["types"]) => void;
}) {
  const t = useT();
  const types = useSpaceTypes(org, space);
  return (
    <div className="mt-4 grid gap-3">
      {types.isError && <p role="alert">{t("accountBinding.loadFailed")}</p>}
      {analysis.types.map((kind) => {
        const selected = value?.find((v) => v.source === kind.source);
        const label = t("integrations.asana.type", {
          name: kind.source_label ?? kind.source,
        });
        return (
          <Field key={kind.source} label={label}>
            <Select
              aria-label={label}
              value={
                selected?.use_default ? "default" : (selected?.type_id ?? null)
              }
              options={[
                {
                  value: "default",
                  label: t("integrations.asana.defaultType"),
                },
                ...(types.data ?? []).map((v) => ({
                  value: v.id,
                  label: v.name,
                })),
              ]}
              onChange={(id) =>
                onChange([
                  ...(value ?? []).filter((v) => v.source !== kind.source),
                  id === "default"
                    ? { source: kind.source, use_default: true }
                    : { source: kind.source, type_id: id },
                ])
              }
            />
          </Field>
        );
      })}
    </div>
  );
}
