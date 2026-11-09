import { useState } from "react";

import type { QuickCommand } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Icon } from "@openfox/ui";

import { useQuickCommandMutations } from "./mutations";
import { QuickCommandForm } from "./QuickCommandForm";
import { QuickCommandGroup } from "./QuickCommandGroup";
import { useQuickCommands } from "./queries";

// 「管理快捷指令」对话框：列表（私有 / 共享两段，可编辑项可拖动排序、编辑、删除）与新建 / 编辑表单。
// 删除的二次确认在同一个对话框里切换显示，不叠第二层模态框（两层都会抢焦点）。

type Mode =
  | { kind: "list" }
  | { kind: "form"; editing: QuickCommand | null }
  | { kind: "confirm"; item: QuickCommand };

export function ManageQuickCommandsDialog({
  orgId,
  spaceId,
  open,
  onClose,
}: {
  orgId: string;
  spaceId: string;
  open: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const list = useQuickCommands(orgId, spaceId, open);
  const { save, remove, reorder } = useQuickCommandMutations(orgId, spaceId);
  const [mode, setMode] = useState<Mode>({ kind: "list" });
  const busy = save.isPending || remove.isPending || reorder.isPending;
  const close = () => {
    setMode({ kind: "list" });
    onClose();
  };
  const toList = () => setMode({ kind: "list" });

  if (mode.kind === "form") {
    const editing = mode.editing;
    return (
      <Dialog
        open={open}
        onClose={close}
        width={520}
        title={
          editing
            ? t("tasks.quickCommands.form.editTitle")
            : t("tasks.quickCommands.form.newTitle")
        }
      >
        <Button
          variant="ghost"
          size="28"
          leading={<Icon name="chevron-left" />}
          onClick={toList}
          className="-mt-2 mb-2 -ml-2"
        >
          {t("tasks.quickCommands.form.back")}
        </Button>
        <QuickCommandForm
          orgId={orgId}
          spaceId={spaceId}
          editing={editing}
          busy={busy}
          onBack={toList}
          onSubmit={(draft) =>
            save.mutate({ draft, editing }, { onSuccess: toList })
          }
        />
      </Dialog>
    );
  }

  if (mode.kind === "confirm") {
    const item = mode.item;
    return (
      <Dialog
        open={open}
        onClose={close}
        width={420}
        title={t("tasks.quickCommands.manage.removeTitle", { name: item.name })}
        description={t("tasks.quickCommands.manage.removeDescription")}
        footer={
          <>
            <Button variant="outline" size="28" onClick={toList}>
              {t("tasks.quickCommands.form.cancel")}
            </Button>
            <Button
              variant="danger"
              size="28"
              loading={remove.isPending}
              disabled={remove.isPending}
              onClick={() => remove.mutate(item, { onSuccess: toList })}
            >
              {t("tasks.quickCommands.manage.removeConfirm")}
            </Button>
          </>
        }
      />
    );
  }

  const groups = [
    { visibility: "PRIVATE" as const, items: list.data?.private ?? [] },
    { visibility: "SHARED" as const, items: list.data?.shared ?? [] },
  ];
  const empty = groups.every((group) => group.items.length === 0);
  return (
    <Dialog
      open={open}
      onClose={close}
      width={520}
      title={t("tasks.quickCommands.manage.title")}
      description={t("tasks.quickCommands.manage.description")}
      footer={
        <>
          <Button
            variant="outline"
            size="28"
            onClick={() => setMode({ kind: "form", editing: null })}
          >
            <Icon name="plus" size={14} />
            {t("tasks.quickCommands.manage.add")}
          </Button>
          <Button size="28" onClick={close}>
            {t("tasks.quickCommands.manage.done")}
          </Button>
        </>
      }
    >
      {empty ? (
        <p className="py-6 text-center text-body-sm text-fg-muted">
          {list.isError
            ? t("tasks.quickCommands.manage.loadFailed")
            : t("tasks.quickCommands.manage.empty")}
        </p>
      ) : (
        <div className="flex flex-col gap-4">
          {groups
            .filter((group) => group.items.length > 0)
            .map((group) => (
              <QuickCommandGroup
                key={group.visibility}
                visibility={group.visibility}
                items={group.items}
                busy={busy}
                onReorder={(ordered) =>
                  reorder.mutate({ visibility: group.visibility, ordered })
                }
                onEdit={(item) => setMode({ kind: "form", editing: item })}
                onDelete={(item) => setMode({ kind: "confirm", item })}
              />
            ))}
        </div>
      )}
    </Dialog>
  );
}
