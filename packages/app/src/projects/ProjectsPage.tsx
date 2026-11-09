import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import type { Project } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  Menu,
  MenuItem,
  Spinner,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
  useToast,
} from "@openfox/ui";

import { canWriteSpace, useSpaceDetail } from "../spaces/queries";
import { RetiredBanner } from "../spaces/settings/parts";
import { formatMonthDay } from "../tasks/primitives/dates";
import { PriorityIcon } from "../tasks/primitives/PriorityIcon";
import { TaskState } from "../tasks/TaskWorkspace";
import { CreateProjectDialog, DeleteProjectDialog } from "./ProjectDialogs";
import { ProgressBar, ProjectIcon, ProjectStatusBadge } from "./parts";
import { useProjects } from "./queries";

/** 复制专项详情页的链接（行菜单与详情顶栏共用）。 */
export function useCopyProjectLink(orgId: string, spaceId: string) {
  const t = useT();
  const toast = useToast();
  return (projectId: string) => {
    const url = `${window.location.origin}/o/${orgId}/spaces/${spaceId}/projects/${projectId}`;
    void navigator.clipboard
      ?.writeText(url)
      .then(() => toast(t("projects.linkCopied")));
  };
}

/** 专项列表 /o/:org/spaces/:space/projects。 */
export function ProjectsPage() {
  const t = useT();
  const { locale } = useI18n();
  const navigate = useNavigate();
  const { orgId = "", spaceId = "" } = useParams();
  const space = useSpaceDetail(orgId, spaceId);
  const enabled = Boolean(space.data?.features.projects);
  const list = useProjects(orgId, spaceId, { enabled });
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<Project | null>(null);
  const copyLink = useCopyProjectLink(orgId, spaceId);

  if (space.isLoading || (enabled && list.isLoading)) {
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );
  }
  if (!space.data) return <TaskState title={t("spaces.loadFailed")} />;
  if (!enabled) {
    return (
      <TaskState
        title={t("projects.disabledTitle")}
        desc={t("projects.disabledDesc")}
        actions={
          space.data.can_manage ? (
            <Link
              className="text-body text-accent hover:underline"
              to={`/o/${orgId}/settings/spaces/${spaceId}`}
            >
              {t("projects.enable")}
            </Link>
          ) : undefined
        }
      />
    );
  }
  const canWrite = canWriteSpace(space.data);
  const projects = list.data?.projects ?? [];
  const create = (
    <Button
      size="28"
      disabled={!canWrite}
      leading={<Icon name="plus" />}
      onClick={() => setCreating(true)}
    >
      {t("projects.new")}
    </Button>
  );

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <h1 className="font-medium">
          {space.data.name} · {t("projects.title")}
        </h1>
        {projects.length > 0 && <div className="ml-auto">{create}</div>}
      </header>
      {space.data.status === "retired" && (
        <div className="px-4 pt-3">
          <RetiredBanner message={t("spaces.task.retiredBanner")} />
        </div>
      )}
      {projects.length === 0 ? (
        <TaskState
          icon={<Icon name="folder" size={28} className="text-fg-muted" />}
          title={t("projects.empty.title")}
          desc={t("projects.empty.desc")}
          actions={create}
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-auto p-4">
          <Table aria-label={t("projects.title")}>
            <TableHead>
              <TableRow>
                <TableHeaderCell>{t("projects.name")}</TableHeaderCell>
                <TableHeaderCell className="w-28">
                  {t("projects.statusLabel")}
                </TableHeaderCell>
                <TableHeaderCell className="w-28">
                  {t("projects.priority")}
                </TableHeaderCell>
                <TableHeaderCell className="w-44">
                  {t("projects.progress")}
                </TableHeaderCell>
                <TableHeaderCell className="w-48">
                  {t("projects.lead")}
                </TableHeaderCell>
                <TableHeaderCell className="w-24">
                  {t("projects.createdAt")}
                </TableHeaderCell>
                <TableHeaderCell className="w-10">
                  <span className="sr-only">{t("projects.actions")}</span>
                </TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {projects.map((project) => (
                <TableRow key={project.id}>
                  <TableCell>
                    <Link
                      to={`/o/${orgId}/spaces/${spaceId}/projects/${project.id}`}
                      className="flex items-center gap-2 font-medium hover:underline"
                    >
                      <ProjectIcon icon={project.icon} />
                      <span className="truncate">{project.name}</span>
                    </Link>
                  </TableCell>
                  <TableCell>
                    <ProjectStatusBadge status={project.status} />
                  </TableCell>
                  <TableCell>
                    <PriorityIcon priority={project.priority} labelled />
                  </TableCell>
                  <TableCell>
                    <ProgressBar progress={project.progress} />
                  </TableCell>
                  <TableCell className="truncate text-body-sm text-fg-secondary">
                    {project.lead?.email ?? "—"}
                  </TableCell>
                  <TableCell className="text-body-sm text-fg-muted">
                    {formatMonthDay(new Date(project.created_at), locale)}
                  </TableCell>
                  <TableCell>
                    <Menu
                      label={`${project.name} ${t("projects.actions")}`}
                      trigger={({ ref, toggle }) => (
                        <button
                          ref={ref}
                          type="button"
                          aria-label={`${project.name} ${t("projects.actions")}`}
                          onClick={toggle}
                          className="flex size-7 items-center justify-center rounded-md text-fg-muted hover:bg-surface-hover"
                        >
                          <Icon name="more" />
                        </button>
                      )}
                    >
                      <MenuItem
                        icon={<Icon name="link" />}
                        onSelect={() => copyLink(project.id)}
                      >
                        {t("projects.copyLink")}
                      </MenuItem>
                      <MenuItem
                        danger
                        icon={<Icon name="trash-2" />}
                        disabled={!canWrite}
                        onSelect={() => setDeleting(project)}
                      >
                        {t("projects.delete.action")}
                      </MenuItem>
                    </Menu>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      {creating && (
        <CreateProjectDialog
          orgId={orgId}
          space={space.data}
          onClose={() => setCreating(false)}
          onCreated={(project) =>
            navigate(`/o/${orgId}/spaces/${spaceId}/projects/${project.id}`)
          }
        />
      )}
      <DeleteProjectDialog
        orgId={orgId}
        project={deleting}
        onClose={() => setDeleting(null)}
        onDeleted={() => setDeleting(null)}
      />
    </div>
  );
}
