"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { useSettings } from "@/components/settings-context";
import { cn } from "@/lib/utils";

type AppShellProps = {
  title: string;
  description?: string;
  children: React.ReactNode;
  fullWidth?: boolean;
};

export function AppShell({ title, description, children, fullWidth = false }: AppShellProps) {
  const pathname = usePathname();
  const { actor } = useSettings();

  return (
    <div className="min-h-screen bg-zinc-950 text-zinc-100">
      <header className="sticky top-0 z-20 border-b border-zinc-800/80 bg-zinc-950/90 backdrop-blur">
        <div
          className={cn(
            "flex items-center justify-between gap-4 py-4",
            fullWidth ? "w-full px-4 md:px-6" : "mx-auto max-w-7xl px-6",
          )}
        >
          <div className="flex items-center gap-3">
            <Link href="/agents" className="text-sm font-medium text-zinc-200 hover:text-zinc-100">
              Agent Builder Platform
            </Link>
          </div>
          <nav className="flex items-center gap-3 text-sm">
            <Link
              href="/agents"
              className={cn(
                "rounded-md px-3 py-1.5 transition-colors",
                pathname.startsWith("/agents") ? "bg-zinc-800 text-zinc-100" : "text-zinc-400 hover:text-zinc-100",
              )}
            >
              Agents
            </Link>
          </nav>
          <input
            aria-label="Actor"
            readOnly
            className="w-56 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 outline-none"
            value={actor}
          />
        </div>
      </header>

      <main className={cn("w-full py-6", fullWidth ? "px-4 md:px-6" : "mx-auto max-w-7xl px-6")}>
        <div className="mb-6">
          <h1 className="text-xl font-semibold text-zinc-100">{title}</h1>
          {description ? <p className="mt-1 text-sm text-zinc-400">{description}</p> : null}
        </div>
        {children}
      </main>
    </div>
  );
}
