import { Navigate, useParams } from "react-router-dom";

import { useT, type MessageKey, type Translate } from "@openfox/i18n";
import { Icon, Switch, cn, useToast } from "@openfox/ui";

import {
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "../spaces/settings/parts";
import { useBetaFeatures, useBetaTier, useSaveBetaFeature } from "./queries";

// 内测功能开关：只有内测账号可见，其余账号回到个人资料。

/**
 * 功能的名称与说明按 key 从文案里取（me.beta.features.<key>.name / .description）；
 * 取不到时名称显示 key，说明不显示。
 */
export function featureText(
  t: Translate,
  key: string,
  field: "name" | "description",
): string | undefined {
  const path = `me.beta.features.${key}.${field}`;
  const text = t(path as MessageKey);
  return text === path ? undefined : text;
}

export function BetaFeaturesPage() {
  const { orgId = "" } = useParams();
  const beta = useBetaTier();
  if (beta === undefined) return null;
  if (!beta) return <Navigate to={`/o/${orgId}/settings/profile`} replace />;
  return <BetaFeaturesBody />;
}

function BetaFeaturesBody() {
  const t = useT();
  const toast = useToast();
  const features = useBetaFeatures();
  const save = useSaveBetaFeature();
  // 保存中的那一项之外的开关禁用
  const pendingKey = save.isPending ? save.variables?.key : undefined;
  const list = features.data ?? [];

  return (
    <SettingsColumn>
      <SettingsSection title={t("me.beta.title")}>
        <SettingsCard>
          {list.length === 0 ? (
            <div className="flex flex-col items-center gap-2 px-4 py-10 text-center">
              <span className="flex size-11 items-center justify-center rounded-full bg-surface-muted text-fg-secondary">
                <Icon name="flask-conical" size={20} />
              </span>
              <p className="text-body font-medium text-fg-default">
                {t("me.beta.empty")}
              </p>
              <p className="text-body-sm text-fg-muted">
                {t("me.beta.emptyHelp")}
              </p>
            </div>
          ) : (
            list.map((feature) => {
              const others =
                pendingKey !== undefined && pendingKey !== feature.key;
              const name = featureText(t, feature.key, "name") ?? feature.key;
              return (
                <div key={feature.key} className={cn(others && "opacity-50")}>
                  <SettingRow
                    title={name}
                    description={featureText(t, feature.key, "description")}
                    control={
                      <Switch
                        label={name}
                        checked={feature.enabled}
                        disabled={others}
                        onCheckedChange={(enabled) =>
                          save.mutate(
                            { key: feature.key, enabled },
                            {
                              onError: () =>
                                toast(t("me.beta.saveFailed"), {
                                  tone: "error",
                                }),
                            },
                          )
                        }
                      />
                    }
                  />
                </div>
              );
            })
          )}
        </SettingsCard>
      </SettingsSection>
    </SettingsColumn>
  );
}
