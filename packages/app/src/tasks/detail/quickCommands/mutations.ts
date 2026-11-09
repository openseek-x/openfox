import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef } from "react";

import {
  isApiError,
  type QuickCommand,
  type QuickCommandList,
  type QuickCommandUpdateInput,
  type QuickCommandVisibility,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../../../context";
import type { QuickCommandDraft } from "./QuickCommandForm";
import { quickCommandError, refreshQuickCommands } from "./queries";

/** 草稿 → 请求字段。目标 "agent:<id>" / "crew:<id>"；没有图标时 icon_kind 为 null */
function fields(draft: QuickCommandDraft) {
  const [kind, id] = (draft.target ?? ":").split(":");
  return {
    name: draft.name.trim(),
    prompt: draft.prompt.trim(),
    visibility: draft.visibility,
    icon_kind: draft.icon.kind,
    ...(draft.icon.kind ? { icon: draft.icon.icon ?? "" } : {}),
    agent_id: kind === "agent" ? id! : null,
    crew_id: kind === "crew" ? id! : null,
  };
}

/** 管理对话框的写操作：新建 / 修改 / 删除 / 排序。冲突时刷新列表，让用户重新操作。 */
export function useQuickCommandMutations(orgId: string, spaceId: string) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const key = keys.quickCommands(orgId, spaceId);
  // 同一份新建表单重试时复用 id，超时重试不会重复创建
  const newId = useRef<string>(crypto.randomUUID());
  const fail = (error: unknown) => {
    toast(quickCommandError(t, error), { tone: "error" });
    if (isApiError(error, "VERSION_CONFLICT"))
      void refreshQuickCommands(queryClient, orgId, spaceId);
  };
  const done = () => void refreshQuickCommands(queryClient, orgId, spaceId);

  const save = useMutation({
    mutationFn: async (input: {
      draft: QuickCommandDraft;
      editing: QuickCommand | null;
    }) => {
      const body = fields(input.draft);
      if (!input.editing)
        return api.quickCommands.create(orgId, spaceId, {
          id: newId.current,
          ...body,
        });
      const { agent_id, crew_id, ...rest } = body;
      const before = input.editing;
      const patch: QuickCommandUpdateInput = {
        version: before.version,
        ...rest,
      };
      if (
        agent_id !== (before.agent_id ?? null) ||
        crew_id !== (before.crew_id ?? null)
      )
        Object.assign(patch, { agent_id, crew_id });
      return api.quickCommands.update(orgId, spaceId, before.id, patch);
    },
    onSuccess: (_, input) => {
      toast(
        t(
          input.editing
            ? "tasks.quickCommands.toast.updated"
            : "tasks.quickCommands.toast.created",
        ),
      );
      if (!input.editing) newId.current = crypto.randomUUID();
      done();
    },
    onError: fail,
  });
  const remove = useMutation({
    mutationFn: (item: QuickCommand) =>
      api.quickCommands.remove(orgId, spaceId, item.id),
    onSuccess: () => {
      toast(t("tasks.quickCommands.toast.removed"));
      done();
    },
    onError: fail,
  });
  // 排序：先乐观更新本地顺序，失败回滚。服务端只接收本人可编辑的条目（共享分组里别人的不动）。
  const reorder = useMutation({
    mutationFn: (input: {
      visibility: QuickCommandVisibility;
      ordered: QuickCommand[];
    }) =>
      api.quickCommands.reorder(
        orgId,
        spaceId,
        input.visibility,
        input.ordered.filter((item) => item.can_edit).map((item) => item.id),
      ),
    onMutate: (input) => {
      const previous = queryClient.getQueryData<QuickCommandList>(key);
      const group = input.visibility === "PRIVATE" ? "private" : "shared";
      if (previous)
        queryClient.setQueryData(key, { ...previous, [group]: input.ordered });
      return { previous };
    },
    onSuccess: (updated, input) => {
      const group = input.visibility === "PRIVATE" ? "private" : "shared";
      queryClient.setQueryData<QuickCommandList>(
        key,
        (current) => current && { ...current, [group]: updated },
      );
    },
    onError: (error, _, context) => {
      if (context?.previous) queryClient.setQueryData(key, context.previous);
      fail(error);
    },
  });
  return { save, remove, reorder };
}
