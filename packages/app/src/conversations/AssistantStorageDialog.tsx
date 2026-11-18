import { useState } from "react";

import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, Skeleton, Tabs } from "@openfox/ui";

import { StorageSection } from "../computers/ComputerStorage";
import { isNotFound, useDevice, useMe } from "../computers/queries";
import { ConversationStoragePanel } from "./ConversationStoragePanel";

export function AssistantStorageDialog({
  orgId,
  deviceId,
  conversationId,
  onClose,
}: {
  orgId: string;
  deviceId: string;
  /** 当前对话；有值时多一个「此对话」页签 */
  conversationId?: string | null;
  onClose: () => void;
}) {
  const t = useT();
  const [scope, setScope] = useState("device");
  const device = useDevice(orgId, deviceId);
  const me = useMe();
  const isOwner = me.data?.id === device.data?.owner_user_id;

  return (
    <Dialog
      open
      title={t("chat.assistant.storageTitle")}
      width={640}
      onClose={onClose}
    >
      {device.isPending ? (
        <div aria-busy="true" className="grid gap-3">
          <Skeleton className="h-5 w-40" />
          <Skeleton className="h-32 w-full rounded-xl" />
        </div>
      ) : device.isError || !device.data ? (
        <Callout tone="warning">
          <p role="alert">
            {t(
              isNotFound(device.error)
                ? "computers.detail.notFound"
                : "computers.loadFailed",
            )}
          </p>
          <Button
            variant="outline"
            size="28"
            className="mt-3"
            loading={device.isFetching}
            onClick={() => void device.refetch()}
          >
            {t("common.retry")}
          </Button>
        </Callout>
      ) : (
        <div className="grid gap-4">
          <div className="grid gap-1">
            <p className="text-body font-medium">{device.data.name}</p>
            <p className="text-body-sm text-fg-muted">
              {t("chat.assistant.storageScope")}
            </p>
          </div>
          {conversationId && (
            <Tabs
              label={t("chat.assistant.storageTabsLabel")}
              value={scope}
              onChange={setScope}
              tabs={[
                { id: "device", label: t("chat.assistant.storageTabDevice") },
                {
                  id: "conversation",
                  label: t("chat.assistant.storageTabConversation"),
                },
              ]}
            />
          )}
          {conversationId && scope === "conversation" ? (
            <ConversationStoragePanel
              orgId={orgId}
              device={device.data}
              conversationId={conversationId}
              isOwner={isOwner}
            />
          ) : (
            <StorageSection
              orgId={orgId}
              device={device.data}
              isOwner={isOwner}
            />
          )}
        </div>
      )}
    </Dialog>
  );
}
