import { useRef, useState } from "react";

import type { Iteration, IterationCompleteInput } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  DateFieldPopover,
  Dialog,
  Field,
  Icon,
  Menu,
  MenuItem,
  Radio,
  TextInput,
  Textarea,
} from "@openfox/ui";

import {
  iterationDisplayName,
  useCompleteIteration,
  useCreateIteration,
  useDeleteIteration,
  useStartIteration,
  useUpdateIteration,
  useUpdateIterationDates,
} from "./queries";

type Dialogs =
  "start" | "complete" | "delete" | "edit" | "dates" | "end" | null;

/** 迭代行 / 详情头共用的「···」菜单与其确认弹窗。手动模式才有开始、完成、删除。 */
export function IterationMenu({
  orgId,
  spaceId,
  iteration,
  manual,
  onDeleted,
}: {
  orgId: string;
  spaceId: string;
  iteration: Iteration;
  manual: boolean;
  onDeleted?: () => void;
}) {
  const t = useT();
  const [dialog, setDialog] = useState<Dialogs>(null);
  const close = () => setDialog(null);
  const start = useStartIteration(orgId, spaceId);
  const complete = useCompleteIteration(orgId, spaceId);
  const remove = useDeleteIteration(orgId, spaceId);
  const open = iteration.status === "planned" || iteration.status === "active";
  const removable =
    iteration.status === "planned" || iteration.status === "canceled";
  const base = { iterationId: iteration.id, version: iteration.version };
  if (!open && !removable) return null;
  return (
    <>
      <Menu
        label={t("spaces.iteration.title")}
        trigger={({ ref, toggle }) => (
          <Button
            ref={ref}
            variant="outline"
            size="28"
            onClick={toggle}
            leading={<Icon name="more" />}
          >
            <span className="sr-only">{t("spaces.iteration.title")}</span>
          </Button>
        )}
      >
        {manual && iteration.status === "planned" && (
          <MenuItem onSelect={() => setDialog("start")}>
            {t("spaces.iteration.startAction")}
          </MenuItem>
        )}
        {manual && iteration.status === "active" && (
          <MenuItem onSelect={() => setDialog("complete")}>
            {t("spaces.iteration.completeAction")}
          </MenuItem>
        )}
        {open && (
          <MenuItem onSelect={() => setDialog("edit")}>
            {t("spaces.iteration.editAction")}
          </MenuItem>
        )}
        {open && (
          <MenuItem onSelect={() => setDialog("dates")}>
            {t("spaces.iteration.changeDates")}
          </MenuItem>
        )}
        {open && (
          <MenuItem onSelect={() => setDialog("end")}>
            {t("spaces.iteration.moveEndDate")}
          </MenuItem>
        )}
        {manual && removable && (
          <MenuItem onSelect={() => setDialog("delete")}>
            {t("spaces.iteration.deleteAction")}
          </MenuItem>
        )}
      </Menu>
      <ConfirmDialog
        open={dialog === "start"}
        title={t("spaces.iteration.startTitle")}
        description={t("spaces.iteration.startDescription")}
        onCancel={close}
        onConfirm={() => start.mutate(base, { onSuccess: close })}
        loading={start.isPending}
      />
      <CompleteDialog
        open={dialog === "complete"}
        loading={complete.isPending}
        onCancel={close}
        onConfirm={(move) =>
          complete.mutate(
            { ...base, move_unfinished_to: move },
            { onSuccess: close },
          )
        }
      />
      <ConfirmDialog
        open={dialog === "delete"}
        title={t("spaces.iteration.deleteTitle")}
        description={t("spaces.iteration.deleteDescription")}
        onCancel={close}
        onConfirm={() =>
          remove.mutate(base, {
            onSuccess: () => {
              close();
              onDeleted?.();
            },
          })
        }
        loading={remove.isPending}
        danger
      />
      <EditDialog
        open={dialog === "edit"}
        orgId={orgId}
        spaceId={spaceId}
        iteration={iteration}
        onClose={close}
      />
      <DatesDialog
        open={dialog === "dates" || dialog === "end"}
        endOnly={dialog === "end"}
        orgId={orgId}
        spaceId={spaceId}
        iteration={iteration}
        onClose={close}
      />
    </>
  );
}

function CompleteDialog({
  open,
  loading,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  loading: boolean;
  onCancel: () => void;
  onConfirm: (move: IterationCompleteInput["move_unfinished_to"]) => void;
}) {
  const t = useT();
  const [move, setMove] =
    useState<IterationCompleteInput["move_unfinished_to"]>("next");
  return (
    <ConfirmDialog
      open={open}
      title={t("spaces.iteration.completeTitle")}
      description={t("spaces.iteration.completeDescription")}
      onCancel={onCancel}
      onConfirm={() => onConfirm(move)}
      loading={loading}
    >
      <div role="radiogroup" className="flex flex-col gap-2">
        {(["next", "backlog"] as const).map((item) => (
          <label
            key={item}
            className="flex cursor-pointer items-center gap-2 text-body"
          >
            <Radio
              name="iteration-move"
              checked={move === item}
              onChange={() => setMove(item)}
            />
            {t(
              item === "next"
                ? "spaces.iteration.moveToNext"
                : "spaces.iteration.moveToBacklog",
            )}
          </label>
        ))}
      </div>
    </ConfirmDialog>
  );
}

function EditDialog({
  open,
  orgId,
  spaceId,
  iteration,
  onClose,
}: {
  open: boolean;
  orgId: string;
  spaceId: string;
  iteration: Iteration;
  onClose: () => void;
}) {
  const t = useT();
  const update = useUpdateIteration(orgId, spaceId);
  const [name, setName] = useState(iteration.name);
  const [description, setDescription] = useState(iteration.description);
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("spaces.iteration.editTitle")}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={update.isPending}
            onClick={() =>
              update.mutate(
                {
                  iterationId: iteration.id,
                  version: iteration.version,
                  name,
                  description,
                },
                { onSuccess: onClose },
              )
            }
          >
            {t("spaces.iteration.confirm")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <Field label={t("spaces.iteration.name")}>
          <TextInput
            value={name}
            maxLength={80}
            placeholder={iterationDisplayName(iteration)}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("spaces.iteration.descriptionLabel")}>
          <Textarea
            value={description}
            maxLength={4000}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
      </div>
    </Dialog>
  );
}

/** 日期按钮 + 日历弹层；值是空间时区的日历日 YYYY-MM-DD。 */
export function DateButton({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string | null;
  onChange: (value: string) => void;
}) {
  const t = useT();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  return (
    <Field label={label}>
      <button
        ref={anchor}
        type="button"
        className="h-9 rounded-lg border border-line-default px-3 text-left text-body hover:bg-surface-hover"
        onClick={() => setOpen(true)}
      >
        {value ?? t("ui.date.pick")}
      </button>
      <DateFieldPopover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        value={value}
        onChange={(next) => next && onChange(next)}
      />
    </Field>
  );
}

function DatesDialog({
  open,
  endOnly,
  orgId,
  spaceId,
  iteration,
  onClose,
}: {
  open: boolean;
  endOnly: boolean;
  orgId: string;
  spaceId: string;
  iteration: Iteration;
  onClose: () => void;
}) {
  const t = useT();
  const update = useUpdateIterationDates(orgId, spaceId);
  const [start, setStart] = useState(iteration.start_date);
  const [end, setEnd] = useState(iteration.end_date);
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t(
        endOnly
          ? "spaces.iteration.moveEndDate"
          : "spaces.iteration.changeDates",
      )}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={update.isPending}
            disabled={end < start}
            onClick={() =>
              update.mutate(
                {
                  iterationId: iteration.id,
                  version: iteration.version,
                  ...(endOnly ? {} : { start_date: start }),
                  end_date: end,
                },
                { onSuccess: onClose },
              )
            }
          >
            {t("spaces.iteration.confirm")}
          </Button>
        </>
      }
    >
      <div className="flex gap-3">
        {!endOnly && (
          <DateButton
            label={t("spaces.iteration.startDate")}
            value={start}
            onChange={setStart}
          />
        )}
        <DateButton
          label={t("spaces.iteration.endDate")}
          value={end}
          onChange={setEnd}
        />
      </div>
    </Dialog>
  );
}

/** 手动模式新建迭代（3.10）。 */
export function CreateIterationDialog({
  open,
  orgId,
  spaceId,
  today,
  onClose,
}: {
  open: boolean;
  orgId: string;
  spaceId: string;
  today: string;
  onClose: () => void;
}) {
  const t = useT();
  const create = useCreateIteration(orgId, spaceId);
  const [name, setName] = useState("");
  const [start, setStart] = useState(today);
  const [end, setEnd] = useState(today);
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("spaces.iteration.createTitle")}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={create.isPending}
            disabled={end < start}
            onClick={() =>
              create.mutate(
                { name, start_date: start, end_date: end },
                { onSuccess: onClose },
              )
            }
          >
            {t("spaces.iteration.newIteration")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <Field label={t("spaces.iteration.name")}>
          <TextInput
            value={name}
            maxLength={80}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <div className="flex gap-3">
          <DateButton
            label={t("spaces.iteration.startDate")}
            value={start}
            onChange={setStart}
          />
          <DateButton
            label={t("spaces.iteration.endDate")}
            value={end}
            onChange={setEnd}
          />
        </div>
      </div>
    </Dialog>
  );
}
