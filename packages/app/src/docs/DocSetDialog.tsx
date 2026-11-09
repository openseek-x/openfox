import { useState } from "react";

import type { DocSet } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Field,
  cn,
  useFieldContext,
  useToast,
} from "@openfox/ui";

import { ViewIconPicker, type ViewIconValue } from "../views/ViewIcon";
import { DOC_SET_NAME_MAX, isDocSetNameValid, nameLength } from "./model";
import { docErrorMessage, useSaveDocSet } from "./queries";

// 新建 / 编辑文档集。新建默认图标 book-open，编辑预填已有图标或 library-big。

const CREATE_DEFAULT_ICON: ViewIconValue = {
  kind: "icon",
  icon: "book-open",
  url: null,
};

function initialIcon(set: DocSet | undefined): ViewIconValue {
  if (!set) return CREATE_DEFAULT_ICON;
  return {
    kind: set.icon_kind ?? "icon",
    icon: set.icon ?? "library-big",
    url: set.icon_url,
  };
}

export function DocSetDialog({
  orgId,
  spaceId,
  set,
  open,
  onClose,
  onCreated,
}: {
  orgId: string;
  spaceId: string;
  /** 有值为编辑，无值为新建 */
  set?: DocSet;
  open: boolean;
  onClose: () => void;
  /** 新建成功后调用，用于展开该文档集 */
  onCreated?: (set: DocSet) => void;
}) {
  if (!open) return null;
  return (
    <DocSetForm
      key={set?.id ?? "new"}
      orgId={orgId}
      spaceId={spaceId}
      set={set}
      onClose={onClose}
      onCreated={onCreated}
    />
  );
}

function DocSetForm({
  orgId,
  spaceId,
  set,
  onClose,
  onCreated,
}: {
  orgId: string;
  spaceId: string;
  set?: DocSet;
  onClose: () => void;
  onCreated?: (set: DocSet) => void;
}) {
  const t = useT();
  const toast = useToast();
  const save = useSaveDocSet(orgId, spaceId);
  const [name, setName] = useState(set?.name ?? "");
  const [icon, setIcon] = useState<ViewIconValue>(() => initialIcon(set));
  const tooLong = nameLength(name) > DOC_SET_NAME_MAX;
  const valid = isDocSetNameValid(name);
  const editing = Boolean(set);

  const submit = async () => {
    if (!valid || save.isPending) return;
    try {
      const created = await save.mutateAsync({
        target: set,
        body: {
          name: name.trim(),
          icon: icon.kind ? { kind: icon.kind, value: icon.icon ?? "" } : null,
        },
      });
      onClose();
      if (!editing) onCreated?.(created);
    } catch (error) {
      toast(
        docErrorMessage(
          t,
          error,
          editing ? "docs.set.saveFailed" : "docs.set.createFailed",
        ),
        { tone: "error" },
      );
    }
  };

  return (
    <Dialog
      open
      onClose={save.isPending ? undefined : onClose}
      title={editing ? t("docs.set.editTitle") : t("docs.set.createTitle")}
      width={448}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={save.isPending}>
            {t("docs.set.cancel")}
          </Button>
          <Button
            variant="primary"
            onClick={() => void submit()}
            disabled={!valid || save.isPending}
            loading={save.isPending}
          >
            {editing ? t("docs.set.save") : t("docs.set.create")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-2"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <Field
          label={t("docs.set.name")}
          error={tooLong ? t("docs.set.nameTooLong") : undefined}
        >
          <NameShell
            orgId={orgId}
            icon={icon}
            onIcon={setIcon}
            name={name}
            onName={setName}
            tooLong={tooLong}
          />
        </Field>
      </form>
    </Dialog>
  );
}

/** 单层外壳：图标选择器 + 输入框 + 计数；聚焦与超限状态都落在外壳上。放在 Field 内以关联标签。 */
function NameShell({
  orgId,
  icon,
  onIcon,
  name,
  onName,
  tooLong,
}: {
  orgId: string;
  icon: ViewIconValue;
  onIcon: (value: ViewIconValue) => void;
  name: string;
  onName: (value: string) => void;
  tooLong: boolean;
}) {
  const t = useT();
  const field = useFieldContext();
  return (
    <div
      className={cn(
        "flex h-9 items-center gap-2 rounded-lg border bg-surface-card pr-3 pl-1 transition-colors focus-within:shadow-focus [&>button]:size-7 [&>button]:border-0",
        tooLong
          ? "border-danger"
          : "border-line-default focus-within:border-line-focus",
      )}
    >
      <ViewIconPicker orgId={orgId} value={icon} onChange={onIcon} />
      <input
        id={field?.inputId}
        aria-describedby={field?.describedBy}
        className="min-w-0 flex-1 bg-transparent text-body text-fg-default outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
        value={name}
        placeholder={t("docs.set.namePlaceholder")}
        aria-invalid={tooLong || undefined}
        autoFocus
        onChange={(event) => onName(event.target.value)}
      />
      <span
        className={cn(
          "text-caption",
          tooLong ? "text-danger" : "text-fg-muted",
        )}
      >
        {t("docs.set.counter", { count: nameLength(name) })}
      </span>
    </div>
  );
}
