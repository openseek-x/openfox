import { useState } from "react";
import { Link, useParams } from "react-router-dom";

import type { Iteration } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Button, Icon, Segmented, Spinner } from "@openfox/ui";

import { useSpaceDetail } from "../spaces/queries";
import {
  BackBar,
  PageHeading,
  RetiredBanner,
  SettingsColumn,
} from "../spaces/settings/parts";
import { TaskState } from "../tasks/TaskWorkspace";
import { formatIterationRange } from "./dates";
import { CreateIterationDialog, IterationMenu } from "./IterationMenu";
import { IterationStatusBadge, iterationCounts, todayIn } from "./parts";
import {
  iterationDisplayName,
  useIterationSettings,
  useIterations,
} from "./queries";

type Scope = "current" | "archived";
const QUERY = {
  current: "planned,active",
  archived: "completed,canceled",
} as const;

/** 迭代列表 /o/:org/spaces/:space/iterations。 */
export function IterationsPage() {
  const t = useT();
  const { locale } = useI18n();
  const { orgId = "", spaceId = "" } = useParams();
  const space = useSpaceDetail(orgId, spaceId);
  const settings = useIterationSettings(orgId, spaceId);
  const [scope, setScope] = useState<Scope>("current");
  const [creating, setCreating] = useState(false);
  const list = useIterations(orgId, spaceId, { status: QUERY[scope] });

  if (space.isLoading || settings.isLoading) {
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );
  }
  if (!space.data || !settings.data) return null;
  const retired = space.data.status === "retired";
  const canManage = space.data.can_manage && !retired;
  const { enabled, mode, timezone } = settings.data;
  const manual = mode === "manual";
  const settingsLink = `/o/${orgId}/settings/spaces/${spaceId}/iterations`;

  if (!enabled) {
    return (
      <div className="flex h-full flex-col">
        <BackBar
          to={`/o/${orgId}/spaces/${spaceId}/tasks`}
          label={space.data.name}
        />
        <SettingsColumn>
          <PageHeading
            title={t("spaces.iteration.title")}
            description={t("spaces.iteration.disabledDescription")}
          />
          {canManage && (
            <Link
              className="text-body text-accent hover:underline"
              to={settingsLink}
            >
              {t("spaces.iteration.enable")}
            </Link>
          )}
        </SettingsColumn>
      </div>
    );
  }

  const iterations = list.data?.iterations ?? [];
  const hasActive = iterations.some((item) => item.status === "active");
  const nextPlanned = iterations.find((item) => item.status === "planned");
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <h1 className="font-medium">
          {space.data.name} · {t("spaces.iteration.title")}
        </h1>
        <Segmented
          label={t("spaces.iteration.title")}
          value={scope}
          onChange={setScope}
          options={[
            { value: "current", label: t("spaces.iteration.title") },
            { value: "archived", label: t("spaces.iteration.archived") },
          ]}
          className="ml-2"
        />
        <div className="ml-auto flex items-center gap-2">
          {canManage && (
            <Link
              className="text-body-sm text-fg-muted hover:text-fg-default"
              to={settingsLink}
            >
              {t("spaces.iteration.mode")}
            </Link>
          )}
          {canManage && manual && (
            <Button
              size="28"
              leading={<Icon name="plus" />}
              onClick={() => setCreating(true)}
            >
              {t("spaces.iteration.newIteration")}
            </Button>
          )}
        </div>
      </header>
      {retired && (
        <div className="px-4 pt-3">
          <RetiredBanner message={t("spaces.task.retiredBanner")} />
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-auto p-4">
        {!manual && scope === "current" && !list.isLoading && !hasActive && (
          <p className="mx-auto mb-3 max-w-[900px] rounded-xl bg-surface-muted px-4 py-3 text-body-sm text-fg-muted">
            {nextPlanned
              ? t("spaces.iteration.cooldownNotice", {
                  date: formatIterationRange(
                    nextPlanned.start_date,
                    nextPlanned.start_date,
                    locale,
                    timezone,
                  ).split(" – ")[0]!,
                })
              : t("spaces.iteration.noActive")}
          </p>
        )}
        {list.isLoading ? (
          <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
        ) : iterations.length === 0 ? (
          <TaskState
            title={t("spaces.iteration.noIterations")}
            desc={
              manual ? undefined : t("spaces.iteration.noIterationsAutomatic")
            }
          />
        ) : (
          <ul className="mx-auto flex max-w-[900px] flex-col gap-2">
            {iterations.map((item) => (
              <IterationRow
                key={item.id}
                orgId={orgId}
                spaceId={spaceId}
                item={item}
                manual={manual}
                canManage={canManage}
                locale={locale}
                timezone={timezone}
              />
            ))}
          </ul>
        )}
      </div>
      {manual && (
        <CreateIterationDialog
          open={creating}
          orgId={orgId}
          spaceId={spaceId}
          today={todayIn(timezone)}
          onClose={() => setCreating(false)}
        />
      )}
    </div>
  );
}

function IterationRow({
  orgId,
  spaceId,
  item,
  manual,
  canManage,
  locale,
  timezone,
}: {
  orgId: string;
  spaceId: string;
  item: Iteration;
  manual: boolean;
  canManage: boolean;
  locale: string;
  timezone: string;
}) {
  const t = useT();
  const counts = iterationCounts(item);
  return (
    <li className="flex items-center gap-3 rounded-xl border border-line-default px-4 py-3">
      <Link
        to={`/o/${orgId}/spaces/${spaceId}/iterations/${item.id}`}
        className="flex min-w-0 flex-1 items-center gap-3"
      >
        <div className="min-w-0 flex-1">
          <div className="truncate font-medium">
            {iterationDisplayName(item)}
          </div>
          <div className="text-body-sm text-fg-muted">
            {formatIterationRange(
              item.start_date,
              item.end_date,
              locale,
              timezone,
            )}
          </div>
        </div>
        <span className="text-body-sm text-fg-muted">
          {t("spaces.iteration.scope")} {counts.done}/{counts.scope}
        </span>
        {item.capacity_percent !== null && (
          <span className="text-body-sm text-fg-muted">
            {t("spaces.iteration.capacity")} {item.capacity_percent}%
          </span>
        )}
        <IterationStatusBadge status={item.status} />
      </Link>
      {canManage && (
        <IterationMenu
          key={item.version}
          orgId={orgId}
          spaceId={spaceId}
          iteration={item}
          manual={manual}
        />
      )}
    </li>
  );
}
