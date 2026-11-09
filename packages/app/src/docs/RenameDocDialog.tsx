import { useState } from "react";

import type { DocNode } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Field, TextInput, useToast } from "@openfox/ui";

import { DOC_TITLE_MAX } from "./model";
import { docErrorMessage, useRenameDocNode } from "./queries";

// 重命名节点（树节点与表格行菜单）。编辑页的标题直接在页面内编辑。

export function RenameDocDialog({
  orgId,
  spaceId,
  node,
  onClose,
}: {
  orgId: string;
  spaceId: string;
  node: DocNode;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const rename = useRenameDocNode(orgId, spaceId);
  const [title, setTitle] = useState(node.title);
  const changed = title.trim() !== node.title;

  const submit = async () => {
    if (!changed || rename.isPending) return;
    try {
      await rename.mutateAsync({ node, title: title.trim() });
      onClose();
    } catch (error) {
      toast(docErrorMessage(t, error, "docs.row.renameFailed"), {
        tone: "error",
      });
    }
  };

  return (
    <Dialog
      open
      onClose={rename.isPending ? undefined : onClose}
      title={t("docs.row.renameTitle")}
      width={420}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={rename.isPending}
          >
            {t("docs.set.cancel")}
          </Button>
          <Button
            variant="primary"
            onClick={() => void submit()}
            disabled={!changed || rename.isPending}
            loading={rename.isPending}
          >
            {t("docs.set.save")}
          </Button>
        </>
      }
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <Field label={t("docs.row.renameLabel")}>
          <TextInput
            className="!w-full"
            value={title}
            maxLength={DOC_TITLE_MAX}
            placeholder={t("docs.untitled")}
            autoFocus
            onChange={(event) => setTitle(event.target.value)}
          />
        </Field>
      </form>
    </Dialog>
  );
}
