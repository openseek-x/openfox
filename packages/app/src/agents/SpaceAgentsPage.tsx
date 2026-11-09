import { useMemo } from "react";
import { useParams } from "react-router-dom";

import { useT } from "@openfox/i18n";
import { Spinner } from "@openfox/ui";

import { NotFoundPage } from "../pages/StatePages";
import { isRetired, useSpaceDetail } from "../spaces/queries";
import { TaskState } from "../tasks/TaskWorkspace";
import { AgentsPage, type AgentsPageSpace } from "./AgentsPage";
import { useSpaces } from "./queries";

/**
 * 空间 Agents 页 /o/:orgId/spaces/:spaceId/agents（复测 D21）。
 * 复用全局 Agent 目录，只列当前空间；空间的取法与任务页一致：先看活跃空间列表，
 * 不在列表里（已停用）再取详情，详情 404 / 403 时显示「页面不存在」。
 */
export function SpaceAgentsPage() {
  const t = useT();
  const { orgId = "", spaceId = "" } = useParams();
  const spaces = useSpaces(orgId);
  const listed = spaces.data?.find((item) => item.id === spaceId);
  const detail = useSpaceDetail(
    orgId,
    spaces.data && !listed ? spaceId : undefined,
  );
  const found = listed ?? detail.data;
  const space = useMemo<AgentsPageSpace | undefined>(
    () =>
      found && {
        id: found.id,
        name: found.name,
        icon: found.icon ?? null,
        retired: isRetired(found),
      },
    [found],
  );

  if (spaces.data && !listed && detail.isError) return <NotFoundPage />;
  if (!space)
    return (
      <TaskState
        busy
        icon={<Spinner />}
        title={t("agents.directory.loading")}
      />
    );
  return <AgentsPage key={space.id} space={space} />;
}
