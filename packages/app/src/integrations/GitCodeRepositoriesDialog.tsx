import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  IntegrationConnection,
  IntegrationRepository,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Checkbox,
  Dialog,
  Field,
  Select,
  TextInput,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import { useSpaceList, canWriteSpace } from "../spaces/queries";
import { gitcodeApi } from "./gitcode-api";
import { integrationErrorText } from "./queries";
export function GitCodeRepositoriesDialog({
  orgId,
  connection,
  onClose,
}: {
  orgId: string;
  connection: IntegrationConnection;
  onClose: () => void;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient(),
    spaces = useSpaceList(orgId);
  const [query, setQuery] = useState(""),
    [search, setSearch] = useState(""),
    [page, setPage] = useState(1),
    [repository, setRepository] = useState<IntegrationRepository | null>(null),
    [space, setSpace] = useState(""),
    [bidi, setBidi] = useState(false),
    [history, setHistory] = useState(false);
  const list = useQuery({
    queryKey: [
      ...keys.integrations(orgId),
      "gitcode-repositories",
      connection.id,
      search,
      page,
    ],
    queryFn: () =>
      gitcodeApi(api.http).repositories(orgId, connection.id, search, page),
    retry: false,
  });
  const add = useMutation({
    mutationFn: () =>
      gitcodeApi(api.http).add(orgId, connection.id, {
        version: connection.version,
        repository_id: repository!.external_id!,
        repository: repository!.full_name,
        space_id: space,
        direction: bidi ? "BIDIRECTIONAL" : "INBOUND",
        import_existing: history,
      }),
    onSuccess: () => {
      void cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
      onClose();
    },
  });
  return (
    <Dialog
      open
      title={t("integrations.gitcode.add")}
      onClose={add.isPending ? undefined : onClose}
      footer={
        <>
          <Button variant="outline" disabled={add.isPending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={!repository?.external_id || !space || add.isPending}
            loading={add.isPending}
            onClick={() => add.mutate()}
          >
            {t("integrations.gitcode.addConfirm")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Callout tone="info">{t("integrations.gitcode.setupHelp")}</Callout>
        <form
          className="flex items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            setSearch(query.trim());
            setPage(1);
            setRepository(null);
          }}
        >
          <Field label={t("integrations.gitcode.search")}>
            <TextInput
              maxLength={200}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </Field>
          <Button type="submit" variant="outline" disabled={list.isFetching}>
            {t("integrations.gitcode.search")}
          </Button>
        </form>
        {(list.isError || add.isError || spaces.isError) && (
          <Callout tone="danger">
            {integrationErrorText(t, add.error ?? list.error ?? spaces.error)}
          </Callout>
        )}
        <Field label={t("integrations.gitcode.repository")}>
          <Select
            value={repository?.external_id ?? null}
            disabled={list.isFetching || add.isPending}
            options={(list.data?.repositories ?? []).map((r) => ({
              value: r.external_id ?? "",
              label: `${r.full_name} · ${r.private ? "private" : "public"}`,
              disabled: !r.external_id || !!r.linked_space_id,
            }))}
            onChange={(id) =>
              setRepository(
                list.data?.repositories.find((r) => r.external_id === id) ??
                  null,
              )
            }
          />
        </Field>
        <div className="flex items-center gap-2">
          <Button
            size="28"
            variant="outline"
            disabled={page === 1 || list.isFetching || add.isPending}
            onClick={() => {
              setPage((v) => v - 1);
              setRepository(null);
            }}
          >
            {t("integrations.gitcode.previous")}
          </Button>
          <span>{page}</span>
          <Button
            size="28"
            variant="outline"
            disabled={
              !list.data?.has_more ||
              page >= 50 ||
              list.isFetching ||
              add.isPending
            }
            onClick={() => {
              setPage((v) => v + 1);
              setRepository(null);
            }}
          >
            {t("integrations.gitcode.next")}
          </Button>
        </div>
        {page === 50 && list.data?.has_more && (
          <Callout tone="warning">
            {t("integrations.gitcode.repoLimit")}
          </Callout>
        )}
        <Field label={t("integrations.gitcode.space")}>
          <Select
            value={space || null}
            onChange={setSpace}
            disabled={add.isPending}
            options={(spaces.data ?? [])
              .filter(canWriteSpace)
              .map((s) => ({ value: s.id, label: s.name }))}
          />
        </Field>
        <label className="flex items-center gap-2">
          <Checkbox
            checked={bidi}
            onChange={(e) => setBidi(e.target.checked)}
            disabled={add.isPending}
          />
          {t("integrations.gitcode.bidirectional")}
        </label>
        <p className="text-body-sm text-fg-muted">
          {t("integrations.gitcode.bidiHelp")}
        </p>
        <label className="flex items-center gap-2">
          <Checkbox
            checked={history}
            onChange={(e) => setHistory(e.target.checked)}
            disabled={add.isPending}
          />
          {t("integrations.gitcode.history")}
        </label>
        <p className="text-body-sm text-fg-muted">
          {t("integrations.gitcode.historyHelp")}
        </p>
      </div>
    </Dialog>
  );
}
