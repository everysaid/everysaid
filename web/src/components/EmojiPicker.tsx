import { EmojiPicker as P } from "frimousse";
import { useTranslation } from "react-i18next";
import { Spinner } from "@/components/ui";

// The emoji to choose from, by category, with a search (in English: the emoji's names come from
// emojibase, served by the app itself) and the ones used lately first. One picker for writing and for
// reactions.

const RECENT = "emoji-recent";

export function recentEmoji(): string[] {
  try {
    const r = JSON.parse(localStorage.getItem(RECENT) ?? "[]");
    return Array.isArray(r) ? r.filter((e) => typeof e === "string") : [];
  } catch {
    return [];
  }
}

function keepRecent(e: string) {
  localStorage.setItem(RECENT, JSON.stringify([e, ...recentEmoji().filter((x) => x !== e)].slice(0, 24)));
}

export function EmojiPicker({ onPick }: { onPick: (emoji: string) => void }) {
  const { t } = useTranslation();
  const recent = recentEmoji();
  const pick = (e: string) => { keepRecent(e); onPick(e); };
  // the categories as emojibase names them (English), in the user's words
  const categories: Record<string, string> = {
    "Smileys & emotion": t("emoji.smileys"), "People & body": t("emoji.people"), "Animals & nature": t("emoji.animals"),
    "Food & drink": t("emoji.food"), "Travel & places": t("emoji.travel"), Activities: t("emoji.activities"),
    Objects: t("emoji.objects"), Symbols: t("emoji.symbols"), Flags: t("emoji.flags"),
  };
  return (
    <P.Root data-emoji-picker locale="en" emojibaseUrl="/emojibase" columns={8} onEmojiSelect={({ emoji }) => pick(emoji)}
      className="isolate flex h-80 w-72 flex-col">
      <P.Search data-emoji-search placeholder={t("emoji.search")} onKeyDown={(e) => e.stopPropagation()}
        className="mx-2 mt-2 h-8 rounded-lg border border-line bg-panel-2 px-2 text-sm outline-none focus:border-accent" />
      {recent.length > 0 && (
        <div data-emoji-recent className="flex flex-wrap gap-0.5 border-b border-line px-1.5 py-1.5">
          {recent.slice(0, 16).map((e) => (
            <button key={e} type="button" onClick={() => pick(e)} aria-label={e}
              className="grid size-8 place-items-center rounded-md text-lg hover:bg-panel-2">{e}</button>
          ))}
        </div>
      )}
      <P.Viewport className="relative flex-1 outline-none">
        <P.Loading className="absolute inset-0 grid place-items-center"><Spinner /></P.Loading>
        <P.Empty className="absolute inset-0 grid place-items-center text-sm text-muted">{t("emoji.none")}</P.Empty>
        <P.List className="select-none pb-1.5" components={{
          CategoryHeader: ({ category, ...props }) => (
            <div {...props} className="bg-panel px-3 pb-1 pt-2.5 text-xs font-medium text-muted">{categories[category.label] ?? category.label}</div>
          ),
          Row: ({ children, ...props }) => <div {...props} className="scroll-my-1.5 px-1.5">{children}</div>,
          Emoji: ({ emoji, ...props }) => (
            <button {...props} data-emoji-option className="grid size-8 place-items-center rounded-md text-lg data-[active]:bg-panel-2">{emoji.emoji}</button>
          ),
        }} />
      </P.Viewport>
    </P.Root>
  );
}
