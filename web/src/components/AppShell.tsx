import { useEffect } from "react";
import { Link, useRouterState } from "@tanstack/react-router";
import { markNav } from "@/lib/back";
import { useTranslation } from "react-i18next";
import { BarChart3, Images, MessagesSquare, MoreHorizontal, Phone, Plug, Search, Settings, Users } from "lucide-react";
import { cn } from "@/lib/utils";
import { useChats, useWide } from "@/lib/hooks";
import { useConnected } from "@/lib/events";
import { closeRead } from "@/lib/push";
import { lastChat } from "@/lib/memory";
import { Logo } from "./Logo";
import { Menu, MenuContent, MenuItem, MenuTrigger, Tip } from "./ui";
import { ChatList } from "./ChatList";
import { useNavigate } from "@tanstack/react-router";

const NAV = [
  { to: "/", icon: MessagesSquare, key: "chats" },
  { to: "/search", icon: Search, key: "search" },
  { to: "/calls", icon: Phone, key: "calls" },
  { to: "/media", icon: Images, key: "media" },
  { to: "/people", icon: Users, key: "people" },
  { to: "/overview", icon: BarChart3, key: "overview" },
  { to: "/sources", icon: Plug, key: "sources" },
  { to: "/settings", icon: Settings, key: "settings" },
] as const;

function useSection() {
  const path = useRouterState({ select: (s) => s.location.pathname });
  if (path === "/" || path.startsWith("/chat/")) return "/";
  return NAV.find((n) => n.to !== "/" && path.startsWith(n.to))?.to ?? path;
}

function useUnread() {
  const chats = useChats();
  return (chats.data?.items ?? []).filter((c) => c.unread && !c.muted).length;
}

// a chat read (here or on another device): its notifications go
function useCloseRead() {
  const chats = useChats();
  useEffect(() => {
    if (chats.data) closeRead(chats.data.items);
  }, [chats.data]);
}

function Rail() {
  const { t } = useTranslation();
  const section = useSection();
  const unread = useUnread();
  const connected = useConnected();
  const last = lastChat();
  return (
    <nav className="flex w-[72px] shrink-0 flex-col items-center gap-1 border-r border-line bg-panel py-3">
      <Link to="/" className="mb-3" aria-label="Everysaid">
        <Logo className="size-10" />
      </Link>
      {NAV.map(({ to, icon: Icon, key }) => (
        <Tip key={to} label={t(`nav.${key}`)}>
          <Link
            // Chats: back to the chat open last (archived or not), as it was left
            {...(key === "chats" && last ? { to: "/chat/$chatId", params: { chatId: last.chatId }, search: { hide: last.hide } } : { to })}
            onClick={markNav}
            className={cn(
              "relative grid size-12 place-items-center rounded-2xl text-muted transition-colors hover:bg-panel-2 hover:text-fg",
              section === to && "bg-accent/12 text-accent hover:bg-accent/15 hover:text-accent",
              key === "overview" && "mt-auto",
            )}
            aria-label={t(`nav.${key}`)}
          >
            <Icon className="size-[22px]" />
            {key === "chats" && unread > 0 && (
              <span className="absolute right-1.5 top-1.5 min-w-4 rounded-full bg-accent px-1 text-[10px] font-bold leading-4 text-accent-fg">
                {unread > 99 ? "99+" : unread}
              </span>
            )}
          </Link>
        </Tip>
      ))}
      <Tip label={connected ? "live" : "offline"}>
        <span className={cn("mt-2 size-2 rounded-full", connected ? "bg-ok" : "bg-muted")} />
      </Tip>
    </nav>
  );
}

function BottomTabs() {
  const { t } = useTranslation();
  const section = useSection();
  const unread = useUnread();
  const navigate = useNavigate();
  const main = NAV.slice(0, 4);
  return (
    <nav className="grid shrink-0 grid-cols-5 border-t border-line bg-panel/95 pb-[env(safe-area-inset-bottom)] backdrop-blur">
      {main.map(({ to, icon: Icon, key }) => (
        <Link
          key={to}
          to={to}
          onClick={markNav}
          className={cn("relative flex flex-col items-center gap-0.5 py-2 text-[11px] text-muted", section === to && "text-accent")}
        >
          <Icon className="size-6" />
          {t(`nav.${key}`)}
          {key === "chats" && unread > 0 && (
            <span className="absolute left-1/2 top-1 ml-2 min-w-4 rounded-full bg-accent px-1 text-[10px] font-bold leading-4 text-accent-fg">
              {unread > 99 ? "99+" : unread}
            </span>
          )}
        </Link>
      ))}
      <Menu>
        <MenuTrigger className={cn("flex flex-col items-center gap-0.5 py-2 text-[11px] text-muted outline-none",
          NAV.slice(4).some((n) => n.to === section) && "text-accent")}>
          <MoreHorizontal className="size-6" />
          {t("common.more")}
        </MenuTrigger>
        <MenuContent>
          {NAV.slice(4).map(({ to, icon: Icon, key }) => (
            <MenuItem key={to} icon={<Icon />} onSelect={() => { markNav(); navigate({ to }); }}>{t(`nav.${key}`)}</MenuItem>
          ))}
        </MenuContent>
      </Menu>
    </nav>
  );
}

export function AppShell({ children }: { children: React.ReactNode }) {
  const wide = useWide();
  // the unread chats in the title too: Ferdium (and the tabs) show the number from it; and on the
  // installed app's icon (a push puts a dot there, this the number, or nothing)
  const unread = useUnread();
  useCloseRead();
  useEffect(() => {
    document.title = unread > 0 ? `(${unread}) Everysaid` : "Everysaid";
    const nav = navigator as unknown as { setAppBadge?: (n: number) => Promise<void>; clearAppBadge?: () => Promise<void> };
    if (unread > 0) nav.setAppBadge?.(unread).catch(() => {});
    else nav.clearAppBadge?.().catch(() => {});
  }, [unread]);
  const path = useRouterState({ select: (s) => s.location.pathname });
  const inChat = path.startsWith("/chat/");
  const chatsSection = path === "/" || inChat;

  if (wide) {
    return (
      <div className="flex h-dvh overflow-hidden">
        <Rail />
        {chatsSection && (
          <aside className="flex w-[360px] shrink-0 flex-col border-r border-line bg-panel lg:w-[400px]">
            <ChatList />
          </aside>
        )}
        <main className="min-w-0 flex-1">{children}</main>
      </div>
    );
  }
  return (
    <div className="flex h-dvh flex-col overflow-hidden">
      <main className="min-h-0 flex-1">{path === "/" ? <ChatList /> : children}</main>
      {!inChat && <BottomTabs />}
    </div>
  );
}
