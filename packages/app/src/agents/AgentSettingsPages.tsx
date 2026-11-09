import { useState, type ReactNode } from "react";
import { useParams } from "react-router-dom";

import type { AgentDirectoryEntry } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@openfox/ui";

import { ConfigPageHeader, EmptyState, ListSkeleton } from "../settings/common";
import { AgentAvatar, CrewAvatar, PersonAvatar } from "./AgentAvatar";
import { CreateAgentDialog } from "./CreateAgentDialog";
import { CreateCrewEntry } from "./CreateCrewDialog";
import { ownerLabel } from "./DirectoryCard";
import { runtimeLabel } from "./model";
import { useAgentSettingsParam, useCrewSettingsParam } from "./params";
import { useDirectory } from "./queries";

/** 共享 / 私有两组表格的外框。 */
function Grouped({
  entries,
  columns,
  row,
}: {
  entries: AgentDirectoryEntry[];
  columns: string[];
  row: (entry: AgentDirectoryEntry) => ReactNode;
}) {
  const t = useT();
  return (
    <>
      {(["SHARED", "PRIVATE"] as const).map((visibility) => {
        const list = entries.filter((e) => e.visibility === visibility);
        const title =
          visibility === "SHARED"
            ? t("agents.settingsPages.shared")
            : t("agents.settingsPages.private");
        return (
          <section
            key={visibility}
            aria-label={title}
            className="flex flex-col gap-2"
          >
            <h2 className="text-title font-medium text-fg-secondary">
              {title}
            </h2>
            {list.length === 0 ? (
              <p className="px-3 py-4 text-body-sm text-fg-muted">
                {t("agents.settingsPages.empty")}
              </p>
            ) : (
              <Table>
                <TableHead>
                  <tr className="bg-surface-muted">
                    {columns.map((column) => (
                      <TableHeaderCell
                        key={column}
                        className="first:rounded-l-lg first:pl-14"
                      >
                        {column}
                      </TableHeaderCell>
                    ))}
                    <TableHeaderCell className="w-12 rounded-r-lg">
                      <span className="sr-only">
                        {t("agents.settingsPages.settings")}
                      </span>
                    </TableHeaderCell>
                  </tr>
                </TableHead>
                <TableBody>{list.map(row)}</TableBody>
              </Table>
            )}
          </section>
        );
      })}
    </>
  );
}

function RowMenu({ name, onOpen }: { name: string; onOpen: () => void }) {
  const t = useT();
  return (
    <TableCell className="w-12" onClick={(event) => event.stopPropagation()}>
      <Menu
        label={t("agents.settingsPages.rowMenu", { name })}
        placement="bottom-end"
        trigger={({ ref, toggle }) => (
          <IconButton
            ref={ref}
            label={t("agents.settingsPages.rowMenu", { name })}
            icon={<Icon name="ellipsis" />}
            size="28"
            onClick={toggle}
          />
        )}
      >
        <MenuItem icon={<Icon name="sliders-horizontal" />} onSelect={onOpen}>
          {t("agents.settingsPages.settings")}
        </MenuItem>
      </Menu>
    </TableCell>
  );
}

/** 设置 → Agents：共享 / 私有两组表格，点击行打开设置抽屉；新建默认共享（决策 8）。 */
export function AgentsSettingsPage() {
  const t = useT();
  const { orgId = "" } = useParams();
  const directory = useDirectory(orgId);
  const drawer = useAgentSettingsParam();
  const [creating, setCreating] = useState(false);
  const entries = (directory.data?.entries ?? []).filter(
    (e) => e.type === "agent",
  );
  return (
    <div className="mx-auto flex max-w-[1100px] flex-col gap-6 px-6 pt-12 pb-10">
      <ConfigPageHeader
        title={t("agents.settingsPages.agentsTitle")}
        subtitle={t("agents.settingsPages.agentsDesc")}
        action={
          <Button
            leading={<Icon name="plus" />}
            onClick={() => setCreating(true)}
          >
            {t("agents.settingsPages.newAgent")}
          </Button>
        }
      />
      {directory.isLoading ? (
        <ListSkeleton label={t("agents.directory.loading")} />
      ) : directory.isError ? (
        <EmptyState
          title={t("agents.directory.errorTitle")}
          action={
            <Button variant="outline" onClick={() => void directory.refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : (
        <Grouped
          entries={entries}
          columns={[
            t("agents.settingsPages.columns.name"),
            t("agents.settingsPages.columns.runtime"),
            t("agents.settingsPages.columns.status"),
            t("agents.settingsPages.columns.visibility"),
            t("agents.settingsPages.columns.owner"),
          ]}
          row={(entry) => {
            const open = () => drawer.open(entry.id, entry.space_id);
            return (
              <TableRow key={entry.id} onClick={open} className="h-[60px]">
                <TableCell>
                  <span className="flex items-center gap-3">
                    <AgentAvatar
                      name={entry.name}
                      avatar={entry.avatar}
                      size={28}
                    />
                    <span className="font-medium">{entry.name}</span>
                    {entry.title && (
                      <span className="text-fg-muted">- {entry.title}</span>
                    )}
                  </span>
                </TableCell>
                <TableCell>
                  <span className="flex flex-col text-caption">
                    <span className="flex items-center gap-1">
                      <Icon
                        name="monitor"
                        size={12}
                        className="text-fg-muted"
                      />
                      {entry.computer_name ?? "—"}
                    </span>
                    <span className="flex items-center gap-1 text-fg-secondary">
                      <Icon name="runtime-codex" size={12} />
                      {runtimeLabel(entry.runtime)} ·{" "}
                      {t("agents.settingsPages.local")}
                    </span>
                  </span>
                </TableCell>
                <TableCell>
                  <span className="flex items-center gap-1.5 text-body-sm">
                    <span
                      aria-hidden
                      className={
                        entry.online
                          ? "size-1.5 rounded-full bg-success"
                          : "size-1.5 rounded-full bg-fg-subtle"
                      }
                    />
                    {entry.online ? t("agents.online") : t("agents.offline")}
                  </span>
                </TableCell>
                <TableCell className="text-body-sm text-fg-muted">
                  {t(`agents.visibility.${entry.visibility}`)}
                </TableCell>
                <TableCell>
                  <span className="flex items-center gap-1.5 text-body-sm">
                    <PersonAvatar name={entry.owner_email} size={20} />
                    {ownerLabel(entry, t("agents.you"))}
                  </span>
                </TableCell>
                <RowMenu name={entry.name} onOpen={open} />
              </TableRow>
            );
          }}
        />
      )}
      {creating && (
        <CreateAgentDialog
          orgId={orgId}
          defaultVisibility="SHARED"
          onClose={() => setCreating(false)}
          onCreated={(agent) => {
            setCreating(false);
            drawer.open(agent.id, agent.space_id);
          }}
        />
      )}
    </div>
  );
}

/** 设置 → 小队：共享 / 私有两组表格（名称 + 描述、队长、可见性、所有者）。 */
export function CrewsSettingsPage() {
  const t = useT();
  const { orgId = "" } = useParams();
  const directory = useDirectory(orgId);
  const drawer = useCrewSettingsParam();
  const agentDrawer = useAgentSettingsParam();
  const [dialog, setDialog] = useState<null | "crew" | "agent">(null);
  const entries = (directory.data?.entries ?? []).filter(
    (e) => e.type === "crew",
  );
  return (
    <div className="mx-auto flex max-w-[1100px] flex-col gap-6 px-6 pt-12 pb-10">
      <ConfigPageHeader
        title={t("agents.settingsPages.crewsTitle")}
        subtitle={t("agents.settingsPages.crewsDesc")}
        action={
          <Button
            leading={<Icon name="plus" />}
            onClick={() => setDialog("crew")}
          >
            {t("agents.settingsPages.newCrew")}
          </Button>
        }
      />
      {directory.isLoading ? (
        <ListSkeleton label={t("agents.directory.loading")} />
      ) : (
        <Grouped
          entries={entries}
          columns={[
            t("agents.settingsPages.columns.name"),
            t("agents.settingsPages.columns.leader"),
            t("agents.settingsPages.columns.visibility"),
            t("agents.settingsPages.columns.owner"),
          ]}
          row={(entry) => {
            const open = () => drawer.open(entry.id, entry.space_id);
            return (
              <TableRow key={entry.id} onClick={open} className="h-[60px]">
                <TableCell>
                  <span className="flex items-center gap-3">
                    <CrewAvatar
                      name={entry.name}
                      members={(entry.members ?? []).filter(
                        (m) => m.kind !== "HUMAN",
                      )}
                      size={28}
                    />
                    <span className="flex min-w-0 flex-col">
                      <span className="font-medium">{entry.name}</span>
                      {entry.description && (
                        <span className="truncate text-caption text-fg-muted">
                          {entry.description}
                        </span>
                      )}
                    </span>
                  </span>
                </TableCell>
                <TableCell className="text-body-sm">
                  {entry.leader ? (
                    <span className="flex items-center gap-1.5">
                      <AgentAvatar name={entry.leader.name} size={20} />
                      {entry.leader.name}
                      {entry.leader.title && (
                        <span className="text-fg-muted">
                          {entry.leader.title}
                        </span>
                      )}
                    </span>
                  ) : (
                    "—"
                  )}
                </TableCell>
                <TableCell className="text-body-sm text-fg-muted">
                  {t(`agents.visibility.${entry.visibility}`)}
                </TableCell>
                <TableCell>
                  <span className="flex items-center gap-1.5 text-body-sm">
                    <PersonAvatar name={entry.owner_email} size={20} />
                    {ownerLabel(entry, t("agents.you"))}
                  </span>
                </TableCell>
                <RowMenu name={entry.name} onOpen={open} />
              </TableRow>
            );
          }}
        />
      )}
      {dialog === "crew" && (
        <CreateCrewEntry
          orgId={orgId}
          defaultVisibility="SHARED"
          onClose={() => setDialog(null)}
          onCreateAgent={() => setDialog("agent")}
          onCreated={(crew) => {
            setDialog(null);
            drawer.open(crew.id, crew.space_id);
          }}
        />
      )}
      {dialog === "agent" && (
        <CreateAgentDialog
          orgId={orgId}
          defaultVisibility="SHARED"
          onClose={() => setDialog(null)}
          onCreated={(agent) => {
            setDialog(null);
            agentDrawer.open(agent.id, agent.space_id);
          }}
        />
      )}
    </div>
  );
}
