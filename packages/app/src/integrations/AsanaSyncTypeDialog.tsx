import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useT } from "@openfox/i18n";
import { Button, Dialog } from "@openfox/ui";
import { keys, useApi } from "../context";
import { AsanaTypeMappings } from "../imports/AsanaTypeMappings";
import { importErrorText } from "../imports/queries";
import { asanaApi, type AsanaSyncTypes } from "./asana-api";

export function AsanaSyncTypeDialog({
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
  const [draft, setDraft] = useState<AsanaSyncTypes["types"]>();
  const query = useQuery({
    queryKey: [...keys.imports(org), job, "asana-types"],
    queryFn: () => asanaApi(api.http).syncTypes(org, job),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const value = draft ?? query.data?.types ?? [];
  const complete = query.data?.sources.every(({ source }) =>
    value.some(
      (mapping) =>
        mapping.source === source && (mapping.use_default || !!mapping.type_id),
    ),
  );
  const save = useMutation({
    mutationFn: () =>
      asanaApi(api.http).saveSyncTypes(org, job, query.data!.version, value),
    onSuccess: async () => {
      await cache.invalidateQueries({ queryKey: keys.imports(org) });
      close();
    },
  });
  const reload = async () => {
    const result = await query.refetch();
    if (result.isSuccess) {
      setDraft(undefined);
      save.reset();
    }
  };
  return (
    <Dialog
      open
      title={t("integrations.asana.typeMappings")}
      onClose={close}
      footer={
        <>
          <Button variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={
              !complete || query.isFetching || query.isError || save.isPending
            }
            loading={save.isPending}
            onClick={() => save.mutate()}
          >
            {t("common.save")}
          </Button>
        </>
      }
    >
      <p className="text-body-sm text-fg-muted">
        {t("integrations.asana.syncTypesHelp")}
      </p>
      {query.isPending && <p role="status">{t("common.loading")}</p>}
      {(query.isError || save.isError) && (
        <div className="mt-3 grid gap-2">
          <p role="alert">{importErrorText(t, save.error ?? query.error)}</p>
          <Button
            variant="outline"
            disabled={query.isFetching}
            onClick={() => void reload()}
          >
            {t("integrations.asana.reloadTypes")}
          </Button>
        </div>
      )}
      {query.data && (
        <AsanaTypeMappings
          org={org}
          space={query.data.space_id}
          analysis={{
            types: query.data.sources.map((source) => ({
              ...source,
              type_id: null,
            })),
          }}
          value={value}
          onChange={setDraft}
        />
      )}
    </Dialog>
  );
}
