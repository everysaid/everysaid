import { useTranslation } from "react-i18next";
import { MessagesSquare } from "lucide-react";
import { Empty } from "@/components/ui";

/** On a wide screen, beside the chat list, until a chat is chosen (the overview has its own tab). */
export function ChatsHome() {
  const { t } = useTranslation();
  return <Empty icon={<MessagesSquare />} title={t("chat.choose")} />;
}
