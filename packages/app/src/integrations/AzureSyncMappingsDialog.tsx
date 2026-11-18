import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Callout } from "@openfox/ui";
import { useApi, keys } from "../context";
import {
  AzureMappings,
  azureMappingsValid,
  type AzureMappingsValue,
} from "../imports/AzureMappings";
import { importErrorText } from "../imports/queries";
import { azureApi } from "./azure-api";
export function AzureSyncMappingsDialog({
  org,
  job,
  close,
}: {
  org: string;
  job: string;
  close: () => void;
}) {
  const api = useApi(),
    t = useT(),
    cache = useQueryClient();
  const [draft, setDraft] = useState<AzureMappingsValue>();
  const q = useQuery({
    queryKey: [...keys.imports(org), job, "azure-mappings"],
    queryFn: () => azureApi(api.http).syncMappings(org, job),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const value = draft ?? {
    types: q.data?.types ?? [],
    statuses: q.data?.statuses ?? [],
    priorities: q.data?.priorities ?? [],
  };
  const save = useMutation({
    mutationFn: () =>
      azureApi(api.http).saveSyncMappings(org, job, {
        version: q.data!.version,
        types: value.types ?? [],
        statuses: value.statuses,
        priorities: value.priorities ?? [],
      }),
    onSuccess: async () => {
      await cache.invalidateQueries({ queryKey: keys.imports(org) });
      close();
    },
  });
  return (
    <Dialog
      open
      title={t("integrations.azure.mappingTitle")}
      onClose={close}
      footer={
        <>
          <Button variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={save.isPending}
            disabled={
              !q.data ||
              q.isFetching ||
              q.isError ||
              save.isPending ||
              !azureMappingsValid(q.data.analysis, value)
            }
            onClick={() => save.mutate()}
          >
            {t("common.save")}
          </Button>
        </>
      }
    >
      {q.isPending && <p role="status">{t("common.loading")}</p>}
      {(q.isError || save.isError) && (
        <Callout tone="warning">
          {importErrorText(t, save.error ?? q.error)}
          <Button
            variant="outline"
            onClick={() => {
              void q.refetch().then((result) => {
                if (result.isSuccess) {
                  setDraft(undefined);
                  save.reset();
                }
              });
            }}
          >
            {t("integrations.azure.reload")}
          </Button>
        </Callout>
      )}
      {q.data && (
        <AzureMappings
          org={org}
          space={q.data.space_id}
          analysis={q.data.analysis}
          value={value}
          onChange={setDraft}
        />
      )}
    </Dialog>
  );
}
