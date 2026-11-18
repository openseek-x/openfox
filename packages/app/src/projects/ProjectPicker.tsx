import type { ReactNode } from "react";

import { useT } from "@openfox/i18n";

import { Picker, type PickerTriggerProps } from "../tasks/pickers";
import { useProjects } from "./queries";

const NONE = "none";

/** 任务的「专项」选择：新建弹窗与详情属性共用，只列出关联到该空间的专项。 */
export function ProjectPicker({
  orgId,
  spaceId,
  value,
  onSelect,
  trigger,
}: {
  orgId: string;
  spaceId: string;
  value: string | null;
  onSelect: (projectId: string | null) => void;
  trigger: (name: string | undefined, props: PickerTriggerProps) => ReactNode;
}) {
  const t = useT();
  const projects = useProjects(orgId, spaceId).data?.projects ?? [];
  return (
    <Picker
      label={t("projects.title")}
      groups={[
        {
          options: [
            { value: NONE, label: t("projects.none") },
            ...projects.map((project) => ({
              value: project.id,
              label: project.name,
            })),
          ],
        },
      ]}
      selected={value ? [value] : []}
      onSelect={(id) => onSelect(id === NONE ? null : id)}
      trigger={(props) =>
        trigger(projects.find((project) => project.id === value)?.name, props)
      }
    />
  );
}
