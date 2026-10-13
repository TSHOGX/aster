import type { Metadata } from "next";
import { headers } from "next/headers";
import "./globals.css";
import { themeInitScript } from "@web/appearance";
import { localeInitScript } from "@/lib/i18n/locale";
import { displayName } from "@web/brand";

export const metadata: Metadata = {
  title: displayName,
  description:
    "AI agent workbench for enterprise teams — shared Project workspaces with independent agents, terminals, and files",
  applicationName: displayName,
  icons: {
    icon: "/favicon.svg",
    shortcut: "/favicon.svg",
  },
};

export default async function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const account = (await headers()).get("x-project-account-id") || undefined;
  return (
    <html lang="en" data-account-id={account} suppressHydrationWarning>
      <head>
        {/* Mobile browser chrome. The init script sets the correct value before
            first paint (identified account cache → OS fallback), then account
            preferences and live system-theme changes keep it synchronized. */}
        <meta name="theme-color" content="#f6f8fb" />
        {/* Set data-theme before first paint to avoid a light-mode flash. */}
        <script dangerouslySetInnerHTML={{ __html: themeInitScript }} />
        {/* Corrects <html lang> from the server's "en" to the selected
            language, before paint. Screen readers pick a pronunciation voice from
            this attribute, so a Chinese UI announced as English is a real defect,
            not a cosmetic one. The visible strings are translated client-side by
            lib/i18n (useT). */}
        <script dangerouslySetInnerHTML={{ __html: localeInitScript }} />
      </head>
      <body>{children}</body>
    </html>
  );
}
