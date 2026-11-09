import { createContext, useCallback, useContext } from "react";

import { useT } from "@openfox/i18n";
import type { MessageKey, Translate } from "@openfox/i18n";

// 飞书家族（飞书、Lark）共用同一套页面组件：服务商标识通过 Context 传入，
// 文案里的品牌词按服务商替换（不复制一份文案）。默认飞书，所以未包裹 Provider 的旧用法不变。

export const IMProviderContext = createContext<string>("feishu");

export function useIMProvider(): string {
  return useContext(IMProviderContext);
}

/** 各服务商的品牌词：中文文案里的「飞书」与英文文案里的「Feishu」换成对应的名字。 */
const BRAND: Record<string, { zh: string; en: string }> = {
  lark: { zh: "Lark", en: "Lark" },
  dingtalk: { zh: "钉钉", en: "DingTalk" },
  wecom: { zh: "企业微信", en: "WeCom" },
};

export function brandText(provider: string, text: string): string {
  const brand = BRAND[provider];
  if (!brand) return text;
  return text.replaceAll("飞书", brand.zh).replaceAll("Feishu", brand.en);
}

/** 钉钉没有扫码流程：与「扫码创建」有关的飞书文案换成钉钉自己的说明。键为飞书版的文案键。 */
const DINGTALK_OVERRIDES: Partial<Record<MessageKey, MessageKey>> = {
  "integrations.feishu.errors.credentialsInvalid":
    "integrations.dingtalk.errors.credentialsInvalid",
  "integrations.feishu.errors.appMismatch":
    "integrations.dingtalk.errors.appMismatch",
  "integrations.feishu.bots.assistant.bindSubtitle":
    "integrations.dingtalk.bots.assistantBindSubtitle",
  "integrations.feishu.bots.agents.cardDesc":
    "integrations.dingtalk.bots.agentsCardDesc",
  "integrations.feishu.bots.agents.dialogSubtitle":
    "integrations.dingtalk.bots.agentsDialogSubtitle",
  "integrations.feishu.bots.assistant.removeBody":
    "integrations.dingtalk.bots.assistantRemoveBody",
  "integrations.feishu.bots.agents.disconnectBody":
    "integrations.dingtalk.bots.agentsDisconnectBody",
};

const WECOM_OVERRIDES: Partial<Record<MessageKey, MessageKey>> = {
  "integrations.feishu.bots.assistant.cardDesc":
    "integrations.wecom.bots.assistantCardDesc",
  "integrations.feishu.bots.assistant.bindSubtitle":
    "integrations.wecom.bots.assistantBindSubtitle",
  "integrations.feishu.bots.agents.cardDesc":
    "integrations.wecom.bots.agentsCardDesc",
  "integrations.feishu.bots.agents.dialogSubtitle":
    "integrations.wecom.bots.agentsDialogSubtitle",
  "integrations.feishu.bots.assistant.removeBody":
    "integrations.wecom.bots.assistantRemoveBody",
  "integrations.feishu.bots.agents.disconnectBody":
    "integrations.wecom.bots.agentsDisconnectBody",
};

/** 与 useT 相同，但会把品牌词换成当前服务商的名字（钉钉还会替换与扫码有关的说明）。 */
export function useIMT(): Translate {
  const t = useT();
  const provider = useIMProvider();
  return useCallback<Translate>(
    (key, params) =>
      brandText(
        provider,
        t(
          (provider === "dingtalk"
            ? DINGTALK_OVERRIDES[key]
            : provider === "wecom"
              ? WECOM_OVERRIDES[key]
              : undefined) ?? key,
          params,
        ),
      ),
    [t, provider],
  );
}
