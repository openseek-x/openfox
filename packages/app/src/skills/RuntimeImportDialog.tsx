import { useEffect, useMemo, useRef, useState } from "react";

import {
  isApiError,
  type Device,
  type DeviceSkillScanResult,
  type SkillRuntimeImport,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Checkbox,
  Dialog,
  Icon,
  Popover,
  Spinner,
  cn,
} from "@openfox/ui";

import { useDeviceCommand } from "../computers/commands";
import { useDevices, useMe } from "../computers/queries";
import { ImportProgress } from "./ImportProgress";
import { RUNTIME_IMPORT_LIMIT, formatBytes } from "./model";
import { skillErrorText, useStartRuntimeImport } from "./queries";

// 从 Runtime 导入：选电脑 → 下发 skill_scan 并轮询 → 勾选技能 → 导入。
// 只列当前用户自己的电脑；选中电脑后清空勾选并重新扫描。

export function RuntimeImportDialog({
  orgId,
  spaceId,
  onClose,
  onOpenSkill,
  onImported,
}: {
  orgId: string;
  spaceId: string;
  onClose: () => void;
  onOpenSkill: (skillId: string) => void;
  /** 透传给 ImportProgress：导入结束时回传成功的技能 id */
  onImported?: (skillIds: string[]) => void;
}) {
  const t = useT();
  const me = useMe();
  const devices = useDevices(orgId);
  const start = useStartRuntimeImport(orgId, spaceId);
  const [deviceId, setDeviceId] = useState<string | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [started, setStarted] = useState<SkillRuntimeImport | null>(null);

  // 当前用户自己的、授权有效的电脑，按名称排序
  const own = useMemo(
    () =>
      (devices.data ?? [])
        .filter(
          (device) =>
            me.data &&
            device.owner_user_id === me.data.id &&
            device.status !== "REVOKED" &&
            device.grant_status === "ACTIVE",
        )
        .sort((a, b) => a.name.localeCompare(b.name)),
    [devices.data, me.data],
  );
  const device = own.find((item) => item.id === deviceId);

  const pick = (id: string) => {
    if (id === deviceId) return;
    setDeviceId(id);
    setSelected([]);
    start.reset();
  };
  const toggle = (name: string) =>
    setSelected((prev) =>
      prev.includes(name)
        ? prev.filter((item) => item !== name)
        : prev.length >= RUNTIME_IMPORT_LIMIT
          ? prev
          : [...prev, name],
    );

  const runImport = () => {
    if (!device || selected.length === 0 || start.isPending) return;
    start.mutate(
      { device_id: device.id, names: selected },
      { onSuccess: (result) => setStarted(result) },
    );
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("skills.runtime.title")}
      width={560}
      className="h-[min(600px,85vh)]"
    >
      {started ? (
        <div className="flex h-full min-h-0 flex-col">
          <ImportProgress
            orgId={orgId}
            spaceId={spaceId}
            initial={started}
            onClose={onClose}
            onOpenSkill={onOpenSkill}
            onImported={onImported}
          />
        </div>
      ) : (
        <div className="flex h-full min-h-0 flex-col gap-3">
          <DevicePicker devices={own} selectedId={deviceId} onSelect={pick} />
          <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
            {start.isError && (
              <Callout tone="danger" className="mb-3">
                {isApiError(start.error) && start.error.status === 409
                  ? t("skills.runtime.busy")
                  : skillErrorText(
                      t,
                      start.error,
                      "skills.runtime.startFailed",
                    )}
              </Callout>
            )}
            {!device ? (
              <Placeholder text={t("skills.runtime.noneSelected")} />
            ) : !device.online ? (
              <Callout tone="warning">
                {t("skills.runtime.offlineHint")}
              </Callout>
            ) : (
              <ScanPanel
                key={device.id}
                orgId={orgId}
                device={device}
                selected={selected}
                onToggle={toggle}
                onSetSelected={setSelected}
              />
            )}
          </div>
          <div className="flex items-center gap-3 rounded-xl bg-surface-muted/30 px-4 py-3">
            <span className="flex-1 text-body-sm text-fg-muted">
              {selected.length === 0
                ? t("skills.runtime.pickOne")
                : t("skills.runtime.selected", { count: selected.length })}
            </span>
            <Button
              disabled={selected.length === 0}
              loading={start.isPending}
              onClick={runImport}
            >
              {selected.length === 0
                ? t("skills.runtime.import")
                : t("skills.runtime.importCount", { count: selected.length })}
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  );
}

function Placeholder({ text }: { text: string }) {
  return (
    <div className="flex flex-1 items-center justify-center rounded-xl border border-dashed border-line-default p-6 text-center text-body-sm text-fg-muted">
      {text}
    </div>
  );
}

/** 电脑 / Runtime 选择条与浮层：按电脑分组，可展开 / 收起，行右侧显示状态。 */
function DevicePicker({
  devices,
  selectedId,
  onSelect,
}: {
  devices: Device[];
  selectedId: string | null;
  onSelect: (deviceId: string) => void;
}) {
  const t = useT();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const current = devices.find((item) => item.id === selectedId);
  return (
    <>
      <button
        ref={anchor}
        type="button"
        aria-label={t("skills.runtime.picker")}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="flex min-h-12 w-full shrink-0 items-center gap-2 rounded-xl border border-line-default bg-surface-card px-3 text-left hover:bg-surface-hover"
      >
        <Icon name="laptop" className="text-fg-muted" />
        <span className="min-w-0 flex-1 truncate text-body">
          {current ? current.name : t("skills.runtime.pickerPlaceholder")}
        </span>
        {current && <Online online={current.online} />}
        <Icon name="chevron-down" className="text-fg-muted" />
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        matchWidth
        role="listbox"
        aria-label={t("skills.runtime.picker")}
        className="max-h-96 min-w-96 overflow-y-auto p-6"
      >
        {devices.length === 0 ? (
          <p className="text-body-sm text-fg-muted">
            {t("skills.runtime.noDevices")}
          </p>
        ) : (
          devices.map((device) => {
            const isCollapsed = collapsed.has(device.id);
            return (
              <div key={device.id} className="flex flex-col">
                <button
                  type="button"
                  aria-expanded={!isCollapsed}
                  onClick={() =>
                    setCollapsed((prev) => {
                      const next = new Set(prev);
                      if (next.has(device.id)) next.delete(device.id);
                      else next.add(device.id);
                      return next;
                    })
                  }
                  className="flex items-center gap-1.5 py-1.5 text-left text-body-sm font-medium"
                >
                  <Icon name={isCollapsed ? "chevron-right" : "chevron-down"} />
                  <span className="truncate">{device.name}</span>
                </button>
                {!isCollapsed &&
                  (device.runtimes.length === 0 ? (
                    <p className="py-1 pl-6 text-caption text-fg-muted">
                      {t("skills.runtime.noRuntimes")}
                    </p>
                  ) : (
                    device.runtimes.map((runtime) => (
                      <button
                        key={runtime.provider}
                        type="button"
                        role="option"
                        aria-selected={device.id === selectedId}
                        onClick={() => {
                          onSelect(device.id);
                          setOpen(false);
                        }}
                        className="flex items-center gap-2 rounded-lg py-1.5 pr-2 pl-6 text-left text-body hover:bg-surface-active"
                      >
                        <span
                          className={cn(
                            "flex size-3.5 items-center justify-center rounded-full",
                            device.id === selectedId
                              ? "bg-accent text-fg-inverse"
                              : "border border-line-default",
                          )}
                        >
                          {device.id === selectedId && (
                            <Icon name="check" size={10} />
                          )}
                        </span>
                        <Icon name="terminal" className="text-fg-muted" />
                        <span className="flex-1 truncate">
                          {runtime.provider}
                        </span>
                        <span className="text-caption text-fg-muted">
                          {!device.online ? (
                            t("skills.runtime.offline")
                          ) : runtime.runnable ? (
                            <Online online />
                          ) : (
                            t("skills.runtime.unavailable")
                          )}
                        </span>
                      </button>
                    ))
                  ))}
              </div>
            );
          })
        )}
      </Popover>
    </>
  );
}

function Online({ online }: { online: boolean }) {
  const t = useT();
  return online ? (
    <Icon
      name="wifi"
      className="text-accent"
      label={t("skills.runtime.online")}
    />
  ) : (
    <span className="text-caption text-fg-muted">
      {t("skills.runtime.offline")}
    </span>
  );
}

/** 扫描并列出这台电脑的本地技能；进入时自动下发 skill_scan。 */
function ScanPanel({
  orgId,
  device,
  selected,
  onToggle,
  onSetSelected,
}: {
  orgId: string;
  device: Device;
  selected: string[];
  onToggle: (name: string) => void;
  onSetSelected: (names: string[]) => void;
}) {
  const t = useT();
  const scan = useDeviceCommand(orgId, device.id, "skill_scan");
  const startedRef = useRef(false);
  const scanStart = scan.start;
  useEffect(() => {
    if (startedRef.current) return;
    startedRef.current = true;
    scanStart();
  }, [scanStart]);
  const command = scan.command;
  const finished =
    command?.status === "SUCCEEDED" ||
    command?.status === "FAILED" ||
    command?.status === "EXPIRED";
  const bad = finished && (command.status !== "SUCCEEDED" || !command.result);

  if (bad)
    return (
      <div className="flex flex-col items-start gap-2">
        <Callout tone="danger">{t("skills.runtime.scanFailed")}</Callout>
        <Button variant="outline" size="28" onClick={() => scan.start()}>
          {t("skills.runtime.scanRetry")}
        </Button>
      </div>
    );
  if (!finished || !command?.result)
    return (
      <div
        role="status"
        className="flex items-center gap-2 py-6 text-body-sm text-fg-muted"
      >
        <Spinner />
        {t("skills.runtime.scanning")}
      </div>
    );

  const result = command.result as unknown as DeviceSkillScanResult;
  if (!result.supported)
    return <Placeholder text={t("skills.runtime.unsupported")} />;
  if (result.skills.length === 0)
    return <Placeholder text={t("skills.runtime.noSkills")} />;

  const importable = result.skills.filter((skill) => skill.importable);
  const allNames = importable
    .slice(0, RUNTIME_IMPORT_LIMIT)
    .map((skill) => skill.name);
  const allSelected =
    allNames.length > 0 && allNames.every((name) => selected.includes(name));
  return (
    <div className="flex flex-col gap-2 pb-2">
      <label className="flex items-center gap-2 text-body-sm">
        <Checkbox
          checked={allSelected}
          indeterminate={!allSelected && selected.length > 0}
          disabled={allNames.length === 0}
          onChange={() => onSetSelected(allSelected ? [] : allNames)}
        />
        {t("skills.runtime.selectAll", { count: importable.length })}
      </label>
      {importable.length > RUNTIME_IMPORT_LIMIT && (
        <p className="text-caption text-fg-muted">
          {t("skills.runtime.limit")}
        </p>
      )}
      {result.skills.map((skill) => {
        const checked = selected.includes(skill.name);
        return (
          <label
            key={skill.name}
            className={cn(
              "flex cursor-pointer items-start gap-3 rounded-[10px] border p-4",
              checked
                ? "border-primary bg-surface-active"
                : "border-line-default",
              !skill.importable && "cursor-not-allowed opacity-60",
            )}
          >
            <Checkbox
              checked={checked}
              disabled={!skill.importable}
              onChange={() => onToggle(skill.name)}
              aria-label={skill.name}
            />
            <span className="flex min-w-0 flex-1 flex-col gap-1">
              <span className="flex items-baseline gap-2">
                <span className="truncate font-mono text-body font-medium">
                  {skill.name}
                </span>
                <span className="shrink-0 text-caption text-fg-muted">
                  {t("skills.runtime.files", { count: skill.file_count })}
                  {" · "}
                  {formatBytes(skill.total_bytes)}
                </span>
              </span>
              {skill.description && (
                <span className="line-clamp-2 text-body-sm text-fg-secondary">
                  {skill.description}
                </span>
              )}
              {!skill.importable && (
                <span className="text-caption text-danger">
                  {t("skills.runtime.notImportable")}
                </span>
              )}
            </span>
          </label>
        );
      })}
    </div>
  );
}
