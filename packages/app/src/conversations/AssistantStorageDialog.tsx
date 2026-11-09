import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, Skeleton } from "@openfox/ui";

import { StorageSection } from "../computers/ComputerStorage";
import { isNotFound, useDevice, useMe } from "../computers/queries";

export function AssistantStorageDialog({
  orgId,
  deviceId,
  onClose,
}: {
  orgId: string;
  deviceId: string;
  onClose: () => void;
}) {
  const t = useT();
  const device = useDevice(orgId, deviceId);
  const me = useMe();

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
          <StorageSection
            orgId={orgId}
            device={device.data}
            isOwner={me.data?.id === device.data.owner_user_id}
          />
        </div>
      )}
    </Dialog>
  );
}
