"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { FormEvent, useMemo, useState } from "react";
import { toast } from "sonner";

import { AppShell } from "@/components/app-shell";
import { useSettings } from "@/components/settings-context";
import { ApiError, createAgent, listAgents } from "@/lib/api";

export function AgentsPage() {
  const queryClient = useQueryClient();
  const { actor } = useSettings();
  const [showForm, setShowForm] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [owners, setOwners] = useState("");
  const [labels, setLabels] = useState("");

  const agentsQuery = useQuery({
    queryKey: ["agents"],
    queryFn: listAgents,
  });

  const createMutation = useMutation({
    mutationFn: createAgent,
    onSuccess: (agent) => {
      toast.success(`Agent "${agent.name}" created`);
      setShowForm(false);
      setName("");
      setDescription("");
      setOwners("");
      setLabels("");
      queryClient.invalidateQueries({ queryKey: ["agents"] });
    },
    onError: (error) => {
      if (error instanceof ApiError) {
        toast.error(error.message);
        return;
      }
      toast.error("Unable to create agent");
    },
  });

  const isBusy = agentsQuery.isPending || createMutation.isPending;
  const sortedAgents = useMemo(() => (agentsQuery.data ?? []).slice().sort((a, b) => a.name.localeCompare(b.name)), [agentsQuery.data]);

  function parseLabels(raw: string) {
    const out: Record<string, string> = {};
    const parts = raw
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
    for (const part of parts) {
      const [k, ...rest] = part.split(":");
      if (!k || rest.length === 0) {
        continue;
      }
      out[k.trim()] = rest.join(":").trim();
    }
    return out;
  }

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    const trimmedName = name.trim().toLowerCase();
    if (!trimmedName) {
      toast.error("Agent name is required");
      return;
    }

    createMutation.mutate({
      actor,
      name: trimmedName,
      description: description.trim(),
      owners: owners
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean),
      labels: parseLabels(labels),
    });
  }

  return (
    <AppShell title="Agents">
      <div className="mb-6 flex items-center justify-between gap-4">
        <p className="text-sm text-zinc-400">Use lowercase names with hyphens. Example: `refund-triage`.</p>
        <button
          type="button"
          onClick={() => setShowForm((current) => !current)}
          className="btn-interactive rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm font-medium text-zinc-100 hover:border-zinc-500"
        >
          {showForm ? "Close" : "New Agent"}
        </button>
      </div>

      {showForm ? (
        <form onSubmit={onSubmit} className="mb-6 grid gap-3 rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
          <div className="grid gap-1">
            <label className="text-xs text-zinc-400">Name</label>
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="refund-triage"
              className="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none ring-blue-500/60 focus:ring-2"
            />
          </div>
          <div className="grid gap-1">
            <label className="text-xs text-zinc-400">Description</label>
            <input
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              placeholder="Triage refund tickets and auto-route decisions."
              className="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none ring-blue-500/60 focus:ring-2"
            />
          </div>
          <div className="grid gap-1">
            <label className="text-xs text-zinc-400">Owners (comma-separated)</label>
            <input
              value={owners}
              onChange={(event) => setOwners(event.target.value)}
              placeholder="ops-leads,support"
              className="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none ring-blue-500/60 focus:ring-2"
            />
          </div>
          <div className="grid gap-1">
            <label className="text-xs text-zinc-400">Labels (comma-separated key:value)</label>
            <input
              value={labels}
              onChange={(event) => setLabels(event.target.value)}
              placeholder="team:support,priority:high"
              className="rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none ring-blue-500/60 focus:ring-2"
            />
          </div>
          <button
            type="submit"
            disabled={createMutation.isPending}
            className="btn-interactive mt-2 rounded-md bg-blue-600 px-3 py-2 text-sm font-medium text-white hover:bg-blue-500"
          >
            {createMutation.isPending ? "Creating..." : "Create Agent"}
          </button>
        </form>
      ) : null}

      {agentsQuery.isError ? (
        <div className="rounded-xl border border-red-900/60 bg-red-950/30 p-4 text-sm text-red-200">
          {(agentsQuery.error as Error).message}
        </div>
      ) : null}

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {sortedAgents.map((agent) => (
          <Link
            key={agent.id}
            href={`/agents/${agent.name}/builder`}
            className="btn-interactive group rounded-xl border border-zinc-800 bg-zinc-900/60 p-4 hover:border-zinc-600"
          >
            <div className="flex items-start justify-between gap-2">
              <h3 className="text-sm font-semibold text-zinc-100">{agent.name}</h3>
              {agent.activeVersionId ? (
                <span className="rounded-full bg-emerald-500/10 px-2 py-0.5 text-[11px] text-emerald-300">active</span>
              ) : (
                <span className="rounded-full bg-zinc-700/40 px-2 py-0.5 text-[11px] text-zinc-300">inactive</span>
              )}
            </div>
            <p className="mt-2 text-sm text-zinc-400">{agent.description || "No description yet."}</p>
            <p className="mt-3 text-xs text-zinc-500 group-hover:text-zinc-300">Open builder &rarr;</p>
          </Link>
        ))}
      </div>

      {!isBusy && sortedAgents.length === 0 ? (
        <div className="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 text-sm text-zinc-400">
          No agents yet. Create your first agent to start building a graph workflow.
        </div>
      ) : null}
    </AppShell>
  );
}
