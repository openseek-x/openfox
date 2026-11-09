import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Tabs } from "@openfox/ui";
import { FeishuAgentsSection } from "../../integrations/FeishuBotSections";
import { IMProviderContext } from "../../integrations/im-provider";
import { WeChatAgentsSection } from "../../integrations/WeChatSections";

export function AgentImDialog({
  orgId,
  agentId,
  name,
  onClose,
}: {
  orgId: string;
  agentId: string;
  name: string;
  onClose: () => void;
}) {
  const t = useT();
  const navigate = useNavigate();
  const [provider, setProvider] = useState("feishu");
  const tabs = (["feishu", "lark", "dingtalk", "wecom", "wechat"] as const).map(
    (id) => ({
      id,
      label: t(`agents.sections.triggers.imProviders.${id}`),
    }),
  );
  const providerName = tabs.find((tab) => tab.id === provider)!.label;
  const title = t("integrations.sections.agents", { name: providerName });
  const description = t("integrations.sectionDesc.agents", {
    name: providerName,
  });
  return (
    <Dialog
      open
      onClose={onClose}
      width={600}
      title={t("agents.sections.triggers.addIm")}
      description={name}
    >
      <div className="flex flex-col gap-4">
        <Tabs
          label={t("agents.sections.triggers.addIm")}
          value={provider}
          onChange={setProvider}
          tabs={tabs}
        />
        {provider === "wechat" ? (
          <WeChatAgentsSection
            key={provider}
            orgId={orgId}
            agentId={agentId}
            title={title}
            description={description}
          />
        ) : (
          <IMProviderContext.Provider key={provider} value={provider}>
            <FeishuAgentsSection
              orgId={orgId}
              agentId={agentId}
              title={title}
              description={description}
            />
          </IMProviderContext.Provider>
        )}
        <Button
          variant="ghost"
          size="28"
          className="self-start"
          onClick={() => {
            onClose();
            navigate(`/o/${orgId}/settings/integrations/${provider}`);
          }}
        >
          {t("integrations.title")}
        </Button>
      </div>
    </Dialog>
  );
}
