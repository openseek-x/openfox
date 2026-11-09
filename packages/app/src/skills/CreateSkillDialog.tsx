import { useState } from "react";

import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Dialog,
  Field,
  TextInput,
  Textarea,
  useToast,
} from "@openfox/ui";

import {
  SKILL_DESCRIPTION_MAX,
  SKILL_INSTRUCTIONS_MAX,
  SKILL_NAME_PATTERN,
} from "./model";
import { skillErrorText, useCreateSkill } from "./queries";

// 手动创建：名称 / 描述 / 指令，服务端拼出 SKILL.md。弹窗里没有文件上传。

export function CreateSkillDialog({
  orgId,
  spaceId,
  onClose,
  onCreated,
}: {
  orgId: string;
  spaceId: string;
  onClose: () => void;
  onCreated: (skillId: string) => void;
}) {
  const t = useT();
  const toast = useToast();
  const create = useCreateSkill(orgId, spaceId);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [instructions, setInstructions] = useState("");
  const trimmed = name.trim();
  const nameInvalid = trimmed !== "" && !SKILL_NAME_PATTERN.test(trimmed);
  const canSave = trimmed !== "" && !nameInvalid && !create.isPending;

  const save = () => {
    if (!canSave) return;
    create.mutate(
      { name: trimmed, description, instructions },
      {
        onSuccess: (skill) => {
          toast(t("skills.create.created"), { tone: "success" });
          onCreated(skill.id);
        },
      },
    );
  };

  return (
    <Dialog
      open
      onClose={create.isPending ? undefined : onClose}
      title={t("skills.create.title")}
      description={t("skills.create.desc")}
      width={560}
      className="max-h-[min(600px,85vh)]"
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={create.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button onClick={save} disabled={!canSave} loading={create.isPending}>
            {create.isPending
              ? t("skills.create.saving")
              : t("skills.create.save")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          save();
        }}
      >
        {create.isError && (
          <Callout tone="danger">
            {skillErrorText(
              t,
              create.error,
              "skills.create.failed",
              "skills.nameTaken",
            )}
          </Callout>
        )}
        <Field
          label={t("skills.create.name")}
          helper={t("skills.create.nameHint")}
          error={nameInvalid ? t("skills.create.nameInvalid") : undefined}
        >
          <TextInput
            variant="form"
            autoFocus
            data-autofocus
            className="font-mono"
            placeholder={t("skills.create.namePlaceholder")}
            maxLength={64}
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("skills.create.description")}>
          <Textarea
            rows={3}
            maxLength={SKILL_DESCRIPTION_MAX}
            placeholder={t("skills.create.descriptionPlaceholder")}
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
        <Field label={t("skills.create.instructions")}>
          <Textarea
            rows={12}
            className="min-h-64"
            maxLength={SKILL_INSTRUCTIONS_MAX}
            placeholder={t("skills.create.instructionsPlaceholder")}
            value={instructions}
            onChange={(event) => setInstructions(event.target.value)}
          />
        </Field>
      </form>
    </Dialog>
  );
}
