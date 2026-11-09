import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactElement } from "react";
import { useNavigate } from "react-router-dom";

import type { RetireDisposition, SpaceDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Icon,
  Menu,
  MenuItem,
  MenuSeparator,
  Spinner,
  TextInput,
  Tooltip,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { putSpace, spaceErrorMessage, useSpaceList } from "../queries";
import { CloneSpaceDialog } from "./CloneSpaceDialog";
import { SettingRow, SettingsCard, SettingsSection } from "./parts";

/** 停用时可移入的目标空间：其他 ACTIVE 空间，且公开或自己是成员。 */
export function moveTargets(
  spaces: readonly SpaceDetail[],
  selfId: string,
): SpaceDetail[] {
  return spaces.filter(
    (space) =>
      space.id !== selfId &&
      space.status !== "retired" &&
      (space.visibility === "shared" || Boolean(space.my_role)),
  );
}

/** 禁用按钮包一层可聚焦的 span，悬停显示原因。 */
function WithReason({
  reason,
  children,
}: {
  reason?: string;
  children: ReactElement<Record<string, unknown>>;
}) {
  if (!reason) return children;
  return (
    <Tooltip content={<span className="block max-w-[260px]">{reason}</span>}>
      <span tabIndex={0} aria-label={reason} className="inline-flex">
        {children}
      </span>
    </Tooltip>
  );
}

/** 危险操作：停用 / 恢复、克隆到另一个组织、删除。 */
export function DangerZone({
  orgId,
  space,
  retired,
}: {
  orgId: string;
  space: SpaceDetail;
  retired: boolean;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const spaces = useSpaceList(orgId);
  const [dialog, setDialog] = useState<"retire" | "delete" | "clone" | null>(
    null,
  );
  // 只能看到自己可读的空间：这里的「最后一个活跃空间」是界面提示，最终以后端 LAST_SPACE 为准
  const activeCount = (spaces.data ?? []).length;
  const lastActive = !retired && activeCount <= 1;

  const restore = useMutation({
    mutationFn: () =>
      api.spaces.restore(orgId, space.id, { version: space.version }),
    onSuccess: (updated) => {
      putSpace(queryClient, orgId, updated);
      void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
      void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
      toast(t("spaces.danger.restored", { name: space.name }));
    },
    onError: (error) =>
      toast(
        spaceErrorMessage(t, error, "spaces.danger.restoreFailed", {
          name: space.name,
        }),
        { tone: "error" },
      ),
  });

  const retireReason = lastActive
    ? t("spaces.danger.retireLastTooltip")
    : undefined;
  const deleteReason = lastActive
    ? t("spaces.danger.deleteLastTooltip")
    : !retired
      ? t("spaces.danger.retireFirstTooltip")
      : undefined;

  return (
    <SettingsSection
      title={t("spaces.danger.title")}
      description={t("spaces.danger.description")}
    >
      <SettingsCard>
        {retired ? (
          <SettingRow
            title={t("spaces.danger.restoreTitle")}
            description={t("spaces.danger.restoreDescription")}
            control={
              <Button
                variant="outline"
                size="28"
                loading={restore.isPending}
                onClick={() => restore.mutate()}
              >
                {restore.isPending
                  ? t("spaces.danger.restoring")
                  : t("spaces.danger.restoreAction")}
              </Button>
            }
          />
        ) : (
          <SettingRow
            title={t("spaces.danger.retireTitle")}
            description={t("spaces.danger.retireDescription")}
            control={
              <WithReason reason={retireReason}>
                <Button
                  variant="outline"
                  size="28"
                  disabled={lastActive}
                  onClick={() => setDialog("retire")}
                >
                  {t("spaces.danger.retireAction")}
                </Button>
              </WithReason>
            }
          />
        )}
        <SettingRow
          title={t("spaces.danger.cloneTitle")}
          description={t("spaces.danger.cloneDescription")}
          control={
            <WithReason
              reason={
                space.can_manage
                  ? undefined
                  : t("spaces.clone.noPermissionTooltip")
              }
            >
              <Button
                variant="outline"
                size="28"
                disabled={!space.can_manage}
                onClick={() => setDialog("clone")}
              >
                {t("spaces.danger.cloneAction")}
              </Button>
            </WithReason>
          }
        />
        <SettingRow
          danger
          title={t("spaces.danger.deleteTitle")}
          description={
            lastActive
              ? t("spaces.danger.mustKeepActive")
              : retired
                ? t("spaces.danger.deleteDescription")
                : t("spaces.danger.retireFirst")
          }
          control={
            <WithReason reason={deleteReason}>
              <Button
                variant="danger-outline"
                size="28"
                disabled={Boolean(deleteReason)}
                onClick={() => setDialog("delete")}
              >
                {t("spaces.danger.deleteAction")}
              </Button>
            </WithReason>
          }
        />
      </SettingsCard>
      {dialog === "retire" && (
        <RetireDialog
          orgId={orgId}
          space={space}
          targets={moveTargets(spaces.data ?? [], space.id)}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === "clone" && (
        <CloneSpaceDialog
          orgId={orgId}
          space={space}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === "delete" && (
        <DeleteDialog
          orgId={orgId}
          space={space}
          onClose={() => setDialog(null)}
        />
      )}
    </SettingsSection>
  );
}

function useImpact(orgId: string, spaceId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.spaceImpact(orgId, spaceId),
    queryFn: () => api.spaces.impact(orgId, spaceId),
    staleTime: 0,
  });
}

/** 停用空间，弹窗宽 448：有未完成任务时选择处置方式。 */
export function RetireDialog({
  orgId,
  space,
  targets,
  onClose,
}: {
  orgId: string;
  space: SpaceDetail;
  targets: SpaceDetail[];
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const impact = useImpact(orgId, space.id);
  const [disposition, setDisposition] = useState<RetireDisposition>("cancel");
  const [target, setTarget] = useState<SpaceDetail | null>(null);
  const openTasks = impact.data?.open_tasks ?? 0;
  const retire = useMutation({
    mutationFn: () =>
      api.spaces.retire(orgId, space.id, {
        version: space.version,
        open_tasks: openTasks > 0 ? disposition : "keep",
        target_space_id:
          openTasks > 0 && disposition === "move" ? target?.id : undefined,
      }),
    onSuccess: (result) => {
      putSpace(queryClient, orgId, result.space);
      void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
      void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
      void queryClient.invalidateQueries({
        queryKey: keys.conversations(orgId),
      });
      toast(t("spaces.danger.retired", { name: space.name }));
      onClose();
    },
    onError: (error) =>
      toast(
        spaceErrorMessage(t, error, "spaces.danger.retireFailed", {
          name: space.name,
        }),
        { tone: "error" },
      ),
  });
  const needsTarget = openTasks > 0 && disposition === "move" && !target;
  const triggerLabel =
    disposition === "cancel"
      ? t("spaces.retire.choiceCancel")
      : disposition === "keep"
        ? t("spaces.retire.choiceKeep")
        : target
          ? t("spaces.retire.choiceMoveTo", { name: target.name })
          : t("spaces.retire.choiceMoveAnother");
  const outcome =
    disposition === "cancel"
      ? t("spaces.retire.unfinishedCanceled")
      : disposition === "keep"
        ? t("spaces.retire.unfinishedKept")
        : t("spaces.retire.unfinishedMoved", {
            name: target?.name ?? t("spaces.retire.chosenSpace"),
          });
  return (
    <Dialog
      open
      width={448}
      onClose={retire.isPending ? undefined : onClose}
      title={t("spaces.retire.title", { name: space.name })}
      description={t("spaces.retire.description")}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={retire.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button
            disabled={impact.isLoading || impact.isError || needsTarget}
            loading={retire.isPending}
            onClick={() => retire.mutate()}
          >
            {retire.isPending
              ? t("spaces.retire.retiring")
              : t("spaces.retire.submit")}
          </Button>
        </>
      }
    >
      {impact.isLoading ? (
        <div className="flex justify-center py-4">
          <Spinner />
        </div>
      ) : impact.isError ? (
        <p role="alert" className="text-body-sm text-danger">
          {t("spaces.retire.impactFailed")}
        </p>
      ) : openTasks > 0 ? (
        <div className="flex flex-col gap-3">
          <div className="flex items-center justify-between gap-3 rounded-xl border border-line-default px-3 py-2.5">
            <span className="text-body font-medium">
              {t("spaces.retire.unfinishedCount", { count: openTasks })}
            </span>
            <Menu
              label={t("spaces.retire.disposition")}
              placement="bottom-end"
              className="w-[224px]"
              trigger={({ ref, toggle, open }) => (
                <button
                  ref={ref}
                  type="button"
                  aria-haspopup="menu"
                  aria-expanded={open}
                  aria-label={t("spaces.retire.disposition")}
                  onClick={toggle}
                  className="flex h-8 max-w-[220px] items-center gap-1.5 rounded-lg border border-line-default px-2.5 text-body-sm hover:bg-surface-hover"
                >
                  <span className="truncate">{triggerLabel}</span>
                  <Icon
                    name="chevron-down"
                    size={14}
                    className="text-fg-muted"
                  />
                </button>
              )}
            >
              <MenuItem
                selected={disposition === "cancel"}
                onSelect={() => setDisposition("cancel")}
              >
                {t("spaces.retire.cancelTasks")}
              </MenuItem>
              <MenuItem
                selected={disposition === "keep"}
                onSelect={() => setDisposition("keep")}
              >
                {t("spaces.retire.keepTasks")}
              </MenuItem>
              {targets.length > 0 && <MenuSeparator />}
              {targets.map((item) => (
                <MenuItem
                  key={item.id}
                  selected={disposition === "move" && target?.id === item.id}
                  icon={
                    <span className="rounded bg-surface-muted px-1 font-mono text-[11px] text-fg-secondary">
                      {item.prefix.toUpperCase()}
                    </span>
                  }
                  onSelect={() => {
                    setDisposition("move");
                    setTarget(item);
                  }}
                >
                  {t("spaces.retire.moveTo", { name: item.name })}
                </MenuItem>
              ))}
            </Menu>
          </div>
          <p className="text-body-sm text-fg-muted">{outcome}</p>
        </div>
      ) : null}
    </Dialog>
  );
}

/** 删除空间：输入与空间名称完全一致后才能删除。 */
export function DeleteDialog({
  orgId,
  space,
  onClose,
}: {
  orgId: string;
  space: SpaceDetail;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const impact = useImpact(orgId, space.id);
  const [typed, setTyped] = useState("");
  const matches = typed === space.name;
  const remove = useMutation({
    mutationFn: () =>
      api.spaces.remove(orgId, space.id, {
        version: space.version,
        confirm_name: typed,
      }),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: keys.space(orgId, space.id) });
      void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
      void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
      void queryClient.invalidateQueries({
        queryKey: keys.agentDirectory(orgId),
      });
      void queryClient.invalidateQueries({ queryKey: keys.devices(orgId) });
      toast(t("spaces.danger.deleted", { name: space.name }));
      navigate(`/o/${orgId}/inbox`, { replace: true });
    },
    onError: (error) =>
      toast(
        spaceErrorMessage(t, error, "spaces.danger.deleteFailed", {
          name: space.name,
        }),
        { tone: "error" },
      ),
  });
  const submit = () => {
    if (matches && !remove.isPending) remove.mutate();
  };
  return (
    <Dialog
      open
      width={448}
      onClose={remove.isPending ? undefined : onClose}
      title={t("spaces.delete.title", { name: space.name })}
      description={t("spaces.delete.description")}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={remove.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button
            variant="danger"
            disabled={!matches}
            loading={remove.isPending}
            onClick={submit}
          >
            {remove.isPending
              ? t("spaces.delete.deleting")
              : t("spaces.delete.submit")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        {impact.data && (
          <ul className="list-inside list-disc text-body-sm text-fg-secondary">
            <li>
              {t("spaces.delete.taskCount", { count: impact.data.total_tasks })}
            </li>
            {impact.data.projects > 0 && (
              <li>
                {t("spaces.delete.projectCount", {
                  count: impact.data.projects,
                })}
              </li>
            )}
            {impact.data.agents > 0 && (
              <li>
                {t("spaces.delete.agentCount", { count: impact.data.agents })}
              </li>
            )}
            {impact.data.active_runs > 0 && (
              <li>
                {t("spaces.delete.runCount", {
                  count: impact.data.active_runs,
                })}
              </li>
            )}
            {impact.data.space_grants > 0 && (
              <li>
                {t("spaces.delete.grantCount", {
                  count: impact.data.space_grants,
                })}
              </li>
            )}
          </ul>
        )}
        <p className="text-body-sm text-fg-muted">
          {t("spaces.common.typeToConfirm", { value: space.name })}
        </p>
        <TextInput
          autoFocus
          aria-label={t("spaces.delete.confirmInput")}
          placeholder={space.name}
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              submit();
            }
          }}
        />
      </div>
    </Dialog>
  );
}
