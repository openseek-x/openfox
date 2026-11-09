import { useNavigate, useParams, useSearchParams } from "react-router-dom";

import { useT } from "@openfox/i18n";

import { MarketBrowser } from "./MarketBrowser";
import { MarketSkillDialog } from "./MarketSkillDialog";
import { MarketTemplateDialog } from "./MarketTemplateDialog";

// 市场页。详情是 Dialog，地址里的 ?skill= / ?template= 记录当前打开的项，刷新后保持。

export function MarketPage() {
  const t = useT();
  const navigate = useNavigate();
  const { orgId = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const skillId = params.get("skill") ?? "";
  const templateId = params.get("template") ?? "";

  const open = (key: "skill" | "template", id: string) =>
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      next.delete("skill");
      next.delete("template");
      next.set(key, id);
      return next;
    });
  const close = () =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete("skill");
        next.delete("template");
        return next;
      },
      { replace: true },
    );

  return (
    <div className="flex h-full flex-col">
      <h1 className="sr-only">{t("market.title")}</h1>
      <div className="flex min-h-0 flex-1 flex-col overflow-auto">
        <div className="mx-auto flex w-full max-w-[1100px] flex-1 flex-col px-8 pt-4 pb-10">
          <MarketBrowser
            orgId={orgId}
            onOpenSkill={(id) => open("skill", id)}
            onOpenTemplate={(id) => open("template", id)}
          />
        </div>
      </div>
      {skillId && (
        <MarketSkillDialog
          key={skillId}
          orgId={orgId}
          marketSkillId={skillId}
          onClose={close}
          onOpenAdded={(spaceId, id) =>
            navigate(`/o/${orgId}/spaces/${spaceId}/skills?skill=${id}`)
          }
        />
      )}
      {templateId && (
        <MarketTemplateDialog
          key={templateId}
          orgId={orgId}
          templateId={templateId}
          onClose={close}
        />
      )}
    </div>
  );
}
