import type { Device } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { InfoCard, SectionTitle, formatDateTime } from "./parts";

const names = { node: "Node.js", npm: "npm", python: "Python", uv: "uv" };
const statusKeys = {
  AVAILABLE: "computers.detail.toolAvailable",
  MISSING: "computers.detail.toolMissing",
  TIMEOUT: "computers.detail.toolTimeout",
  ERROR: "computers.detail.toolError",
} as const;

export function DevelopmentTools({
  tools,
}: {
  tools: Device["development_tools"];
}) {
  const t = useT();
  const { locale } = useI18n();
  return (
    <section
      className="flex flex-col gap-2"
      aria-label={t("computers.detail.developmentTools")}
    >
      <SectionTitle>{t("computers.detail.developmentTools")}</SectionTitle>
      <p className="text-caption text-fg-muted">
        {t("computers.detail.developmentToolsHint")}
      </p>
      {!tools?.length ? (
        <p className="text-body-sm text-fg-muted">
          {t("computers.detail.developmentToolsEmpty")}
        </p>
      ) : (
        <InfoCard>
          {tools.map((tool) => (
            <div
              key={tool.name}
              className="flex flex-col gap-1 border-b border-line-soft px-4 py-3 last:border-b-0"
            >
              <div className="flex items-center justify-between gap-3">
                <span className="text-body font-medium text-fg-default">
                  {names[tool.name]}
                </span>
                <span className="text-body-sm text-fg-secondary">
                  {t(statusKeys[tool.status])}
                </span>
              </div>
              {tool.version && (
                <span className="text-body-sm text-fg-default">
                  {tool.version}
                </span>
              )}
              {tool.path && (
                <code className="break-all text-caption text-fg-muted">
                  {tool.path}
                </code>
              )}
              <time
                dateTime={tool.checked_at}
                className="text-caption text-fg-muted"
              >
                {t("computers.detail.toolCheckedAt", {
                  time: formatDateTime(tool.checked_at, locale),
                })}
              </time>
            </div>
          ))}
        </InfoCard>
      )}
    </section>
  );
}
