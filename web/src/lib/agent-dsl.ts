import type { Edge, Node } from "@xyflow/react";
import YAML from "js-yaml";

import type { AgentDocument, AgentNode, BranchRef, NodeType, RetryPolicy, SwitchCase } from "@/lib/types";
import { randomId } from "@/lib/utils";

export type BuilderNodeData = {
  label: string;
  type: NodeType;
  config?: Record<string, unknown>;
  retry?: RetryPolicy;
  timeout?: string;
  onError?: string;
  cases?: SwitchCase[];
  branches?: BranchRef[];
  join?: "all" | "any";
};

export type BuilderNode = Node<BuilderNodeData>;
export type BuilderEdge = Edge;

const DEFAULT_INPUT_SCHEMA: Record<string, unknown> = {
  type: "object",
  properties: {},
};

const DEFAULT_OUTPUT_SCHEMA: Record<string, unknown> = {
  type: "object",
  properties: {},
};

const GRID_COLUMNS = 4;
const NODE_X_GAP = 260;
const NODE_Y_GAP = 170;
const GRID_CENTER_X = 860;
const GRID_START_Y = 120;

function centeredGridPosition(index: number, total: number) {
  const columns = Math.min(GRID_COLUMNS, Math.max(1, total));
  const col = index % columns;
  const row = Math.floor(index / columns);
  const rowWidth = (columns - 1) * NODE_X_GAP;
  const startX = GRID_CENTER_X - rowWidth / 2;
  return {
    x: startX + col * NODE_X_GAP,
    y: GRID_START_Y + row * NODE_Y_GAP,
  };
}

export function createStarterFlow(name: string): {
  metadata: AgentDocument["metadata"];
  inputsSchema: Record<string, unknown>;
  outputsSchema: Record<string, unknown>;
  startNodeId: string;
  nodes: BuilderNode[];
  edges: BuilderEdge[];
} {
  const nodeId = `transform-${randomId("node")}`;
  return {
    metadata: {
      name,
      description: "",
      owners: [],
      labels: {},
    },
    inputsSchema: structuredClone(DEFAULT_INPUT_SCHEMA),
    outputsSchema: structuredClone(DEFAULT_OUTPUT_SCHEMA),
    startNodeId: nodeId,
    nodes: [
      {
        id: nodeId,
        type: "default",
        position: centeredGridPosition(0, 1),
        data: {
          label: nodeId,
          type: "transform",
          config: { expression: "{{ inputs | toJson }}" },
          onError: "fail",
        },
      },
    ],
    edges: [],
  };
}

function uniqEdges(edges: Array<{ from: string; to: string }>) {
  const seen = new Set<string>();
  const out: Array<{ from: string; to: string }> = [];
  for (const edge of edges) {
    if (!edge.from || !edge.to) {
      continue;
    }
    const key = `${edge.from}__${edge.to}`;
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    out.push(edge);
  }
  return out;
}

function normalizeNodeType(type: string): NodeType {
  switch (type) {
    case "llm":
    case "http":
    case "db":
    case "transform":
    case "control.switch":
    case "control.parallel":
    case "control.wait":
      return type;
    default:
      return "transform";
  }
}

export function parseDslYaml(rawYaml: string): AgentDocument {
  const parsed = YAML.load(rawYaml);
  if (!parsed || typeof parsed !== "object") {
    throw new Error("Invalid YAML document");
  }
  return parsed as AgentDocument;
}

export function dumpDslYaml(document: AgentDocument) {
  return YAML.dump(document, {
    lineWidth: 120,
    noRefs: true,
  });
}

export function documentToFlow(document: AgentDocument) {
  const metadata = document.metadata ?? {
    name: "",
    description: "",
    owners: [],
    labels: {},
  };
  const startNodeId = document.spec.start || document.spec.nodes[0]?.id || "";

  const edgeAccumulator: Array<{ from: string; to: string }> = [...(document.spec.edges ?? [])];
  for (const node of document.spec.nodes ?? []) {
    for (const nextNodeId of node.next ?? []) {
      edgeAccumulator.push({ from: node.id, to: nextNodeId });
    }
    if (node.type === "control.switch") {
      for (const c of node.cases ?? []) {
        edgeAccumulator.push({ from: node.id, to: c.to });
      }
    }
    if (node.type === "control.parallel") {
      for (const branch of node.branches ?? []) {
        edgeAccumulator.push({ from: node.id, to: branch.to });
      }
      for (const continuation of node.next ?? []) {
        edgeAccumulator.push({ from: node.id, to: continuation });
      }
    }
  }

  const edges = uniqEdges(edgeAccumulator).map((edge, index) => ({
    id: `${edge.from}-${edge.to}-${index}`,
    source: edge.from,
    target: edge.to,
    animated: edge.from === startNodeId,
  }));

  const totalNodes = (document.spec.nodes ?? []).length;
  const nodes = (document.spec.nodes ?? []).map((node, index) => ({
    id: node.id,
    type: "default",
    position: centeredGridPosition(index, totalNodes),
    data: {
      label: node.id,
      type: normalizeNodeType(node.type),
      config: node.config ?? {},
      retry: node.retry,
      timeout: node.timeout,
      onError: node.onError,
      cases: node.cases,
      branches: node.branches,
      join: node.join as "all" | "any" | undefined,
    },
  }));

  return {
    metadata: {
      name: metadata.name ?? "",
      description: metadata.description ?? "",
      owners: metadata.owners ?? [],
      labels: metadata.labels ?? {},
    },
    startNodeId,
    nodes,
    edges,
    inputsSchema: document.spec.inputs?.schema ?? structuredClone(DEFAULT_INPUT_SCHEMA),
    outputsSchema: document.spec.outputs?.schema ?? structuredClone(DEFAULT_OUTPUT_SCHEMA),
  };
}

function sortUnique(items: string[]) {
  return Array.from(new Set(items.filter(Boolean))).sort();
}

export function flowToDocument(input: {
  metadata: AgentDocument["metadata"];
  startNodeId: string;
  nodes: BuilderNode[];
  edges: BuilderEdge[];
  inputsSchema: Record<string, unknown>;
  outputsSchema: Record<string, unknown>;
}): AgentDocument {
  const outgoing = new Map<string, string[]>();
  for (const edge of input.edges) {
    const current = outgoing.get(edge.source) ?? [];
    current.push(edge.target);
    outgoing.set(edge.source, current);
  }

  const dslNodes: AgentNode[] = input.nodes
    .map((node) => {
      const next = sortUnique(outgoing.get(node.id) ?? []);
      const data = node.data;
      const entry: AgentNode = {
        id: node.id,
        type: data.type,
      };

      if (data.config && Object.keys(data.config).length > 0) {
        entry.config = data.config;
      }
      if (data.retry) {
        entry.retry = data.retry;
      }
      if (data.timeout) {
        entry.timeout = data.timeout;
      }
      if (data.onError) {
        entry.onError = data.onError;
      }

      if (data.type === "control.switch") {
        entry.cases = (data.cases ?? []).filter((c) => c.to);
      } else if (data.type === "control.parallel") {
        entry.branches = (data.branches ?? []).filter((b) => b.to);
        entry.join = data.join ?? "all";
        entry.next = next.slice(0, 1);
      } else {
        entry.next = next;
      }

      return entry;
    })
    .sort((a, b) => a.id.localeCompare(b.id));

  const edges = input.edges.map((edge) => ({ from: edge.source, to: edge.target }));

  return {
    apiVersion: "agents.v1",
    kind: "Agent",
    metadata: {
      name: input.metadata.name,
      description: input.metadata.description ?? "",
      owners: input.metadata.owners ?? [],
      labels: input.metadata.labels ?? {},
    },
    spec: {
      inputs: { schema: input.inputsSchema },
      outputs: { schema: input.outputsSchema },
      nodes: dslNodes,
      edges: uniqEdges(edges),
      start: input.startNodeId,
    },
  };
}
