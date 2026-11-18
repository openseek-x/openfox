import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { type SpaceDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog } from "@openfox/ui";

import { keys, useApi } from "../context";
import { putSpace, spaceErrorMessage } from "../spaces/queries";
import { useViews, viewErrorMessage } from "../views/queries";

export function DefaultViewDialog({
  orgId,
  space,
  onClose,
}: {
  orgId: string;
  space: SpaceDetail;
  onClose: () => void;
}) {
  const api = useApi();
  const cache = useQueryClient();
  const t = useT();
  const [selected, select] = useState(space.default_view_id ?? "");
  const [version, setVersion] = useState(space.version);
  const list = useViews(orgId, space.id, space.features.views);
  const save = useMutation({
    mutationFn: () =>
      api.spaces.update(orgId, space.id, {
        version,
        default_view_id: selected || null,
      }),
    onSuccess: (next) => {
      putSpace(cache, orgId, next);
      void cache.invalidateQueries({ queryKey: keys.tasks(orgId) });
      onClose();
    },
  });
  const refresh = useMutation({
    mutationFn: () => api.spaces.get(orgId, space.id),
    onSuccess: (next) => {
      setVersion(next.version);
      putSpace(cache, orgId, next);
      save.reset();
    },
  });
  const pending = save.isPending || refresh.isPending;
  const options = (list.data ?? []).filter(
    (v) => v.visibility === "space" && v.space_id === space.id,
  );
  const unavailable =
    selected !== "" &&
    (!space.features.views || !options.some((v) => v.id === selected));
  return (
    <Dialog
      open
      title={t("tasks.defaultView.manage")}
      description={t("tasks.defaultView.description")}
      onClose={() => {
        if (!pending) onClose();
      }}
      footer={
        <>
          <Button variant="secondary" disabled={pending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={
              pending || unavailable || (space.features.views && list.isPending)
            }
            onClick={() => save.mutate()}
          >
            {t("tasks.defaultView.save")}
          </Button>
        </>
      }
    >
      <label className="flex flex-col gap-2">
        {t("tasks.defaultView.choose")}
        <select
          aria-label={t("tasks.defaultView.choose")}
          value={selected}
          disabled={pending}
          onChange={(event) => {
            select(event.target.value);
            save.reset();
          }}
          className="h-8 rounded-md border border-border bg-surface-card px-2"
        >
          <option value="">{t("tasks.defaultView.none")}</option>
          {options.map((v) => (
            <option key={v.id} value={v.id}>
              {v.name}
            </option>
          ))}
        </select>
      </label>
      {list.isError && <p role="alert">{viewErrorMessage(t, list.error)}</p>}
      {(save.isError || refresh.isError) && (
        <div role="alert">
          <p>{spaceErrorMessage(t, save.error ?? refresh.error)}</p>
          <Button
            variant="secondary"
            disabled={pending}
            onClick={() => refresh.mutate()}
          >
            {t("tasks.defaultView.refresh")}
          </Button>
        </div>
      )}
    </Dialog>
  );
}
