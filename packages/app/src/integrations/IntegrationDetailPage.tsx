import { SitePersonalSection } from "./SitePersonalSection";
import { ProjectSyncDialog, ProjectSyncList } from "./ProjectSyncDialog";
import { ProviderTokenSection } from "./ProviderTokenSection";
import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import {
  isApiError,
  type IntegrationConnection,
  type IntegrationDetail,
  type IntegrationProvider,
  type IntegrationSyncLink,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Skeleton,
  Tooltip,
} from "@openfox/ui";

import {
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "../spaces/settings/parts";
import { DisconnectDialog } from "./DisconnectDialog";
import {
  GitHubBindSection,
  GitHubMoreSection,
  GitHubOAuthProvider,
  GitHubPersonalSection,
  GitHubPrSection,
} from "./GitHubSections";
import {
  FeishuAgentsSection,
  FeishuAssistantSection,
} from "./FeishuBotSections";
import { FeishuBindSection, FeishuPersonalSection } from "./FeishuSections";
import { GitProviderContext, isGitLab, isGitProvider } from "./git-provider";
import { IMProviderContext } from "./im-provider";
import { GitHubSyncSection } from "./SyncSections";
import { WeChatAgentsSection } from "./WeChatSections";
import { WeComPersonalSection } from "./WeComPersonalSection";
import { connectionTone } from "./model";
import { ProviderIcon } from "./ProviderIcon";
import { useIntegrationDetail } from "./queries";

// 外部应用集成详情：按 template 渲染绑定、同步和其他分区。

type Section =
  "bind" | "personal" | "sync" | "pr" | "more" | "assistant" | "agents";

/** 飞书家族：飞书、Lark、钉钉和企业微信共用页面组件。 */
const isFeishuFamily = (id: string) =>
  id === "feishu" || id === "lark" || id === "dingtalk" || id === "wecom";

const SECTIONS: Record<IntegrationProvider["template"], Section[]> = {
  A: ["bind", "personal", "sync"],
  B: ["bind", "personal", "sync", "pr", "more"],
  C1: ["bind", "personal", "assistant", "agents"],
  C2: ["assistant", "agents"],
  C3: ["agents"],
  C4: ["bind", "personal", "assistant", "agents"],
};

export function IntegrationDetailPage() {
  const { orgId = "", provider = "" } = useParams();
  const t = useT();
  const detail = useIntegrationDetail(orgId, provider);
  const base = `/o/${orgId}/settings/integrations`;
  const back = (
    <Link
      to={base}
      className="flex w-fit items-center gap-1 text-body-sm text-fg-muted hover:text-fg-default"
    >
      <Icon name="chevron-left" />
      {t("integrations.title")}
    </Link>
  );
  if (detail.isPending)
    return (
      <SettingsColumn>
        {back}
        <Skeleton className="h-[120px] rounded-xl" />
      </SettingsColumn>
    );
  if (detail.isError)
    return (
      <SettingsColumn>
        {back}
        <p role="alert" className="text-body text-fg-muted">
          {isApiError(detail.error) && detail.error.status === 404
            ? t("integrations.notFound")
            : t("integrations.loadFailed")}
        </p>
      </SettingsColumn>
    );
  return (
    <SettingsColumn>
      {back}
      <DetailBody orgId={orgId} detail={detail.data} />
    </SettingsColumn>
  );
}

function DetailBody({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT();
  const { provider, can_manage: canManage } = detail;
  return (
    <>
      <header className="flex items-center gap-3 px-1">
        <ProviderIcon name={provider.name} size={40} />
        <div className="flex min-w-0 flex-col gap-0.5">
          <h1 className="text-title font-bold">{provider.name}</h1>
          <p className="text-body-sm text-fg-muted">
            {isGitLab(provider.id)
              ? t("integrations.gitlab.templateDesc", { name: provider.name })
              : t(`integrations.templateDesc.${provider.template}`, {
                  name: provider.name,
                })}
          </p>
        </div>
      </header>
      {["tapd", "pingcode", "cnb", "shortcut", "teambition"].includes(
        provider.id,
      ) ? (
        <ProviderTokenSection orgId={orgId} detail={detail} />
      ) : provider.availability === "coming_soon" ? (
        <SettingsCard>
          <SettingRow
            icon="clock"
            title={t("integrations.comingSoon")}
            description={t("integrations.comingSoonDesc", {
              name: provider.name,
            })}
          />
        </SettingsCard>
      ) : (
        <>
          {!canManage &&
            !isFeishuFamily(provider.id) &&
            provider.id !== "wechat" && (
              <Callout tone="info">
                {t("integrations.readOnly", { name: provider.name })}
              </Callout>
            )}
          {isGitProvider(provider.id) ? (
            <GitProviderContext.Provider
              value={{ id: provider.id, name: provider.name }}
            >
              <GitHubOAuthProvider orgId={orgId}>
                {SECTIONS[provider.template].map((section) => (
                  <SectionView
                    key={section}
                    section={section}
                    orgId={orgId}
                    detail={detail}
                  />
                ))}
              </GitHubOAuthProvider>
            </GitProviderContext.Provider>
          ) : (
            (provider.id === "wecom"
              ? (["personal", "assistant", "agents"] as Section[])
              : SECTIONS[provider.template]
            ).map((section) => (
              <SectionView
                key={section}
                section={section}
                orgId={orgId}
                detail={detail}
              />
            ))
          )}
        </>
      )}
    </>
  );
}

function SectionView({
  section,
  orgId,
  detail,
}: {
  section: Section;
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT();
  const navigate = useNavigate();
  const { provider } = detail;
  const name = provider.name;
  const vars = { name };
  const gitlabPr = isGitLab(provider.id) && section === "pr";
  const title = gitlabPr
    ? t("integrations.gitlab.sections.pr")
    : section === "sync" && provider.capabilities.includes("import")
      ? t("integrations.sections.syncImport", vars)
      : t(`integrations.sections.${section}`, vars);
  const description = gitlabPr
    ? t("integrations.gitlab.sectionDesc.pr")
    : t(`integrations.sectionDesc.${section}`, vars);

  if (
    section === "personal" &&
    (provider.id === "jira" || provider.id === "zentao")
  )
    return (
      <SitePersonalSection orgId={orgId} detail={detail}>
        <ConnectionList
          orgId={orgId}
          provider={provider}
          connections={detail.personal_connections}
          empty={t("integrations.emptyPersonal", vars)}
          canManage={detail.can_manage}
          personal
        />
      </SitePersonalSection>
    );
  if (isFeishuFamily(provider.id)) {
    // 飞书与 Lark 共用同一组分区，服务商标识经 Context 传给分区里的查询与文案
    const wrap = (node: ReactNode) => (
      <IMProviderContext.Provider value={provider.id}>
        {node}
      </IMProviderContext.Provider>
    );
    if (section === "bind")
      return wrap(
        <FeishuBindSection
          orgId={orgId}
          provider={provider}
          title={title}
          description={description}
        />,
      );
    if (section === "personal")
      return wrap(
        provider.id === "wecom" ? (
          <WeComPersonalSection
            orgId={orgId}
            title={title}
            description={description}
          />
        ) : (
          <FeishuPersonalSection orgId={orgId} title={title} />
        ),
      );
    if (section === "assistant")
      return wrap(
        <FeishuAssistantSection
          orgId={orgId}
          title={title}
          description={description}
        />,
      );
    if (section === "agents")
      return wrap(
        <FeishuAgentsSection
          orgId={orgId}
          title={title}
          description={description}
        />,
      );
  }

  if (provider.id === "wechat" && section === "agents")
    return (
      <WeChatAgentsSection
        orgId={orgId}
        title={title}
        description={description}
      />
    );

  if (isGitProvider(provider.id)) {
    if (section === "bind")
      return (
        <GitHubBindSection
          orgId={orgId}
          detail={detail}
          title={title}
          description={description}
        />
      );
    if (section === "personal")
      return (
        <GitHubPersonalSection
          orgId={orgId}
          detail={detail}
          title={title}
          description={description}
        />
      );
    if (section === "sync")
      return (
        <GitHubSyncSection
          orgId={orgId}
          detail={detail}
          title={title}
          description={description}
        />
      );
    if (section === "pr")
      return (
        <GitHubPrSection
          orgId={orgId}
          detail={detail}
          title={title}
          description={description}
        />
      );
    if (section === "more")
      return (
        <GitHubMoreSection
          orgId={orgId}
          title={title}
          description={description}
        />
      );
  }

  if (section === "bind")
    return (
      <SettingsSection
        title={title}
        description={description}
        action={
          <ComingButton
            provider={provider}
            canManage={detail.can_manage}
            label={t("integrations.bind")}
          />
        }
      >
        {!provider.configured && (
          <Callout tone="warning">
            {t("integrations.notConfigured", vars)}
          </Callout>
        )}
        <ConnectionList
          orgId={orgId}
          provider={provider}
          connections={detail.connections}
          empty={t("integrations.emptyBind", vars)}
          canManage={detail.can_manage}
          personal={false}
        />
      </SettingsSection>
    );
  if (section === "personal")
    return (
      <SettingsSection
        title={title}
        description={description}
        action={
          <ComingButton
            provider={provider}
            canManage
            label={t("integrations.bindMine")}
          />
        }
      >
        <ConnectionList
          orgId={orgId}
          provider={provider}
          connections={detail.personal_connections}
          empty={t("integrations.emptyPersonal", vars)}
          canManage={detail.can_manage}
          personal
        />
      </SettingsSection>
    );
  if (section === "sync")
    return (
      <SettingsSection
        title={title}
        description={description}
        action={
          <div className="flex gap-2">
            {provider.capabilities.includes("import") && (
              <Button
                variant="outline"
                size="32"
                onClick={() =>
                  navigate(
                    `/o/${orgId}/settings/import-export?source=${provider.id}&method=api`,
                  )
                }
              >
                {t("integrations.importIssues")}
              </Button>
            )}
            {provider.id === "jira" || provider.id === "zentao" ? (
              <Button
                size="32"
                onClick={() =>
                  navigate(
                    `/o/${orgId}/settings/import-export?source=${provider.id}&method=api&sync=1`,
                  )
                }
              >
                {t("integrations.newSync")}
              </Button>
            ) : (
              <Tooltip content={t("integrations.openSoon")}>
                <span tabIndex={0} className="inline-flex">
                  <Button size="32" disabled>
                    {t("integrations.newSync")}
                  </Button>
                </span>
              </Tooltip>
            )}
          </div>
        }
      >
        {provider.id === "jira" || provider.id === "zentao" ? (
          detail.connections.some((c) => c.status === "ACTIVE") ? (
            detail.connections
              .filter((c) => c.status === "ACTIVE")
              .map((c) => (
                <ProjectSyncList
                  key={c.id}
                  org={orgId}
                  connection={c.id}
                  brand={provider.name}
                />
              ))
          ) : (
            <p className="text-body text-fg-muted">
              {t("integrations.emptySync")}
            </p>
          )
        ) : (
          <SyncList
            links={detail.sync_links}
            empty={t("integrations.emptySync")}
          />
        )}
      </SettingsSection>
    );
  if (section === "pr")
    return (
      <SettingsSection title={title}>
        <SettingsCard>
          <SettingRow
            icon="link"
            title={t("integrations.prTitle")}
            description={description}
          />
        </SettingsCard>
      </SettingsSection>
    );
  if (section === "more")
    return (
      <SettingsSection title={title}>
        <SettingsCard>
          <SettingRow
            icon="git-branch"
            title={t("integrations.branchTitle")}
            description={description}
            control={
              <code className="rounded-md bg-surface-muted px-2 py-1 text-caption">
                username/identifier-title
              </code>
            }
          />
        </SettingsCard>
      </SettingsSection>
    );
  // 未开放的 assistant / agents 分区显示说明
  return (
    <SettingsSection title={title} description={description}>
      <SettingsCard>
        <SettingRow
          icon="clock"
          title={t("integrations.openSoon")}
          description={t(`integrations.disabled.${section}`, vars)}
        />
      </SettingsCard>
    </SettingsSection>
  );
}

/** 绑定按钮：bindable=false（或无管理权限）时禁用，悬停提示「即将开放」。 */
function ComingButton({
  provider,
  canManage,
  label,
}: {
  provider: IntegrationProvider;
  canManage: boolean;
  label: string;
}) {
  const t = useT();
  return (
    <Tooltip content={t("integrations.openSoon")}>
      <span tabIndex={0} className="inline-flex">
        <Button size="32" disabled={!provider.bindable || !canManage}>
          {label}
        </Button>
      </span>
    </Tooltip>
  );
}

const statusKey: Record<IntegrationConnection["status"], MessageKey> = {
  ACTIVE: "integrations.status.bound",
  NEEDS_ATTENTION: "integrations.status.needsAttention",
  DISCONNECTED: "integrations.status.disconnected",
};

function ConnectionList({
  orgId,
  provider,
  connections,
  empty,
  canManage,
  personal,
}: {
  orgId: string;
  provider: IntegrationProvider;
  connections: IntegrationConnection[];
  empty: string;
  canManage: boolean;
  personal: boolean;
}) {
  const t = useT();
  const [syncId, setSyncId] = useState("");
  const [target, setTarget] = useState<IntegrationConnection | null>(null);
  if (connections.length === 0)
    return (
      <SettingsCard>
        <SettingRow title={empty} />
      </SettingsCard>
    );
  return (
    <>
      <SettingsCard>
        {connections.map((connection) => {
          const tone = connectionTone(connection.status);
          const allowed =
            connection.status !== "DISCONNECTED" && (personal || canManage);
          return (
            <SettingRow
              key={connection.id}
              title={connection.display_name || connection.external_id}
              description={
                personal
                  ? connection.external_id
                  : t("integrations.enabledBy", {
                      email: connection.created_by_email,
                      date: connection.created_at.slice(0, 10),
                    })
              }
              control={
                <div className="flex items-center gap-2">
                  {!personal &&
                    allowed &&
                    (provider.id === "jira" || provider.id === "zentao") &&
                    connection.status === "ACTIVE" && (
                      <Button
                        size="28"
                        variant="outline"
                        onClick={() => setSyncId(connection.id)}
                      >
                        {t("imports.options.syncProjects")}
                      </Button>
                    )}
                  <Badge
                    variant="status"
                    small
                    tone={
                      tone === "ok"
                        ? "success"
                        : tone === "attention"
                          ? "warning"
                          : "muted"
                    }
                  >
                    {t(statusKey[connection.status])}
                  </Badge>
                  {allowed && (
                    <Menu
                      label={t("integrations.rowMenu", {
                        name: connection.display_name || connection.external_id,
                      })}
                      placement="bottom-end"
                      trigger={(props) => (
                        <IconButton
                          ref={props.ref}
                          label={t("integrations.rowMenu", {
                            name:
                              connection.display_name || connection.external_id,
                          })}
                          icon={<Icon name="ellipsis" />}
                          size="28"
                          onClick={props.toggle}
                        />
                      )}
                    >
                      <MenuItem
                        danger
                        icon={<Icon name="unplug" />}
                        onSelect={() => setTarget(connection)}
                      >
                        {t("integrations.disconnect")}
                      </MenuItem>
                    </Menu>
                  )}
                </div>
              }
            />
          );
        })}
      </SettingsCard>
      {syncId && (
        <ProjectSyncDialog
          org={orgId}
          connection={syncId}
          brand={provider.name}
          close={() => setSyncId("")}
        />
      )}
      {target && (
        <DisconnectDialog
          orgId={orgId}
          provider={provider}
          connection={target}
          onClose={() => setTarget(null)}
        />
      )}
    </>
  );
}

function SyncList({
  links,
  empty,
}: {
  links: IntegrationSyncLink[];
  empty: string;
}) {
  const t = useT();
  if (links.length === 0)
    return (
      <SettingsCard>
        <SettingRow title={empty} />
      </SettingsCard>
    );
  return (
    <SettingsCard>
      {links.map((link): ReactNode => (
        <SettingRow
          key={link.id}
          title={`${link.external_project_name} ${link.direction === "BIDIRECTIONAL" ? "↔" : "→"} ${link.space_name}`}
          description={
            link.status === "ACTIVE"
              ? t("integrations.sync.active")
              : link.status === "PAUSED"
                ? t("integrations.sync.paused")
                : t("integrations.sync.needsAttention")
          }
        />
      ))}
    </SettingsCard>
  );
}
