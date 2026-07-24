import type { Metadata } from "next";
import "./globals.css";
import { ThemedToaster } from "@/components/theme/themed-toaster";
import { themeInitScript } from "@/lib/theme";

export const metadata: Metadata = {
  title: "BatteryGPT",
  description: "AI agent workbench for battery research and engineering — sandboxed sessions for data analysis, modeling, and diagnostics",
  applicationName: "BatteryGPT",
  icons: {
    icon: "/favicon.svg",
    shortcut: "/favicon.svg"
  }
};

export default function RootLayout({
  children
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        {/* Mobile browser chrome. The init script sets the correct value before
            first paint (stored choice → OS fallback) and setTheme() updates it
            on every toggle, so it always tracks the in-app theme. */}
        <meta name="theme-color" content="#f7f6f2" />
        {/* Set data-theme before first paint to avoid a light-mode flash. */}
        <script dangerouslySetInnerHTML={{ __html: themeInitScript }} />
      </head>
      <body>
        {children}
        <ThemedToaster />
      </body>
    </html>
  );
}
