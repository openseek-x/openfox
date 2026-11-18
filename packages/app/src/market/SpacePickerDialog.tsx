import { useState } from "react";

import { useT } from "@openfox/i18n";
import { Button, Dialog, Icon, Radio, Skeleton } from "@openfox/ui";

import { useSpaces } from "../agents/queries";
import { canWriteSpace } from "../spaces/queries";

/** 选择目标空间：只列当前用户可写、未停用的空间。 */
export function SpacePickerDialog({
  orgId,
  title,
  confirmLabel,
  busy = false,
  onPick,
  onClose,
}: {
  orgId: string;
  title: string;
  confirmLabel: string;
  busy?: boolean;
  onPick: (spaceId: string) => void;
  onClose: () => void;
}) {
  const t = useT();
  const spaces = useSpaces(orgId);
  const writable = (spaces.data ?? []).filter((space) => canWriteSpace(space));
  const [picked, setPicked] = useState("");
  const current = picked || writable[0]?.id || "";
  return (
    <Dialog
      open
      onClose={busy ? undefined : onClose}
      title={title}
      width={420}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={busy}
            disabled={!current}
            onClick={() => onPick(current)}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      {spaces.isError ? (
        <p role="alert" className="text-body text-danger">
          {t("market.space.loadFailed")}
        </p>
      ) : !spaces.data ? (
        <div aria-busy="true" className="flex flex-col gap-2">
          <Skeleton className="h-8 w-full" />
          <Skeleton className="h-8 w-full" />
        </div>
      ) : writable.length === 0 ? (
        <p className="text-body-sm text-fg-muted">{t("market.space.none")}</p>
      ) : (
        <div
          role="radiogroup"
          aria-label={t("market.space.label")}
          className="flex max-h-[320px] flex-col gap-1 overflow-y-auto"
        >
          {writable.map((space) => (
            <label
              key={space.id}
              className="flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 hover:bg-surface-hover"
            >
              <Radio
                name="market-space"
                checked={current === space.id}
                onChange={() => setPicked(space.id)}
              />
              <Icon name="layout-grid" className="text-fg-muted" />
              <span className="min-w-0 flex-1 truncate text-body">
                {space.name}
              </span>
            </label>
          ))}
        </div>
      )}
    </Dialog>
  );
}
