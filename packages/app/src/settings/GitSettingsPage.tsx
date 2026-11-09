import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useParams } from "react-router-dom";
import { isApiError, type GitSettings, type SpaceDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, Select, Switch, TextInput, useToast } from "@openfox/ui";
import { useApi } from "../context";
import { isRetired, useSpaceList } from "../spaces/queries";
import { SettingsColumn, SettingsSection } from "../spaces/settings/parts";
import { ReposSection } from "../spaces/settings/ReposSection";
import { GIT_NAME_MODES, GIT_EMAIL_MODES, gitSettingsDraft } from "./gitModes";

export function GitSettingsPage() {
  const { orgId = "" } = useParams();
  const spaces = useSpaceList(orgId);
  const t = useT();
  return (
    <SettingsColumn wide>
      <h1 className="px-1 text-heading font-semibold">
        {t("gitSettings.title")}
      </h1>
      <GitSettingsSection orgId={orgId} />
      <SettingsSection
        title={t("gitSettings.spaces")}
        description={t("gitSettings.spacesHelp")}
      >
        {spaces.isError ? (
          <Button variant="outline" onClick={() => void spaces.refetch()}>
            {t("common.retry")}
          </Button>
        ) : (
          <div className="flex flex-col gap-3">
            {(spaces.data ?? []).map((space) => (
              <SpaceGitSettings key={space.id} orgId={orgId} space={space} />
            ))}
            {spaces.isSuccess && !spaces.data?.length && (
              <p className="p-4 text-body-sm text-fg-muted">
                {t("gitSettings.noSpaces")}
              </p>
            )}
          </div>
        )}
      </SettingsSection>
    </SettingsColumn>
  );
}

function SpaceGitSettings({
  orgId,
  space,
}: {
  orgId: string;
  space: SpaceDetail;
}) {
  const [expanded, setExpanded] = useState(false);
  const [visited, setVisited] = useState(false);
  const contentId = useId();
  return (
    <section
      className="rounded-xl border border-line-default"
      aria-label={space.name}
    >
      <h3>
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={contentId}
          className="flex w-full items-center gap-3 rounded-xl px-4 py-3 text-left hover:bg-surface-hover"
          onClick={() => {
            setExpanded(!expanded);
            setVisited(true);
          }}
        >
          <Icon name="folder-git-2" className="text-fg-muted" />
          <span className="min-w-0 flex-1 truncate">{space.name}</span>
          <Icon
            name={expanded ? "chevron-up" : "chevron-down"}
            className="text-fg-muted"
          />
        </button>
      </h3>
      <div id={contentId} hidden={!expanded}>
        {visited && (
          <div className="flex flex-col gap-6 border-t border-line-soft p-4">
            <ReposSection
              orgId={orgId}
              space={space}
              retired={isRetired(space)}
              id={`${contentId}-repos`}
            />
            <GitSettingsSection
              orgId={orgId}
              spaceId={space.id}
              disabled={isRetired(space)}
            />
          </div>
        )}
      </div>
    </section>
  );
}

export function GitSettingsSection({
  orgId,
  spaceId,
  disabled = false,
}: {
  orgId: string;
  spaceId?: string;
  disabled?: boolean;
}) {
  const api = useApi();
  const t = useT();
  const settings = useQuery({
    queryKey: ["git-settings", orgId, spaceId ?? ""],
    queryFn: () => api.gitSettings.get(orgId, spaceId),
  });
  const parent = useQuery({
    queryKey: ["git-settings", orgId, ""],
    queryFn: () => api.gitSettings.get(orgId),
    enabled: Boolean(spaceId),
  });
  return (
    <SettingsSection
      title={t(spaceId ? "gitSettings.spaceTitle" : "gitSettings.globalTitle")}
      description={t("gitSettings.description")}
    >
      {settings.isError ? (
        <div role="alert" className="flex items-center gap-3 text-body-sm">
          <span>{t("gitSettings.loadFailed")}</span>
          <Button variant="outline" onClick={() => void settings.refetch()}>
            {t("common.retry")}
          </Button>
        </div>
      ) : settings.data ? (
        <GitSettingsForm
          key={`${orgId}:${spaceId ?? ""}`}
          orgId={orgId}
          spaceId={spaceId}
          settings={settings.data}
          parent={spaceId ? parent.data?.effective : undefined}
          disabled={disabled}
        />
      ) : (
        <p role="status" className="text-body-sm text-fg-muted">
          {t("common.loading")}
        </p>
      )}
    </SettingsSection>
  );
}

function GitSettingsForm({
  orgId,
  spaceId,
  settings,
  parent,
  disabled,
}: {
  orgId: string;
  spaceId?: string;
  settings: GitSettings;
  parent?: GitSettings["effective"];
  disabled: boolean;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const t = useT();
  const toast = useToast();
  const saved = gitSettingsDraft(settings, Boolean(spaceId));
  const [draft, setDraft] = useState(() => saved);
  const save = useMutation({
    mutationFn: () =>
      api.gitSettings.put(
        orgId,
        {
          version: draft.version,
          author_name_mode: draft.author_name_mode,
          author_email_mode: draft.author_email_mode,
          author_name:
            draft.author_name_mode === "MANUAL" ? draft.author_name.trim() : "",
          author_email:
            draft.author_email_mode === "MANUAL"
              ? draft.author_email.trim()
              : "",
          co_author_enabled: draft.co_author_enabled,
        },
        spaceId,
      ),
    onSuccess: (next) => {
      setDraft(gitSettingsDraft(next, Boolean(spaceId)));
      queryClient.setQueryData(["git-settings", orgId, spaceId ?? ""], next);
      void queryClient.invalidateQueries({ queryKey: ["git-settings", orgId] });
      toast(t("organization.saved"));
    },
  });
  const readonly = disabled || !settings.can_manage || save.isPending;
  const invalid =
    (draft.author_email_mode === "MANUAL" &&
      !/^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/.test(draft.author_email.trim())) ||
    (draft.author_name_mode === "MANUAL" &&
      (!draft.author_name.trim() ||
        /[<>\u0000-\u001f\u007f]/.test(draft.author_name)));
  const changed =
    draft.author_name_mode !== saved.author_name_mode ||
    draft.author_email_mode !== saved.author_email_mode ||
    draft.author_name !== settings.author_name ||
    draft.author_email !== settings.author_email ||
    draft.co_author_enabled !== settings.co_author_enabled;
  return (
    <form
      className="flex flex-col gap-4 rounded-xl border border-line-default p-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (!readonly && !invalid && changed) save.mutate();
      }}
    >
      <div className="flex flex-wrap items-center justify-between gap-3 text-body-sm">
        <div>
          <p>{t("agents.sections.git.authorName")}</p>
          <p className="mt-1 text-caption text-fg-muted">
            {t("agents.sections.git.authorNameDesc")}
          </p>
        </div>
        <Select
          aria-label={t("agents.sections.git.authorName")}
          className="w-72 max-w-full"
          disabled={readonly}
          value={draft.author_name_mode}
          options={GIT_NAME_MODES.filter(
            (mode) => spaceId || mode !== "INHERIT",
          ).map((mode) => ({
            value: mode,
            label:
              mode === "INHERIT"
                ? t("gitSettings.inheritOrganization")
                : t(`agents.sections.git.nameModes.${mode}`),
            description:
              mode === "INHERIT" && parent?.author_name_mode
                ? parent.author_name_mode === "MANUAL"
                  ? parent.author_name
                  : t(
                      `agents.sections.git.nameModes.${parent.author_name_mode}`,
                    )
                : undefined,
          }))}
          onChange={(mode) => setDraft({ ...draft, author_name_mode: mode })}
        />
      </div>
      {draft.author_name_mode === "MANUAL" && (
        <TextInput
          aria-label={t("agents.sections.git.authorName")}
          maxLength={128}
          value={draft.author_name}
          disabled={readonly}
          placeholder={t("agents.sections.git.manualName")}
          onChange={(event) =>
            setDraft({ ...draft, author_name: event.target.value })
          }
        />
      )}
      <div className="flex flex-wrap items-center justify-between gap-3 text-body-sm">
        <div>
          <p>{t("agents.sections.git.authorEmail")}</p>
          <p className="mt-1 text-caption text-fg-muted">
            {t("agents.sections.git.authorEmailDesc")}
          </p>
        </div>
        <Select
          aria-label={t("agents.sections.git.authorEmail")}
          className="w-72 max-w-full"
          disabled={readonly}
          value={draft.author_email_mode}
          options={GIT_EMAIL_MODES.filter(
            (mode) => spaceId || mode !== "INHERIT",
          ).map((mode) => ({
            value: mode,
            label:
              mode === "INHERIT"
                ? t("gitSettings.inheritOrganization")
                : t(`agents.sections.git.emailModes.${mode}`),
            description:
              mode === "INHERIT" && parent?.author_email_mode
                ? parent.author_email_mode === "MANUAL"
                  ? parent.author_email
                  : t(
                      `agents.sections.git.emailModes.${parent.author_email_mode}`,
                    )
                : undefined,
          }))}
          onChange={(mode) => setDraft({ ...draft, author_email_mode: mode })}
        />
      </div>
      {draft.author_email_mode === "MANUAL" && (
        <TextInput
          aria-label={t("agents.sections.git.authorEmail")}
          type="email"
          maxLength={254}
          value={draft.author_email}
          disabled={readonly}
          placeholder={t("agents.sections.git.manualEmail")}
          onChange={(event) =>
            setDraft({ ...draft, author_email: event.target.value })
          }
        />
      )}
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-48 flex-1">
          <p className="text-body-sm">{t("gitSettings.coAuthor")}</p>
          <p className="mt-1 text-caption text-fg-muted">
            {t("gitSettings.coAuthorHelp")}
          </p>
        </div>
        {spaceId ? (
          <Select
            aria-label={t("gitSettings.coAuthor")}
            className="w-72 max-w-full"
            disabled={readonly}
            value={
              draft.co_author_enabled == null
                ? "inherit"
                : draft.co_author_enabled
                  ? "on"
                  : "off"
            }
            options={[
              ...(spaceId
                ? [{ value: "inherit", label: t("gitSettings.inherit") }]
                : []),
              { value: "on", label: t("gitSettings.on") },
              { value: "off", label: t("gitSettings.off") },
            ]}
            onChange={(value) =>
              setDraft({
                ...draft,
                co_author_enabled: value === "inherit" ? null : value === "on",
              })
            }
          />
        ) : (
          <Switch
            label={t("gitSettings.coAuthor")}
            tone="success"
            disabled={readonly}
            checked={draft.co_author_enabled === true}
            onCheckedChange={(enabled) =>
              setDraft({ ...draft, co_author_enabled: enabled })
            }
          />
        )}
      </div>
      {save.isError && (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-2 text-body-sm text-danger"
        >
          {t(
            isApiError(save.error) && save.error.status === 409
              ? "gitSettings.conflict"
              : "organization.saveFailed",
          )}
          <Button
            type="button"
            variant="ghost"
            onClick={async () => {
              try {
                const next = await api.gitSettings.get(orgId, spaceId);
                setDraft(gitSettingsDraft(next, Boolean(spaceId)));
                queryClient.setQueryData(
                  ["git-settings", orgId, spaceId ?? ""],
                  next,
                );
                save.reset();
              } catch {
                toast(t("gitSettings.loadFailed"), { tone: "error" });
              }
            }}
          >
            {t("gitSettings.reload")}
          </Button>
        </div>
      )}
      {settings.can_manage && !disabled && (
        <Button
          type="submit"
          className="self-end"
          disabled={readonly || invalid || !changed}
        >
          {t("common.save")}
        </Button>
      )}
    </form>
  );
}
