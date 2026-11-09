import type { ReactNode } from "react";

import type { SpaceDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Segmented, Select, TextInput } from "@openfox/ui";

import { NEW_SPACE, type NewSpaceDraft } from "./model";

// 向导各步骤共用的小组件。

export function StepTitle({
  title,
  description,
}: {
  title: string;
  description?: string;
}) {
  return (
    <header className="flex flex-col gap-1">
      <h2 className="text-title font-bold">{title}</h2>
      {description && (
        <p className="text-body-sm text-fg-muted">{description}</p>
      )}
    </header>
  );
}

export function OptionRow({
  title,
  description,
  control,
}: {
  title: string;
  description: string;
  control: ReactNode;
}) {
  return (
    <div className="flex items-center gap-3 border-b border-line-soft px-4 py-3 last:border-b-0">
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="text-body font-medium">{title}</span>
        <span className="text-caption text-fg-muted">{description}</span>
      </div>
      {control}
    </div>
  );
}

/** 一行表单：标签、控件、可选提示。控件自己带 aria-label。 */
export function FormRow({
  label,
  hint,
  error,
  children,
}: {
  label: string;
  hint?: string;
  error?: string;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1.5 text-body-sm">
      <span className="font-medium">{label}</span>
      {children}
      {error ? (
        <span role="alert" className="text-caption text-danger">
          {error}
        </span>
      ) : (
        hint && <span className="text-caption text-fg-muted">{hint}</span>
      )}
    </div>
  );
}

/** 目标空间下拉；最后一项「新建空间…」展开名称、前缀与可见性。 */
export function SpaceTarget({
  spaces,
  spaceId,
  onSpace,
  newSpace,
  onNewSpace,
  prefixDefault = "",
  prefixHint,
}: {
  spaces: readonly SpaceDetail[];
  spaceId: string;
  onSpace: (id: string) => void;
  newSpace: NewSpaceDraft;
  onNewSpace: (value: NewSpaceDraft) => void;
  /** 前缀留空时的默认值（API 导入取项目 Key 的大写）。 */
  prefixDefault?: string;
  prefixHint: string;
}) {
  const t = useT();
  return (
    <div className="flex flex-col gap-3">
      <FormRow label={t("imports.upload.space")}>
        <Select
          aria-label={t("imports.upload.space")}
          value={spaceId || null}
          onChange={onSpace}
          placeholder={t("imports.upload.spacePlaceholder")}
          options={[
            ...spaces.map((space) => ({ value: space.id, label: space.name })),
            { value: NEW_SPACE, label: t("imports.newSpace.option") },
          ]}
        />
      </FormRow>
      {spaceId === NEW_SPACE && (
        <div className="flex flex-col gap-3 rounded-xl border border-line-default p-4">
          <FormRow label={t("imports.newSpace.name")}>
            <TextInput
              aria-label={t("imports.newSpace.name")}
              value={newSpace.name}
              onChange={(event) =>
                onNewSpace({ ...newSpace, name: event.target.value })
              }
            />
          </FormRow>
          <FormRow label={t("imports.newSpace.prefix")} hint={prefixHint}>
            <TextInput
              aria-label={t("imports.newSpace.prefix")}
              value={newSpace.prefix || prefixDefault}
              maxLength={10}
              onChange={(event) =>
                onNewSpace({
                  ...newSpace,
                  prefix: event.target.value.toUpperCase(),
                })
              }
            />
          </FormRow>
          <FormRow label={t("imports.newSpace.visibility")}>
            <Segmented
              label={t("imports.newSpace.visibility")}
              value={newSpace.visibility}
              onChange={(visibility) => onNewSpace({ ...newSpace, visibility })}
              options={[
                { value: "PRIVATE", label: t("imports.newSpace.private") },
                { value: "SHARED", label: t("imports.newSpace.shared") },
              ]}
            />
          </FormRow>
        </div>
      )}
    </div>
  );
}
