// Everysaid's mark: two speech bubbles, one behind the other (many conversations, one place),
// with the lines of a message in the front one.
export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 64 64" className={className} aria-hidden>
      <defs>
        <linearGradient id="lg" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#7c7cff" />
          <stop offset="1" stopColor="#4f46e5" />
        </linearGradient>
      </defs>
      <rect width="64" height="64" rx="16" fill="url(#lg)" />
      <path d="M22 14h24a8 8 0 0 1 8 8v13a8 8 0 0 1-8 8h-2v6l-7-6H22a8 8 0 0 1-8-8V22a8 8 0 0 1 8-8Z" fill="#fff" opacity=".28" transform="translate(4 -2)" />
      <path d="M18 20h24a8 8 0 0 1 8 8v13a8 8 0 0 1-8 8H29l-8 7v-7h-3a8 8 0 0 1-8-8V28a8 8 0 0 1 8-8Z" fill="#fff" />
      <rect x="18" y="29" width="22" height="3.2" rx="1.6" fill="#5b5bf0" />
      <rect x="18" y="36" width="15" height="3.2" rx="1.6" fill="#5b5bf0" opacity=".6" />
    </svg>
  );
}
