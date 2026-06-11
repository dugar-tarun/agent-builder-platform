import type { Agent, Run, TimelineEvent, Version } from "@/lib/types";

type ApiErrorPayload = {
  error?: {
    code?: string;
    message?: string;
    details?: unknown;
  };
};

export class ApiError extends Error {
  status: number;
  code?: string;
  details?: unknown;

  constructor(status: number, message: string, code?: string, details?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

const API_PREFIX = "/api/controlplane";

function pick<T = unknown>(obj: unknown, ...keys: string[]): T | undefined {
  if (!obj || typeof obj !== "object") {
    return undefined;
  }
  const source = obj as Record<string, unknown>;
  for (const key of keys) {
    if (key in source) {
      return source[key] as T;
    }
  }
  return undefined;
}

async function parseResponse<T>(response: Response): Promise<T> {
  const text = await response.text();
  if (!text) {
    return {} as T;
  }
  return JSON.parse(text) as T;
}

async function apiRequest<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_PREFIX}${path}`, {
    ...init,
    headers: {
      ...(init?.headers ?? {}),
    },
    cache: "no-store",
  });

  if (!response.ok) {
    let payload: ApiErrorPayload | undefined;
    try {
      payload = await parseResponse<ApiErrorPayload>(response);
    } catch {
      payload = undefined;
    }
    throw new ApiError(
      response.status,
      payload?.error?.message ?? `Request failed with status ${response.status}`,
      payload?.error?.code,
      payload?.error?.details,
    );
  }

  return parseResponse<T>(response);
}

function normalizeAgent(raw: unknown): Agent {
  return {
    id: pick<string>(raw, "id", "ID") ?? "",
    name: pick<string>(raw, "name", "Name") ?? "",
    description: pick<string>(raw, "description", "Description") ?? "",
    owners: pick<string[]>(raw, "owners", "Owners") ?? [],
    labels: pick<Record<string, string>>(raw, "labels", "Labels") ?? {},
    activeVersionId: pick<string | undefined>(raw, "activeVersionId", "ActiveVersionID"),
    createdAt: pick<string | undefined>(raw, "createdAt", "CreatedAt"),
  };
}

function normalizeVersion(raw: unknown): Version {
  return {
    id: pick<string>(raw, "id", "ID") ?? "",
    agentId: pick<string>(raw, "agentId", "AgentID") ?? "",
    versionNum: pick<number>(raw, "versionNum", "VersionNum") ?? 0,
    status: pick<string>(raw, "status", "Status") ?? "draft",
    dslYaml: pick<string>(raw, "dslYaml", "DSLYAML") ?? "",
    compiledGraph: pick<string>(raw, "compiledGraph", "CompiledGraph") ?? "",
    createdBy: pick<string>(raw, "createdBy", "CreatedBy") ?? "",
    createdAt: pick<string | undefined>(raw, "createdAt", "CreatedAt"),
    publishedAt: pick<string | undefined>(raw, "publishedAt", "PublishedAt"),
  };
}

function normalizeRun(raw: unknown): Run {
  return {
    id: pick<string>(raw, "id", "ID") ?? "",
    agentName: pick<string | undefined>(raw, "agentName", "AgentName"),
    agentId: pick<string>(raw, "agentId", "AgentID") ?? "",
    versionId: pick<string>(raw, "versionId", "VersionID") ?? "",
    temporalWorkflowId: pick<string>(raw, "temporalWorkflowId", "TemporalWorkflowID") ?? "",
    temporalRunId: pick<string>(raw, "temporalRunId", "TemporalRunID") ?? "",
    status: (pick<Run["status"]>(raw, "status", "Status") ?? "queued") as Run["status"],
    error: pick<unknown>(raw, "error", "Error"),
    startedAt: pick<string | undefined>(raw, "startedAt", "StartedAt"),
    endedAt: pick<string | undefined>(raw, "endedAt", "EndedAt"),
    triggeredBy: pick<string | undefined>(raw, "triggeredBy", "TriggeredBy"),
  };
}

function normalizeTimelineEvent(raw: unknown): TimelineEvent {
  return {
    runId: pick<string>(raw, "runId", "RunID") ?? "",
    seq: pick<number>(raw, "seq", "Seq") ?? 0,
    ts: pick<string>(raw, "ts", "TS") ?? "",
    nodeId: pick<string | undefined>(raw, "nodeId", "NodeID"),
    eventType: pick<string>(raw, "eventType", "EventType") ?? "",
    payload: pick<unknown>(raw, "payload", "Payload"),
    payloadBlobId: pick<string | undefined>(raw, "payloadBlobId", "PayloadID"),
    meta: pick<unknown>(raw, "meta", "Meta"),
  };
}

export function makeIdempotencyKey() {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `${Date.now()}-${Math.random().toString(36).slice(2, 12)}`;
}

export async function listAgents() {
  const payload = await apiRequest<{ items?: unknown[] }>("/v1/agents");
  return (payload.items ?? []).map(normalizeAgent);
}

export async function createAgent(input: {
  actor: string;
  name: string;
  description?: string;
  owners?: string[];
  labels?: Record<string, string>;
}) {
  const payload = await apiRequest<unknown>("/v1/agents", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Actor": input.actor,
    },
    body: JSON.stringify({
      name: input.name,
      description: input.description ?? "",
      owners: input.owners ?? [],
      labels: input.labels ?? {},
    }),
  });
  return normalizeAgent(payload);
}

export async function getAgent(name: string) {
  const payload = await apiRequest<unknown>(`/v1/agents/${encodeURIComponent(name)}`);
  return normalizeAgent(payload);
}

export async function updateAgent(input: {
  actor: string;
  name: string;
  description: string;
  owners: string[];
  labels: Record<string, string>;
}) {
  const payload = await apiRequest<unknown>(`/v1/agents/${encodeURIComponent(input.name)}`, {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
      "X-Actor": input.actor,
    },
    body: JSON.stringify({
      description: input.description,
      owners: input.owners,
      labels: input.labels,
    }),
  });
  return normalizeAgent(payload);
}

export async function listVersions(agentName: string) {
  const payload = await apiRequest<{ items?: unknown[] }>(`/v1/agents/${encodeURIComponent(agentName)}/versions`);
  return (payload.items ?? []).map(normalizeVersion);
}

export async function listRuns(agentName: string, limit = 25) {
  const params = new URLSearchParams({ limit: String(limit) });
  try {
    const payload = await apiRequest<{ items?: unknown[] }>(
      `/v1/agents/${encodeURIComponent(agentName)}/runs?${params.toString()}`,
    );
    return (payload.items ?? []).map(normalizeRun);
  } catch (error) {
    if (error instanceof ApiError && error.status === 405) {
      throw new ApiError(
        405,
        "Runs listing endpoint is unavailable on the running controlplane. Restart controlplane with latest code to enable GET /v1/agents/{name}/runs.",
        error.code,
        error.details,
      );
    }
    throw error;
  }
}

export async function getVersion(agentName: string, versionId: string) {
  const payload = await apiRequest<unknown>(`/v1/agents/${encodeURIComponent(agentName)}/versions/${versionId}`);
  return normalizeVersion(payload);
}

export async function createVersion(input: { actor: string; agentName: string; yaml: string }) {
  return apiRequest<{
    id: string;
    agentName: string;
    versionNum: number;
    status: string;
    compiledHash: string;
    createdAt: string;
  }>(`/v1/agents/${encodeURIComponent(input.agentName)}/versions`, {
    method: "POST",
    headers: {
      "Content-Type": "application/yaml",
      "X-Actor": input.actor,
    },
    body: input.yaml,
  });
}

export async function updateVersion(input: {
  actor: string;
  agentName: string;
  versionId: string;
  yaml: string;
}) {
  const payload = await apiRequest<unknown>(
    `/v1/agents/${encodeURIComponent(input.agentName)}/versions/${input.versionId}`,
    {
      method: "PUT",
      headers: {
        "Content-Type": "application/yaml",
        "X-Actor": input.actor,
      },
      body: input.yaml,
    },
  );
  return normalizeVersion(payload);
}

export async function publishVersion(input: {
  actor: string;
  versionId: string;
  idempotencyKey?: string;
}) {
  return apiRequest<{
    versionId: string;
    agentId: string;
    status: string;
    activeVersionId: string;
  }>(`/v1/versions/${input.versionId}:publish`, {
    method: "POST",
    headers: {
      "X-Actor": input.actor,
      "Idempotency-Key": input.idempotencyKey ?? makeIdempotencyKey(),
    },
  });
}

export async function rollbackAgent(input: {
  actor: string;
  agentName: string;
  toVersionId: string;
  reason?: string;
}) {
  return apiRequest<{
    agentId: string;
    fromVersionId?: string;
    toVersionId: string;
    activeVersionId: string;
  }>(`/v1/agents/${encodeURIComponent(input.agentName)}:rollback`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Actor": input.actor,
    },
    body: JSON.stringify({
      toVersionId: input.toVersionId,
      reason: input.reason ?? "",
    }),
  });
}

export async function startRun(input: {
  actor: string;
  agentName: string;
  versionId?: string;
  idempotencyKey?: string;
  inputs: Record<string, unknown>;
}) {
  return apiRequest<{
    runId: string;
    versionId: string;
    temporalWorkflowId: string;
    status: string;
  }>(`/v1/agents/${encodeURIComponent(input.agentName)}/runs`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Actor": input.actor,
      "Idempotency-Key": input.idempotencyKey ?? makeIdempotencyKey(),
    },
    body: JSON.stringify({ versionId: input.versionId, inputs: input.inputs }),
  });
}

export async function getRun(runId: string) {
  const payload = await apiRequest<unknown>(`/v1/runs/${runId}`);
  return normalizeRun(payload);
}

export async function getTimeline(runId: string) {
  const payload = await apiRequest<{ items?: unknown[] }>(`/v1/runs/${runId}/timeline`);
  return (payload.items ?? []).map(normalizeTimelineEvent);
}
