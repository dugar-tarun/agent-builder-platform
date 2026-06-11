"use client";

import "@xyflow/react/dist/style.css";

import {
  addEdge,
  Background,
  type Connection,
  Controls,
  type Edge,
  Handle,
  type Node,
  type NodeProps,
  Panel,
  Position,
  ReactFlow,
  type ReactFlowInstance,
  useEdgesState,
  useNodesState,
} from "@xyflow/react";
import YAML from "js-yaml";
import { Play, Plus, Trash2 } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";

import { AppShell } from "@/components/app-shell";
import { useSettings } from "@/components/settings-context";
import {
  ApiError,
  createVersion,
  getAgent,
  getVersion,
  listVersions,
  publishVersion,
  rollbackAgent,
  startRun,
  updateAgent,
  updateVersion,
} from "@/lib/api";
import {
  createStarterFlow,
  documentToFlow,
  dumpDslYaml,
  flowToDocument,
  parseDslYaml,
  type BuilderNode,
} from "@/lib/agent-dsl";
import type { BranchRef, NodeType, SwitchCase } from "@/lib/types";
import { randomId, safeJsonParse } from "@/lib/utils";

const NODE_TEMPLATES: Record<NodeType, Record<string, unknown>> = {
  llm: {
    provider: "openai",
    model: "gpt-4o-mini",
    userPrompt: "Ticket: {{ inputs.ticketId }}",
  },
  http: {
    method: "POST",
    url: "https://api.example.com/action",
    headers: {
      Authorization: "Bearer {{ secrets.api_token }}",
    },
    body: {},
    expectStatus: [200],
  },
  db: {
    source: "zendesk_ro",
    query: "SELECT * FROM tickets WHERE id = $1",
    params: ["{{ inputs.ticketId }}"],
    resultMode: "single",
  },
  transform: {
    expression: "{{ nodes.lookup.output | toJson }}",
  },
  "control.wait": {
    durationMs: 1000,
  },
  "control.switch": {},
  "control.parallel": {},
};

const ON_ERROR_OPTIONS = ["fail", "continue", "route"] as const;
type OnErrorOption = (typeof ON_ERROR_OPTIONS)[number];

type NodeDefinition = {
  type: NodeType;
  label: string;
  category: "AI" | "Integration" | "Data" | "Control";
  description: string;
  configHelp: string;
};

const NODE_LIBRARY: NodeDefinition[] = [
  {
    type: "llm",
    label: "LLM",
    category: "AI",
    description: "Generate text or structured model output from prompts and context.",
    configHelp: "provider, model, userPrompt, systemPrompt, responseFormat",
  },
  {
    type: "http",
    label: "HTTP",
    category: "Integration",
    description: "Call external APIs with templated headers, body, and status checks.",
    configHelp: "method, url, headers, body, expectStatus",
  },
  {
    type: "db",
    label: "DB",
    category: "Data",
    description: "Run parameterized SQL against a named database connection.",
    configHelp: "source, query, params, resultMode",
  },
  {
    type: "transform",
    label: "Transform",
    category: "Data",
    description: "Shape or compute values from inputs and prior node outputs.",
    configHelp: "expression",
  },
  {
    type: "control.switch",
    label: "Control Switch",
    category: "Control",
    description: "Route execution based on ordered conditions plus a default path.",
    configHelp: "cases[].when, cases[].to, default case",
  },
  {
    type: "control.parallel",
    label: "Control Parallel",
    category: "Control",
    description: "Run branch sub-flows in parallel and continue after a join.",
    configHelp: "branches[].to, join(all|any), next continuation",
  },
  {
    type: "control.wait",
    label: "Control Wait",
    category: "Control",
    description: "Pause workflow execution for a fixed duration.",
    configHelp: "durationMs",
  },
];

type AgentBuilderPageProps = {
  agentName: string;
};

function formatJson(value: unknown) {
  return JSON.stringify(value ?? {}, null, 2);
}

function formatYamlMap(value: unknown) {
  return YAML.dump(value ?? {}, { lineWidth: 120, noRefs: true });
}

function parseYamlMap(rawText: string) {
  const parsed = YAML.load(rawText);
  if (parsed === null || parsed === undefined) {
    return {};
  }
  if (typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("Config must be a YAML mapping/object.");
  }
  return parsed as Record<string, unknown>;
}

function inferStartNodeId(nodes: BuilderNode[], edges: Edge[]) {
  if (nodes.length === 0) {
    return "";
  }
  const incomingTargets = new Set(edges.map((edge) => edge.target));
  const root = nodes.find((node) => !incomingTargets.has(node.id));
  return root?.id ?? nodes[0].id;
}

function parseLabels(raw: string) {
  const out: Record<string, string> = {};
  for (const entry of raw.split(",").map((item) => item.trim()).filter(Boolean)) {
    const [key, ...rest] = entry.split(":");
    if (!key || rest.length === 0) {
      continue;
    }
    out[key.trim()] = rest.join(":").trim();
  }
  return out;
}

function labelsToText(labels: Record<string, string>) {
  return Object.entries(labels)
    .map(([key, value]) => `${key}:${value}`)
    .join(", ");
}

function makeNodeBaseName(nodeType: NodeType) {
  return nodeType.replace("control.", "").replace(".", "-");
}

function nextUniqueNodeName(nodeType: NodeType, nodes: BuilderNode[]) {
  const base = makeNodeBaseName(nodeType);
  const existing = new Set(nodes.map((node) => node.id));
  let index = 1;
  let candidate = `${base}-${index}`;
  while (existing.has(candidate)) {
    index += 1;
    candidate = `${base}-${index}`;
  }
  return candidate;
}

function getOnErrorMode(value?: string): OnErrorOption {
  if (!value || value === "fail") {
    return "fail";
  }
  if (value === "continue") {
    return "continue";
  }
  if (value.startsWith("route:")) {
    return "route";
  }
  return "fail";
}

function getRouteTarget(value?: string) {
  if (!value || !value.startsWith("route:")) {
    return "";
  }
  return value.slice("route:".length).trim();
}

function localValidation(nodes: BuilderNode[], edges: Edge[], startNodeId: string, schemaIssues: string[]) {
  const issues: string[] = [];
  const nodeIds = new Set<string>();

  for (const node of nodes) {
    if (nodeIds.has(node.id)) {
      issues.push(`Duplicate node id: ${node.id}`);
    }
    nodeIds.add(node.id);
  }

  if (nodes.length === 0) {
    issues.push("Add at least one node.");
  }
  if (!startNodeId) {
    issues.push("Unable to infer a start node from the sequence.");
  }
  if (startNodeId && !nodes.some((node) => node.id === startNodeId)) {
    issues.push("Start node must exist in the canvas.");
  }
  for (const edge of edges) {
    if (!nodeIds.has(edge.source)) {
      issues.push(`Edge source ${edge.source} does not exist.`);
    }
    if (!nodeIds.has(edge.target)) {
      issues.push(`Edge target ${edge.target} does not exist.`);
    }
  }

  for (const node of nodes) {
    if (node.data.type === "control.switch") {
      const cases = node.data.cases ?? [];
      if (cases.length === 0) {
        issues.push(`${node.id}: switch needs at least one case.`);
      }
      for (const c of cases) {
        if (!c.to) {
          issues.push(`${node.id}: switch case target is required.`);
        } else if (!nodeIds.has(c.to)) {
          issues.push(`${node.id}: switch case target ${c.to} does not exist.`);
        }
      }
      if (!cases.some((item) => item.default)) {
        issues.push(`${node.id}: switch needs one default case.`);
      }
    }
    if (node.data.type === "control.parallel") {
      const branches = node.data.branches ?? [];
      if (branches.length === 0) {
        issues.push(`${node.id}: parallel needs branch roots.`);
      }
      for (const branch of branches) {
        if (!branch.to) {
          issues.push(`${node.id}: parallel branch target is required.`);
        } else if (!nodeIds.has(branch.to)) {
          issues.push(`${node.id}: parallel branch target ${branch.to} does not exist.`);
        }
      }
    }
    if (node.data.onError?.startsWith("route:")) {
      const target = getRouteTarget(node.data.onError);
      if (!target) {
        issues.push(`${node.id}: onError route target is empty.`);
      } else if (!nodeIds.has(target)) {
        issues.push(`${node.id}: onError route target ${target} does not exist.`);
      }
    }
  }
  issues.push(...schemaIssues);
  return issues;
}

type GraphNodeData = BuilderNode["data"] & {
  onAddFromNode: (nodeId: string) => void;
  onDeleteNode: (nodeId: string) => void;
};

function GraphNodeCard({ id, data, selected }: NodeProps<Node<GraphNodeData>>) {
  return (
    <div className="relative">
      <button
        type="button"
        className="nodrag btn-interactive absolute -right-3 -top-3 z-10 rounded-full border border-red-700 bg-zinc-950 p-1 text-red-300 hover:border-red-500"
        onClick={(event) => {
          event.stopPropagation();
          data.onDeleteNode(id);
        }}
        aria-label={`Delete node ${id}`}
        title="Delete node"
      >
        <Trash2 size={12} />
      </button>
      <button
        type="button"
        className="nodrag btn-interactive absolute -bottom-3 -right-3 z-10 rounded-full border border-zinc-600 bg-zinc-950 p-1 text-zinc-200 hover:border-zinc-400"
        onClick={(event) => {
          event.stopPropagation();
          data.onAddFromNode(id);
        }}
        aria-label={`Add node after ${id}`}
        title="Add node"
      >
        <Plus size={12} />
      </button>
      <div
        className={`min-w-[190px] rounded-lg border px-3 py-2 text-xs shadow-sm transition ${
          selected ? "border-blue-600 bg-zinc-900" : "border-zinc-700 bg-zinc-900/95"
        }`}
      >
      <Handle type="target" position={Position.Top} className="!h-2 !w-2 !border-zinc-300 !bg-zinc-600" />
      <p className="truncate text-sm font-medium text-zinc-100">{id}</p>
      <p className="mt-0.5 text-[11px] text-zinc-400">{data.type}</p>
      <Handle type="source" position={Position.Bottom} className="!h-2 !w-2 !border-zinc-300 !bg-zinc-600" />
      </div>
    </div>
  );
}

const NODE_TYPES = { builderNode: GraphNodeCard };

export function AgentBuilderPage({ agentName }: AgentBuilderPageProps) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const reactFlowRef = useRef<ReactFlowInstance<BuilderNode, Edge> | null>(null);
  const { actor } = useSettings();
  const starter = useMemo(() => createStarterFlow(agentName), [agentName]);

  const [metadataDescription, setMetadataDescription] = useState(starter.metadata.description ?? "");
  const [metadataOwners, setMetadataOwners] = useState((starter.metadata.owners ?? []).join(", "));
  const [metadataLabels, setMetadataLabels] = useState(labelsToText(starter.metadata.labels ?? {}));
  const [inputsSchemaText, setInputsSchemaText] = useState(formatJson(starter.inputsSchema));
  const [outputsSchemaText, setOutputsSchemaText] = useState(formatJson(starter.outputsSchema));
  const [runInputsText, setRunInputsText] = useState(formatJson({ ticketId: "T-12345" }));
  const [isMetadataEditing, setIsMetadataEditing] = useState(false);
  const [isNodeEditing, setIsNodeEditing] = useState(false);
  const [selectedNodeId, setSelectedNodeId] = useState<string>(starter.startNodeId);
  const [selectedVersionId, setSelectedVersionId] = useState("");
  const [nodeNameText, setNodeNameText] = useState(starter.startNodeId);
  const [configText, setConfigText] = useState(formatYamlMap(starter.nodes[0]?.data.config ?? {}));
  const [isNodeLibraryOpen, setIsNodeLibraryOpen] = useState(false);
  const [nodeSearch, setNodeSearch] = useState("");
  const [pendingSourceNodeId, setPendingSourceNodeId] = useState<string | null>(null);
  const [isRunModalOpen, setIsRunModalOpen] = useState(false);

  const [nodes, setNodes, onNodesChange] = useNodesState(starter.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(starter.edges);
  const inferredStartNodeId = useMemo(() => inferStartNodeId(nodes, edges), [nodes, edges]);

  const selectedNode = useMemo(() => nodes.find((node) => node.id === selectedNodeId), [nodes, selectedNodeId]);
  const selectedNodeDefinition = useMemo(
    () => NODE_LIBRARY.find((entry) => entry.type === selectedNode?.data.type),
    [selectedNode?.data.type],
  );
  const filteredNodeLibrary = useMemo(() => {
    const query = nodeSearch.trim().toLowerCase();
    if (!query) {
      return NODE_LIBRARY;
    }
    return NODE_LIBRARY.filter((entry) =>
      [entry.label, entry.type, entry.category, entry.description, entry.configHelp].join(" ").toLowerCase().includes(query),
    );
  }, [nodeSearch]);

  function selectNode(nodeId: string) {
    setSelectedNodeId(nodeId);
    setIsNodeEditing(false);
    setNodeNameText(nodeId);
    const node = nodes.find((entry) => entry.id === nodeId);
    setConfigText(formatYamlMap(node?.data.config ?? {}));
  }

  const agentQuery = useQuery({
    queryKey: ["agent", agentName],
    queryFn: () => getAgent(agentName),
  });

  const versionsQuery = useQuery({
    queryKey: ["versions", agentName],
    queryFn: () => listVersions(agentName),
  });
  const selectedVersion = useMemo(
    () => (versionsQuery.data ?? []).find((version) => version.id === selectedVersionId),
    [selectedVersionId, versionsQuery.data],
  );
  const effectiveMetadataDescription = isMetadataEditing
    ? metadataDescription
    : (agentQuery.data?.description ?? metadataDescription);
  const effectiveMetadataOwnersText = isMetadataEditing
    ? metadataOwners
    : (agentQuery.data ? (agentQuery.data.owners ?? []).join(", ") : metadataOwners);
  const effectiveMetadataLabelsText = isMetadataEditing
    ? metadataLabels
    : (agentQuery.data ? labelsToText(agentQuery.data.labels ?? {}) : metadataLabels);

  const schemaState = useMemo(() => {
    const issues: string[] = [];
    let inputs: Record<string, unknown> = { type: "object", properties: {} };
    let outputs: Record<string, unknown> = { type: "object", properties: {} };
    try {
      inputs = JSON.parse(inputsSchemaText) as Record<string, unknown>;
    } catch (error) {
      issues.push(`Input schema JSON is invalid: ${(error as Error).message}`);
    }
    try {
      outputs = JSON.parse(outputsSchemaText) as Record<string, unknown>;
    } catch (error) {
      issues.push(`Output schema JSON is invalid: ${(error as Error).message}`);
    }
    return { inputs, outputs, issues };
  }, [inputsSchemaText, outputsSchemaText]);

  const generatedYaml = useMemo(() => {
    try {
      const document = flowToDocument({
        metadata: {
          name: agentName,
          description: effectiveMetadataDescription.trim(),
          owners: effectiveMetadataOwnersText
            .split(",")
            .map((item) => item.trim())
            .filter(Boolean),
          labels: parseLabels(effectiveMetadataLabelsText),
        },
        startNodeId: inferredStartNodeId,
        nodes,
        edges,
        inputsSchema: schemaState.inputs,
        outputsSchema: schemaState.outputs,
      });
      return dumpDslYaml(document);
    } catch (error) {
      return `# Unable to generate YAML:\n# ${(error as Error).message}`;
    }
  }, [
    agentName,
    edges,
    effectiveMetadataDescription,
    effectiveMetadataLabelsText,
    effectiveMetadataOwnersText,
    inferredStartNodeId,
    nodes,
    schemaState.inputs,
    schemaState.outputs,
  ]);

  const validationIssues = useMemo(
    () => localValidation(nodes, edges, inferredStartNodeId, schemaState.issues),
    [edges, inferredStartNodeId, nodes, schemaState.issues],
  );
  const hasMountedValidationRef = useRef(false);
  const validationSignal = useMemo(
    () =>
      JSON.stringify({
        nodeIds: nodes.map((node) => node.id),
        edgeRefs: edges.map((edge) => `${edge.source}->${edge.target}`),
        inputsSchemaText,
        outputsSchemaText,
      }),
    [edges, inputsSchemaText, nodes, outputsSchemaText],
  );
  const activeVersionId = agentQuery.data?.activeVersionId ?? "";
  const runVersionId = selectedVersionId || activeVersionId;
  const runVersion = useMemo(
    () => (versionsQuery.data ?? []).find((version) => version.id === runVersionId),
    [runVersionId, versionsQuery.data],
  );
  const canTriggerRun = Boolean(runVersionId);

  useEffect(() => {
    if (!hasMountedValidationRef.current) {
      hasMountedValidationRef.current = true;
      return;
    }
    const timeout = setTimeout(() => {
      if (validationIssues.length === 0) {
        toast.success("Local validation passed.");
      }
    }, 450);
    return () => clearTimeout(timeout);
  }, [validationIssues.length, validationSignal]);

  const loadVersionMutation = useMutation({
    mutationFn: (versionId: string) => getVersion(agentName, versionId),
    onSuccess: (version) => {
      try {
        const doc = parseDslYaml(version.dslYaml);
        const mapped = documentToFlow(doc);
        setMetadataDescription(mapped.metadata.description ?? "");
        setMetadataOwners((mapped.metadata.owners ?? []).join(", "));
        setMetadataLabels(labelsToText(mapped.metadata.labels ?? {}));
        setInputsSchemaText(formatJson(mapped.inputsSchema));
        setOutputsSchemaText(formatJson(mapped.outputsSchema));
        setNodes(mapped.nodes);
        setEdges(mapped.edges as Edge[]);
        const initialSelected = mapped.startNodeId || mapped.nodes[0]?.id || "";
        setSelectedNodeId(initialSelected);
        setIsNodeEditing(false);
        setNodeNameText(initialSelected);
        setConfigText(formatYamlMap(mapped.nodes.find((node) => node.id === initialSelected)?.data.config ?? {}));
        toast.success(`Loaded version v${version.versionNum}`);
      } catch (error) {
        toast.error(`Could not parse version YAML: ${(error as Error).message}`);
      }
    },
    onError: (error) => {
      toast.error((error as Error).message);
    },
  });

  const createDraftMutation = useMutation({
    mutationFn: () =>
      createVersion({
        actor,
        agentName,
        yaml: generatedYaml,
      }),
    onSuccess: (result) => {
      toast.success(`Created draft version v${result.versionNum}`);
      setSelectedVersionId(result.id);
      queryClient.invalidateQueries({ queryKey: ["versions", agentName] });
    },
    onError: (error) => {
      if (error instanceof ApiError) {
        toast.error(error.message);
      } else {
        toast.error("Could not create draft version");
      }
    },
  });

  const updateDraftMutation = useMutation({
    mutationFn: (versionId: string) =>
      updateVersion({
        actor,
        agentName,
        versionId,
        yaml: generatedYaml,
      }),
    onSuccess: () => {
      toast.success("Draft updated");
      queryClient.invalidateQueries({ queryKey: ["versions", agentName] });
    },
    onError: (error) => {
      toast.error((error as Error).message);
    },
  });

  const publishMutation = useMutation({
    mutationFn: (versionId: string) =>
      publishVersion({
        actor,
        versionId,
      }),
    onSuccess: () => {
      toast.success("Version published");
      queryClient.invalidateQueries({ queryKey: ["versions", agentName] });
      queryClient.invalidateQueries({ queryKey: ["agent", agentName] });
    },
    onError: (error) => {
      toast.error((error as Error).message);
    },
  });

  const rollbackMutation = useMutation({
    mutationFn: (toVersionId: string) =>
      rollbackAgent({
        actor,
        agentName,
        toVersionId,
        reason: "Set active version from builder",
      }),
    onSuccess: () => {
      toast.success("Active version updated");
      queryClient.invalidateQueries({ queryKey: ["versions", agentName] });
      queryClient.invalidateQueries({ queryKey: ["agent", agentName] });
    },
    onError: (error) => {
      toast.error((error as Error).message);
    },
  });

  const selectedIsActive = selectedVersionId !== "" && selectedVersionId === agentQuery.data?.activeVersionId;
  const canSetActive = Boolean(
    selectedVersionId &&
      selectedVersion &&
      (selectedVersion.status === "draft" || (selectedVersion.status === "published" && !selectedIsActive)),
  );
  const setActiveButtonLabel = selectedVersion?.status === "draft"
    ? "Publish + Set Active"
    : selectedVersion?.status === "published"
      ? (selectedIsActive ? "Already Active" : "Set Selected Active")
      : "Set Active Version";

  const updateMetadataMutation = useMutation({
    mutationFn: () =>
      updateAgent({
        actor,
        name: agentName,
        description: metadataDescription.trim(),
        owners: metadataOwners
          .split(",")
          .map((item) => item.trim())
          .filter(Boolean),
        labels: parseLabels(metadataLabels),
      }),
    onSuccess: (updatedAgent) => {
      setMetadataDescription(updatedAgent.description ?? "");
      setMetadataOwners((updatedAgent.owners ?? []).join(", "));
      setMetadataLabels(labelsToText(updatedAgent.labels ?? {}));
      setIsMetadataEditing(false);
      queryClient.invalidateQueries({ queryKey: ["agent", agentName] });
      toast.success("Agent metadata saved");
    },
    onError: (error) => {
      toast.error((error as Error).message);
    },
  });

  const runMutation = useMutation({
    mutationFn: async () => {
      if (!canTriggerRun || !runVersionId) {
        throw new Error("No runnable version available.");
      }
      const inputs = safeJsonParse<Record<string, unknown>>(runInputsText, {});
      return startRun({
        actor,
        agentName,
        versionId: runVersionId,
        inputs,
      });
    },
    onSuccess: (run) => {
      toast.success(`Run started (${run.runId})`);
      setIsRunModalOpen(false);
      router.push(`/runs/${run.runId}`);
    },
    onError: (error) => {
      toast.error((error as Error).message);
    },
  });

  function addNode(nodeType: NodeType, sourceNodeId?: string | null) {
    const nodeId = nextUniqueNodeName(nodeType, nodes);
    let basePosition = { x: 860, y: 260 };
    if (reactFlowRef.current && typeof window !== "undefined") {
      basePosition = reactFlowRef.current.screenToFlowPosition({
        x: window.innerWidth / 2,
        y: window.innerHeight / 2,
      });
    }
    const offsetX = (nodes.length % 3) * 36;
    const offsetY = Math.floor(nodes.length / 3) * 22;
    setNodes((current) => [
      ...current,
      {
        id: nodeId,
        type: "builderNode",
        position: { x: basePosition.x + offsetX, y: basePosition.y + offsetY },
        data: {
          label: nodeId,
          type: nodeType,
          config: structuredClone(NODE_TEMPLATES[nodeType]),
          onError: "fail",
          cases:
            nodeType === "control.switch"
              ? [
                  { when: "true", to: "", default: false },
                  { to: "", default: true },
                ]
              : undefined,
          branches: nodeType === "control.parallel" ? [{ to: "" }] : undefined,
          join: nodeType === "control.parallel" ? "all" : undefined,
        },
      },
    ]);
    if (sourceNodeId && nodes.some((node) => node.id === sourceNodeId)) {
      setEdges((current) =>
        addEdge(
          {
            id: `${sourceNodeId}-${nodeId}-${randomId("edge")}`,
            source: sourceNodeId,
            target: nodeId,
            animated: sourceNodeId === inferredStartNodeId,
          },
          current,
        ),
      );
    }
    setSelectedNodeId(nodeId);
    setNodeNameText(nodeId);
    setConfigText(formatYamlMap(NODE_TEMPLATES[nodeType]));
    setIsNodeLibraryOpen(false);
    setNodeSearch("");
    setPendingSourceNodeId(null);
  }

  function updateSelectedNode(mutator: (node: BuilderNode) => BuilderNode) {
    if (!selectedNodeId) {
      return;
    }
    setNodes((current) => current.map((node) => (node.id === selectedNodeId ? mutator(node) : node)));
  }

  function removeNode(nodeId: string) {
    if (!nodeId) {
      return;
    }
    setNodes((current) => current.filter((node) => node.id !== nodeId));
    setEdges((current) => current.filter((edge) => edge.source !== nodeId && edge.target !== nodeId));
    if (selectedNodeId === nodeId) {
      setSelectedNodeId("");
      setIsNodeEditing(false);
      setNodeNameText("");
      setConfigText("{}");
    }
  }

  function updateNodeReferences(oldId: string, newId: string) {
    setNodes((current) =>
      current.map((node) => {
        const nextCases = node.data.cases?.map((item) => ({
          ...item,
          to: item.to === oldId ? newId : item.to,
        }));
        const nextBranches = node.data.branches?.map((item) => ({
          ...item,
          to: item.to === oldId ? newId : item.to,
        }));
        const nextOnError =
          node.data.onError?.startsWith("route:") && getRouteTarget(node.data.onError) === oldId
            ? `route:${newId}`
            : node.data.onError;

        return {
          ...node,
          id: node.id === oldId ? newId : node.id,
          data: {
            ...node.data,
            cases: nextCases,
            branches: nextBranches,
            onError: nextOnError,
          },
        };
      }),
    );
    setEdges((current) =>
      current.map((edge) => ({
        ...edge,
        source: edge.source === oldId ? newId : edge.source,
        target: edge.target === oldId ? newId : edge.target,
      })),
    );
    setSelectedNodeId((current) => (current === oldId ? newId : current));
    setPendingSourceNodeId((current) => (current === oldId ? newId : current));
  }

  function renameSelectedNode(rawValue: string) {
    if (!selectedNode) {
      return false;
    }
    const nextId = rawValue.trim();
    if (!nextId) {
      toast.error("Node name is required");
      setNodeNameText(selectedNode.id);
      return false;
    }
    if (nextId === selectedNode.id) {
      setNodeNameText(nextId);
      return true;
    }
    if (nodes.some((node) => node.id === nextId)) {
      toast.error("Node name must be unique");
      setNodeNameText(selectedNode.id);
      return false;
    }
    updateNodeReferences(selectedNode.id, nextId);
    setNodeNameText(nextId);
    return true;
  }

  function saveNodeInspectorChanges() {
    if (!selectedNode) {
      return;
    }
    if (selectedNode.data.type !== "control.switch" && selectedNode.data.type !== "control.parallel") {
      try {
        const parsed = parseYamlMap(configText);
        updateSelectedNode((node) => ({
          ...node,
          data: {
            ...node.data,
            config: parsed,
          },
        }));
        setConfigText(formatYamlMap(parsed));
      } catch {
        toast.error("Config must be valid YAML mapping");
        return;
      }
    }
    const renamed = renameSelectedNode(nodeNameText);
    if (!renamed) {
      return;
    }
    setIsNodeEditing(false);
    toast.success("Node changes saved");
  }

  function onConnect(connection: Connection) {
    setEdges((existing) =>
      addEdge(
        {
          ...connection,
          animated: connection.source === inferredStartNodeId,
        },
        existing,
      ),
    );
  }

  return (
    <AppShell
      title={`Builder · ${agentName}`}
      fullWidth
    >
      <div className="mx-auto mb-4 grid w-full max-w-[1800px] items-stretch gap-4 xl:grid-cols-2">
        <section className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
          <div className="mb-3 flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-zinc-100">Agent Metadata</h3>
            {isMetadataEditing ? (
              <button
                type="button"
                onClick={() => updateMetadataMutation.mutate()}
                disabled={updateMetadataMutation.isPending}
                className="btn-interactive w-[116px] rounded-md bg-blue-600 px-3 py-1.5 text-center text-xs font-medium text-white hover:bg-blue-500"
              >
                {updateMetadataMutation.isPending ? "Saving..." : "Save Metadata"}
              </button>
            ) : (
              <button
                type="button"
                onClick={() => {
                  if (agentQuery.data) {
                    setMetadataDescription(agentQuery.data.description ?? "");
                    setMetadataOwners((agentQuery.data.owners ?? []).join(", "));
                    setMetadataLabels(labelsToText(agentQuery.data.labels ?? {}));
                  }
                  setIsMetadataEditing(true);
                }}
                className="btn-interactive w-[116px] rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-center text-xs text-zinc-200"
              >
                Edit Metadata
              </button>
            )}
          </div>

          <fieldset
            disabled={!isMetadataEditing || updateMetadataMutation.isPending}
            className="grid gap-3 md:grid-cols-2"
          >
            <label className="grid gap-1 md:col-span-2">
              <span className="text-xs text-zinc-400">Description</span>
              <input
                value={effectiveMetadataDescription}
                onChange={(event) => setMetadataDescription(event.target.value)}
                className="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none ring-blue-500/60 focus:ring-2 disabled:cursor-not-allowed disabled:opacity-60"
                placeholder="What this agent automates"
              />
            </label>
            <label className="grid gap-1">
              <span className="text-xs text-zinc-400">Owners</span>
              <input
                value={effectiveMetadataOwnersText}
                onChange={(event) => setMetadataOwners(event.target.value)}
                className="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none ring-blue-500/60 focus:ring-2 disabled:cursor-not-allowed disabled:opacity-60"
                placeholder="ops-leads,support"
              />
            </label>
            <label className="grid gap-1">
              <span className="text-xs text-zinc-400">Labels</span>
              <input
                value={effectiveMetadataLabelsText}
                onChange={(event) => setMetadataLabels(event.target.value)}
                className="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none ring-blue-500/60 focus:ring-2 disabled:cursor-not-allowed disabled:opacity-60"
                placeholder="team:support"
              />
            </label>
          </fieldset>
        </section>

        <section className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-zinc-100">Version Actions</h3>
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => setIsRunModalOpen(true)}
                disabled={!canTriggerRun}
                className="btn-interactive inline-flex items-center gap-1 rounded-md border border-emerald-700 bg-emerald-900/40 px-3 py-1.5 text-xs font-medium text-emerald-100 hover:border-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
              >
                <Play size={12} />
                Trigger Run
              </button>
              <Link
                href={`/agents/${encodeURIComponent(agentName)}/runs`}
                className="btn-interactive rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-200"
              >
                View Runs
              </Link>
            </div>
          </div>

          <div className="grid gap-3 md:grid-cols-2">
            <div className="rounded-md border border-zinc-800 bg-zinc-950/70 p-3">
              <p className="text-xs text-zinc-500">Active version</p>
              <p className="mt-1 break-all text-sm text-zinc-200">{agentQuery.data?.activeVersionId ?? "None"}</p>
            </div>

            <div className="grid gap-1 rounded-md border border-zinc-800 bg-zinc-950/70 p-3">
              <label className="text-xs text-zinc-500">Working version</label>
              <select
                value={selectedVersionId}
                onChange={(event) => {
                  const nextVersionId = event.target.value;
                  setSelectedVersionId(nextVersionId);
                  if (nextVersionId) {
                    loadVersionMutation.mutate(nextVersionId);
                  }
                }}
                className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-2 text-sm outline-none ring-blue-500/60 focus:ring-2"
              >
                <option value="">Select version...</option>
                {(versionsQuery.data ?? []).map((version) => (
                  <option key={version.id} value={version.id}>
                    v{version.versionNum} · {version.status}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div className="mt-3 flex flex-wrap gap-2">
            <button
              type="button"
              onClick={() => createDraftMutation.mutate()}
              disabled={createDraftMutation.isPending}
              className="btn-interactive rounded-md bg-blue-600 px-3 py-2 text-sm font-medium text-white hover:bg-blue-500"
            >
              {createDraftMutation.isPending ? "Creating..." : "Create Draft"}
            </button>

            <button
              type="button"
              onClick={() => {
                if (!selectedVersionId) {
                  toast.error("Select a draft version first");
                  return;
                }
                if (selectedVersion?.status !== "draft") {
                  toast.error("Only draft versions can be updated");
                  return;
                }
                updateDraftMutation.mutate(selectedVersionId);
              }}
              disabled={updateDraftMutation.isPending || !selectedVersionId || selectedVersion?.status !== "draft"}
              className="btn-interactive rounded-md border border-zinc-600 bg-zinc-900 px-3 py-2 text-sm font-medium text-zinc-100 hover:border-zinc-400"
            >
              {updateDraftMutation.isPending ? "Saving..." : "Save Changes to Draft"}
            </button>

            <button
              type="button"
              onClick={() => {
                if (!selectedVersionId) {
                  toast.error("Select a version first");
                  return;
                }
                if (!selectedVersion) {
                  toast.error("Selected version could not be loaded");
                  return;
                }
                if (selectedVersion.status === "draft") {
                  publishMutation.mutate(selectedVersionId);
                  return;
                }
                if (selectedVersion.status === "published") {
                  if (selectedIsActive) {
                    toast.error("This version is already active");
                    return;
                  }
                  rollbackMutation.mutate(selectedVersionId);
                  return;
                }
                toast.error("Only draft or published versions can be set active");
              }}
              disabled={publishMutation.isPending || rollbackMutation.isPending || !canSetActive}
              className="btn-interactive rounded-md border border-emerald-700 bg-emerald-900/40 px-3 py-2 text-sm font-medium text-emerald-100 hover:border-emerald-500"
            >
              {publishMutation.isPending ? "Publishing..." : rollbackMutation.isPending ? "Updating..." : setActiveButtonLabel}
            </button>
          </div>

        </section>
      </div>

      {isNodeLibraryOpen ? (
        <div className="fixed inset-0 z-40 flex items-start justify-center p-4 pt-16">
          <button
            type="button"
            aria-label="Close node library"
            className="absolute inset-0 bg-black/60 backdrop-blur-[1px]"
            onClick={() => {
              setIsNodeLibraryOpen(false);
              setPendingSourceNodeId(null);
            }}
          />
          <div className="relative z-10 w-full max-w-4xl rounded-xl border border-zinc-700 bg-zinc-950 shadow-2xl">
            <div className="flex items-center justify-between border-b border-zinc-800 px-4 py-3">
              <div>
                <h3 className="text-sm font-semibold text-zinc-100">Node Library</h3>
                <p className="text-xs text-zinc-500">Search, understand, and add nodes with confidence.</p>
              </div>
              <button
                type="button"
                onClick={() => {
                  setIsNodeLibraryOpen(false);
                  setPendingSourceNodeId(null);
                }}
                className="btn-interactive rounded-md border border-zinc-700 px-2 py-1 text-xs text-zinc-300"
              >
                Close
              </button>
            </div>
            {pendingSourceNodeId ? (
              <div className="border-b border-zinc-800 px-4 py-2 text-xs text-zinc-400">
                Adding node after <span className="text-zinc-200">{pendingSourceNodeId}</span>
              </div>
            ) : null}

            <div className="border-b border-zinc-800 px-4 py-3">
              <input
                value={nodeSearch}
                onChange={(event) => setNodeSearch(event.target.value)}
                placeholder="Search by name, type, category, or config..."
                className="w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-200 outline-none ring-blue-500/60 focus:ring-2"
              />
            </div>

            <div className="max-h-[62vh] overflow-y-auto p-4">
              <div className="grid gap-3 sm:grid-cols-2">
                {filteredNodeLibrary.map((entry) => (
                  <div key={entry.type} className="rounded-lg border border-zinc-800 bg-zinc-900/70 p-3">
                    <div className="mb-2 flex items-center justify-between gap-2">
                      <p className="text-sm font-medium text-zinc-100">{entry.label}</p>
                      <span className="rounded-full bg-zinc-800 px-2 py-0.5 text-[10px] text-zinc-300">{entry.category}</span>
                    </div>
                    <p className="text-xs text-zinc-300">{entry.description}</p>
                    <p className="mt-2 text-[11px] text-zinc-500">Config: {entry.configHelp}</p>
                    <button
                      type="button"
                      onClick={() => addNode(entry.type, pendingSourceNodeId)}
                      className="btn-interactive mt-3 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-100 hover:border-zinc-500"
                    >
                      + Add {entry.label}
                    </button>
                  </div>
                ))}
              </div>
              {filteredNodeLibrary.length === 0 ? (
                <p className="text-sm text-zinc-400">No nodes matched your search.</p>
              ) : null}
            </div>
          </div>
        </div>
      ) : null}

      {isRunModalOpen ? (
        <div className="fixed inset-0 z-50 flex items-start justify-center p-4 pt-16">
          <button
            type="button"
            aria-label="Close run modal"
            className="absolute inset-0 bg-black/60 backdrop-blur-[1px]"
            onClick={() => setIsRunModalOpen(false)}
          />
          <div className="relative z-10 w-full max-w-2xl rounded-xl border border-zinc-700 bg-zinc-950 shadow-2xl">
            <div className="flex items-center justify-between border-b border-zinc-800 px-4 py-3">
              <div>
                <h3 className="text-sm font-semibold text-zinc-100">Trigger Run</h3>
                <p className="text-xs text-zinc-500">Provide run input payload and start execution.</p>
              </div>
              <button
                type="button"
                onClick={() => setIsRunModalOpen(false)}
                className="btn-interactive rounded-md border border-zinc-700 px-2 py-1 text-xs text-zinc-300"
              >
                Close
              </button>
            </div>
            <div className="space-y-3 p-4">
              <div className="rounded-md border border-zinc-800 bg-zinc-900/60 px-3 py-2 text-xs text-zinc-300">
                Running version:{" "}
                {runVersionId
                  ? `${selectedVersionId ? "selected" : "active"} · v${runVersion?.versionNum ?? "?"} (${runVersionId})`
                  : "None"}
              </div>
              <textarea
                value={runInputsText}
                onChange={(event) => setRunInputsText(event.target.value)}
                rows={12}
                className="w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 font-mono text-xs text-zinc-100 outline-none ring-emerald-500/60 focus:ring-2"
              />
              <button
                type="button"
                onClick={() => runMutation.mutate()}
                disabled={runMutation.isPending || !canTriggerRun}
                className="btn-interactive inline-flex items-center gap-2 rounded-md bg-emerald-600 px-3 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
              >
                <Play size={14} />
                {runMutation.isPending ? "Starting..." : "Start Run"}
              </button>
            </div>
          </div>
        </div>
      ) : null}

      <div className="mx-auto grid w-full max-w-[1800px] items-stretch gap-4 xl:grid-cols-[minmax(0,1fr)_390px]">
        <div className="h-full overflow-hidden rounded-xl border border-zinc-800 bg-zinc-900/50">
          <div className="h-full min-h-[760px] w-full xl:min-h-[900px]">
            <ReactFlow<BuilderNode, Edge>
              nodes={nodes.map((node) => ({
                ...node,
                type: "builderNode",
                data: {
                  ...node.data,
                  onAddFromNode: (sourceNodeId: string) => {
                    setPendingSourceNodeId(sourceNodeId);
                    setIsNodeLibraryOpen(true);
                  },
                  onDeleteNode: (nodeId: string) => removeNode(nodeId),
                },
              }))}
              edges={edges}
              nodeTypes={NODE_TYPES}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              onConnect={onConnect}
              onNodeClick={(_, node) => selectNode(node.id)}
              onInit={(instance) => {
                reactFlowRef.current = instance;
                instance.fitView({ padding: 0.2 });
              }}
              fitView
              colorMode="dark"
            >
              <Controls />
              <Background color="#27272a" gap={16} />
              {nodes.length === 0 ? (
                <Panel position="top-left">
                  <button
                    type="button"
                    onClick={() => {
                      setPendingSourceNodeId(null);
                      setIsNodeLibraryOpen(true);
                    }}
                    className="btn-interactive inline-flex items-center gap-2 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-200 hover:border-zinc-500"
                  >
                    <Plus size={12} />
                    Add first node
                  </button>
                </Panel>
              ) : null}
            </ReactFlow>
          </div>
        </div>

        <div className="space-y-4">
          <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
            <div className="mb-2 flex items-center justify-between gap-2">
              <h2 className="text-sm font-semibold text-zinc-100">Node Inspector</h2>
              {selectedNode ? (
                isNodeEditing ? (
                  <button
                    type="button"
                    onClick={saveNodeInspectorChanges}
                    className="btn-interactive rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-500"
                  >
                    Save Node
                  </button>
                ) : (
                  <button
                    type="button"
                    onClick={() => setIsNodeEditing(true)}
                    className="btn-interactive rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-200"
                  >
                    Edit Node
                  </button>
                )
              ) : null}
            </div>
            {!selectedNode ? (
              <p className="text-sm text-zinc-400">Select a node from the canvas.</p>
            ) : (
              <fieldset disabled={!isNodeEditing} className="space-y-3">
                <label className="grid gap-1">
                  <span className="text-xs text-zinc-500">Node name</span>
                  <input
                    value={nodeNameText}
                    onChange={(event) => setNodeNameText(event.target.value)}
                    className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-sm outline-none ring-blue-500/60 focus:ring-2"
                  />
                  <p className="text-[11px] text-zinc-500">Must be unique within this agent.</p>
                </label>
                <div>
                  <p className="text-xs text-zinc-500">Type</p>
                  <p className="text-sm text-zinc-200">{selectedNode.data.type}</p>
                </div>
                {selectedNodeDefinition ? (
                  <div className="rounded-md border border-zinc-800 bg-zinc-950/60 p-2">
                    <p className="text-xs text-zinc-400">{selectedNodeDefinition.description}</p>
                    <p className="mt-1 text-[11px] text-zinc-500">Config: {selectedNodeDefinition.configHelp}</p>
                  </div>
                ) : null}
                <label className="grid gap-1">
                  <span className="text-xs text-zinc-500">onError</span>
                  <select
                    value={getOnErrorMode(selectedNode.data.onError)}
                    onChange={(event) => {
                      const nextMode = event.target.value as OnErrorOption;
                      updateSelectedNode((node) => {
                        if (nextMode === "fail") {
                          return { ...node, data: { ...node.data, onError: "fail" } };
                        }
                        if (nextMode === "continue") {
                          return { ...node, data: { ...node.data, onError: "continue" } };
                        }
                        const routeTarget =
                          nodes.find((entry) => entry.id !== node.id)?.id ?? getRouteTarget(node.data.onError);
                        return {
                          ...node,
                          data: {
                            ...node.data,
                            onError: routeTarget ? `route:${routeTarget}` : "fail",
                          },
                        };
                      });
                    }}
                    className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-sm outline-none ring-blue-500/60 focus:ring-2"
                  >
                    {ON_ERROR_OPTIONS.map((option) => (
                      <option key={option} value={option}>
                        {option === "route" ? "route to node" : option}
                      </option>
                    ))}
                  </select>
                </label>
                {getOnErrorMode(selectedNode.data.onError) === "route" ? (
                  <label className="grid gap-1">
                    <span className="text-xs text-zinc-500">Route target node</span>
                    {nodes.filter((node) => node.id !== selectedNode.id).length === 0 ? (
                      <p className="rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-500">
                        Add another node to use route mode.
                      </p>
                    ) : (
                      <select
                        value={getRouteTarget(selectedNode.data.onError)}
                        onChange={(event) =>
                          updateSelectedNode((node) => ({
                            ...node,
                            data: {
                              ...node.data,
                              onError: `route:${event.target.value}`,
                            },
                          }))
                        }
                        className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-sm outline-none ring-blue-500/60 focus:ring-2"
                      >
                        {nodes
                          .filter((node) => node.id !== selectedNode.id)
                          .map((node) => (
                            <option key={node.id} value={node.id}>
                              {node.id}
                            </option>
                          ))}
                      </select>
                    )}
                  </label>
                ) : null}
                <label className="grid gap-1">
                  <span className="text-xs text-zinc-500">timeout (optional, e.g. 30s)</span>
                  <input
                    value={selectedNode.data.timeout ?? ""}
                    onChange={(event) =>
                      updateSelectedNode((node) => ({
                        ...node,
                        data: {
                          ...node.data,
                          timeout: event.target.value || undefined,
                        },
                      }))
                    }
                    className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-sm outline-none ring-blue-500/60 focus:ring-2"
                  />
                </label>

                {selectedNode.data.type === "control.switch" ? (
                  <div className="space-y-2 rounded-md border border-zinc-800 bg-zinc-950/70 p-2">
                    <p className="text-xs text-zinc-400">Cases</p>
                    {(selectedNode.data.cases ?? []).map((item, index) => (
                      <div key={`${selectedNode.id}-case-${index}`} className="grid grid-cols-[1fr_90px_62px] gap-2">
                        <input
                          value={item.when ?? ""}
                          onChange={(event) =>
                            updateSelectedNode((node) => {
                              const cases = [...(node.data.cases ?? [])];
                              cases[index] = { ...cases[index], when: event.target.value };
                              return { ...node, data: { ...node.data, cases } };
                            })
                          }
                          placeholder="when expression"
                          className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1 text-xs"
                        />
                        <input
                          value={item.to}
                          onChange={(event) =>
                            updateSelectedNode((node) => {
                              const cases = [...(node.data.cases ?? [])];
                              cases[index] = { ...cases[index], to: event.target.value };
                              return { ...node, data: { ...node.data, cases } };
                            })
                          }
                          placeholder="to node"
                          className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1 text-xs"
                        />
                        <label className="flex items-center justify-center gap-1 rounded-md border border-zinc-700 bg-zinc-950 px-1 text-[10px]">
                          <input
                            type="checkbox"
                            checked={Boolean(item.default)}
                            onChange={(event) =>
                              updateSelectedNode((node) => {
                                const cases = [...(node.data.cases ?? [])];
                                cases[index] = { ...cases[index], default: event.target.checked };
                                return { ...node, data: { ...node.data, cases } };
                              })
                            }
                          />
                          default
                        </label>
                      </div>
                    ))}
                    <button
                      type="button"
                      className="btn-interactive rounded-md border border-zinc-700 px-2 py-1 text-xs"
                      onClick={() =>
                        updateSelectedNode((node) => ({
                          ...node,
                          data: {
                            ...node.data,
                            cases: [...(node.data.cases ?? []), { when: "", to: "", default: false } as SwitchCase],
                          },
                        }))
                      }
                    >
                      + Add case
                    </button>
                  </div>
                ) : null}

                {selectedNode.data.type === "control.parallel" ? (
                  <div className="space-y-2 rounded-md border border-zinc-800 bg-zinc-950/70 p-2">
                    <label className="grid gap-1">
                      <span className="text-xs text-zinc-500">Join</span>
                      <select
                        value={selectedNode.data.join ?? "all"}
                        onChange={(event) =>
                          updateSelectedNode((node) => ({
                            ...node,
                            data: {
                              ...node.data,
                              join: event.target.value as "all" | "any",
                            },
                          }))
                        }
                        className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1 text-xs"
                      >
                        <option value="all">all</option>
                        <option value="any">any</option>
                      </select>
                    </label>
                    {(selectedNode.data.branches ?? []).map((item, index) => (
                      <input
                        key={`${selectedNode.id}-branch-${index}`}
                        value={item.to}
                        onChange={(event) =>
                          updateSelectedNode((node) => {
                            const branches = [...(node.data.branches ?? [])];
                            branches[index] = { to: event.target.value };
                            return {
                              ...node,
                              data: {
                                ...node.data,
                                branches,
                              },
                            };
                          })
                        }
                        placeholder="branch root node id"
                        className="w-full rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1 text-xs"
                      />
                    ))}
                    <button
                      type="button"
                      className="btn-interactive rounded-md border border-zinc-700 px-2 py-1 text-xs"
                      onClick={() =>
                        updateSelectedNode((node) => ({
                          ...node,
                          data: {
                            ...node.data,
                            branches: [...(node.data.branches ?? []), { to: "" } as BranchRef],
                          },
                        }))
                      }
                    >
                      + Add branch
                    </button>
                  </div>
                ) : null}

                {selectedNode.data.type !== "control.switch" && selectedNode.data.type !== "control.parallel" ? (
                  <label className="grid gap-1">
                    <span className="text-xs text-zinc-500">Config (YAML)</span>
                    <textarea
                      value={configText}
                      onChange={(event) => setConfigText(event.target.value)}
                      rows={8}
                      className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-2 font-mono text-xs text-zinc-200 outline-none ring-blue-500/60 focus:ring-2"
                    />
                  </label>
                ) : null}
              </fieldset>
            )}
          </div>

          <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
            <h2 className="mb-2 text-sm font-semibold text-zinc-100">Schema + Validation</h2>
            <label className="mb-2 grid gap-1">
              <span className="text-xs text-zinc-500">Input schema (JSON)</span>
              <textarea
                value={inputsSchemaText}
                onChange={(event) => setInputsSchemaText(event.target.value)}
                rows={5}
                className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-2 font-mono text-xs"
              />
            </label>
            <label className="grid gap-1">
              <span className="text-xs text-zinc-500">Output schema (JSON)</span>
              <textarea
                value={outputsSchemaText}
                onChange={(event) => setOutputsSchemaText(event.target.value)}
                rows={5}
                className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-2 font-mono text-xs"
              />
            </label>
            <div className="mt-3 space-y-1">
              {validationIssues.length > 0
                ? (
                validationIssues.map((issue) => (
                  <p key={issue} className="rounded-md bg-amber-600/15 px-2 py-1 text-xs text-amber-100">
                    {issue}
                  </p>
                ))
                  )
                : null}
            </div>
          </div>

        </div>
      </div>
    </AppShell>
  );
}
