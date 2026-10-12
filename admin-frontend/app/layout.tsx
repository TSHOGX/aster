import type { Metadata } from "next";
import { Administration } from "@/components/administration";
import { headers } from "next/headers";
import { themeInitScript } from "@web/appearance";
import { displayName } from "@web/brand";
import "./globals.css";
export const metadata: Metadata = { title: `Administration — ${displayName}`, icons: { icon: "/admin/favicon.svg" } };
export default async function Layout({ children }: { children: React.ReactNode }) {
  const account = (await headers()).get("x-project-account-id") || undefined;
  return <html lang="en" data-account-id={account} suppressHydrationWarning><head>
    <meta name="theme-color" content="#f7f7f8" />
    <script dangerouslySetInnerHTML={{ __html: themeInitScript }} />
  </head><body><Administration />{children}</body></html>;
}
