import { Navigate, useParams } from "react-router-dom";

import { isApiError } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button } from "@openfox/ui";

import {
  canManageSpace,
  isRetired,
  useOrgRole,
  useSpaceDetail,
} from "../queries";
import { spaceSettingsPath } from "../SpacesNav";
import { EstimatesPage } from "./EstimatesPage";
import { GeneralPage, type SpacePageProps } from "./GeneralPage";
import { SpaceMembersPage } from "./MembersPage";
import { BackBar, SettingsColumn, SettingsSection } from "./parts";
import { SpaceLabelsPage } from "./SpaceLabelsPage";
import { TaskTypeDetailPage, TaskTypesPage } from "./TaskTypesPage";
import { IterationSettingsPage } from "./IterationSettingsPage";
import { TriageSettingsPage } from "./TriageSettingsPage";

/**
 * 空间设置路由 /o/:orgId/settings/spaces/:spaceId/:section。
 * 空间不存在、已删除或不可读时显示「未找到空间」（1.10）。
 */
export function SpaceSettingsPage() {
  const t = useT();
  const params = useParams();
  const { orgId = "", spaceId = "", typeId } = params;
  // 类型详情路由 task-types/:typeId 没有 section 参数
  const section = typeId ? "task-types" : (params.section ?? "general");
  const detail = useSpaceDetail(orgId, spaceId);
  const orgRole = useOrgRole(orgId);
  // 列表加载中时整页空白，不显示骨架屏
  if (detail.isLoading) return null;
  if (detail.isError || !detail.data) {
    const notFound =
      !detail.error ||
      (isApiError(detail.error) && detail.error.status === 404);
    return (
      <div className="flex flex-col">
        <BackBar
          to={`/o/${orgId}/settings/spaces`}
          label={t("spaces.list.title")}
        />
        <SettingsColumn>
          <SettingsSection
            title={
              notFound ? t("spaces.notFound.title") : t("spaces.loadFailed")
            }
            description={
              notFound ? t("spaces.notFound.description") : undefined
            }
            action={
              notFound ? undefined : (
                <Button
                  variant="outline"
                  size="28"
                  onClick={() => void detail.refetch()}
                >
                  {t("common.retry")}
                </Button>
              )
            }
          />
        </SettingsColumn>
      </div>
    );
  }
  const space = detail.data;
  // 访客（VIEWER）不进入设置，直接去任务页
  if (space.my_role === "VIEWER" && orgRole !== "OWNER" && orgRole !== "ADMIN")
    return <Navigate to={`/o/${orgId}/spaces/${space.id}/tasks`} replace />;
  const props: SpacePageProps = {
    orgId,
    space,
    canManage: canManageSpace(space, orgRole),
    retired: isRetired(space),
  };
  if (section === "general" || section === "repos")
    return (
      <GeneralPage
        {...props}
        focus={section === "repos" ? "repos" : undefined}
      />
    );
  const back = (
    <BackBar to={spaceSettingsPath(orgId, space.id)} label={space.name} />
  );
  const page = (() => {
    switch (section) {
      case "members":
        return <SpaceMembersPage {...props} />;
      case "task-types":
        return typeId ? (
          <TaskTypeDetailPage {...props} typeId={typeId} />
        ) : (
          <TaskTypesPage {...props} />
        );
      case "labels":
        return <SpaceLabelsPage {...props} />;
      case "estimates":
        return <EstimatesPage {...props} />;
      case "iterations":
        return <IterationSettingsPage {...props} />;
      case "triage":
        return <TriageSettingsPage {...props} />;
      default:
        return null;
    }
  })();
  if (!page)
    return <Navigate to={spaceSettingsPath(orgId, space.id)} replace />;
  return (
    <div className="flex flex-col">
      {section === "task-types" && typeId ? (
        <BackBar
          to={spaceSettingsPath(orgId, space.id, "task-types")}
          label={t("spaces.sections.taskTypes")}
        />
      ) : (
        back
      )}
      {page}
    </div>
  );
}
