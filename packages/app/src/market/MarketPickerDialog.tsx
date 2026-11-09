import { useState } from "react";

import { useT } from "@openfox/i18n";
import { Dialog } from "@openfox/ui";

import { MarketBrowser } from "./MarketBrowser";
import { MarketSkillDialog } from "./MarketSkillDialog";

/** 空间技能页的「从技能市场添加」：弹窗里浏览市场技能，点卡片看详情再添加到当前空间。 */
export function MarketPickerDialog({
  orgId,
  spaceId,
  onClose,
  onOpenSkill,
}: {
  orgId: string;
  spaceId: string;
  onClose: () => void;
  /** 添加成功（或空间里已有）后打开该技能 */
  onOpenSkill: (skillId: string) => void;
}) {
  const t = useT();
  const [selected, setSelected] = useState("");
  return (
    <>
      <Dialog
        open
        onClose={selected ? undefined : onClose}
        title={t("market.picker.title")}
        width={960}
        className="h-5/6"
      >
        <div className="min-h-0 flex-1 overflow-y-auto">
          <MarketBrowser
            orgId={orgId}
            skillsOnly
            onOpenSkill={setSelected}
            onOpenTemplate={() => undefined}
          />
        </div>
      </Dialog>
      {selected && (
        <MarketSkillDialog
          key={selected}
          orgId={orgId}
          marketSkillId={selected}
          spaceId={spaceId}
          onClose={() => setSelected("")}
          onOpenAdded={(_space, skillId) => onOpenSkill(skillId)}
        />
      )}
    </>
  );
}
