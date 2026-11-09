import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";

import {
  isApiError,
  newIdempotencyKey,
  type AgentKind,
  type AgentVisibility,
  type SpaceAgent,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Dialog,
  Field,
  Icon,
  IconButton,
  Select,
  TextInput,
  Textarea,
  Tooltip,
  cn,
  useToast,
} from "@openfox/ui";

import { runtimeIcon } from "../computers/model";
import { useIdempotencyKey } from "../settings/common";
import { AvatarPicker } from "./AgentAvatar";
import {
  AVATAR_COLORS,
  CONFIG_DEFAULTS,
  parseDirectoryRoots,
  runtimeChoices,
  runtimeLabel,
  validDirectoryRoot,
  workModeToDirectory,
  writeAvatar,
  type AvatarSpec,
  type RuntimeChoice,
  type WorkMode,
} from "./model";
import {
  agentErrorMessage,
  storeAgent,
  useDevices,
  useMe,
  useSpaces,
} from "./queries";
import { useApi } from "../context";

const REPO_URL =
  /^(https:\/\/\S+|(ssh:\/\/)?[A-Za-z0-9._-]+@[A-Za-z0-9.-]+[:/]\S+)$/;

export function validRepoUrl(url: string): boolean {
  return REPO_URL.test(url.trim());
}

/** 类型切换：代码开发 / 日常办公；设计创意禁用。 */
export function KindSwitch({
  value,
  onChange,
}: {
  value: AgentKind;
  onChange: (kind: AgentKind) => void;
}) {
  const t = useT();
  const options: Array<{
    value: AgentKind | "DESIGN";
    icon: "code" | "building" | "pen-tool";
  }> = [
    { value: "CODE", icon: "code" },
    { value: "OFFICE", icon: "building" },
    { value: "DESIGN", icon: "pen-tool" },
  ];
  return (
    <div
      role="radiogroup"
      aria-label={t("agents.create.kind")}
      className="inline-flex h-9 items-center gap-0.5 self-center rounded-full bg-surface-muted p-[3px]"
    >
      {options.map((option) => {
        const disabled = option.value === "DESIGN";
        const selected = option.value === value;
        const button = (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={selected}
            aria-disabled={disabled || undefined}
            onClick={() => {
              if (!disabled) onChange(option.value as AgentKind);
            }}
            className={cn(
              "flex h-full items-center gap-1.5 rounded-full px-3 text-body-sm font-medium whitespace-nowrap",
              selected
                ? "bg-primary text-fg-inverse"
                : "text-fg-secondary hover:text-fg-default",
              disabled &&
                "cursor-not-allowed opacity-50 hover:text-fg-secondary",
            )}
          >
            <Icon name={option.icon} />
            {t(`agents.create.kinds.${option.value}`)}
          </button>
        );
        return disabled ? (
          <Tooltip key={option.value} content={t("agents.comingSoon")}>
            {button}
          </Tooltip>
        ) : (
          button
        );
      })}
    </div>
  );
}

/** Runtime 下拉的选项（不可用的置灰并写明原因）。 */
export function runtimeOptions(
  choices: RuntimeChoice[],
  t: ReturnType<typeof useT>,
) {
  return choices.map((choice) => ({
    value: choice.value,
    label: `${runtimeLabel(choice.provider)} · ${choice.deviceName}`,
    icon: <Icon name={runtimeIcon(choice.provider)} />,
    disabled: choice.block !== null,
    description: choice.block
      ? (choice.reason ?? t(`agents.create.blocks.${choice.block}`))
      : undefined,
  }));
}

interface RepoDraft {
  key: number;
  url: string;
  branch: string;
}

/**
 * 创建 Agent 弹窗。
 * 可见性默认值由入口决定：设置页传 SHARED，其他入口 PRIVATE（决策 8）。
 * 创建成功后添加 Git 仓库，再交给调用方打开设置抽屉。
 */
export function CreateAgentDialog({
  orgId,
  defaultVisibility = "PRIVATE",
  defaultSpaceId,
  initial,
  onClose,
  onCreated,
}: {
  orgId: string;
  defaultVisibility?: AgentVisibility;
  defaultSpaceId?: string;
  /** 预填值（市场模板创建 Agent 用）；只在弹窗打开时读取一次 */
  initial?: { name?: string; title?: string; instructions?: string };
  onClose: () => void;
  onCreated: (agent: SpaceAgent) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const me = useMe();
  const spaces = useSpaces(orgId);
  const devices = useDevices(orgId);
  const idempotencyKey = useIdempotencyKey();

  const [avatar, setAvatar] = useState<AvatarSpec>(() => ({
    kind: "icon",
    value: "bot",
    color: AVATAR_COLORS[1],
  }));
  const [kind, setKind] = useState<AgentKind>("CODE");
  const [name, setName] = useState(initial?.name ?? "");
  const [title, setTitle] = useState(initial?.title ?? "");
  const [spaceId, setSpaceId] = useState<string | null>(defaultSpaceId ?? null);
  const [visibility, setVisibility] =
    useState<AgentVisibility>(defaultVisibility);
  const [runtimePick, setRuntimePick] = useState<string | null>(null);
  const [workMode, setWorkMode] = useState<WorkMode>("git");
  const [rootsText, setRootsText] = useState("");
  const [repos, setRepos] = useState<RepoDraft[]>([]);
  const [instructions, setInstructions] = useState(initial?.instructions ?? "");
  const [nameError, setNameError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [avatarUploading, setAvatarUploading] = useState(false);
  const [showErrors, setShowErrors] = useState(false);

  const space =
    spaces.data?.find((item) => item.id === spaceId) ?? spaces.data?.[0];
  const choices = useMemo(
    () =>
      space && me.data
        ? runtimeChoices(devices.data ?? [], space.id, me.data.id)
        : [],
    [devices.data, space, me.data],
  );
  const available = choices.filter((choice) => choice.block === null);
  const runtime =
    available.find((choice) => choice.value === runtimePick) ?? available[0];
  const noComputer = devices.isSuccess && available.length === 0;

  const roots = parseDirectoryRoots(rootsText);
  const rootsInvalid =
    workMode === "explicit" &&
    (roots.length === 0 ||
      roots.length > 32 ||
      !roots.every(validDirectoryRoot));
  const repoList = workMode === "git" ? repos.filter((r) => r.url.trim()) : [];
  const repoInvalid = repoList.some((repo) => !validRepoUrl(repo.url));

  const submit = async () => {
    if (busy || avatarUploading) return;
    setShowErrors(true);
    const trimmed = name.trim();
    if (!trimmed) {
      setNameError(t("agents.create.nameRequired"));
      return;
    }
    if (!space || !runtime || rootsInvalid || repoInvalid) return;
    const input = {
      name: trimmed,
      description: "",
      kind,
      visibility,
      ...(title.trim() ? { title: title.trim() } : {}),
      avatar: writeAvatar(avatar),
      allow_assign: true,
      allow_direct_chat: true,
      config: {
        ...CONFIG_DEFAULTS,
        computer_id: runtime.deviceId,
        grant_id: runtime.grantId,
        runtime: runtime.provider,
        model: "default",
        instructions: instructions.trim(),
        directory_mode: workModeToDirectory[workMode],
        directory_roots: workMode === "explicit" ? roots : [],
      },
    };
    setBusy(true);
    setNameError(null);
    let agent: SpaceAgent;
    try {
      agent = await api.agents.create(
        orgId,
        space.id,
        input,
        idempotencyKey({ spaceId: space.id, ...input }),
      );
    } catch (error) {
      setBusy(false);
      if (isApiError(error, "NAME_CONFLICT") || isApiError(error, "NAME_TAKEN"))
        setNameError(t("agents.create.nameTaken"));
      else toast(agentErrorMessage(t, error), { tone: "error" });
      return;
    }
    let repoFailed = false;
    for (const [index, repo] of repoList.entries()) {
      try {
        await api.agents.repos.create(
          orgId,
          agent.space_id,
          agent.id,
          {
            url: repo.url.trim(),
            ...(repo.branch.trim() ? { base_branch: repo.branch.trim() } : {}),
            position: index,
          },
          newIdempotencyKey(),
        );
      } catch {
        repoFailed = true;
      }
    }
    if (repoFailed) toast(t("agents.toast.repoFailed"), { tone: "error" });
    storeAgent(queryClient, orgId, agent);
    setBusy(false);
    toast(t("agents.toast.agentReady"));
    onCreated(agent);
  };

  const workModes: WorkMode[] = ["git", "explicit", "none"];

  return (
    <Dialog
      open
      onClose={busy ? undefined : onClose}
      title={t("agents.create.title")}
      width={600}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={busy}
            disabled={!runtime || !space || noComputer || avatarUploading}
            onClick={() => void submit()}
          >
            {t("agents.create.submit")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4 px-0.5 pb-0.5">
        <div className="flex flex-col items-center gap-4">
          <AvatarPicker
            orgId={orgId}
            name={name}
            value={avatar}
            onChange={setAvatar}
            disabled={busy}
            onBusyChange={setAvatarUploading}
          />
          <KindSwitch value={kind} onChange={setKind} />
        </div>
        <Field label={t("agents.create.name")} error={nameError ?? undefined}>
          <div className="flex items-center gap-2">
            <TextInput
              variant="labeled"
              data-autofocus
              aria-label={t("agents.create.name")}
              value={name}
              maxLength={255}
              placeholder={t("agents.create.namePlaceholder")}
              onChange={(event) => {
                setName(event.target.value);
                setNameError(null);
              }}
            />
            <span aria-hidden className="text-fg-muted">
              -
            </span>
            <TextInput
              variant="labeled"
              aria-label={t("agents.create.titleLabel")}
              value={title}
              maxLength={64}
              invalid={false}
              placeholder={t("agents.create.titlePlaceholder")}
              onChange={(event) => setTitle(event.target.value)}
            />
          </div>
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field label={t("agents.create.space")}>
            <Select
              variant="labeled"
              value={space?.id ?? null}
              onChange={setSpaceId}
              leading={<Icon name="layout-grid" />}
              options={(spaces.data ?? []).map((item) => ({
                value: item.id,
                label: item.name,
              }))}
            />
          </Field>
          <Field label={t("agents.create.visibility")}>
            <Select
              variant="labeled"
              value={visibility}
              onChange={setVisibility}
              options={(["SHARED", "PRIVATE"] as const).map((value) => ({
                value,
                label: t(`agents.visibility.${value}`),
                icon: <Icon name={value === "SHARED" ? "users" : "lock"} />,
                description:
                  value === "SHARED"
                    ? t("agents.visibility.sharedDesc")
                    : t("agents.visibility.privateDesc"),
              }))}
            />
          </Field>
        </div>
        <Field label={t("agents.create.runtime")}>
          <Select
            variant="labeled"
            value={runtime?.value ?? null}
            onChange={setRuntimePick}
            disabled={choices.length === 0}
            placeholder={
              devices.isSuccess
                ? t("agents.create.runtimeNone")
                : t("common.loading")
            }
            options={runtimeOptions(choices, t)}
          />
        </Field>
        {noComputer && (
          <Callout tone="warning" title={t("agents.create.noComputerTitle")}>
            {t("agents.create.noComputerDesc")}
          </Callout>
        )}
        <Field label={t("agents.create.workMode")}>
          <Select
            variant="labeled"
            value={workMode}
            onChange={setWorkMode}
            options={workModes.map((mode) => ({
              value: mode,
              label:
                mode === "git"
                  ? `${t("agents.create.workModes.git")}（${t("agents.create.workModes.recommended")}）`
                  : t(`agents.create.workModes.${mode}`),
              icon: (
                <Icon
                  name={
                    mode === "git"
                      ? "git-branch"
                      : mode === "explicit"
                        ? "folder"
                        : "circle-slash"
                  }
                />
              ),
              description: t(`agents.create.workModes.${mode}Desc`),
            }))}
          />
        </Field>
        {workMode === "explicit" && (
          <Field
            label={t("agents.create.roots")}
            error={
              showErrors && rootsInvalid
                ? t("agents.create.rootsInvalid")
                : undefined
            }
          >
            <Textarea
              rows={2}
              value={rootsText}
              placeholder={t("agents.create.rootsPlaceholder")}
              onChange={(event) => setRootsText(event.target.value)}
            />
          </Field>
        )}
        {workMode === "git" && (
          <div className="flex flex-col gap-2">
            {repos.map((repo) => {
              const invalid =
                showErrors && repo.url.trim() !== "" && !validRepoUrl(repo.url);
              return (
                <div key={repo.key} className="flex items-start gap-2">
                  <Field
                    className="flex-1"
                    error={invalid ? t("agents.create.repoInvalid") : undefined}
                  >
                    <TextInput
                      aria-label={t("agents.create.repoUrl")}
                      value={repo.url}
                      placeholder={t("agents.create.repoUrlPlaceholder")}
                      onChange={(event) =>
                        setRepos((list) =>
                          list.map((r) =>
                            r.key === repo.key
                              ? { ...r, url: event.target.value }
                              : r,
                          ),
                        )
                      }
                    />
                  </Field>
                  <TextInput
                    className="w-36"
                    aria-label={t("agents.create.repoBranch")}
                    value={repo.branch}
                    placeholder={t("agents.create.repoBranch")}
                    onChange={(event) =>
                      setRepos((list) =>
                        list.map((r) =>
                          r.key === repo.key
                            ? { ...r, branch: event.target.value }
                            : r,
                        ),
                      )
                    }
                  />
                  <IconButton
                    label={t("agents.create.removeRepo")}
                    icon={<Icon name="x" />}
                    onClick={() =>
                      setRepos((list) => list.filter((r) => r.key !== repo.key))
                    }
                  />
                </div>
              );
            })}
            {repos.length < 20 && (
              <Button
                variant="ghost"
                size="28"
                className="self-center"
                leading={<Icon name="plus" />}
                onClick={() =>
                  setRepos((list) => [
                    ...list,
                    { key: Date.now() + list.length, url: "", branch: "" },
                  ])
                }
              >
                {t("agents.create.addRepo")}
              </Button>
            )}
          </div>
        )}
        <Field label={t("agents.create.instructions")}>
          <Textarea
            rows={3}
            value={instructions}
            maxLength={100000}
            placeholder={t("agents.create.instructionsPlaceholder")}
            onChange={(event) => setInstructions(event.target.value)}
          />
        </Field>
      </div>
    </Dialog>
  );
}
