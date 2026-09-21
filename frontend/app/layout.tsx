import type { Metadata } from "next";
import "./globals.css";
import { themeInitScript } from "@/lib/theme";
import { localeInitScript } from "@/lib/i18n/locale";
import { brandName } from "@/lib/ui-config";

export const metadata: Metadata = {
  title: brandName,
  description:
    "AI agent workbench for battery research and engineering — shared Project workspaces with independent agents, terminals, and files",
  applicationName: brandName,
  icons: {
    icon: "/favicon.svg",
    shortcut: "/favicon.svg",
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        {/* Mobile browser chrome. The init script sets the correct value before
            first paint (stored choice → OS fallback) and setTheme() updates it
            on every toggle, so it always tracks the in-app theme. */}
        <meta name="theme-color" content="#f6f8fb" />
        {/* Set data-theme before first paint to avoid a light-mode flash. */}
        <script dangerouslySetInnerHTML={{ __html: themeInitScript }} />
        {/* Corrects <html lang> from the server's "en" to the browser's actual
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
