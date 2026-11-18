import type { ImportAnalysis, ImportConfig } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Callout, Field, Select } from "@openfox/ui";
import { useSpaceTypes } from "../tasks/queries";

export type YunxiaoMapping = Pick<
  ImportConfig,
  "types" | "priority_fields" | "priorities"
>;
export function YunxiaoMappings({
  org,
  space,
  analysis,
  value,
  onChange,
}: {
  org: string;
  space?: string;
  analysis: ImportAnalysis;
  value: YunxiaoMapping;
  onChange: (v: YunxiaoMapping) => void;
}) {
  const t = useT();
  const types = useSpaceTypes(org, space);
  const updateType = (source: string, id: string) =>
    onChange({
      ...value,
      types: [
        ...(value.types ?? []).filter((m) => m.source !== source),
        id === "default"
          ? { source, use_default: true }
          : { source, type_id: id },
      ],
    });
  return (
    <div className="mt-5 flex flex-col gap-4">
      <Callout>{t("integrations.yunxiao.importScope")}</Callout>
      {!!analysis.archived_excluded && (
        <p>
          {t("integrations.yunxiao.archived", {
            count: analysis.archived_excluded,
          })}
        </p>
      )}
      {types.isError && <p role="alert">{t("accountBinding.loadFailed")}</p>}
      {analysis.types.map((kind) => {
        const selected = value.types?.find((m) => m.source === kind.source);
        const fields =
          analysis.priority_fields?.filter(
            (f) => f.type_source === kind.source,
          ) ?? [];
        const field = value.priority_fields?.find(
          (f) => f.type_source === kind.source,
        );
        const choices =
          fields.find((f) => f.field_id === field?.field_id)?.values ?? [];
        const label = kind.source_label ?? kind.source;
        return (
          <div
            key={kind.source}
            className="flex flex-col gap-3 rounded-lg border border-line p-3"
          >
            <Field label={t("integrations.yunxiao.type", { name: label })}>
              <Select
                aria-label={t("integrations.yunxiao.type", { name: label })}
                value={
                  selected?.use_default
                    ? "default"
                    : (selected?.type_id ?? null)
                }
                options={[
                  {
                    value: "default",
                    label: t("integrations.yunxiao.defaultType"),
                  },
                  ...(types.data ?? []).map((v) => ({
                    value: v.id,
                    label: v.name,
                  })),
                ]}
                onChange={(id) => updateType(kind.source, id)}
              />
            </Field>
            {!!fields.length && (
              <Field
                label={t("integrations.yunxiao.priorityField", { name: label })}
              >
                <Select
                  aria-label={t("integrations.yunxiao.priorityField", {
                    name: label,
                  })}
                  value={field ? field.field_id || "none" : null}
                  options={[
                    {
                      value: "none",
                      label: t("integrations.yunxiao.noPriority"),
                    },
                    ...fields.map((f) => ({
                      value: f.field_id,
                      label: f.field_label,
                    })),
                  ]}
                  onChange={(id) =>
                    onChange({
                      ...value,
                      priority_fields: [
                        ...(value.priority_fields ?? []).filter(
                          (f) => f.type_source !== kind.source,
                        ),
                        {
                          type_source: kind.source,
                          field_id: id === "none" ? "" : id,
                        },
                      ],
                    })
                  }
                />
              </Field>
            )}
            {choices.map((v) => (
              <Field key={v.source} label={v.option_label}>
                <Select
                  aria-label={t("integrations.yunxiao.priorityValue", {
                    name: v.option_label,
                  })}
                  value={
                    value.priorities?.find((m) => m.source === v.source)
                      ?.priority ?? null
                  }
                  options={[
                    "NONE",
                    "URGENT",
                    "HIGH",
                    "NORMAL",
                    "LOW",
                    "LOWEST",
                  ].map((p) => ({
                    value: p,
                    label: t(`tasks.priority.${p}` as Parameters<typeof t>[0]),
                  }))}
                  onChange={(priority) =>
                    onChange({
                      ...value,
                      priorities: [
                        ...(value.priorities ?? []).filter(
                          (m) => m.source !== v.source,
                        ),
                        {
                          source: v.source,
                          priority: priority as NonNullable<
                            ImportConfig["priorities"]
                          >[number]["priority"],
                        },
                      ],
                    })
                  }
                />
              </Field>
            ))}
          </div>
        );
      })}
    </div>
  );
}
