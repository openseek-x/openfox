import type { ConversationEffectiveConfig } from "@openfox/api";
import { useT } from "@openfox/i18n";

export function RuntimeDefaultsSummary({
  effective,
}: {
  effective?: ConversationEffectiveConfig | null;
}) {
  const t = useT();
  if (!effective?.model_source && !effective?.environment?.length) return null;
  return (
    <details className="mb-2 text-caption text-fg-muted">
      <summary>{t("computers.runtimeDefaults.effective")}</summary>
      <p>
        {effective.model} ·{" "}
        {t(
          `computers.runtimeDefaults.sources.${effective.model_source ?? "runtime"}`,
        )}
      </p>
      {!!effective.environment?.length && (
        <ul>
          {effective.environment.map((v) => (
            <li key={v.name}>
              <code>{v.name}</code> ·{" "}
              {t(`computers.runtimeDefaults.sources.${v.source}`)}
            </li>
          ))}
        </ul>
      )}
      <p>{t("computers.runtimeDefaults.gitAuthor")}</p>
    </details>
  );
}
