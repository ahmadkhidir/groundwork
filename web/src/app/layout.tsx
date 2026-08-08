import type { Metadata } from "next";
import { IBM_Plex_Mono, IBM_Plex_Sans, Inter } from "next/font/google";
import "./globals.css";

/**
 * Typeface bindings. Each exposes a CSS variable consumed by the `--font-*` tokens in globals.css,
 * so components reference `font-display` / `font-body` / `font-mono` and never a family name.
 *
 * IBM Plex Sans + IBM Plex Mono is a deliberate family relationship, not an arbitrary pairing: it
 * makes "narrative vs. query" a visual system. Inter carries body text for legibility at the small
 * sizes dense report tables demand.
 */
const plexSans = IBM_Plex_Sans({
  subsets: ["latin"],
  weight: ["400", "600", "700"],
  variable: "--font-plex-sans",
  display: "swap",
});

const inter = Inter({
  subsets: ["latin"],
  variable: "--font-inter",
  display: "swap",
});

const plexMono = IBM_Plex_Mono({
  subsets: ["latin"],
  weight: ["400", "500"],
  variable: "--font-plex-mono",
  display: "swap",
});

export const metadata: Metadata = {
  title: {
    default: "Groundwork",
    template: "%s · Groundwork",
  },
  description: "Every number, traced back to the data.",
  // Favicon comes from the app/icon.png file convention (the user's brand icon) — no metadata
  // entry needed; Next serves it automatically.
};

/**
 * Applies the persisted or system theme before first paint.
 *
 * This runs as a blocking inline script on purpose. Deferring it to a React effect would paint the
 * light theme first and then swap — a flash that is especially unpleasant for a dark-mode user and
 * impossible to fix from inside React, because an effect necessarily runs after paint.
 *
 * Kept tiny and dependency-free, and wrapped in try/catch: localStorage throws in some embedded
 * webviews and private-browsing modes, and a theme preference must never break the page.
 */
const THEME_INIT_SCRIPT = `
(function() {
  try {
    var stored = localStorage.getItem('groundwork-theme');
    var prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
    if (stored === 'dark' || (stored !== 'light' && prefersDark)) {
      document.documentElement.classList.add('dark');
    }
  } catch (e) {}
})();
`;

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    // suppressHydrationWarning: the inline script above mutates <html>'s class list before React
    // hydrates, so server and client markup differ here by design.
    <html
      lang="en"
      suppressHydrationWarning
      className={`${plexSans.variable} ${inter.variable} ${plexMono.variable} h-full`}
    >
      <head>
        <script dangerouslySetInnerHTML={{ __html: THEME_INIT_SCRIPT }} />
      </head>
      <body className="flex min-h-full flex-col bg-surface text-text-primary antialiased">
        {/*
          Skip link: the first tab stop on every page, letting keyboard users bypass navigation and
          go straight to content. Visually hidden until focused.
        */}
        <a
          href="#main-content"
          className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-primary focus:px-4 focus:py-2 focus:text-white"
        >
          Skip to content
        </a>
        {children}
      </body>
    </html>
  );
}
