import { Link, useParams } from "react-router-dom";

import type { IntegrationProvider } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Icon, Skeleton, cn } from "@openfox/ui";

import { SettingsColumn, SettingsSection } from "../spaces/settings/parts";
import {
  GROUPS,
  cardsFor,
  enabledProviders,
  enabledSummaryKey,
  type Card,
} from "./model";
import { ProviderIcon } from "./ProviderIcon";
import { useIntegrationOverview } from "./queries";

// 外部应用集成总览；当前不提供「催一催」操作。

export function IntegrationsPage() {
  const { orgId = "" } = useParams();
  const t = useT();
  const overview = useIntegrationOverview(orgId);
  const providers = overview.data?.providers ?? [];
  const enabled = enabledProviders(providers);
  return (
    <SettingsColumn wide>
      <header className="flex flex-col gap-1 px-1">
        <h1 className="text-title font-bold">{t("integrations.title")}</h1>
        <p className="text-body-sm text-fg-muted">
          {t("integrations.description")}
        </p>
      </header>
      {overview.isPending ? (
        <div className="grid grid-cols-2 gap-3" aria-busy>
          {Array.from({ length: 4 }, (_, index) => (
            <Skeleton key={index} className="h-[72px] rounded-xl" />
          ))}
        </div>
      ) : overview.isError ? (
        <p role="alert" className="text-body text-fg-muted">
          {t("integrations.loadFailed")}
        </p>
      ) : (
        <>
          {enabled.length > 0 && (
            <SettingsSection title={t("integrations.groups.enabled")}>
              <div className="grid grid-cols-4 gap-3">
                {enabled.map((provider) => (
                  <EnabledCard
                    key={provider.id}
                    orgId={orgId}
                    provider={provider}
                  />
                ))}
              </div>
            </SettingsSection>
          )}
          {GROUPS.map((group) => (
            <SettingsSection key={group.category} title={t(group.title)}>
              <div className="grid grid-cols-2 gap-3">
                {cardsFor(providers, group.category).map((card) => (
                  <ProviderCard key={card.key} orgId={orgId} card={card} />
                ))}
              </div>
            </SettingsSection>
          ))}
        </>
      )}
    </SettingsColumn>
  );
}

function EnabledCard({
  orgId,
  provider,
}: {
  orgId: string;
  provider: IntegrationProvider;
}) {
  const t = useT();
  const attention = provider.summary.status === "attention";
  return (
    <Link
      to={`/o/${orgId}/settings/integrations/${provider.id}`}
      className="flex items-center gap-2.5 rounded-xl border border-line-default bg-surface-card p-3 hover:bg-surface-hover"
    >
      <ProviderIcon name={provider.name} size={28} />
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-body font-medium">{provider.name}</span>
        <span className="truncate text-caption text-fg-muted">
          {t(enabledSummaryKey(provider), {
            count: provider.summary.connections,
            name: provider.name,
          })}
        </span>
      </div>
      <span
        role="img"
        aria-label={t(
          attention
            ? "integrations.status.attention"
            : "integrations.status.ok",
        )}
        className={cn(
          "size-2 shrink-0 rounded-full",
          attention ? "bg-warning" : "bg-accent",
        )}
      />
    </Link>
  );
}

function ProviderCard({ orgId, card }: { orgId: string; card: Card }) {
  const t = useT();
  const { provider, name } = card;
  const body = (
    <>
      <ProviderIcon name={name} />
      <span className="min-w-0 flex-1 truncate text-body font-medium">
        {name}
      </span>
    </>
  );
  if (provider.availability === "coming_soon")
    return (
      <div
        aria-disabled="true"
        data-testid={`provider-${card.key}`}
        className="flex h-[60px] items-center gap-3 rounded-xl border border-line-default bg-surface-card px-4 opacity-60"
      >
        {body}
        <Badge tone="muted">{t("integrations.comingSoon")}</Badge>
      </div>
    );
  return (
    <Link
      to={`/o/${orgId}/settings/integrations/${provider.id}`}
      data-testid={`provider-${card.key}`}
      className="flex h-[60px] items-center gap-3 rounded-xl border border-line-default bg-surface-card px-4 hover:bg-surface-hover"
    >
      {body}
      <Icon name="chevron-right" className="text-fg-muted" />
    </Link>
  );
}
