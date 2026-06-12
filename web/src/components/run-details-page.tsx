"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useMemo, useState } from "react";

import { AppShell } from "@/components/app-shell";
import { getRun, getTimeline } from "@/lib/api";
import type { TimelineEvent } from "@/lib/types";

type RunDetailsPageProps = {
  runId: string;
};

function isActiveStatus(status?: string) {
  return status === "queued" || status === "running";
}

function pretty(value: unknown) {
  if (value === undefined || value === null) {
    return "null";
  }
  if (typeof value === "string") {
    return value;
  }
  return JSON.stringify(value, null, 2);
}

export function RunDetailsPage({ runId }: RunDetailsPageProps) {
  const [selectedSeq, setSelectedSeq] = useState<number | null>(null);

  const runQuery = useQuery({
    queryKey: ["run", runId],
    queryFn: () => getRun(runId),
    refetchInterval: (query) => (isActiveStatus(query.state.data?.status) ? 2_000 : false),
  });

  const timelineQuery = useQuery({
    queryKey: ["timeline", runId],
    queryFn: () => getTimeline(runId),
    refetchInterval: () => (isActiveStatus(runQuery.data?.status) ? 2_000 : false),
  });

  const events = useMemo(() => timelineQuery.data ?? [], [timelineQuery.data]);
  const selectedEvent = useMemo<TimelineEvent | undefined>(
    () => events.find((event) => event.seq === selectedSeq) ?? events[events.length - 1],
    [events, selectedSeq],
  );

  return (
    <AppShell
      title={`Run · ${runId}`}
      description="Track execution status, inspect node events, and investigate payload-level errors quickly."
    >
      <div className="mb-4 grid gap-3 sm:grid-cols-3">
        <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-3">
          <p className="text-xs text-zinc-500">Status</p>
          <p className="mt-1 text-lg font-semibold text-zinc-100">{runQuery.data?.status ?? "loading..."}</p>
        </div>
        <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-3">
          <p className="text-xs text-zinc-500">Version ID</p>
          <p className="mt-1 text-sm text-zinc-200">{runQuery.data?.versionId ?? "-"}</p>
        </div>
        <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-3">
          <p className="text-xs text-zinc-500">Triggered by</p>
          <p className="mt-1 text-sm text-zinc-200">{runQuery.data?.triggeredBy || "-"}</p>
        </div>
      </div>

      <div className="grid gap-4 lg:grid-cols-[360px_1fr]">
        <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-3">
          <h2 className="mb-2 text-sm font-semibold text-zinc-100">Timeline</h2>
          <div className="max-h-[520px] space-y-2 overflow-y-auto pr-1">
            {events.map((event) => (
              <button
                key={event.seq}
                type="button"
                onClick={() => setSelectedSeq(event.seq)}
                className={`w-full rounded-md border px-2 py-2 text-left transition ${
                  selectedEvent?.seq === event.seq
                    ? "border-blue-600 bg-blue-600/10"
                    : "border-zinc-800 bg-zinc-950/50 hover:border-zinc-600"
                }`}
              >
                <div className="flex items-center justify-between gap-2">
                  <p className="text-xs font-medium text-zinc-200">{event.eventType}</p>
                  <span className="text-[11px] text-zinc-500">#{event.seq}</span>
                </div>
                <p className="mt-1 truncate text-xs text-zinc-400">{event.nodeId || "run"}</p>
              </button>
            ))}
          </div>
        </div>

        <div className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-3">
          <h2 className="mb-2 text-sm font-semibold text-zinc-100">Event details</h2>
          {!selectedEvent ? (
            <p className="text-sm text-zinc-400">No events yet. Waiting for workflow progress...</p>
          ) : (
            <div className="space-y-3">
              <div className="grid grid-cols-2 gap-3">
                <div className="rounded-md border border-zinc-800 bg-zinc-950/60 p-2">
                  <p className="text-xs text-zinc-500">Event type</p>
                  <p className="text-sm text-zinc-200">{selectedEvent.eventType}</p>
                </div>
                <div className="rounded-md border border-zinc-800 bg-zinc-950/60 p-2">
                  <p className="text-xs text-zinc-500">Node</p>
                  <p className="text-sm text-zinc-200">{selectedEvent.nodeId || "run"}</p>
                </div>
              </div>

              <div className="rounded-md border border-zinc-800 bg-zinc-950/60 p-2">
                <p className="mb-1 text-xs text-zinc-500">Payload</p>
                <pre className="max-h-[180px] overflow-auto rounded bg-zinc-950 p-2 text-xs text-zinc-300">
                  {pretty(selectedEvent.payload)}
                </pre>
              </div>

              <div className="rounded-md border border-zinc-800 bg-zinc-950/60 p-2">
                <p className="mb-1 text-xs text-zinc-500">Meta</p>
                <pre className="max-h-[180px] overflow-auto rounded bg-zinc-950 p-2 text-xs text-zinc-300">
                  {pretty(selectedEvent.meta)}
                </pre>
              </div>
            </div>
          )}
        </div>
      </div>

      <div className="mt-4 flex items-center gap-3 text-sm text-zinc-400">
        {runQuery.data?.agentName ? (
          <Link href={`/agents/${encodeURIComponent(runQuery.data.agentName)}/builder`} className="hover:text-zinc-200">
            &larr; Back to builder
          </Link>
        ) : (
          <Link href="/agents" className="hover:text-zinc-200">
            &larr; Back to agents
          </Link>
        )}
      </div>
    </AppShell>
  );
}
