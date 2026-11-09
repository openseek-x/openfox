import { StoragePolicyForm } from "./StoragePolicyForm";
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { DeviceDetail } from "@openfox/api";
import { useI18n } from "@openfox/i18n";
import {
  Button,
  Callout,
  Checkbox,
  Dialog,
  TextInput,
  useToast,
} from "@openfox/ui";
import { keys } from "../context";
import { useDeviceCommand } from "./commands";
import { formatBytes } from "./model";
import {
  commandSettledError,
  commandRequestError,
  StorageUsage,
} from "./ComputerStorage";

const categories = ["task", "conversation", "automation", "workflow"] as const;
const statuses = [
  "TRIAGE",
  "BACKLOG",
  "TODO",
  "IN_PROGRESS",
  "DONE",
  "CANCELLED",
  "DUPLICATE",
] as const;
type Category = (typeof categories)[number];
type Status = (typeof statuses)[number];
type Preview = { id: string; count: number; bytes: number };
function toggle<T>(items: T[], v: T) {
  return items.includes(v) ? items.filter((x) => x !== v) : [...items, v];
}

export function StorageManager({
  orgId,
  device,
  onClose,
  onLegacy,
}: {
  orgId: string;
  device: DeviceDetail;
  onClose: () => void;
  onLegacy: () => void;
}) {
  const { t, locale } = useI18n(),
    toast = useToast(),
    cache = useQueryClient();
  const [mode, setMode] = useState<"time" | "status" | null>(null);
  const [age, setAge] = useState("0"),
    [custom, setCustom] = useState("7");
  const [sources, setSources] = useState<Category[]>([...categories]);
  const [selected, setSelected] = useState<Status[]>([
    "DONE",
    "CANCELLED",
    "DUPLICATE",
  ]);
  const [preview, setPreview] = useState<Preview | null>(null),
    [error, setError] = useState("");
  const command = useDeviceCommand(orgId, device.id, "storage_cleanup", {
    onError: (e) => {
      setError(commandRequestError(e, t));
      setPreview(null);
    },
    onSettled: (c) => {
      const failure = commandSettledError(c, t);
      if (failure) {
        setError(failure);
        setPreview(null);
        return;
      }
      const r = c.result;
      if (
        r &&
        typeof r.preview_id === "string" &&
        typeof r.worktrees === "number" &&
        typeof r.bytes === "number"
      ) {
        setPreview({ id: r.preview_id, count: r.worktrees, bytes: r.bytes });
        return;
      }
      if (r && typeof r.freed_bytes === "number") {
        toast(
          t("connect.storage.done", {
            size: formatBytes(r.freed_bytes, locale),
          }),
        );
        void cache.invalidateQueries({
          queryKey: keys.device(orgId, device.id),
        });
        onClose();
      }
    },
  });
  const days = mode === "status" ? 0 : Number(age === "custom" ? custom : age);
  const valid =
    mode === "time"
      ? sources.length > 0 &&
        Number.isInteger(days) &&
        days >= 0 &&
        days <= 3650 &&
        (age !== "custom" || days > 0)
      : selected.length > 0;
  function reset() {
    setPreview(null);
    setError("");
  }
  return (
    <Dialog
      open
      title={t(
        mode === "time"
          ? "connect.storage.byTime"
          : mode === "status"
            ? "connect.storage.byStatus"
            : "connect.storage.manage",
      )}
      onClose={onClose}
      footer={
        mode && (
          <>
            <Button
              variant="outline"
              disabled={command.running}
              onClick={() => {
                reset();
                setMode(null);
              }}
            >
              {t("common.back")}
            </Button>
            <Button
              loading={command.running}
              disabled={!valid || !device.online || preview?.count === 0}
              onClick={() => {
                setError("");
                command.start({
                  action: preview ? "clean" : "preview",
                  days,
                  categories: mode === "status" ? ["task"] : sources,
                  ...(mode === "status" ? { statuses: selected } : {}),
                  ...(preview ? { preview_id: preview.id } : {}),
                });
              }}
            >
              {t(
                preview ? "connect.storage.confirm" : "connect.storage.preview",
              )}
            </Button>
          </>
        )
      }
    >
      {!mode ? (
        <div className="grid gap-4">
          <div className="flex justify-between">
            <span>{t("connect.storage.total")}</span>
            <strong>
              {device.storage
                ? formatBytes(device.storage.total_bytes, locale)
                : "—"}
            </strong>
          </div>
          {device.storage && (
            <StorageUsage
              categories={device.storage.categories}
              locale={locale}
            />
          )}
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => setMode("time")}>
              {t("connect.storage.byTime")}
            </Button>
            <Button variant="outline" onClick={() => setMode("status")}>
              {t("connect.storage.byStatus")}
            </Button>
            <Button variant="outline" onClick={onLegacy}>
              {t("connect.storage.retentionCleanup")}
            </Button>
          </div>
          <p className="text-caption text-fg-muted">
            {t("connect.storage.footnote")}
          </p>
          {device.online && (
            <StoragePolicyForm orgId={orgId} deviceId={device.id} />
          )}
        </div>
      ) : (
        <fieldset disabled={command.running} className="grid gap-4 text-body">
          {mode === "time" ? (
            <>
              <div className="flex flex-wrap gap-2">
                {["7", "30", "0", "custom"].map((v) => (
                  <Button
                    key={v}
                    variant={age === v ? "secondary" : "outline"}
                    aria-pressed={age === v}
                    onClick={() => {
                      setAge(v);
                      reset();
                    }}
                  >
                    {v === "0"
                      ? t("connect.storage.allTime")
                      : v === "custom"
                        ? t("connect.storage.customDays")
                        : t("connect.storage.olderThan", { days: v })}
                  </Button>
                ))}
              </div>
              {age === "custom" && (
                <TextInput
                  type="number"
                  min={1}
                  max={3650}
                  aria-label={t("connect.storage.customDays")}
                  value={custom}
                  onChange={(e) => {
                    setCustom(e.target.value);
                    reset();
                  }}
                />
              )}
              {categories.map((v) => (
                <label className="flex items-center gap-2" key={v}>
                  <Checkbox
                    checked={sources.includes(v)}
                    onChange={() => {
                      setSources(toggle(sources, v));
                      reset();
                    }}
                  />
                  {t(`connect.storage.category.${v}`)}
                </label>
              ))}
            </>
          ) : (
            statuses.map((v) => (
              <label className="flex items-center gap-2" key={v}>
                <Checkbox
                  checked={selected.includes(v)}
                  onChange={() => {
                    setSelected(toggle(selected, v));
                    reset();
                  }}
                />
                {t(`tasks.statusCategory.${v}`)}
              </label>
            ))
          )}
          {preview && (
            <Callout tone="info">
              {t("connect.storage.previewResult", {
                count: preview.count,
                size: formatBytes(preview.bytes, locale),
              })}
            </Callout>
          )}
          {error && <Callout tone="warning">{error}</Callout>}
          {!device.online && (
            <Callout tone="warning">
              {t("connect.storage.confirmOffline")}
            </Callout>
          )}
        </fieldset>
      )}
    </Dialog>
  );
}
