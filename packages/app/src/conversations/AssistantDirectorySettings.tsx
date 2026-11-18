import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Spinner } from "@openfox/ui";
import { keys, useApi } from "../context";
import { workspaceOptionsKey } from "./WorkDirectoryPicker";

export function AssistantDirectorySettings({
  orgId,
  agentId,
  onClose,
}: {
  orgId: string;
  agentId: string;
  onClose: () => void;
}) {
  const api = useApi(),
    cache = useQueryClient(),
    t = useT();
  const settings = useQuery({
    queryKey: [...keys.assistant(orgId), agentId, "work-directory"],
    queryFn: () => api.assistant.directorySettings(orgId),
    retry: false,
    staleTime: 0,
  });
  const [choice, setChoice] = useState<string | null>(null);
  const save = useMutation({
    mutationFn: () => {
      const data = settings.data!;
      return api.assistant.setDirectory(orgId, {
        device_id: data.device_id,
        version: data.version,
        directory: choice ?? data.directory,
      });
    },
    onSuccess: async () => {
      await Promise.all([
        cache.invalidateQueries({ queryKey: keys.assistant(orgId) }),
        cache.invalidateQueries({
          queryKey: workspaceOptionsKey(orgId, agentId),
        }),
      ]);
      onClose();
    },
  });
  const reload = () => {
    setChoice(null);
    save.reset();
    void settings.refetch();
  };
  return (
    <Dialog
      open
      title={t("chat.workspace.assistantDefault")}
      description={t("chat.workspace.assistantGrant")}
      width={420}
      onClose={() => {
        if (!save.isPending) onClose();
      }}
      footer={
        <>
          <Button variant="outline" disabled={save.isPending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={
              !settings.data ||
              settings.isFetching ||
              settings.isError ||
              save.isPending ||
              save.isError ||
              choice === null ||
              choice === settings.data.directory
            }
            onClick={() => save.mutate()}
          >
            {t("common.save")}
          </Button>
        </>
      }
    >
      {settings.isLoading && <Spinner />}
      {settings.data && (
        <div className="flex max-h-64 flex-col gap-2 overflow-y-auto">
          {["", ...settings.data.directories].map((directory) => (
            <label
              key={directory}
              className="flex items-center gap-2 py-1 text-body-sm"
            >
              <input
                type="radio"
                name="assistant-work-directory"
                disabled={save.isPending}
                checked={(choice ?? settings.data.directory) === directory}
                onChange={() => setChoice(directory)}
              />
              {directory || t("chat.workspace.temporary")}
            </label>
          ))}
        </div>
      )}
      {(settings.isError || save.isError) && (
        <div role="alert" className="text-body-sm text-danger">
          {t(
            settings.isError
              ? "chat.workspace.settingsUnavailable"
              : "chat.workspace.settingsFailed",
          )}{" "}
          <button type="button" className="underline" onClick={reload}>
            {t("common.retry")}
          </button>
        </div>
      )}
    </Dialog>
  );
}
