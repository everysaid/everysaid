import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

// "Sending" with its dots coming one by one (., .., ..., and again), in a place of fixed width so
// that nothing beside it moves.
export function Sending() {
  const { t } = useTranslation();
  const [n, setN] = useState(1);
  useEffect(() => {
    const tick = setInterval(() => setN((x) => (x % 3) + 1), 450);
    return () => clearInterval(tick);
  }, []);
  return <span data-sending>{t("chat.sending")}<span className="inline-block w-[1em] text-left">{".".repeat(n)}</span></span>;
}
