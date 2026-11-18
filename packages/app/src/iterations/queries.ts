import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type {
  Iteration,
  IterationCompleteInput,
  IterationCreateInput,
  IterationDatesInput,
  IterationListQuery,
  IterationPatchInput,
  IterationSettingsInput,
  IterationTaskBatchInput,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";

export function useIterationSettings(orgId: string, spaceId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.iterationSettings(orgId, spaceId),
    queryFn: () => api.iterationSettings.get(orgId, spaceId),
    enabled: Boolean(spaceId),
  });
}

export function useIterations(
  orgId: string,
  spaceId: string,
  query: IterationListQuery = {},
  enabled = true,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.iterations(orgId, spaceId, query),
    queryFn: () => api.iterations.list(orgId, spaceId, query),
    enabled: Boolean(spaceId) && enabled,
  });
}

export function useIteration(
  orgId: string,
  spaceId: string,
  iterationId: string | undefined,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.iteration(orgId, spaceId, iterationId ?? ""),
    queryFn: () => api.iterations.get(orgId, spaceId, iterationId!),
    enabled: Boolean(iterationId),
  });
}

function invalidateIterationQueries(
  queryClient: ReturnType<typeof useQueryClient>,
  orgId: string,
  spaceId: string,
) {
  void queryClient.invalidateQueries({
    queryKey: keys.iterations(orgId, spaceId),
  });
  void queryClient.invalidateQueries({
    queryKey: keys.iterationSettings(orgId, spaceId),
  });
  void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
}

export function useUpdateIterationSettings(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useMutation({
    scope: { id: `iteration-settings:${spaceId}` },
    mutationFn: (input: IterationSettingsInput) =>
      api.iterationSettings.update(orgId, spaceId, input),
    onSuccess: (settings) => {
      queryClient.setQueryData(
        keys.iterationSettings(orgId, spaceId),
        settings,
      );
      invalidateIterationQueries(queryClient, orgId, spaceId);
      toast(t("spaces.iteration.saved"));
    },
    onError: () => toast(t("spaces.iteration.saveFailed"), { tone: "error" }),
  });
}

export function useIterationMutation<TInput>(
  orgId: string,
  spaceId: string,
  mutate: (input: TInput) => Promise<unknown>,
) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useMutation({
    mutationFn: mutate,
    onSuccess: () => {
      invalidateIterationQueries(queryClient, orgId, spaceId);
      toast(t("spaces.iteration.saved"));
    },
    onError: () => toast(t("spaces.iteration.saveFailed"), { tone: "error" }),
  });
}

export function useCreateIteration(orgId: string, spaceId: string) {
  const api = useApi();
  return useIterationMutation<IterationCreateInput>(orgId, spaceId, (input) =>
    api.iterations.create(orgId, spaceId, input),
  );
}

export function useUpdateIteration(orgId: string, spaceId: string) {
  const api = useApi();
  return useIterationMutation<IterationPatchInput & { iterationId: string }>(
    orgId,
    spaceId,
    ({ iterationId, ...input }) =>
      api.iterations.update(orgId, spaceId, iterationId, input),
  );
}

export function useUpdateIterationDates(orgId: string, spaceId: string) {
  const api = useApi();
  return useIterationMutation<IterationDatesInput & { iterationId: string }>(
    orgId,
    spaceId,
    ({ iterationId, ...input }) =>
      api.iterations.updateDates(orgId, spaceId, iterationId, input),
  );
}

export function useStartIteration(orgId: string, spaceId: string) {
  const api = useApi();
  return useIterationMutation<{ iterationId: string; version: number }>(
    orgId,
    spaceId,
    ({ iterationId, version }) =>
      api.iterations.start(orgId, spaceId, iterationId, version),
  );
}

export function useCompleteIteration(orgId: string, spaceId: string) {
  const api = useApi();
  return useIterationMutation<IterationCompleteInput & { iterationId: string }>(
    orgId,
    spaceId,
    ({ iterationId, ...input }) =>
      api.iterations.complete(orgId, spaceId, iterationId, input),
  );
}

export function useDeleteIteration(orgId: string, spaceId: string) {
  const api = useApi();
  return useIterationMutation<{ iterationId: string; version: number }>(
    orgId,
    spaceId,
    ({ iterationId, version }) =>
      api.iterations.remove(orgId, spaceId, iterationId, version),
  );
}

export function useUpdateIterationTasks(orgId: string, spaceId: string) {
  const api = useApi();
  return useIterationMutation<
    IterationTaskBatchInput & { iterationId: string }
  >(orgId, spaceId, ({ iterationId, ...input }) =>
    api.iterations.updateTasks(orgId, spaceId, iterationId, input),
  );
}

export function iterationDisplayName(
  iteration: Pick<Iteration, "display_name" | "number">,
) {
  return iteration.display_name || `迭代 ${iteration.number}`;
}
