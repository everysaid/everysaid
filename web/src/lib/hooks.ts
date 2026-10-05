import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, qs, type ChatSummary } from "./api";

export function useMedia(query: string) {
  const [m, setM] = useState(() => matchMedia(query).matches);
  useEffect(() => {
    const mq = matchMedia(query);
    const f = () => setM(mq.matches);
    mq.addEventListener("change", f);
    return () => mq.removeEventListener("change", f);
  }, [query]);
  return m;
}

export const useWide = () => useMedia("(min-width: 768px)");

export function useChats(params: { kind?: string; q?: string; archived?: boolean } = {}) {
  return useQuery({
    queryKey: ["chats", params],
    queryFn: () => api.get<{ items: ChatSummary[] }>(`/api/chats${qs(params)}`),
    staleTime: 15_000,
  });
}

export function useSettings() {
  return useQuery({ queryKey: ["settings"], queryFn: () => api.get<Record<string, unknown>>("/api/settings") });
}

export function useDebounced<T>(value: T, ms = 250) {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}
