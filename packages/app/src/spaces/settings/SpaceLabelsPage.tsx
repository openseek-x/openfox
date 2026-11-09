import { LabelsPage } from "../../settings/labels/LabelsPage";
import type { SpacePageProps } from "./GeneralPage";
import { RetiredBanner } from "./parts";

/** 空间任务标签：与全局配置的标签页同组件，范围固定为本空间。 */
export function SpaceLabelsPage({ space, canManage, retired }: SpacePageProps) {
  return (
    <div className="flex flex-col">
      {retired && (
        <div className="mx-auto w-full max-w-[1004px] px-12 pt-6">
          <RetiredBanner />
        </div>
      )}
      <LabelsPage space={{ id: space.id, canManage: canManage && !retired }} />
    </div>
  );
}
