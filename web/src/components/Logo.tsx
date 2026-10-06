import { useId } from "react";

// Everysaid's mark: a speech bubble whose sheet rolls up backwards into a scroll at its left edge
// (what was said, kept), with the lines of a message on it. The same drawing as public/favicon.svg.
const SHEET =
  "M52.19 53.52H135.37A20.71 20.71 0 0 1 156.08 74.23V126.49A20.71 20.71 0 0 1 135.37 147.2H129.4L134.18 169.5L96.35 147.2H47.81A23.89 23.89 0 0 1 23.92 123.31V34.01";
const ROLL_TOP = "M52.19 53.52A28.27 19.51 0 0 1 23.92 34.01C23.92 21.27 37.06 14.5 52.19 14.5C68.92 14.5 80.87 22.86 80.47 35.61V53.52";

export function Logo({ className }: { className?: string }) {
  // ids unique per instance: the logo can be on the page twice, one copy hidden
  const id = useId().replace(/[^\w-]/g, "");
  const paper = `${id}-paper`, curl = `${id}-curl`, face = `${id}-face`;
  return (
    <svg viewBox="0 0 180 180" className={className} aria-hidden>
      <defs>
        <linearGradient id={paper} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#fffaf0" />
          <stop offset="1" stopColor="#efdfba" />
        </linearGradient>
        <linearGradient id={curl} gradientUnits="userSpaceOnUse" x1="23.92" y1="0" x2="83.25" y2="0">
          <stop offset="0" stopColor="#8a6330" stopOpacity=".55" />
          <stop offset=".3" stopColor="#b8925a" stopOpacity=".18" />
          <stop offset=".55" stopColor="#fff" stopOpacity=".6" />
          <stop offset="1" stopColor="#fff" stopOpacity="0" />
        </linearGradient>
        <linearGradient id={face} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#ead3a0" />
          <stop offset="1" stopColor="#c29c62" />
        </linearGradient>
      </defs>
      <g transform="translate(90 92) scale(1.06) translate(-90 -92)" fill="none" stroke="#5e4428" strokeWidth="5" strokeLinecap="round" strokeLinejoin="round">
        <path d={`${SHEET}A28.27 19.51 0 0 0 52.19 53.52Z`} fill={`url(#${paper})`} stroke="none" />
        <path d={`${SHEET}A28.27 19.51 0 0 0 52.19 53.52Z`} fill={`url(#${curl})`} stroke="none" />
        <path d={`${ROLL_TOP}Z`} fill={`url(#${face})`} stroke="none" />
        <path d={ROLL_TOP} />
        <path d={SHEET} />
        <path
          d="M52.19 34.01A2.59 1.79 0 0 1 57.37 34.01A7.77 5.36 0 0 1 41.84 34.01A12.94 8.93 0 0 1 67.72 34.01A18.12 12.5 0 0 1 31.49 34.01"
          stroke="#4a3520"
          strokeWidth="4"
        />
        <path d="M62.55 76.22H139M62.55 92.15H139M62.55 108.08H139M62.55 124.01H113.52" stroke="#5a4630" />
      </g>
    </svg>
  );
}
