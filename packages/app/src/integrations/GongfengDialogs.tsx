import { useState, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  isApiError,
  type GongfengProject,
  type GongfengRepository,
  type GongfengRepositoryReconcile,
  type IntegrationConnection,
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
import {
  gongfengError,
  gongfengPrivacy,
  gongfengPushValid,
  gongfengSelectable,
  gongfengWindowValid,
} from "./gongfeng-model";

type Direction = "INBOUND" | "BIDIRECTIONAL";
type Options = {
  direction: Direction;
  mr_enabled: boolean;
  commit_enabled: boolean;
  backlink_enabled: boolean;
};
type BaseProps = {
  orgId: string;
  connection: IntegrationConnection;
  version: number;
  refresh: () => void;
  onClose: () => void;
};
function Check({
  children,
  checked,
  onChange,
  disabled,
}: {
  children: ReactNode;
  checked: boolean;
  onChange: (value: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <label className="flex items-start gap-2 text-body-sm">
      <Checkbox
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      {children}
    </label>
  );
}
function OptionsFields({
  value,
  onChange,
  privacy,
  disabled,
}: {
  value: Options;
  onChange: (value: Options) => void;
  privacy: boolean | null;
  disabled: boolean;
}) {
  const t = useT();
  return (
    <>
      <Field label={t("integrations.gongfeng.direction")}>
        <Select
          value={value.direction}
          onChange={(direction) => onChange({ ...value, direction })}
          disabled={disabled}
          options={[
            { value: "INBOUND", label: t("integrations.gongfeng.inbound") },
            {
              value: "BIDIRECTIONAL",
              label: t("integrations.gongfeng.bidirectional"),
            },
          ]}
        />
      </Field>
      <p className="text-body-sm text-fg-muted">
        {t("integrations.gongfeng.directionHelp")}
      </p>
      <Check
        checked={value.mr_enabled}
        onChange={(mr_enabled) => onChange({ ...value, mr_enabled })}
        disabled={disabled}
      >
        {t("integrations.gongfeng.mr")}
      </Check>
      <Check
        checked={value.commit_enabled}
        onChange={(commit_enabled) => onChange({ ...value, commit_enabled })}
        disabled={disabled}
      >
        {t("integrations.gongfeng.commits")}
      </Check>
      <Check
        checked={value.backlink_enabled}
        onChange={(backlink_enabled) =>
          onChange({ ...value, backlink_enabled })
        }
        disabled={disabled || (privacy === null && !value.backlink_enabled)}
      >
        {t("integrations.gongfeng.backlink")}
      </Check>
      {privacy === null ? (
        <Callout tone="warning">
          {t("integrations.gongfeng.unknownPrivacy")}
        </Callout>
      ) : (
        !privacy && (
          <Callout tone="warning">
            {t("integrations.gongfeng.visibility")}
          </Callout>
        )
      )}
    </>
  );
}

export function GongfengCreateDialog({
  orgId,
  connection,
  version,
  refresh,
  onDone,
  onClose,
}: BaseProps & { onDone: () => void }) {
  const t = useT(),
    api = useApi();
  const [search, setSearch] = useState(""),
    [query, setQuery] = useState<{ q: string; page: number } | null>(null);
  const [project, setProject] = useState<GongfengProject | null>(null),
    [space, setSpace] = useState<string | null>(null);
  const [history, setHistory] = useState(false),
    [consent, setConsent] = useState(false);
  const [options, setOptions] = useState<Options>({
    direction: "INBOUND",
    mr_enabled: true,
    commit_enabled: false,
    backlink_enabled: false,
  });
  const projects = useQuery({
    queryKey: [
      ...keys.integrations(orgId),
      "gongfeng",
      connection.id,
      "projects",
      query,
    ],
    queryFn: () =>
      api.integrations.gongfengProjects(orgId, connection.id, query!),
    enabled: !!query,
    retry: false,
  });
  const spaces = useQuery({
    queryKey: ["gongfeng-spaces", orgId],
    queryFn: () => api.spaces.list(orgId),
    retry: false,
  });
  const create = useMutation({
    mutationFn: () =>
      api.integrations.createGongfengRepository(orgId, {
        connection_id: connection.id,
        connection_version: version,
        project_id: project!.id,
        space_id: space!,
        import_existing: history,
        ...options,
      }),
    onSuccess: onDone,
    onError: (e) => {
      if (isApiError(e) && e.status === 409) refresh();
    },
  });
  function select(p: GongfengProject) {
    setProject(p);
    setOptions((o) => ({
      ...o,
      backlink_enabled: gongfengPrivacy(p) === true,
    }));
  }
  return (
    <Dialog
      open
      width={600}
      title={t("integrations.gongfeng.add")}
      onClose={create.isPending ? undefined : onClose}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={create.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button
            loading={create.isPending}
            disabled={!project || !space || !consent}
            onClick={() => create.mutate()}
          >
            {t("integrations.gongfeng.create")}
          </Button>
        </>
      }
    >
      <div className="grid gap-4">
        <Field label={t("integrations.gongfeng.search")}>
          <TextInput
            value={search}
            maxLength={200}
            onChange={(e) => setSearch(e.target.value)}
            disabled={create.isPending}
          />
        </Field>
        <Button
          variant="outline"
          disabled={create.isPending}
          loading={projects.isFetching}
          onClick={() => {
            setQuery({ q: search, page: 1 });
            if (query?.q === search && query.page === 1)
              void projects.refetch();
          }}
        >
          {t("integrations.gongfeng.browse")}
        </Button>
        {projects.isError && (
          <Callout tone="danger">{gongfengError(t, projects.error)}</Callout>
        )}
        {projects.data && (
          <>
            <Field label={t("integrations.gongfeng.project")}>
              <Select
                value={project ? String(project.id) : null}
                disabled={create.isPending}
                onChange={(id) => {
                  const p = projects.data.projects.find(
                    (p) => String(p.id) === id,
                  );
                  if (p) select(p);
                }}
                options={[
                  ...(project &&
                  !projects.data.projects.some((p) => p.id === project.id)
                    ? [
                        {
                          value: String(project.id),
                          label: project.path_with_namespace,
                        },
                      ]
                    : []),
                  ...projects.data.projects
                    .filter(gongfengSelectable)
                    .map((p) => ({
                      value: String(p.id),
                      label: p.path_with_namespace,
                    })),
                ]}
              />
            </Field>
            {!projects.data.projects.some(gongfengSelectable) && (
              <p className="text-body-sm">
                {t("integrations.gongfeng.noProjects")}
              </p>
            )}
            <div className="flex gap-2">
              <Button
                variant="outline"
                disabled={!query || query.page <= 1 || create.isPending}
                onClick={() =>
                  query && setQuery({ ...query, page: query.page - 1 })
                }
              >
                {t("integrations.gongfeng.previous")}
              </Button>
              <Button
                variant="outline"
                disabled={!projects.data.next_page || create.isPending}
                onClick={() =>
                  query && setQuery({ ...query, page: projects.data.next_page })
                }
              >
                {t("integrations.gongfeng.next")}
              </Button>
            </div>
          </>
        )}
        {spaces.isError && (
          <Callout tone="danger">{gongfengError(t, spaces.error)}</Callout>
        )}
        {spaces.data && (
          <>
            <Field label={t("integrations.gongfeng.space")}>
              <Select
                value={space}
                onChange={setSpace}
                disabled={create.isPending}
                options={spaces.data.map((s) => ({
                  value: s.id,
                  label: s.name,
                }))}
              />
            </Field>
            {!spaces.data.length && (
              <p className="text-body-sm">
                {t("integrations.gongfeng.noSpaces")}
              </p>
            )}
          </>
        )}
        <OptionsFields
          value={options}
          onChange={setOptions}
          privacy={project ? gongfengPrivacy(project) : null}
          disabled={create.isPending}
        />
        <Check
          checked={history}
          onChange={setHistory}
          disabled={create.isPending}
        >
          {t("integrations.gongfeng.history")}
        </Check>
        <Callout tone="info">{t("integrations.gongfeng.hookHelp")}</Callout>
        <Check
          checked={consent}
          onChange={setConsent}
          disabled={create.isPending}
        >
          {t("integrations.gongfeng.hookConsent")}
        </Check>
        {create.isError && (
          <Callout tone="danger">{gongfengError(t, create.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}

export function GongfengEditDialog({
  orgId,
  connection,
  version,
  repo,
  direction,
  refresh,
  onClose,
}: BaseProps & { repo: GongfengRepository; direction: Direction }) {
  const t = useT(),
    api = useApi();
  const [options, setOptions] = useState<Options>({
    direction,
    mr_enabled: repo.mr_enabled,
    commit_enabled: repo.commit_enabled,
    backlink_enabled: repo.backlink_enabled,
  });
  const save = useMutation({
    mutationFn: () =>
      api.integrations.updateGongfengRepository(orgId, connection.id, repo.id, {
        connection_version: version,
        generation: repo.generation,
        ...options,
      }),
    onSuccess: () => {
      refresh();
      onClose();
    },
    onError: (e) => {
      if (isApiError(e) && e.status === 409) refresh();
    },
  });
  return (
    <Dialog
      open
      title={t("integrations.gongfeng.edit")}
      description={repo.path}
      onClose={save.isPending ? undefined : onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={save.isPending}>
            {t("common.cancel")}
          </Button>
          <Button onClick={() => save.mutate()} loading={save.isPending}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <div className="grid gap-3">
        <OptionsFields
          value={options}
          onChange={setOptions}
          privacy={repo.private}
          disabled={save.isPending}
        />
        {save.isError && (
          <Callout tone="danger">{gongfengError(t, save.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}

export function GongfengReconcileDialog({
  orgId,
  connection,
  version,
  repo,
  refresh,
  onClose,
}: BaseProps & { repo: GongfengRepository }) {
  const t = useT(),
    api = useApi();
  const [mode, setMode] =
      useState<GongfengRepositoryReconcile["mode"]>("issues"),
    [from, setFrom] = useState(""),
    [to, setTo] = useState(""),
    [ref, setRef] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const valid =
    mode === "push"
      ? gongfengPushValid(from, to, ref)
      : gongfengWindowValid(from, to);
  const reconcile = useMutation({
    mutationFn: () =>
      api.integrations.reconcileGongfengRepository(
        orgId,
        connection.id,
        repo.id,
        {
          connection_version: version,
          generation: repo.generation,
          mode,
          from,
          to,
          ...(mode === "push" ? { ref } : {}),
        },
      ),
    onSuccess: () => {
      refresh();
      onClose();
    },
    onError: (e) => {
      if (isApiError(e) && e.status === 409) refresh();
    },
  });
  return (
    <Dialog
      open
      title={t("integrations.gongfeng.reconcile")}
      description={t("integrations.gongfeng.reconcileHelp")}
      onClose={reconcile.isPending ? undefined : onClose}
      footer={
        <>
          <Button
            variant="outline"
            disabled={reconcile.isPending}
            onClick={onClose}
          >
            {t("common.cancel")}
          </Button>
          <Button
            loading={reconcile.isPending}
            onClick={() => {
              setSubmitted(true);
              if (valid) reconcile.mutate();
            }}
          >
            {t("integrations.gongfeng.reconcile")}
          </Button>
        </>
      }
    >
      <div className="grid gap-3">
        <Field label={t("integrations.gongfeng.mode")}>
          <Select
            value={mode}
            disabled={reconcile.isPending}
            onChange={(v) => {
              setMode(v);
              setFrom("");
              setTo("");
              setSubmitted(false);
            }}
            options={[
              { value: "issues", label: t("integrations.gongfeng.issues") },
              {
                value: "merge_requests",
                label: t("integrations.gongfeng.merge_requests"),
                disabled: !repo.mr_enabled,
              },
              {
                value: "push",
                label: t("integrations.gongfeng.push"),
                disabled: !repo.commit_enabled,
              },
            ]}
          />
        </Field>
        <Field
          label={t(
            mode === "push"
              ? "integrations.gongfeng.fromSHA"
              : "integrations.gongfeng.from",
          )}
        >
          <TextInput
            value={from}
            maxLength={64}
            onChange={(e) => setFrom(e.target.value)}
            disabled={reconcile.isPending}
            placeholder={mode === "push" ? undefined : "2026-11-14T00:00:00Z"}
          />
        </Field>
        <Field
          label={t(
            mode === "push"
              ? "integrations.gongfeng.toSHA"
              : "integrations.gongfeng.to",
          )}
        >
          <TextInput
            value={to}
            maxLength={64}
            onChange={(e) => setTo(e.target.value)}
            disabled={reconcile.isPending}
            placeholder={mode === "push" ? undefined : "2026-11-14T12:00:00Z"}
          />
        </Field>
        {mode === "push" && (
          <Field label={t("integrations.gongfeng.ref")}>
            <TextInput
              value={ref}
              maxLength={1024}
              onChange={(e) => setRef(e.target.value)}
              disabled={reconcile.isPending}
            />
          </Field>
        )}
        {submitted && !valid && (
          <Callout tone="warning">
            {t(
              mode === "push"
                ? "integrations.gongfeng.pushInvalid"
                : "integrations.gongfeng.windowInvalid",
            )}
          </Callout>
        )}
        {reconcile.isError && (
          <Callout tone="danger">{gongfengError(t, reconcile.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
