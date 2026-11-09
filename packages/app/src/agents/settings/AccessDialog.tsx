import { useState, type ReactNode } from "react";

import type { AgentVisibility } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Field, Icon, Select } from "@openfox/ui";

import { usePeople } from "../../tasks/queries";
import { PersonAvatar } from "../AgentAvatar";
import { useSpaces } from "../queries";

export interface AccessValue {
  spaceId: string;
  visibility: AgentVisibility;
  ownerId: string;
}

/**
 * 访问权限弹窗：空间 / 可见性 / 所有者。
 * 所有者候选为目标空间可指派的成员；block 返回非空文案时显示红字并禁用保存（小队换空间时队长不在新空间）。
 */
export function AccessDialog({
  orgId,
  initial,
  ownerEmail,
  block,
  saving,
  onClose,
  onSave,
}: {
  orgId: string;
  initial: AccessValue;
  ownerEmail: string;
  block?: (value: AccessValue) => ReactNode | null;
  saving: boolean;
  onClose: () => void;
  onSave: (changes: Partial<AccessValue>) => void;
}) {
  const t = useT();
  const spaces = useSpaces(orgId);
  const [value, setValue] = useState(initial);
  const space = spaces.data?.find((item) => item.id === value.spaceId);
  const people = usePeople(orgId, space);
  const owners = people.some((p) => p.id === initial.ownerId)
    ? people
    : [{ id: initial.ownerId, email: ownerEmail }, ...people];
  const blocked = block?.(value) ?? null;
  const changes: Partial<AccessValue> = {};
  if (value.spaceId !== initial.spaceId) changes.spaceId = value.spaceId;
  if (value.visibility !== initial.visibility)
    changes.visibility = value.visibility;
  if (value.ownerId !== initial.ownerId) changes.ownerId = value.ownerId;
  const dirty = Object.keys(changes).length > 0;

  return (
    <Dialog
      open
      onClose={saving ? undefined : onClose}
      title={t("agents.access.title")}
      width={448}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={saving}
            disabled={!dirty || blocked !== null}
            onClick={() => onSave(changes)}
          >
            {t("agents.access.save")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4 px-0.5 pb-0.5">
        <Field label={t("agents.access.space")}>
          <Select
            value={value.spaceId}
            onChange={(spaceId) => setValue((v) => ({ ...v, spaceId }))}
            leading={<Icon name="layout-grid" />}
            options={(spaces.data ?? []).map((item) => ({
              value: item.id,
              label: item.name,
            }))}
          />
        </Field>
        <Field label={t("agents.access.visibility")}>
          <Select
            value={value.visibility}
            onChange={(visibility) => setValue((v) => ({ ...v, visibility }))}
            options={(["SHARED", "PRIVATE"] as const).map((item) => ({
              value: item,
              label: t(`agents.visibility.${item}`),
              icon: <Icon name={item === "SHARED" ? "users" : "lock"} />,
            }))}
          />
        </Field>
        <Field label={t("agents.access.owner")}>
          <Select
            value={value.ownerId}
            onChange={(ownerId) => setValue((v) => ({ ...v, ownerId }))}
            options={owners.map((person) => ({
              value: person.id,
              label: person.email,
              icon: <PersonAvatar name={person.email} size={20} />,
            }))}
          />
        </Field>
        {blocked && (
          <p role="alert" className="text-body-sm text-danger">
            {blocked}
          </p>
        )}
      </div>
    </Dialog>
  );
}
