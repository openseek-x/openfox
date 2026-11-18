import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { isApiError, type DeviceDetail } from "@openfox/api";
import { useI18n } from "@openfox/i18n";
import { Button, Callout, useToast } from "@openfox/ui";

import { keys } from "../context";
import {
  commandRequestError,
  commandSettledError,
} from "../computers/ComputerStorage";
import { useDeviceCommand } from "../computers/commands";
import { formatBytes } from "../computers/model";

type Item = {
  name: string;
  kind: string;
  bytes: number;
  deletable: boolean;
};
type Preview = { id: string; count: number; bytes: number; items: Item[] };

function parseItems(value: unknown): Item[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((v) => {
    const i = v as Partial<Item>;
    return typeof i?.name === "string" &&
      typeof i.kind === "string" &&
      typeof i.bytes === "number" &&
      typeof i.deletable === "boolean"
      ? [{ name: i.name, kind: i.kind, bytes: i.bytes, deletable: i.deletable }]
      : [];
  });
}

/** 「此对话」页签：按会话预览并清理工作区；只显示逻辑目录名，不含本机路径。 */
export function ConversationStoragePanel({
  orgId,
  device,
  conversationId,
  isOwner,
}: {
  orgId: string;
  device: DeviceDetail;
  conversationId: string;
  isOwner: boolean;
}) {
  const { t, locale } = useI18n();
  const toast = useToast();
  const cache = useQueryClient();
  const [preview, setPreview] = useState<Preview | null>(null);
  const [error, setError] = useState("");
  const supported =
    (device.capabilities as Record<string, unknown>)?.[
      "storage.conversation_filter"
    ] === true;
  const fail = (message: string) => {
    setError(message);
    setPreview(null);
  };
  const command = useDeviceCommand(orgId, device.id, "storage_cleanup", {
    onError: (e) =>
      fail(
        isApiError(e) && e.code === "DEVICE_CAPABILITY_UNSUPPORTED"
          ? t("chat.assistant.storageConvUnsupported")
          : commandRequestError(e, t),
      ),
    onSettled: (c) => {
      const failure = commandSettledError(c, t);
      if (failure) return fail(failure);
      const r = c.result;
      if (
        r &&
        typeof r.preview_id === "string" &&
        typeof r.worktrees === "number" &&
        typeof r.bytes === "number"
      ) {
        setPreview({
          id: r.preview_id,
          count: r.worktrees,
          bytes: r.bytes,
          items: parseItems(r.items),
        });
      } else if (r && typeof r.freed_bytes === "number") {
        toast(
          t("connect.storage.done", {
            size: formatBytes(r.freed_bytes, locale),
          }),
        );
        setPreview(null);
        void cache.invalidateQueries({
          queryKey: keys.device(orgId, device.id),
        });
      }
    },
  });
  const blocked = !isOwner
    ? "chat.assistant.storageConvOwnerOnly"
    : !device.online
      ? "chat.assistant.storageConvOffline"
      : !supported
        ? "chat.assistant.storageConvUnsupported"
        : null;
  const start = (action: "preview" | "clean") => {
    setError("");
    command.start({
      action,
      days: 0,
      categories: ["conversation"],
      conversation_id: conversationId,
      ...(action === "clean" && preview ? { preview_id: preview.id } : {}),
    });
  };
  return (
    <div className="grid gap-3">
      <p className="text-body-sm text-fg-muted">
        {t("chat.assistant.storageConvScope")}
      </p>
      {blocked && <Callout tone="warning">{t(blocked)}</Callout>}
      {preview && (
        <div className="grid gap-2">
          <Callout tone="info">
            {preview.count === 0
              ? t("chat.assistant.storageConvEmpty")
              : t("chat.assistant.storageConvSummary", {
                  count: preview.count,
                  size: formatBytes(preview.bytes, locale),
                })}
          </Callout>
          <ul className="grid gap-1 text-body-sm">
            {preview.items.map((item, index) => (
              <li key={index} className="flex justify-between gap-3">
                <span className="truncate">
                  {item.name || t("chat.assistant.storageConvDefaultName")}
                </span>
                <span className="text-fg-muted">
                  {item.deletable
                    ? formatBytes(item.bytes, locale)
                    : t("chat.assistant.storageConvKept")}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {error && <Callout tone="warning">{error}</Callout>}
      <div className="flex justify-end gap-2">
        {preview && preview.count > 0 ? (
          <Button
            loading={command.running}
            disabled={Boolean(blocked)}
            onClick={() => start("clean")}
          >
            {t("chat.assistant.storageConvClean")}
          </Button>
        ) : (
          <Button
            variant="outline"
            loading={command.running}
            disabled={Boolean(blocked)}
            onClick={() => start("preview")}
          >
            {t("chat.assistant.storageConvPreview")}
          </Button>
        )}
      </div>
    </div>
  );
}
