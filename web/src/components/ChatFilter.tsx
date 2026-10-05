import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { X } from "lucide-react";
import { api, type ChatDetail } from "@/lib/api";
import { Avatar } from "./ui";

/** The one chat a page is narrowed to (its calls, its media, a search in it), and the way out. */
export function ChatFilter({ chat, onClear }: { chat?: string; onClear: () => void }) {
  const { t } = useTranslation();
  const detail = useQuery({ queryKey: ["chat", chat], queryFn: () => api.get<ChatDetail>(`/api/chats/${chat}`), enabled: !!chat });
  if (!chat || !detail.data) return null;
  return (
    <span data-chat-filter className="flex items-center gap-1.5 rounded-full bg-accent/12 py-1 pl-1 pr-2 text-sm text-accent">
      <Avatar name={detail.data.title} size={22} group={detail.data.type === "group"} /> {detail.data.title}
      <button onClick={onClear} aria-label={t("common.close")}><X className="size-3.5" /></button>
    </span>
  );
}
