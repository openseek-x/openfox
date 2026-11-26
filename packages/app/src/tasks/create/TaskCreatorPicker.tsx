import type { AgentDirectoryEntry } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon } from "@openfox/ui";

import { AgentAvatar, CrewAvatar } from "../../agents/AgentAvatar";
import { Picker } from "../pickers";

function creatorAvatar(entry: AgentDirectoryEntry) {
  return entry.type === "crew" ? (
    <CrewAvatar
      name={entry.name}
      members={(entry.members ?? []).filter((m) => m.kind !== "HUMAN")}
      size={28}
    />
  ) : (
    <AgentAvatar
      agentId={entry.id}
      spaceId={entry.space_id}
      name={entry.name}
      avatar={entry.avatar}
      size={20}
    />
  );
}

export function TaskCreatorPicker({
  creators,
  creator,
  loading,
  onSelect,
}: {
  creators: AgentDirectoryEntry[];
  creator?: AgentDirectoryEntry;
  loading: boolean;
  onSelect: (value: string) => void;
}) {
  const t = useT();
  const label = t("tasks.create.creator");
  const value = creator ? `${creator.type}:${creator.id}` : "";
  return (
    <Picker
      label={label}
      groups={(["agent", "crew"] as const).map((kind) => ({
        id: kind,
        label: t(
          kind === "agent" ? "tasks.filters.agents" : "agents.crewBadge",
        ),
        options: creators
          .filter((entry) => entry.type === kind)
          .map((entry) => ({
            value: `${kind}:${entry.id}`,
            label: entry.name,
            icon: creatorAvatar(entry),
          })),
      }))}
      selected={value ? [value] : []}
      onSelect={onSelect}
      trigger={({ ref, toggle, open }) => (
        <button
          ref={ref}
          type="button"
          aria-haspopup="listbox"
          aria-expanded={open}
          disabled={creators.length === 0}
          onClick={toggle}
          className="-ml-1 inline-flex min-h-8 max-w-full items-center gap-2 self-start rounded-md px-1 text-body-sm text-fg-muted enabled:hover:bg-surface-hover"
        >
          <span className="shrink-0">{label}</span>
          {creator ? (
            <>
              {creatorAvatar(creator)}
              <span className="truncate font-medium text-fg-default">
                {creator.name}
              </span>
              <Icon name="chevron-down" size={12} />
            </>
          ) : (
            <span>
              {t(
                loading
                  ? "common.loading"
                  : creators.length
                    ? "tasks.create.selectCreator"
                    : "tasks.create.noCreators",
              )}
            </span>
          )}
        </button>
      )}
    />
  );
}
