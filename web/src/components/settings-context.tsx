"use client";

import { createContext, useContext, useMemo, useState } from "react";

type SettingsContextValue = {
  actor: string;
  setActor: (value: string) => void;
};

const SettingsContext = createContext<SettingsContextValue | undefined>(undefined);
const ACTOR_STORAGE_KEY = "agent-builder-ui.actor";
const DEFAULT_ACTOR = "ops@example.com";

export function SettingsProvider({ children }: { children: React.ReactNode }) {
  const [actor, setActor] = useState(() => {
    if (typeof window === "undefined") {
      return DEFAULT_ACTOR;
    }
    const stored = window.localStorage.getItem(ACTOR_STORAGE_KEY);
    return stored && stored.trim().length > 0 ? stored : DEFAULT_ACTOR;
  });

  const value = useMemo(
    () => ({
      actor,
      setActor: (nextActor: string) => {
        const normalized = nextActor.trim();
        setActor(normalized || DEFAULT_ACTOR);
        window.localStorage.setItem(ACTOR_STORAGE_KEY, normalized || DEFAULT_ACTOR);
      },
    }),
    [actor],
  );

  return <SettingsContext.Provider value={value}>{children}</SettingsContext.Provider>;
}

export function useSettings() {
  const ctx = useContext(SettingsContext);
  if (!ctx) {
    throw new Error("useSettings must be used inside SettingsProvider");
  }
  return ctx;
}
