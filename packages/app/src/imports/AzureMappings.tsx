import type {
  ImportAnalysis,
  ImportConfig,
  ImportStatusMapping,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Callout, Field, Select, TextInput } from "@openfox/ui";
import { useSpaceTypes } from "../tasks/queries";
import { useStatuses } from "../tasks/queries";
import { statusMappingsValid, sortedStatuses } from "./model";
export type AzureMappingsValue = Pick<
  ImportConfig,
  "types" | "statuses" | "priorities"
>;
export const azureReconcileStatuses = (
  analysis: ImportAnalysis,
  current: ImportStatusMapping[],
) =>
  analysis.statuses.map(
    (s) => current.find((m) => m.source === s.source) ?? { source: s.source },
  );
export function azureMappingsValid(
  analysis: ImportAnalysis,
  value: AzureMappingsValue,
) {
  return (
    analysis.types.every((v) =>
      value.types?.some(
        (m) => m.source === v.source && (m.use_default || !!m.type_id),
      ),
    ) &&
    analysis.statuses.every((v) =>
      value.statuses.some(
        (m) => m.source === v.source && statusMappingsValid([m]),
      ),
    ) &&
    (analysis.priority_fields ?? []).every((f) =>
      f.values.every((v) =>
        value.priorities?.some((m) => m.source === v.source && !!m.priority),
      ),
    )
  );
}
export function AzureMappings({
  org,
  space,
  analysis,
  value,
  onChange,
}: {
  org: string;
  space?: string;
  analysis: ImportAnalysis;
  value: AzureMappingsValue;
  onChange: (v: AzureMappingsValue) => void;
}) {
  const t = useT(),
    types = useSpaceTypes(org, space),
    statuses = useStatuses(org);
  const changeStatus = (source: string, m: ImportStatusMapping) =>
    onChange({
      ...value,
      statuses: [...value.statuses.filter((v) => v.source !== source), m],
    });
  return (
    <div className="grid gap-4">
      <Callout>{t("integrations.azure.mappingHelp")}</Callout>
      {(types.isError || statuses.isError) && (
        <p role="alert">{t("accountBinding.loadFailed")}</p>
      )}
      {analysis.types.map((kind) => {
        const current = value.types?.find((v) => v.source === kind.source),
          label = t("integrations.azure.type", {
            name: kind.source_label ?? kind.source,
          });
        return (
          <Field key={kind.source} label={label}>
            <Select
              aria-label={label}
              value={
                current?.use_default ? "default" : (current?.type_id ?? null)
              }
              options={[
                {
                  value: "default",
                  label: t("integrations.azure.defaultType"),
                },
                ...(types.data ?? []).map((v) => ({
                  value: v.id,
                  label: v.name,
                })),
              ]}
              onChange={(id) =>
                onChange({
                  ...value,
                  types: [
                    ...(value.types ?? []).filter(
                      (m) => m.source !== kind.source,
                    ),
                    id === "default"
                      ? { source: kind.source, use_default: true }
                      : { source: kind.source, type_id: id },
                  ],
                })
              }
            />
          </Field>
        );
      })}
      {analysis.statuses.map((state) => {
        const current = value.statuses.find((v) => v.source === state.source),
          label = t("imports.statuses.mapAria", {
            name: state.source_label ?? state.source,
          });
        const isNew = !!current?.new_name;
        return (
          <div className="grid gap-2" key={state.source}>
            <Field label={label}>
              <Select
                aria-label={label}
                value={current?.status_id ?? (isNew ? "new" : null)}
                options={[
                  ...sortedStatuses(statuses.data ?? []).map((s) => ({
                    value: s.id,
                    label: s.name,
                  })),
                  { value: "new", label: t("imports.statuses.createNew") },
                ]}
                onChange={(id) =>
                  changeStatus(
                    state.source,
                    id === "new"
                      ? {
                          source: state.source,
                          new_name:
                            (state.source_label ?? state.source)
                              .split(" / ")
                              .at(-1) ?? "",
                        }
                      : { source: state.source, status_id: id },
                  )
                }
              />
            </Field>
            {isNew && (
              <div className="flex gap-2">
                <TextInput
                  aria-label={t("imports.statuses.newName", {
                    name: state.source_label ?? state.source,
                  })}
                  value={current?.new_name ?? ""}
                  onChange={(e) =>
                    changeStatus(state.source, {
                      ...current!,
                      new_name: e.target.value,
                    })
                  }
                />
                <Select
                  aria-label={t("imports.statuses.newCategory", {
                    name: state.source_label ?? state.source,
                  })}
                  value={current?.new_category ?? null}
                  options={[
                    "TRIAGE",
                    "BACKLOG",
                    "TODO",
                    "IN_PROGRESS",
                    "DONE",
                    "CANCELLED",
                    "DUPLICATE",
                  ].map((v) => ({
                    value: v,
                    label: t(
                      `tasks.statusCategory.${v}` as Parameters<typeof t>[0],
                    ),
                  }))}
                  onChange={(v) =>
                    changeStatus(state.source, {
                      ...current!,
                      new_category: v as ImportStatusMapping["new_category"],
                    })
                  }
                />
              </div>
            )}
          </div>
        );
      })}
      {(analysis.priority_fields ?? []).flatMap((f) =>
        f.values.map((v) => {
          const label = t("integrations.azure.priorityValue", {
            name: `${f.field_label}: ${v.option_label}`,
          });
          return (
            <Field key={v.source} label={label}>
              <Select
                aria-label={label}
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
                onChange={(p) =>
                  onChange({
                    ...value,
                    priorities: [
                      ...(value.priorities ?? []).filter(
                        (m) => m.source !== v.source,
                      ),
                      {
                        source: v.source,
                        priority: p as NonNullable<
                          ImportConfig["priorities"]
                        >[number]["priority"],
                      },
                    ],
                  })
                }
              />
            </Field>
          );
        }),
      )}
    </div>
  );
}
