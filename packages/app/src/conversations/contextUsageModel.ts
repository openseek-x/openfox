import type { Run, RunEvent } from "@openfox/api";

export interface ContextSample {
  runtime: "codex";
  current: number | null;
  window: number | null;
  model: string | null;
  requestedModel: string;
  sampledAt: string;
}

export function requestedContextModel(model: string): string {
  const value = model.trim();
  return value.toLowerCase() === "default" ? "" : value;
}

const record = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);
const counter = (value: unknown, minimum: number): number | null =>
  typeof value === "number" && Number.isSafeInteger(value) && value >= minimum
    ? value
    : null;

/** Only the current generation's final sample is meaningful, never a maximum.
 * A malformed newer sample must not make an older value look current. */
export function contextSample(
  run: Pick<Run, "id" | "generation">,
  events: readonly RunEvent[],
): ContextSample | null {
  let latest: RunEvent | undefined;
  for (const event of events) {
    if (
      event.run_id === run.id &&
      event.generation === run.generation &&
      event.type === "context_usage" &&
      Number.isSafeInteger(event.sequence) &&
      (!latest || event.sequence > latest.sequence)
    )
      latest = event;
  }
  if (!latest || !record(latest.safe_payload)) return null;
  const data = latest.safe_payload.data;
  if (
    !record(data) ||
    data.schema !== 1 ||
    data.runtime !== "codex" ||
    typeof data.requested_model !== "string" ||
    data.requested_model.length > 128 ||
    !Number.isFinite(Date.parse(latest.occurred_at))
  )
    return null;
  return {
    runtime: "codex",
    current: counter(data.current_tokens, 0),
    window: counter(data.window_tokens, 1),
    model:
      typeof data.model === "string" &&
      data.model.trim() !== "" &&
      data.model.length <= 128
        ? data.model
        : null,
    requestedModel: requestedContextModel(data.requested_model),
    sampledAt: latest.occurred_at,
  };
}
