import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Merge, Scissors, Sparkles, X } from "lucide-react";
import { toast } from "sonner";
import { api, type ChatDetail } from "@/lib/api";
import { dateOnly, number } from "@/lib/format";
import { useChats, useDebounced } from "@/lib/hooks";
import { Avatar, Button, Dialog, Input, ServiceBadge } from "./ui";

/** Two groups that look like one, and why. */
export interface GroupSuggestion {
  why: string[];
  shared: number;
  chats: { chat_id: string; title: string; services: string[]; last_ts: number; members: number }[];
}

const WHY: Record<string, string> = { members: "chat.alikeMembers", name: "chat.alikeName" };

/** After a merge or a split: the lists and the chats again, and where the chat now is. */
function useRefresh() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return (go?: string) => {
    qc.invalidateQueries({ queryKey: ["chats"] });
    qc.invalidateQueries({ queryKey: ["chat"] });
    qc.invalidateQueries({ queryKey: ["group-suggestions"] });
    qc.invalidateQueries({ queryKey: ["stream"] });
    if (go) navigate({ to: "/chat/$chatId", params: { chatId: go } });
  };
}

function useMerge() {
  const { t } = useTranslation();
  const refresh = useRefresh();
  return useMutation({
    mutationFn: ({ into, other }: { into: string; other: string }) => api.post<{ id: string }>(`/api/chats/${into}/merge`, { other }),
    onSuccess: (r) => { refresh(r.id); toast.success(t("common.done")); },
    onError: (e: Error) => toast.error(e.message),
  });
}

function useDismiss() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (chats: string[]) => api.post("/api/groups/suggestions/dismiss", { chats }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["group-suggestions"] }),
  });
}

/** In a group's info: the groups it is made of (each can leave), others that look like it, and a
 * merge with any other group. */
export function GroupParts({ chat }: { chat: ChatDetail }) {
  const { t } = useTranslation();
  const refresh = useRefresh();
  const [merging, setMerging] = useState(false);
  const merge = useMerge();
  const dismiss = useDismiss();
  const alike = useQuery({
    queryKey: ["group-suggestions", chat.id],
    queryFn: () => api.get<{ items: GroupSuggestion[] }>(`/api/groups/suggestions?chat=${chat.id}`),
  });
  const split = useMutation({
    mutationFn: (conversation: number) => api.post<{ id: string }>(`/api/chats/${chat.id}/split`, { conversation }),
    onSuccess: (r) => { refresh(r.id !== chat.id ? r.id : undefined); toast.success(t("common.done")); },
    onError: (e: Error) => toast.error(e.message),
  });
  const groups = chat.groups ?? [];
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <div className="text-xs font-semibold uppercase tracking-wide text-muted">{t("chat.groupParts")}</div>
        <Button size="sm" variant="ghost" data-merge-group onClick={() => setMerging(true)}><Merge className="size-4" />{t("chat.mergeGroup")}</Button>
      </div>
      <div className="divide-y divide-line rounded-2xl border border-line">
        {groups.map((g) => (
          <div key={g.conversation_id} data-group-part className="flex items-center gap-2 px-3 py-2 text-sm">
            <span className="min-w-0 flex-1">
              <span className="block truncate">{g.title ?? chat.title}</span>
              <span className="block text-xs text-muted">{number(g.messages)} {t("people.messages")}{g.last_ts ? ` · ${dateOnly(g.last_ts)}` : ""}</span>
            </span>
            <ServiceBadge id={g.service} />
            {groups.length > 1 && (
              <Button size="sm" variant="ghost" data-split-group title={t("chat.splitGroupHint")} onClick={() => split.mutate(g.conversation_id)}
                loading={split.isPending && split.variables === g.conversation_id}>
                <Scissors className="size-3.5" />{t("people.split")}
              </Button>
            )}
          </div>
        ))}
      </div>
      {(alike.data?.items.length ?? 0) > 0 && (
        <div className="space-y-1.5 pt-1">
          <div className="flex items-center gap-1.5 text-xs text-muted"><Sparkles className="size-3.5" />{t("chat.alike")}</div>
          {alike.data!.items.map((s) => {
            const other = s.chats.find((c) => c.chat_id !== chat.id)!;
            return (
              <div key={other.chat_id} data-alike className="flex items-center gap-2 rounded-2xl border border-line px-3 py-2 text-sm">
                <Avatar name={other.title} size={28} group />
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{other.title}</span>
                  <span className="block text-[11px] text-muted">{s.why.map((w) => t(WHY[w] ?? w)).join(" · ")} · {t("chat.sharedMembers", { count: s.shared })}</span>
                </span>
                {other.services.map((x) => <ServiceBadge key={x} id={x} />)}
                <Button size="sm" variant="primary" onClick={() => merge.mutate({ into: chat.id, other: other.chat_id })} loading={merge.isPending}>{t("chat.mergeConfirm")}</Button>
                <button className="rounded-full p-1 text-muted hover:bg-panel-2 hover:text-fg" title={t("chat.notSameGroup")} aria-label={t("chat.notSameGroup")}
                  onClick={() => dismiss.mutate([chat.id, other.chat_id])}><X className="size-4" /></button>
              </div>
            );
          })}
        </div>
      )}
      <MergeGroupDialog open={merging} onOpenChange={setMerging} chat={chat} />
    </div>
  );
}

function MergeGroupDialog({ open, onOpenChange, chat }: { open: boolean; onOpenChange: (o: boolean) => void; chat: ChatDetail }) {
  const { t } = useTranslation();
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 200);
  const found = useChats({ kind: "group", q: dq || undefined, archived: true });
  const merge = useMerge();
  const others = (found.data?.items ?? []).filter((c) => c.type === "group" && c.id !== chat.id).slice(0, 30);
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={t("chat.mergeGroup")} description={t("chat.mergeGroupHint")}>
      <Input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("chat.searchGroups")} />
      <div className="mt-3 max-h-80 divide-y divide-line overflow-y-auto">
        {others.map((x) => (
          <div key={x.id} className="flex items-center gap-3 py-2">
            <Avatar name={x.title} size={36} group />
            <span className="min-w-0 flex-1 truncate">{x.title}</span>
            {x.services.map((s) => <ServiceBadge key={s} id={s} />)}
            <Button size="sm" variant="primary" onClick={() => merge.mutate({ into: chat.id, other: x.id }, { onSuccess: () => onOpenChange(false) })}
              loading={merge.isPending}>{t("chat.mergeConfirm")}</Button>
          </div>
        ))}
      </div>
    </Dialog>
  );
}

/** Over the list of groups: pairs that look like one group, to merge or turn down. */
export function GroupSuggestions() {
  const { t } = useTranslation();
  const sugg = useQuery({
    queryKey: ["group-suggestions"],
    queryFn: () => api.get<{ items: GroupSuggestion[] }>("/api/groups/suggestions"),
  });
  const merge = useMerge();
  const dismiss = useDismiss();
  if (!sugg.data?.items.length) return null;
  return (
    <div data-group-suggestions className="px-4 pb-2">
      <div className="mb-1.5 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted"><Sparkles className="size-3.5" />{t("chat.alikeGroups")}</div>
      <div className="flex gap-2 overflow-x-auto pb-1">
        {sugg.data.items.map((s) => {
          const [a, b] = s.chats;
          return (
            <div key={`${a.chat_id}-${b.chat_id}`} className="flex shrink-0 items-center rounded-2xl border border-line text-sm">
              <div className="py-1.5 pl-3 pr-2">
                <div className="max-w-56 truncate">{a.title} <span className="text-muted">+</span> {b.title}</div>
                <div className="text-[11px] text-muted">{s.why.map((w) => t(WHY[w] ?? w)).join(" · ")} · {t("chat.sharedMembers", { count: s.shared })}</div>
              </div>
              <button className="self-stretch px-2 text-accent hover:bg-panel-2" title={t("chat.mergeConfirm")} aria-label={t("chat.mergeConfirm")}
                onClick={() => merge.mutate({ into: a.last_ts >= b.last_ts ? a.chat_id : b.chat_id, other: a.last_ts >= b.last_ts ? b.chat_id : a.chat_id })}>
                <Merge className="size-4" />
              </button>
              <button className="self-stretch rounded-r-2xl px-2 text-muted hover:bg-panel-2 hover:text-fg" title={t("chat.notSameGroup")}
                aria-label={t("chat.notSameGroup")} onClick={() => dismiss.mutate([a.chat_id, b.chat_id])}><X className="size-4" /></button>
            </div>
          );
        })}
      </div>
    </div>
  );
}
