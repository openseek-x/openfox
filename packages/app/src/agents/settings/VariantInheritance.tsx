import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { AgentVariantGroup, SpaceAgent } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, useToast } from "@openfox/ui";
import { useApi } from "../../context";
import { agentErrorMessage, storeAgent } from "../queries";
import { useAgentSettingsParam } from "../params";

const groups: AgentVariantGroup[] = [
  "instructions",
  "computer",
  "runtime",
  "directory",
  "limits",
  "repos",
  "env",
  "run_settings",
];

/** 分身组继承状态与恢复操作；服务端版本和权限仍是最终约束。 */
export function VariantInheritance({
  orgId,
  agent,
}: {
  orgId: string;
  agent: SpaceAgent;
}) {
  const t = useT();
  const toast = useToast();
  const api = useApi();
  const qc = useQueryClient();
  const drawer = useAgentSettingsParam();
  const [saving, setSaving] = useState<AgentVariantGroup | null>(null);
  async function reset(group: AgentVariantGroup) {
    if (saving) return;
    setSaving(group);
    try {
      const saved = await api.agents.resetInheritance(
        orgId,
        agent.space_id,
        agent.id,
        [group],
      );
      storeAgent(qc, orgId, saved);
    } catch (error) {
      toast(agentErrorMessage(t, error), { tone: "error" });
    } finally {
      setSaving(null);
    }
  }
  return (
    <div className="space-y-3 rounded-xl border border-line-default bg-surface-card p-4">
      <h3 className="text-body font-medium">
        {t("agents.variants.inheritance")}
      </h3>
      {agent.parent_unavailable && (
        <p role="status" className="text-body-sm text-fg-muted">
          {t("agents.variants.parentUnavailable")}
        </p>
      )}
      {agent.parent && (
        <button
          type="button"
          className="text-body text-accent hover:underline"
          onClick={() => drawer.open(agent.parent!.id, agent.parent!.space_id)}
        >
          {t("agents.variants.inheritedFrom", { name: agent.parent.name })}
        </button>
      )}
      {agent.parent && (
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          {groups.map((group) => {
            const inherited = agent.inherited_groups?.includes(group);
            return (
              <div
                key={group}
                className="flex items-center justify-between gap-2 rounded-md bg-surface-muted p-2 text-body-sm"
              >
                <span>{t(`agents.variants.groups.${group}`)}</span>
                {inherited ? (
                  <span className="text-fg-muted">
                    {t("agents.variants.inheritedFrom", {
                      name: agent.parent!.name,
                    })}
                  </span>
                ) : (
                  <Button
                    size="28"
                    variant="outline"
                    loading={saving === group}
                    onClick={() => void reset(group)}
                  >
                    {t("agents.variants.reset")}
                  </Button>
                )}
              </div>
            );
          })}
        </div>
      )}
      {agent.parent && (
        <p className="text-caption text-fg-muted">
          {t("agents.variants.readOnly")}
        </p>
      )}
      {agent.variants && agent.variants.length > 0 && (
        <>
          <p className="text-caption text-fg-muted">
            {t("agents.variants.children")}
          </p>
          <div className="flex flex-wrap gap-2">
            {agent.variants.map((variant) => (
              <button
                key={variant.id}
                type="button"
                className="rounded-md border border-line-default px-2 py-1 text-body-sm hover:bg-surface-hover"
                onClick={() => drawer.open(variant.id, variant.space_id)}
              >
                {variant.name}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
