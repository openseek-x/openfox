import { useState } from "react";

import type { SpaceAgent } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Dialog,
  Skeleton,
  Tabs,
  cn,
  useToast,
} from "@openfox/ui";

import { CreateAgentDialog } from "../agents/CreateAgentDialog";
import { Markdown } from "../conversations/Markdown";
import { MarketIcon } from "./MarketIcon";
import { pickText } from "./model";
import {
  marketErrorText,
  useApplyMarketTemplate,
  useMarketTemplate,
} from "./queries";
import { SpacePickerDialog } from "./SpacePickerDialog";

// Agent 模板详情：简介、关键词、快速提示、包含技能、Agent 指令；
// 底栏「使用模板创建 Agent」：选空间 → 打开创建 Agent 弹窗并预填 → 创建后装配模板技能并用 toast 汇总。

type Tab = "overview" | "instructions";

/** 职位（title）字段上限与创建弹窗一致 */
const TITLE_MAX = 64;

export function MarketTemplateDialog({
  orgId,
  templateId,
  onClose,
}: {
  orgId: string;
  templateId: string;
  onClose: () => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const toast = useToast();
  const detail = useMarketTemplate(orgId, templateId);
  const apply = useApplyMarketTemplate(orgId);
  const [tab, setTab] = useState<Tab>("overview");
  const [step, setStep] = useState<"idle" | "space" | "create">("idle");
  const [spaceId, setSpaceId] = useState("");
  const template = detail.data;
  const name = template ? pickText(template.name, locale) : "";
  const jobTitle = template ? pickText(template.job_title, locale) : "";
  const instructions = template ? pickText(template.instructions, locale) : "";

  const onCreated = (agent: SpaceAgent) => {
    apply.mutate(
      { spaceId: agent.space_id, agentId: agent.id, templateId },
      {
        onSuccess: ({ items }) => {
          const added = items.filter((i) =>
            ["added", "reused", "attached"].includes(i.status),
          ).length;
          const missing = items.filter((i) => i.status === "missing").length;
          const skipped = items.filter((i) => i.status === "skipped").length;
          toast(t("market.template.applied", { added, missing, skipped }), {
            tone: skipped > 0 ? "info" : "success",
          });
        },
        onError: (error) =>
          toast(
            `${t("market.template.applyFailed")} ${marketErrorText(t, error, "market.template.applyFailed")}`,
            { tone: "error" },
          ),
      },
    );
    setStep("idle");
    onClose();
  };

  return (
    <>
      <Dialog
        open
        onClose={step === "idle" ? onClose : undefined}
        title={template ? name : t("common.loading")}
        width={720}
        className="h-5/6 gap-3"
        footer={
          template ? (
            <>
              <Button variant="outline" onClick={onClose}>
                {t("common.close")}
              </Button>
              <Button onClick={() => setStep("space")}>
                {t("market.template.use")}
              </Button>
            </>
          ) : undefined
        }
      >
        {detail.isError ? (
          <div role="alert" className="flex flex-col items-start gap-2">
            <p className="text-body text-danger">
              {t("market.template.loadFailed")}
            </p>
            <Button
              variant="outline"
              size="28"
              onClick={() => void detail.refetch()}
            >
              {t("market.preview.reload")}
            </Button>
          </div>
        ) : !template ? (
          <div aria-busy="true" className="flex flex-col gap-2">
            <Skeleton className="h-6 w-1/3" />
            <Skeleton className="h-4 w-full" />
          </div>
        ) : (
          <div className="flex min-h-0 flex-1 flex-col gap-3">
            <div className="flex items-center gap-3">
              <MarketIcon
                src={template.avatar?.url}
                name={name}
                size={48}
                round
              />
              <div className="flex min-w-0 flex-col">
                <span className="truncate text-body font-medium">{name}</span>
                {jobTitle && (
                  <span className="truncate text-body-sm text-fg-muted">
                    {jobTitle}
                  </span>
                )}
              </div>
            </div>
            <Tabs
              label={t("market.template.tabs")}
              value={tab}
              onChange={(id) => setTab(id as Tab)}
              tabs={[
                { id: "overview", label: t("market.template.overview") },
                {
                  id: "instructions",
                  label: t("market.template.instructions"),
                },
              ]}
            />
            <div className="min-h-0 flex-1 overflow-y-auto">
              {tab === "instructions" ? (
                instructions ? (
                  <Markdown source={instructions} />
                ) : (
                  <p className="text-body-sm text-fg-muted">
                    {t("market.template.noInstructions")}
                  </p>
                )
              ) : (
                <div className="flex flex-col gap-4">
                  {pickText(template.description, locale) && (
                    <p className="text-body whitespace-pre-wrap">
                      {pickText(template.description, locale)}
                    </p>
                  )}
                  {template.keywords.length > 0 && (
                    <div className="flex flex-wrap gap-1.5">
                      {template.keywords.map((word) => (
                        <Badge key={word} tone="neutral">
                          {word}
                        </Badge>
                      ))}
                    </div>
                  )}
                  {template.quick_prompts.length > 0 && (
                    <section className="flex flex-col gap-1.5">
                      <h3 className="text-body-sm font-medium text-fg-muted">
                        {t("market.template.quickPrompts")}
                      </h3>
                      <ul className="flex flex-col gap-1.5">
                        {template.quick_prompts.map((prompt, index) => (
                          <li
                            key={index}
                            className="rounded-lg border border-line-default px-3 py-2 text-body-sm"
                          >
                            {pickText(prompt, locale)}
                          </li>
                        ))}
                      </ul>
                    </section>
                  )}
                  <section className="flex flex-col gap-1.5">
                    <h3 className="text-body-sm font-medium text-fg-muted">
                      {t("market.template.skills", {
                        count: template.skills.length,
                      })}
                    </h3>
                    {template.skills.length === 0 ? (
                      <p className="text-body-sm text-fg-muted">
                        {t("market.template.noSkills")}
                      </p>
                    ) : (
                      <ul className="flex flex-col gap-1.5">
                        {template.skills.map((ref) => {
                          const label = ref.skill
                            ? pickText(ref.skill.name, locale)
                            : ref.market_skill_id;
                          return (
                            <li
                              key={ref.market_skill_id}
                              data-available={ref.available}
                              className={cn(
                                "flex items-center gap-2.5 rounded-lg border border-line-default px-3 py-2",
                                !ref.available && "opacity-50",
                              )}
                            >
                              <MarketIcon
                                src={ref.skill?.icon}
                                name={label}
                                size={24}
                              />
                              <span className="min-w-0 flex-1 truncate text-body-sm">
                                {label}
                              </span>
                              {!ref.available && (
                                <Badge tone="muted">
                                  {t("market.template.unavailable")}
                                </Badge>
                              )}
                            </li>
                          );
                        })}
                      </ul>
                    )}
                  </section>
                </div>
              )}
            </div>
          </div>
        )}
      </Dialog>
      {step === "space" && (
        <SpacePickerDialog
          orgId={orgId}
          title={t("market.template.pickSpace")}
          confirmLabel={t("common.continue")}
          onPick={(id) => {
            setSpaceId(id);
            setStep("create");
          }}
          onClose={() => setStep("idle")}
        />
      )}
      {step === "create" && template && (
        <CreateAgentDialog
          orgId={orgId}
          defaultSpaceId={spaceId}
          initial={{
            name,
            title: jobTitle.slice(0, TITLE_MAX),
            instructions,
          }}
          onClose={() => setStep("idle")}
          onCreated={onCreated}
        />
      )}
    </>
  );
}
