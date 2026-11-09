import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";

import type { SpaceDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Checkbox,
  ConfirmDialog,
  Dialog,
  Icon,
  TextInput,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { useSpaceList } from "../spaces/queries";
import { SpaceIcon } from "../spaces/SpaceIcon";
import type { MemberRow } from "./MembersSettingsPage";

/**
 * 管理成员的空间：勾选即生效。移出空间、加入公开空间直接提交；加入私密空间先二次确认。
 * 该成员唯一的活跃空间不能移出（请改用从组织中移除）。列表是当前用户能读到的活跃空间。
 */
export function ManageSpacesDialog({
  orgId,
  member,
  onClose,
}: {
  orgId: string;
  member: MemberRow;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const spaces = useSpaceList(orgId);
  const [query, setQuery] = useState("");
  const [joined, setJoined] = useState(
    () => new Set(member.spaces.map((space) => space.id)),
  );
  const [busy, setBusy] = useState<string | null>(null);
  const [confirming, setConfirming] = useState<SpaceDetail | null>(null);
  const list = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return (spaces.data ?? [])
      .filter((space) => space.status === "active")
      .filter(
        (space) =>
          !needle ||
          space.name.toLowerCase().includes(needle) ||
          space.prefix.toLowerCase().includes(needle),
      )
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [spaces.data, query]);

  const apply = async (space: SpaceDetail, join: boolean) => {
    setBusy(space.id);
    try {
      if (join)
        await api.spaceMembership.put(
          orgId,
          space.id,
          member.user_id,
          "MEMBER",
        );
      else await api.spaceMembership.remove(orgId, space.id, member.user_id);
      setJoined((current) => {
        const next = new Set(current);
        if (join) next.add(space.id);
        else next.delete(space.id);
        return next;
      });
      void queryClient.invalidateQueries({ queryKey: keys.members(orgId) });
      void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
    } catch {
      toast(t("organization.members.spaceUpdateFailed"), { tone: "error" });
    } finally {
      setBusy(null);
    }
  };

  const toggle = (space: SpaceDetail) => {
    const isMember = joined.has(space.id);
    if (isMember && joined.size === 1) {
      toast(t("organization.members.onlySpace"), { tone: "error" });
      return;
    }
    if (!isMember && space.visibility === "private") setConfirming(space);
    else void apply(space, !isMember);
  };

  return (
    <>
      <Dialog
        open
        width={448}
        onClose={onClose}
        title={t("organization.members.manageTitle", { name: member.email })}
        footer={
          <Button onClick={onClose}>{t("organization.members.done")}</Button>
        }
      >
        <div className="flex flex-col gap-2">
          <TextInput
            autoFocus
            aria-label={t("organization.members.searchSpaces")}
            placeholder={t("organization.members.searchSpaces")}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          <ul className="max-h-80 overflow-y-auto">
            {list.length === 0 && (
              <li className="py-6 text-center text-body-sm text-fg-muted">
                {t("organization.members.noSpaces")}
              </li>
            )}
            {list.map((space) => {
              const isMember = joined.has(space.id);
              const only = isMember && joined.size === 1;
              return (
                <li key={space.id}>
                  <label
                    title={
                      only ? t("organization.members.onlySpace") : undefined
                    }
                    className={cn(
                      "flex h-11 cursor-pointer items-center gap-2.5 rounded-lg px-2 hover:bg-surface-hover",
                      only && "opacity-50",
                    )}
                  >
                    <Checkbox
                      checked={isMember}
                      disabled={busy !== null}
                      aria-label={space.name}
                      onChange={() => toggle(space)}
                    />
                    <SpaceIcon icon={space.icon} size={20} />
                    <span className="min-w-0 flex-1 truncate text-body">
                      {space.name}
                    </span>
                    {space.visibility === "private" && (
                      <Icon
                        name="lock"
                        size={12}
                        className="text-fg-muted"
                        aria-label={t("organization.members.private")}
                      />
                    )}
                    <span className="font-mono text-caption text-fg-muted">
                      {space.prefix}
                    </span>
                  </label>
                </li>
              );
            })}
          </ul>
        </div>
      </Dialog>
      <ConfirmDialog
        open={confirming !== null}
        title={t("organization.members.privateTitle", { name: member.email })}
        description={t("organization.members.privateBody")}
        confirmLabel={t("common.confirm")}
        onCancel={() => setConfirming(null)}
        onConfirm={() => {
          if (confirming) void apply(confirming, true);
          setConfirming(null);
        }}
      >
        {confirming && (
          <div className="flex items-center gap-2 text-body">
            <SpaceIcon icon={confirming.icon} size={16} />
            {confirming.name}
          </div>
        )}
      </ConfirmDialog>
    </>
  );
}
