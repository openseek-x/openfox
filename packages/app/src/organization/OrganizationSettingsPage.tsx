import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import {
  isApiError,
  type Organization,
  type OrganizationSettings,
  type OrganizationSettingsPatch,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Dialog,
  Icon,
  IconButton,
  Select,
  Textarea,
  TextInput,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "../spaces/settings/parts";
import { OrgIconPicker } from "./OrgIcon";
import {
  organizationErrorMessage,
  useOrganizationSettings,
  useUpdateOrganization,
} from "./queries";

// 组织设置。

const SLUG_PATTERN = /^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/;

export function OrganizationSettingsPage() {
  const { orgId = "" } = useParams();
  const settings = useOrganizationSettings(orgId);
  // 组织数据未加载时整页空白，不显示骨架屏
  if (!settings.data) return null;
  return <SettingsBody orgId={orgId} settings={settings.data} />;
}

function SettingsBody({
  orgId,
  settings,
}: {
  orgId: string;
  settings: OrganizationSettings;
}) {
  const t = useT();
  const toast = useToast();
  const update = useUpdateOrganization(orgId);
  const [downgrade, setDowngrade] = useState<number | null>(null);
  const api = useApi();
  const busy = update.isPending;
  const save = (patch: Omit<OrganizationSettingsPatch, "version">) =>
    update.mutate(patch, {
      onSuccess: () => toast(t("organization.saved")),
      onError: (error) =>
        toast(organizationErrorMessage(t, error, "organization.saveFailed"), {
          tone: "error",
        }),
    });

  const choosePriority = async (levels: 4 | 5) => {
    if (levels === settings.priority_levels) return;
    if (levels === 5) return save({ priority_levels: 5 });
    const count = await api.organizationSettings
      .lowestPriorityCount(orgId)
      .catch(() => 0);
    if (count > 0) setDowngrade(count);
    else save({ priority_levels: 4 });
  };

  return (
    <SettingsColumn>
      {settings.can_manage && (
        <>
          <SettingsSection
            title={t("organization.title")}
            description={t("organization.description")}
          >
            <SettingsCard>
              <SettingRow
                title={t("organization.iconName")}
                description={t("organization.iconNameDescription")}
                control={
                  <div className="flex w-[276px] items-center gap-2">
                    <OrgIconPicker
                      orgId={orgId}
                      org={settings}
                      disabled={busy}
                      onChange={(icon) => save({ icon })}
                    />
                    <NameField
                      value={settings.name}
                      disabled={busy}
                      onCommit={(name) => save({ name })}
                    />
                  </div>
                }
              />
              <DescriptionRow
                settings={settings}
                disabled={busy}
                onSave={(description, done) =>
                  update.mutate(
                    { description },
                    {
                      onSuccess: () => {
                        toast(t("organization.saved"));
                        done();
                      },
                      onError: (error) =>
                        toast(
                          organizationErrorMessage(
                            t,
                            error,
                            "organization.saveFailed",
                          ),
                          { tone: "error" },
                        ),
                    },
                  )
                }
                saving={busy}
              />
              <SlugRow
                slug={settings.slug}
                disabled={busy}
                onCommit={(slug) => save({ slug })}
              />
              <SettingRow
                title={t("organization.locale")}
                description={t("organization.localeHelp")}
                control={
                  <Select
                    aria-label={t("organization.locale")}
                    value={settings.default_locale}
                    disabled={busy}
                    className="min-w-[140px]"
                    options={[
                      { value: "zh-CN", label: "简体中文" },
                      { value: "en-US", label: "English" },
                    ]}
                    onChange={(value) => {
                      if (value !== settings.default_locale)
                        save({ default_locale: value });
                    }}
                  />
                }
              />
            </SettingsCard>
          </SettingsSection>
          <SettingsSection
            title={t("organization.tasksSection")}
            description={t("organization.tasksDescription")}
          >
            <SettingsCard>
              <SettingRow
                title={t("organization.priority")}
                description={t("organization.priorityHelp")}
                control={
                  <Select
                    aria-label={t("organization.priority")}
                    value={String(settings.priority_levels)}
                    disabled={busy}
                    className="min-w-[180px]"
                    options={[
                      { value: "4", label: t("organization.levels4") },
                      { value: "5", label: t("organization.levels5") },
                    ]}
                    onChange={(value) =>
                      void choosePriority(value === "5" ? 5 : 4)
                    }
                  />
                }
              />
            </SettingsCard>
          </SettingsSection>
        </>
      )}
      <DangerSection orgId={orgId} settings={settings} />
      <ConfirmDialog
        open={downgrade !== null}
        title={t("organization.downgradeTitle")}
        description={t("organization.downgradeBody", { count: downgrade ?? 0 })}
        confirmLabel={t("common.confirm")}
        onCancel={() => setDowngrade(null)}
        onConfirm={() => {
          setDowngrade(null);
          save({ priority_levels: 4 });
        }}
      />
    </SettingsColumn>
  );
}

/** 名称：失焦或回车时保存（非空且有变化）。 */
function NameField({
  value,
  disabled,
  onCommit,
}: {
  value: string;
  disabled: boolean;
  onCommit: (name: string) => void;
}) {
  const t = useT();
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  const commit = () => {
    const next = draft.trim();
    if (!next) return setDraft(value);
    if (next !== value) onCommit(next);
  };
  return (
    <TextInput
      aria-label={t("organization.nameLabel")}
      value={draft}
      maxLength={100}
      disabled={disabled}
      onChange={(event) => setDraft(event.target.value)}
      onBlur={commit}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          commit();
        }
      }}
    />
  );
}

function DescriptionRow({
  settings,
  disabled,
  saving,
  onSave,
}: {
  settings: OrganizationSettings;
  disabled: boolean;
  saving: boolean;
  onSave: (description: string, done: () => void) => void;
}) {
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(settings.description);
  return (
    <>
      <SettingRow
        title={t("organization.descriptionTitle")}
        description={t("organization.descriptionHelp")}
        control={
          <div className="flex items-center gap-1">
            <span
              className={cn(
                "max-w-[280px] truncate text-body-sm sm:max-w-[360px]",
                settings.description ? "text-fg-default" : "text-fg-subtle",
              )}
            >
              {settings.description || t("organization.descriptionPlaceholder")}
            </span>
            <IconButton
              label={t("organization.editDescription")}
              icon={<Icon name="pencil" />}
              size="28"
              disabled={disabled}
              onClick={() => {
                setDraft(settings.description);
                setEditing(true);
              }}
            />
          </div>
        }
      />
      <Dialog
        open={editing}
        width={448}
        onClose={saving ? undefined : () => setEditing(false)}
        title={t("organization.descriptionTitle")}
        footer={
          <>
            <Button
              variant="ghost"
              disabled={saving}
              onClick={() => setEditing(false)}
            >
              {t("common.cancel")}
            </Button>
            <Button
              loading={saving}
              onClick={() => {
                const next = draft.trim();
                if (next === settings.description) return setEditing(false);
                onSave(next, () => setEditing(false));
              }}
            >
              {t("organization.saveChanges")}
            </Button>
          </>
        }
      >
        <Textarea
          autoFocus
          rows={5}
          maxLength={500}
          aria-label={t("organization.descriptionTitle")}
          placeholder={t("organization.descriptionPlaceholder")}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
        />
      </Dialog>
    </>
  );
}

/** 组织 URL：前缀为站点地址（只读），实时校验，失焦且合法且有变化时提交。路由仍用组织 ID。 */
function SlugRow({
  slug,
  disabled,
  onCommit,
}: {
  slug: string;
  disabled: boolean;
  onCommit: (slug: string) => void;
}) {
  const t = useT();
  const [draft, setDraft] = useState(slug);
  useEffect(() => setDraft(slug), [slug]);
  const error: MessageKey | null = !draft
    ? "organization.slugEmpty"
    : SLUG_PATTERN.test(draft)
      ? null
      : "organization.errors.slugInvalid";
  return (
    <SettingRow
      title={t("organization.url")}
      description={
        <span className="flex flex-col gap-0.5">
          {error && draft !== slug && (
            <span role="alert" className="text-danger">
              {t(error)}
            </span>
          )}
          {t("organization.urlHelp")}
        </span>
      }
      control={
        <div className="flex h-8 w-[276px] items-center overflow-hidden rounded-lg border border-line-default focus-within:border-line-focus">
          <span className="shrink-0 pl-2.5 text-body-sm text-fg-muted select-none">
            {window.location.origin}/
          </span>
          <input
            aria-label={t("organization.url")}
            value={draft}
            disabled={disabled}
            placeholder={t("organization.slugPlaceholder")}
            onChange={(event) => setDraft(event.target.value)}
            onBlur={() => {
              if (!error && draft !== slug) onCommit(draft);
            }}
            onKeyDown={(event) => {
              if (event.key === "Enter") event.currentTarget.blur();
            }}
            className="min-w-0 flex-1 bg-transparent pr-2.5 text-body-sm outline-none"
          />
        </div>
      }
    />
  );
}

/** 离开或删除后跳到另一个组织（没有时去创建组织页）。 */
function useLeaveTo() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  return (orgId: string) => {
    const others = (
      queryClient.getQueryData<Organization[]>(keys.organizations) ?? []
    ).filter((item) => item.id !== orgId);
    queryClient.setQueryData(keys.organizations, others);
    queryClient.removeQueries({ queryKey: ["org", orgId] });
    navigate(others[0] ? `/o/${others[0].id}` : "/welcome", { replace: true });
  };
}

function DangerSection({
  orgId,
  settings,
}: {
  orgId: string;
  settings: OrganizationSettings;
}) {
  const t = useT();
  const toast = useToast();
  const api = useApi();
  const leaveTo = useLeaveTo();
  const [dialog, setDialog] = useState<"leave" | "delete" | null>(null);
  const leave = useMutation({
    mutationFn: () => api.organizationSettings.leave(orgId),
    onSuccess: () => leaveTo(orgId),
    onError: (error) =>
      toast(organizationErrorMessage(t, error, "organization.leaveFailed"), {
        tone: "error",
      }),
  });
  const leaveDescription =
    settings.leave_blocked === "sole_owner"
      ? t("organization.leaveSoleOwner")
      : settings.leave_blocked === "sole_member"
        ? t("organization.leaveSoleMember")
        : t("organization.leaveDescription");
  return (
    <SettingsSection
      title={t("organization.dangerSection")}
      description={t("organization.dangerDescription")}
    >
      <SettingsCard>
        <SettingRow
          title={t("organization.leave")}
          description={leaveDescription}
          control={
            <Button
              variant="ghost"
              size="28"
              className="text-danger"
              disabled={Boolean(settings.leave_blocked)}
              onClick={() => setDialog("leave")}
            >
              {t("organization.leave")}
            </Button>
          }
        />
        {settings.can_delete && (
          <SettingRow
            danger
            title={t("organization.delete")}
            description={t("organization.deleteDescription")}
            control={
              <Button
                variant="danger-outline"
                size="28"
                onClick={() => setDialog("delete")}
              >
                {t("organization.delete")}
              </Button>
            }
          />
        )}
      </SettingsCard>
      <ConfirmDialog
        open={dialog === "leave"}
        title={t("organization.leave")}
        description={t("organization.leaveConfirm", { name: settings.name })}
        confirmLabel={
          leave.isPending ? t("organization.leaving") : t("common.confirm")
        }
        loading={leave.isPending}
        onCancel={() => setDialog(null)}
        onConfirm={() => leave.mutate()}
      />
      {dialog === "delete" && (
        <DeleteDialog
          orgId={orgId}
          name={settings.name}
          onClose={() => setDialog(null)}
          onDeleted={() => leaveTo(orgId)}
        />
      )}
    </SettingsSection>
  );
}

/** 删除组织：输入值与组织名完全相等（区分大小写、不去空白）才可删除；回车触发同一动作。 */
function DeleteDialog({
  orgId,
  name,
  onClose,
  onDeleted,
}: {
  orgId: string;
  name: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const api = useApi();
  const [typed, setTyped] = useState("");
  const remove = useMutation({
    mutationFn: () => api.organizationSettings.remove(orgId, typed),
    onSuccess: onDeleted,
    onError: (error) =>
      toast(
        isApiError(error)
          ? organizationErrorMessage(t, error, "organization.deleteFailed")
          : t("organization.deleteFailed"),
        { tone: "error" },
      ),
  });
  const matches = typed === name;
  const submit = () => {
    if (matches && !remove.isPending) remove.mutate();
  };
  return (
    <Dialog
      open
      onClose={remove.isPending ? undefined : onClose}
      title={t("organization.delete")}
      description={t("organization.deleteWarning")}
      footer={
        <>
          <Button
            variant="outline"
            disabled={remove.isPending}
            onClick={onClose}
          >
            {t("common.cancel")}
          </Button>
          <Button
            variant="danger"
            disabled={!matches}
            loading={remove.isPending}
            onClick={submit}
          >
            {remove.isPending
              ? t("organization.deleting")
              : t("organization.delete")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-2">
        <p className="text-body-sm text-fg-muted">
          {t("organization.deleteConfirm", { name })}
        </p>
        <TextInput
          autoFocus
          autoComplete="off"
          autoCorrect="off"
          spellCheck={false}
          aria-label={t("organization.delete")}
          placeholder={name}
          value={typed}
          disabled={remove.isPending}
          onChange={(event) => setTyped(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              submit();
            }
          }}
        />
      </div>
    </Dialog>
  );
}
