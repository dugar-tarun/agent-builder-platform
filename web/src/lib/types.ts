export type NodeType =
  | "llm"
  | "http"
  | "db"
  | "transform"
  | "control.switch"
  | "control.parallel"
  | "control.wait";

export type Agent = {
  id: string;
  name: string;
  description: string;
  owners: string[];
  labels: Record<string, string>;
  activeVersionId?: string;
  createdAt?: string;
};

export type Version = {
  id: string;
  agentId: string;
  versionNum: number;
  status: string;
  dslYaml: string;
  compiledGraph: string;
  createdBy: string;
  createdAt?: string;
  publishedAt?: string;
};

export type Run = {
  id: string;
  agentName?: string;
  agentId: string;
  versionId: string;
  temporalWorkflowId: string;
  temporalRunId: string;
  status: "queued" | "running" | "succeeded" | "failed" | "cancelled" | "timed_out";
  error?: unknown;
  startedAt?: string;
  endedAt?: string;
  triggeredBy?: string;
};

export type TimelineEvent = {
  runId: string;
  seq: number;
  ts: string;
  nodeId?: string;
  eventType: string;
  payload?: unknown;
  payloadBlobId?: string;
  meta?: unknown;
};

export type RetryPolicy = {
  maxAttempts?: number;
  initialIntervalMs?: number;
  backoffCoefficient?: number;
};

export type SwitchCase = {
  when?: string;
  default?: boolean;
  to: string;
};

export type BranchRef = {
  to: string;
};

export type AgentNode = {
  id: string;
  type: NodeType;
  config?: Record<string, unknown>;
  retry?: RetryPolicy;
  timeout?: string;
  onError?: string;
  next?: string[];
  cases?: SwitchCase[];
  branches?: BranchRef[];
  join?: "all" | "any";
};

export type AgentDocument = {
  apiVersion: "agents.v1";
  kind: "Agent";
  metadata: {
    name: string;
    description?: string;
    owners?: string[];
    labels?: Record<string, string>;
  };
  spec: {
    inputs: { schema: Record<string, unknown> };
    outputs: { schema: Record<string, unknown> };
    nodes: AgentNode[];
    edges: Array<{ from: string; to: string }>;
    start: string;
  };
};
