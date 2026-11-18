import { BitbucketJobs } from "./BitbucketJobs";
import { useEffect, useState } from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  bitbucketApi,
  type BitbucketRepositoryInput,
  type BitbucketRepositorySelection,
  type IntegrationConnection,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  Dialog,
  Select,
  TextInput,
  useToast,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import { integrationErrorText } from "./queries";

export function BitbucketRepositoriesDialog({
  org,
  connection,
  close,
}: {
  org: string;
  connection: IntegrationConnection;
  close: () => void;
}) {
  const api = useApi(),
    t = useT(),
    cache = useQueryClient(),
    client = bitbucketApi(api.http);
  const toast = useToast();
  const copy = async (value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      toast(t("common.copied"));
    } catch {
      toast(t("integrations.bitbucket.copyFailed"), { tone: "error" });
    }
  };
  const active = connection.status === "ACTIVE";
  useEffect(() => {
    setEditing(null);
    setRotateConfirmed(false);
  }, [connection.version]);
  const [editing, setEditing] = useState<
    BitbucketRepositorySelection | "new" | null
  >(null);
  const [removing, setRemoving] = useState<BitbucketRepositorySelection | null>(
    null,
  );
  const [removeConfirmed, setRemoveConfirmed] = useState(false);
  const [showHook, setShowHook] = useState(false),
    [showSecret, setShowSecret] = useState(false);
  const [rotateConfirmed, setRotateConfirmed] = useState(false);
  const selected = useQuery({
    queryKey: ["bitbucket-repositories", org, connection.id],
    queryFn: () => client.selections(org, connection.id),
    retry: false,
    refetchInterval: 5000,
  });
  const hook = useQuery({
    queryKey: ["bitbucket-hook", org, connection.id],
    queryFn: () => client.webhook(org, connection.id),
    enabled: showHook,
    retry: false,
    staleTime: 0,
    gcTime: 0,
  });
  const rotate = useMutation({
    mutationFn: () =>
      client.rotateWebhook(org, connection.id, hook.data!.version),
    onSuccess: async (result) => {
      setRotateConfirmed(false);
      setShowSecret(false);
      cache.setQueryData(["bitbucket-hook", org, connection.id], result);
      await Promise.all([
        cache.invalidateQueries({ queryKey: keys.integrations(org) }),
        selected.refetch(),
      ]);
    },
  });
  const remove = useMutation({
    mutationFn: () =>
      client.removeRepository(
        org,
        connection.id,
        removing!.id,
        removing!.version,
      ),
    onSuccess: async () => {
      setRemoving(null);
      setRemoveConfirmed(false);
      await selected.refetch();
    },
  });
  return (
    <Dialog
      open
      onClose={close}
      title={t("integrations.bitbucket.repositoriesTitle")}
      width={680}
    >
      <div className="grid gap-4">
        <p className="text-body-sm text-fg-muted">
          {t("integrations.bitbucket.repositoryScope")}
        </p>
        {!active && (
          <Callout tone="warning">
            {t("integrations.bitbucket.needsAttention")}
          </Callout>
        )}
        {selected.isError && (
          <Callout tone="warning">
            {integrationErrorText(t, selected.error)}
          </Callout>
        )}
        <Button
          size="32"
          variant="outline"
          disabled={!active || (selected.data?.repositories.length ?? 0) >= 100}
          onClick={() => setEditing("new")}
        >
          {t("integrations.bitbucket.selectRepository")}
        </Button>
        <div className="grid gap-3">
          {selected.data?.repositories.map((item) => (
            <div
              key={item.id}
              className="grid gap-2 rounded border border-line-default p-3"
            >
              <div className="flex flex-wrap items-center gap-2">
                <strong>{item.full_name}</strong>
                <Badge
                  tone={
                    item.hook_ready && item.status === "ACTIVE"
                      ? "success"
                      : "warning"
                  }
                >
                  {t(
                    item.hook_ready
                      ? "integrations.bitbucket.hookReady"
                      : "integrations.bitbucket.hookPending",
                  )}
                </Badge>
              </div>
              <p className="text-caption text-fg-muted">{item.repository_id}</p>
              <p className="text-body-sm">
                {t(
                  item.is_private
                    ? "integrations.bitbucket.privateRepository"
                    : item.is_private === false
                      ? "integrations.bitbucket.publicRepository"
                      : "integrations.bitbucket.privacyUnknown",
                )}
              </p>
              {item.status !== "ACTIVE" && (
                <Callout tone="warning">
                  {t("integrations.bitbucket.repositoryPaused")}{" "}
                  {item.error_code}
                </Callout>
              )}
              <div className="flex gap-2">
                <Button
                  size="28"
                  variant="outline"
                  disabled={!active}
                  onClick={() => setEditing(item)}
                >
                  {t("integrations.bitbucket.repositorySettings")}
                </Button>
                <Button
                  size="28"
                  variant="outline"
                  onClick={() => {
                    setRemoving(item);
                    setRemoveConfirmed(false);
                  }}
                >
                  {t("integrations.bitbucket.removeRepository")}
                </Button>
              </div>
            </div>
          ))}
        </div>
        {removing && (
          <Callout tone="warning">
            <p>{removing.full_name}</p>
            <p>{t("integrations.bitbucket.removeDescription")}</p>
            <label className="flex gap-2">
              <input
                type="checkbox"
                checked={removeConfirmed}
                onChange={(e) => setRemoveConfirmed(e.target.checked)}
              />
              {t("integrations.bitbucket.removeConsent")}
            </label>
            {remove.isError && (
              <p role="alert">{integrationErrorText(t, remove.error)}</p>
            )}
            <div className="flex gap-2">
              <Button
                disabled={!removeConfirmed}
                loading={remove.isPending}
                onClick={() => remove.mutate()}
              >
                {t("integrations.bitbucket.confirmRemove")}
              </Button>
              <Button variant="ghost" onClick={() => setRemoving(null)}>
                {t("common.cancel")}
              </Button>
            </div>
          </Callout>
        )}
        <section className="grid gap-3 border-t border-line-default pt-4">
          <h3 className="text-body font-medium">
            {t("integrations.bitbucket.webhookTitle")}
          </h3>
          <p className="text-body-sm">
            {t("integrations.bitbucket.webhookInstructions")}
          </p>
          {!showHook ? (
            <Button
              size="32"
              variant="outline"
              onClick={() => setShowHook(true)}
            >
              {t("integrations.bitbucket.viewWebhook")}
            </Button>
          ) : (
            <>
              {hook.isError && (
                <Callout tone="warning">
                  {integrationErrorText(t, hook.error)}
                </Callout>
              )}
              {hook.data && (
                <>
                  {hook.data.payload_url ? (
                    <>
                      <label className="grid gap-1 text-body-sm">
                        {t("integrations.bitbucket.payloadURL")}
                        <TextInput
                          aria-label={t("integrations.bitbucket.payloadURL")}
                          value={hook.data.payload_url}
                          readOnly
                        />
                      </label>
                      <label className="grid gap-1 text-body-sm">
                        {t("integrations.bitbucket.signingSecret")}
                        <TextInput
                          aria-label={t("integrations.bitbucket.signingSecret")}
                          type={showSecret ? "text" : "password"}
                          value={hook.data.secret}
                          readOnly
                        />
                      </label>
                      <div className="flex flex-wrap gap-2">
                        <Button
                          size="28"
                          variant="outline"
                          onClick={() => void copy(hook.data!.payload_url)}
                        >
                          {t("integrations.bitbucket.copyPayloadURL")}
                        </Button>
                        <Button
                          size="28"
                          variant="outline"
                          onClick={() => void copy(hook.data!.secret)}
                        >
                          {t("integrations.bitbucket.copySecret")}
                        </Button>
                      </div>
                      <Button
                        size="28"
                        variant="ghost"
                        onClick={() => setShowSecret((v) => !v)}
                      >
                        {t(
                          showSecret
                            ? "integrations.bitbucket.hideSecret"
                            : "integrations.bitbucket.showSecret",
                        )}
                      </Button>
                    </>
                  ) : (
                    <Callout tone="info">
                      {t("integrations.bitbucket.webhookUnconfigured")}
                    </Callout>
                  )}
                  <p className="text-body-sm">
                    Content-Type: {hook.data.content_type}
                  </p>
                  <ul className="list-disc pl-5 text-body-sm">
                    {hook.data.events.map((event) => (
                      <li key={event}>{event}</li>
                    ))}
                  </ul>
                  <Callout tone="warning">
                    <p>{t("integrations.bitbucket.rotateDescription")}</p>
                    <label className="flex gap-2">
                      <input
                        type="checkbox"
                        checked={rotateConfirmed}
                        onChange={(e) => setRotateConfirmed(e.target.checked)}
                      />
                      {t("integrations.bitbucket.rotateConsent")}
                    </label>
                  </Callout>
                  {rotate.isError && (
                    <Callout tone="warning">
                      {integrationErrorText(t, rotate.error)}
                    </Callout>
                  )}
                  <Button
                    disabled={!active || !rotateConfirmed}
                    loading={rotate.isPending}
                    onClick={() => rotate.mutate()}
                  >
                    {t(
                      hook.data.payload_url
                        ? "integrations.bitbucket.rotateWebhook"
                        : "integrations.bitbucket.createWebhook",
                    )}
                  </Button>
                  <h4 className="text-body-sm font-medium">
                    {t("integrations.bitbucket.recentDeliveries")}
                  </h4>
                  <p className="text-body-sm text-fg-muted">
                    {t("integrations.bitbucket.queuedNotice")}
                  </p>
                  {hook.data.recent.map((event, index) => (
                    <p
                      key={`${event.received_at}-${index}`}
                      className="text-caption"
                    >
                      {event.event} · {event.status} · {event.detail}
                    </p>
                  ))}
                </>
              )}
            </>
          )}
        </section>
        <BitbucketJobs org={org} connection={connection} />
        {editing && (
          <BitbucketRepositoryEditor
            key={editing === "new" ? "new" : `${editing.id}-${editing.version}`}
            org={org}
            connection={connection}
            selection={editing === "new" ? null : editing}
            close={() => setEditing(null)}
            saved={() => {
              setEditing(null);
              void selected.refetch();
            }}
          />
        )}
      </div>
    </Dialog>
  );
}

function BitbucketRepositoryEditor({
  org,
  connection,
  selection,
  close,
  saved,
}: {
  org: string;
  connection: IntegrationConnection;
  selection: BitbucketRepositorySelection | null;
  close: () => void;
  saved: () => void;
}) {
  const api = useApi(),
    t = useT(),
    client = bitbucketApi(api.http);
  const [repository, setRepository] = useState(selection?.repository_id ?? "");
  const [spaceIDs, setSpaceIDs] = useState(selection?.space_ids ?? []);
  const [pulls, setPulls] = useState(selection?.pull_requests ?? true),
    [commits, setCommits] = useState(selection?.commits ?? false);
  const [privateBacklink, setPrivateBacklink] = useState(
      selection?.backlink_private ?? true,
    ),
    [publicBacklink, setPublicBacklink] = useState(
      selection?.backlink_public ?? false,
    );
  const [metadataConsent, setMetadataConsent] = useState(false),
    [senderConsent, setSenderConsent] = useState(false),
    [publicConsent, setPublicConsent] = useState(false);
  const pages = useInfiniteQuery({
    queryKey: ["bitbucket-available", org, connection.id, connection.version],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      client.repositories(org, connection.id, pageParam, signal),
    getNextPageParam: (last, _pages, _last, params) =>
      last.next_cursor && !params.includes(last.next_cursor)
        ? last.next_cursor
        : undefined,
    enabled: !selection,
    retry: false,
  });
  const spaces = useQuery({
    queryKey: keys.spaces(org),
    queryFn: () => api.spaces.list(org),
    retry: false,
  });
  const repositories = Array.from(
    new Map(
      (pages.data?.pages.flatMap((p) => p.repositories) ?? []).map((r) => [
        r.uuid,
        r,
      ]),
    ).values(),
  );
  const save = useMutation({
    mutationFn: () =>
      client.saveRepository(org, connection.id, {
        repository_id: repository,
        connection_version: connection.version,
        version: selection?.version ?? 0,
        space_ids: spaceIDs,
        pull_requests: pulls,
        commits,
        backlink_private: privateBacklink,
        backlink_public: publicBacklink,
        confirm_metadata: true,
        confirm_sender: senderConsent,
        confirm_public: publicConsent,
      } satisfies BitbucketRepositoryInput),
    onSuccess: saved,
  });
  const ready =
    repository &&
    spaceIDs.length > 0 &&
    metadataConsent &&
    (!(privateBacklink || publicBacklink) || senderConsent) &&
    (!publicBacklink || publicConsent) &&
    connection.status === "ACTIVE";
  return (
    <Dialog
      open
      onClose={save.isPending ? undefined : close}
      title={t("integrations.bitbucket.repositorySettings")}
      width={680}
    >
      <div className="grid gap-4">
        {selection ? (
          <p>
            {selection.full_name} · {selection.repository_id}
          </p>
        ) : (
          <>
            {pages.isError && (
              <Callout tone="warning">
                {integrationErrorText(t, pages.error)}
              </Callout>
            )}
            <Select
              aria-label={t("integrations.bitbucket.repository")}
              value={repository || null}
              onChange={setRepository}
              options={repositories.map((r) => ({
                value: r.uuid,
                label: `${r.full_name} · ${t(r.is_private ? "integrations.bitbucket.privateRepository" : "integrations.bitbucket.publicRepository")}`,
              }))}
              placeholder={t("integrations.bitbucket.selectRepository")}
            />
            <p className="text-caption">
              {t("integrations.bitbucket.loadedRepositories", {
                count: repositories.length,
              })}
            </p>
            {pages.hasNextPage && (
              <Button
                size="28"
                variant="outline"
                loading={pages.isFetchingNextPage}
                onClick={() => void pages.fetchNextPage()}
              >
                {t("integrations.bitbucket.loadMore")}
              </Button>
            )}
            {pages.data?.pages.some((p) => p.limit_reached) && (
              <Callout tone="warning">
                {t("integrations.bitbucket.browseLimit")}
              </Callout>
            )}
          </>
        )}
        <fieldset className="grid gap-2">
          <legend className="text-body-sm font-medium">
            {t("integrations.bitbucket.allowedSpaces")}
          </legend>
          {spaces.isError && (
            <Callout tone="warning">
              {integrationErrorText(t, spaces.error)}
            </Callout>
          )}
          {(spaces.data ?? []).map((space) => (
            <label key={space.id} className="flex gap-2">
              <input
                type="checkbox"
                checked={spaceIDs.includes(space.id)}
                onChange={(e) =>
                  setSpaceIDs((ids) =>
                    e.target.checked
                      ? [...ids, space.id]
                      : ids.filter((id) => id !== space.id),
                  )
                }
              />
              {space.name}
            </label>
          ))}
        </fieldset>
        <label className="flex gap-2">
          <input
            type="checkbox"
            checked={pulls}
            onChange={(e) => setPulls(e.target.checked)}
          />
          {t("integrations.bitbucket.pullRequests")}
        </label>
        <label className="flex gap-2">
          <input
            type="checkbox"
            checked={commits}
            onChange={(e) => setCommits(e.target.checked)}
          />
          {t("integrations.bitbucket.commits")}
        </label>
        <label className="flex gap-2">
          <input
            type="checkbox"
            checked={privateBacklink}
            onChange={(e) => setPrivateBacklink(e.target.checked)}
          />
          {t("integrations.bitbucket.privateBacklink")}
        </label>
        <label className="flex gap-2">
          <input
            type="checkbox"
            checked={publicBacklink}
            onChange={(e) => {
              setPublicBacklink(e.target.checked);
              setPublicConsent(false);
            }}
          />
          {t("integrations.bitbucket.publicBacklink")}
        </label>
        <Callout tone="warning">
          <label className="flex gap-2">
            <input
              type="checkbox"
              checked={metadataConsent}
              onChange={(e) => setMetadataConsent(e.target.checked)}
            />
            {t("integrations.bitbucket.metadataConsent")}
          </label>
          {(privateBacklink || publicBacklink) && (
            <label className="flex gap-2">
              <input
                type="checkbox"
                checked={senderConsent}
                onChange={(e) => setSenderConsent(e.target.checked)}
              />
              {t("integrations.bitbucket.backlinkConsent", {
                name:
                  connection.bitbucket_delegating_user ||
                  connection.bitbucket_delegating_user_id ||
                  "",
              })}
            </label>
          )}
          {publicBacklink && (
            <label className="flex gap-2">
              <input
                type="checkbox"
                checked={publicConsent}
                onChange={(e) => setPublicConsent(e.target.checked)}
              />
              {t("integrations.bitbucket.publicConsent")}
            </label>
          )}
        </Callout>
        {save.isError && (
          <Callout tone="warning">
            {integrationErrorText(t, save.error)}
          </Callout>
        )}
        <Button
          disabled={!ready}
          loading={save.isPending}
          onClick={() => save.mutate()}
        >
          {t("common.save")}
        </Button>
      </div>
    </Dialog>
  );
}
