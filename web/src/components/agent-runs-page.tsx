"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";

import { AppShell } from "@/components/app-shell";
import { ApiError, listRuns } from "@/lib/api";

type AgentRunsPageProps = {
  agentName: string;
};

function statusPillClass(status: string) {
  if (status === "succeeded") {
    return "bg-emerald-500/15 text-emerald-300";
  }
  if (status === "failed" || status === "timed_out" || status === "cancelled") {
    return "bg-red-500/15 text-red-300";
  }
  return "bg-blue-500/15 text-blue-300";
}

export function AgentRunsPage({ agentName }: AgentRunsPageProps) {
  const runsQuery = useQuery({
    queryKey: ["runs", agentName],
    queryFn: () => listRuns(agentName, 100),
    refetchInterval: 5_000,
  });

  return (
    <AppShell title={`Runs · ${agentName}`} description="Monitor recent executions and inspect failures quickly.">
      <div className="mb-4 flex items-center justify-between gap-3">
        <Link
          href={`/agents/${encodeURIComponent(agentName)}/builder`}
          className="btn-interactive rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-200"
        >
          &larr; Back to builder
        </Link>
        <button
          type="button"
          onClick={() => runsQuery.refetch()}
          className="btn-interactive rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-200"
        >
          Refresh
        </button>
      </div>

      {runsQuery.isError ? (
        <div className="rounded-xl border border-red-900/60 bg-red-950/30 p-4 text-sm text-red-200">
          {(runsQuery.error as Error).message}
          {runsQuery.error instanceof ApiError && runsQuery.error.status === 405 ? (
            <p className="mt-2 text-xs text-red-100/90">
              Quick fix: restart the controlplane process, then refresh this page.
            </p>
          ) : null}
        </div>
      ) : null}

      <div className="space-y-2">
        {(runsQuery.data ?? []).map((run) => (
          <Link
            key={run.id}
            href={`/runs/${run.id}`}
            className="btn-interactive block rounded-xl border border-zinc-800 bg-zinc-900/60 p-4 transition hover:border-zinc-600"
          >
            <div className="flex items-center justify-between gap-3">
              <p className="truncate text-sm text-zinc-200">{run.id}</p>
              <span className={`rounded-full px-2 py-0.5 text-xs ${statusPillClass(run.status)}`}>{run.status}</span>
            </div>
            <div className="mt-2 flex items-center justify-between gap-4 text-xs text-zinc-500">
              <span>Started: {run.startedAt ? new Date(run.startedAt).toLocaleString() : "-"}</span>
              <span>Version: {run.versionId}</span>
            </div>
          </Link>
        ))}
      </div>

      {!runsQuery.isPending && (runsQuery.data ?? []).length === 0 ? (
        <div className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 text-sm text-zinc-400">No runs found for this agent.</div>
      ) : null}
    </AppShell>
  );
}
