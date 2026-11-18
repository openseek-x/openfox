import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { Task, TaskFollower } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Avatar, Button, Icon, useToast } from "@openfox/ui";

import { AgentAvatar } from "../../agents/AgentAvatar";
import { keys, useApi } from "../../context";
import { ReadOnlyTip, useReadOnly } from "../../spaces/readonly";
import { Picker } from "../pickers";
import { personName, taskErrorMessage, type Person } from "../queries";
import type { AssigneeAgent } from "./propertyPickers";

export function Followers({
  orgId,
  task,
  people,
  agents,
}: {
  orgId: string;
  task: Task;
  people: Person[];
  agents: AssigneeAgent[];
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const readOnly = useReadOnly();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const queryKey = keys.taskSub(orgId, task.id, "followers");
  type FollowersData = Awaited<ReturnType<typeof api.tasks.followers>>;
  const followers = useQuery({
    queryKey,
    queryFn: () => api.tasks.followers(orgId, task.space_id, task.id),
  });
  const toggle = useMutation({
    mutationFn: ({
      follower,
      follow,
    }: {
      follower: TaskFollower;
      follow: boolean;
    }) =>
      api.tasks[follow ? "follow" : "unfollow"](
        orgId,
        task.space_id,
        task.id,
        follower.kind === "AGENT" ? "agent" : "human",
        follower.id,
      ),
    onMutate: async ({ follower, follow }) => {
      await queryClient.cancelQueries({ queryKey });
      const previous = queryClient.getQueryData<FollowersData>(queryKey);
      if (previous) {
        const kept = previous.followers.filter(
          (item) => item.id !== follower.id || item.kind !== follower.kind,
        );
        const next = follow ? [...kept, follower] : kept;
        queryClient.setQueryData<FollowersData>(queryKey, {
          followers: next,
          following: next.some(
            (item) => item.kind === "HUMAN" && item.id === me.data?.id,
          ),
        });
      }
      return { previous };
    },
    onError: (error, _variables, context) => {
      if (context?.previous)
        queryClient.setQueryData(queryKey, context.previous);
      toast(taskErrorMessage(t, error), { tone: "error" });
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey }),
  });
  const list = followers.data?.followers ?? [];
  const following = followers.data?.following ?? false;
  const disabled = readOnly || !followers.data || toggle.isPending;
  const candidates: TaskFollower[] = [
    ...people.map((person) => ({
      kind: "HUMAN" as const,
      id: person.id,
      name: personName(person),
    })),
    ...agents
      .filter((agent) => !agent.archived_at && !agent.builtin_kind)
      .map((agent) => ({
        kind: "AGENT" as const,
        id: agent.id,
        name: agent.name,
      })),
  ];
  for (const follower of list)
    if (
      !candidates.some(
        (item) => item.kind === follower.kind && item.id === follower.id,
      )
    )
      candidates.push(follower);
  const avatar = (follower: TaskFollower) =>
    follower.kind === "AGENT" ? (
      <AgentAvatar
        name={follower.name}
        avatar={agents.find((agent) => agent.id === follower.id)?.avatar}
        size={20}
      />
    ) : (
      <Avatar
        name={follower.name}
        src={people.find((person) => person.id === follower.id)?.avatar_url}
        size={20}
      />
    );
  return (
    <div className="flex items-center gap-2">
      <ReadOnlyTip>
        <Button
          variant="ghost-muted"
          size="28"
          disabled={disabled || !me.data}
          onClick={() => {
            if (disabled || !me.data) return;
            toggle.mutate({
              follower: { kind: "HUMAN", id: me.data.id, name: me.data.email },
              follow: !following,
            });
          }}
        >
          {following ? t("tasks.detail.unfollow") : t("tasks.detail.follow")}
        </Button>
      </ReadOnlyTip>
      <Picker
        label={t("tasks.detail.followers")}
        placeholder={t("tasks.detail.changeFollowers")}
        multiple
        selected={list.map((item) => `${item.kind}:${item.id}`)}
        groups={(["HUMAN", "AGENT"] as const).map((kind) => ({
          id: kind,
          label: t(
            kind === "HUMAN" ? "tasks.filters.people" : "tasks.filters.agents",
          ),
          options: candidates
            .filter((item) => item.kind === kind)
            .map((item) => ({
              value: `${item.kind}:${item.id}`,
              label: item.name,
              icon: avatar(item),
              disabled,
            })),
        }))}
        onSelect={(value) => {
          if (disabled) return;
          const follower = candidates.find(
            (item) => `${item.kind}:${item.id}` === value,
          );
          if (follower)
            toggle.mutate({
              follower,
              follow: !list.some(
                (item) =>
                  item.kind === follower.kind && item.id === follower.id,
              ),
            });
        }}
        trigger={({ ref, toggle: openPicker, open }) => (
          <ReadOnlyTip>
            <button
              ref={ref}
              type="button"
              aria-label={t("tasks.detail.editFollowers")}
              aria-expanded={open}
              disabled={readOnly || !followers.data}
              onClick={openPicker}
              className="flex min-h-7 items-center rounded-md px-1 hover:bg-surface-hover disabled:cursor-not-allowed"
            >
              {list.length ? (
                <span className="flex -space-x-1">
                  {list.slice(0, 8).map((item) => (
                    <span
                      key={`${item.kind}:${item.id}`}
                      className="rounded-full ring-2 ring-surface-card"
                    >
                      {avatar(item)}
                    </span>
                  ))}
                </span>
              ) : (
                <Icon name="user-plus" className="text-fg-muted" />
              )}
            </button>
          </ReadOnlyTip>
        )}
      />
    </div>
  );
}
