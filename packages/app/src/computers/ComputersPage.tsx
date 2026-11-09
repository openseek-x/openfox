import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useParams, useSearchParams } from "react-router-dom";

import type { Device } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Avatar,
  Button,
  Icon,
  IconButton,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@openfox/ui";

import { desktopLocalStatusKey, isDesktop } from "../desktop";
import { ConfigPageHeader, EmptyState, ListSkeleton } from "../settings/common";
import { ComputerDetailPage } from "./ComputerDetailPage";
import { ConnectComputerDialog } from "./ConnectComputer";
import { LocalComputerCard } from "./LocalComputerCard";
import {
  computerPartition,
  sortComputers,
  systemText,
  visibilityText,
} from "./model";
import {
  ComputerStatusLabel,
  OsBadge,
  RuntimeIcons,
  SectionTitle,
} from "./parts";
import { useDevices, useMe } from "./queries";

/** 设置 · 电脑：?computer=<id> 时显示详情整页，否则显示列表。 */
export function ComputersPage() {
  const [params] = useSearchParams();
  const computerId = params.get("computer");
  if (computerId) return <ComputerDetailPage deviceId={computerId} />;
  return <ComputerListPage />;
}

function ComputerListPage() {
  const t = useT();
  const { orgId = "" } = useParams();
  const queryClient = useQueryClient();
  const devices = useDevices(orgId);
  const me = useMe();
  const desktop = isDesktop();
  const [connecting, setConnecting] = useState(false);

  const { shared, personal } = useMemo(() => {
    const sorted = sortComputers(devices.data ?? [], me.data?.id);
    return {
      shared: sorted.filter((item) => computerPartition(item) === "shared"),
      personal: sorted.filter((item) => computerPartition(item) === "private"),
    };
  }, [devices.data, me.data?.id]);

  const refresh = () => {
    void devices.refetch();
    if (desktop)
      void queryClient.invalidateQueries({ queryKey: desktopLocalStatusKey });
  };

  return (
    <div className="mx-auto flex max-w-[1132px] flex-col gap-8 px-6 pt-12 pb-10">
      <ConfigPageHeader
        title={t("computers.title")}
        subtitle={t("computers.subtitle")}
        action={
          <div className="flex items-center gap-2">
            <IconButton
              label={t("computers.refresh")}
              icon={<Icon name="refresh" />}
              variant="outline"
              size="32"
              onClick={refresh}
            />
            <Button
              leading={<Icon name="plus" />}
              onClick={() => setConnecting(true)}
            >
              {t("computers.connect")}
            </Button>
          </div>
        }
      />
      {desktop && (
        <section className="flex flex-col gap-2">
          <SectionTitle>{t("computers.sections.local")}</SectionTitle>
          <LocalComputerCard orgId={orgId} />
        </section>
      )}
      {devices.isError ? (
        <EmptyState
          title={t("computers.loadFailed")}
          action={
            <Button variant="outline" onClick={() => void devices.refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : (
        <>
          <ComputerSection
            title={t("computers.sections.shared")}
            empty={t("computers.empty.shared")}
            devices={shared}
            loading={devices.isLoading}
          />
          <ComputerSection
            title={t("computers.sections.private")}
            empty={t("computers.empty.private")}
            devices={personal}
            loading={devices.isLoading}
          />
        </>
      )}
      <ConnectComputerDialog
        orgId={orgId}
        open={connecting}
        onClose={() => setConnecting(false)}
      />
    </div>
  );
}

function ComputerSection({
  title,
  empty,
  devices,
  loading,
}: {
  title: string;
  empty: string;
  devices: Device[];
  loading: boolean;
}) {
  const t = useT();
  const [, setParams] = useSearchParams();
  const open = (id: string) => setParams({ computer: id });
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <SectionTitle>{title}</SectionTitle>
      {loading ? (
        <ListSkeleton rows={2} label={title} />
      ) : devices.length === 0 ? (
        <p className="rounded-xl border border-dashed border-line-default px-4 py-3 text-body-sm text-fg-muted">
          {empty}
        </p>
      ) : (
        <Table>
          <TableHead>
            <tr className="bg-surface-muted">
              <TableHeaderCell className="w-[38%] rounded-l-lg pl-14">
                {t("computers.columns.name")}
              </TableHeaderCell>
              <TableHeaderCell className="w-[22%]">
                {t("computers.columns.runtime")}
              </TableHeaderCell>
              <TableHeaderCell className="w-[12%]">
                {t("computers.columns.status")}
              </TableHeaderCell>
              <TableHeaderCell className="w-[12%]">
                {t("computers.columns.visibility")}
              </TableHeaderCell>
              <TableHeaderCell className="rounded-r-lg">
                {t("computers.columns.owner")}
              </TableHeaderCell>
            </tr>
          </TableHead>
          <TableBody>
            {devices.map((device) => (
              <TableRow
                key={device.id}
                onClick={() => open(device.id)}
                className="h-14"
              >
                <TableCell>
                  <button
                    type="button"
                    onClick={(event) => {
                      event.stopPropagation();
                      open(device.id);
                    }}
                    className="flex min-w-0 items-center gap-3 text-left"
                  >
                    <OsBadge os={device.operating_system} />
                    <span className="flex min-w-0 flex-col">
                      <span className="truncate text-body font-medium text-fg-default">
                        {device.name}
                      </span>
                      <span className="truncate text-caption text-fg-muted">
                        {systemText(device)}
                      </span>
                    </span>
                  </button>
                </TableCell>
                <TableCell>
                  <RuntimeIcons runtimes={device.runtimes} />
                </TableCell>
                <TableCell>
                  <ComputerStatusLabel device={device} />
                </TableCell>
                <TableCell className="text-body-sm text-fg-secondary">
                  {visibilityText(device, t)}
                </TableCell>
                <TableCell>
                  {device.owner_email ? (
                    <span className="flex min-w-0 items-center gap-2 text-body-sm text-fg-secondary">
                      <Avatar name={device.owner_email} size={20} />
                      <span className="truncate">{device.owner_email}</span>
                    </span>
                  ) : (
                    <span className="text-fg-muted">—</span>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </section>
  );
}
